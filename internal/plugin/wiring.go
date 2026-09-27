package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/events"
	wiki "github.com/popinjayjohn/dine-and-dash-semiplane/internal/http"
)

// The capabilities that reach outside the process: a route, a command, an event
// subscription.
//
// They are in one file because they share the property that makes them safe to add
// at all, which is that **each is a request somebody else made.** A page is a
// request a reader made, a command is a request a DM typed, an event is a notice
// about a request — and in none of the three does a plugin get to decide whether the
// request happened. That is the line between a capability and a back door, and it is
// why there is no "override the page route" and no "publish an event" here.

// Command is a subcommand a plugin adds to `wiki`.
//
// It is a value with a name rather than a bare function so that `wiki help` can
// print it, so that two plugins cannot both want `wiki wordcount` without the second
// being told which one it lost to, and so that the argument list is a *slice* — a
// plugin cannot reach `os.Args` and does not have to.
//
// The name is a single word for the same reason a page type is: it appears in a
// shell completion, in a help listing, and in a log line, and a name that needed
// quoting in any of those is a name nobody will type.
type Command struct {
	// Name is the word after `wiki`, normalised the way a plugin's own name is.
	Name string

	// Summary is one line for `wiki help`. It is not optional for the same reason a
	// page type's is not: a command nobody can read the meaning of is a command
	// nobody will run.
	Summary string

	// Plugin is the name of the plugin that registered the command, filled in by
	// [Registry.AddCommand] rather than by the plugin, so `wiki help` can print
	// which of four plugins added a word and the attribution cannot be wrong.
	Plugin string

	// Run is the command. `args` is what came after the command name, so a command
	// parses its own flags and a typo in one is a usage message rather than a panic.
	//
	// `stdout` and `stderr` are separate because a command that writes a usage
	// message to the same stream it writes results to cannot be piped, and this is
	// the one place a plugin writes to a terminal rather than to a database.
	Run func(ctx context.Context, args []string, stdout, stderr io.Writer) error
}

// commandName is the shape of a name, and it is the same shape a plugin's own name
// is after normalisation.
//
// A regexp rather than a character loop because the loop is the thing that gets one
// more condition added to it every time somebody wants a new character, and the
// regexp says the whole rule on one line and cannot drift from the tests.
var commandName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// AddCommand registers a subcommand.
//
// The name is normalised and checked rather than taken as given, and the check is
// the same as for a plugin's own name for the same reasons: `wiki help` prints it
// and a shell completes it. A plugin may not take a core command's name either —
// `sync` and `reindex` are core's, and a plugin that claimed one would be a plugin
// that runs when somebody expected the campaign to be indexed.
func (r *Registry) AddCommand(command Command) error {
	if r == nil {
		return errors.New("plugin: adding a command to a nil registry")
	}
	if strings.TrimSpace(command.Name) == "" {
		return errors.New("plugin: a command needs a name")
	}
	if !commandName.MatchString(command.Name) {
		return fmt.Errorf("%w: command name %q is not a single lower-case word", ErrInvalidName, command.Name)
	}
	if coreCommands[command.Name] {
		return fmt.Errorf("%w: command %q is a core command", ErrReservedName, command.Name)
	}
	if _, taken := r.commands[command.Name]; taken {
		return fmt.Errorf("%w: command %q", ErrDuplicateName, command.Name)
	}
	if strings.TrimSpace(command.Summary) == "" {
		return fmt.Errorf("plugin: command %q needs a summary", command.Name)
	}
	if command.Run == nil {
		return fmt.Errorf("plugin: command %q has nothing to run", command.Name)
	}

	// Attributed here, where the plugin's name is known, because a usage message
	// printed by `wiki help` says which plugin added the command and there is no
	// other place that knows.
	command.Plugin = r.current

	r.commands[command.Name] = command
	return nil
}

// coreCommands are the subcommands `cmd/wiki` owns.
//
// They are here rather than in `cmd/wiki` for the same reason `coreFields` is
// [vault.CoreKeys]: a list over there is a list that goes stale, and the two places
// that need it — the refusal here and the dispatch loop — are the same list twice.
// The test `TestTheCoreCommandListIsTheDispatchersOwn` holds them together.
var coreCommands = map[string]bool{
	"serve":    true,
	"init":     true,
	"mint":     true,
	"sync":     true,
	"reindex":  true,
	"versions": true,
	"help":     true,
}

// Commands is every registered command, keyed by name, in `(Priority, Name)` order.
//
// The map is a copy for the same reason [Registry.PageTypes] is: a map handed out by
// reference is a map the dispatch loop and `wiki help` are one write away from
// racing over.
func (r *Registry) Commands() map[string]Command {
	if r == nil {
		return map[string]Command{}
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	copied := make(map[string]Command, len(r.commands))
	for name, command := range r.commands {
		copied[name] = command
	}
	return copied
}

// AddRoute mounts a path inside the campaign group.
//
// The pattern is checked here rather than by the router at the first request,
// because a router's failure mode for a bad pattern is a panic in a request
// goroutine and a startup refusal is the one nobody has to reproduce under load.
func (r *Registry) AddRoute(route wiki.Route) error {
	if r == nil {
		return errors.New("plugin: adding a route to a nil registry")
	}
	// The route type is named by the consumer, which is this package's own HTTP
	// layer, and the layer does not import the registry. That is the whole reason a
	// plugin's route is a plain struct here rather than an interface: the dependency
	// runs from the registry to the application and not back.
	if strings.TrimSpace(route.Pattern) == "" {
		return errors.New("plugin: a route needs a pattern")
	}
	if route.Handler == nil {
		return fmt.Errorf("plugin: route %q has no handler", route.Pattern)
	}
	if err := validPattern(route.Pattern); err != nil {
		return fmt.Errorf("plugin: route %q: %w", route.Pattern, err)
	}

	if route.Plugin == "" {
		route.Plugin = r.current
	}
	r.routes = append(r.routes, route)
	return nil
}

// Routes is every plugin's route, in `(Priority, Name)` order.
func (r *Registry) Routes() []wiki.Route {
	if r == nil {
		return nil
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	return slices.Clone(r.routes)
}

// AddSubscriber registers a plugin as a listener for one or more events.
//
// The bus is the registry's, built once at [Registry.Add]'s first caller rather than
// passed in, so that a plugin cannot bring its own: a bus a plugin owns is a bus
// whose subscribers a plugin chose, and this is the one place that decides who is
// listening to the application's events.
func (r *Registry) AddSubscriber(names []events.Name, subscriber events.Subscriber) error {
	if r == nil {
		return errors.New("plugin: adding a subscriber to a nil registry")
	}
	if subscriber == nil {
		return fmt.Errorf("plugin: %q subscribed with nothing to call", r.current)
	}
	if len(names) == 0 {
		return fmt.Errorf("plugin: %q subscribed to no events", r.current)
	}

	return r.bus.Subscribe(r.current, names, subscriber)
}

// Events is the bus, for the caller that wires it into the HTTP layer and the editor.
//
// It is never nil for a registry that is not itself nil, because a publisher calls
// it unconditionally and a nil check on the path of a page view is a check somebody
// will eventually forget in one of the two places there are.
func (r *Registry) Events() *events.Bus {
	if r == nil {
		return nil
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.bus
}

// validPattern is whether a chi pattern is one chi will accept.
//
// It is answered by *asking chi*, in a throwaway router, rather than by a regexp of
// our own. A second validator for a third party's syntax is a second answer, and the
// one this project would write is the one that is wrong about a pattern chi accepts
// today. The throwaway router is created, used and discarded on the path that runs
// once at startup.
//
// chi panics on a bad pattern rather than returning an error, so the recovery is the
// interface rather than an implementation detail of this function.
func validPattern(pattern string) (err error) {
	if !strings.HasPrefix(pattern, "/") {
		return errors.New("a pattern mounted in the campaign group starts with a slash")
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("chi refused the pattern: %v", recovered)
		}
	}()

	chi.NewRouter().Get(pattern, func(http.ResponseWriter, *http.Request) {})
	return nil
}
