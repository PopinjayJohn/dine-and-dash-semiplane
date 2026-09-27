package access

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/safe"
)

// # A policy a plugin may compose with, and never replace
//
// ADR 0002's `Capabilities.Access *access.Policies` is a pointer to a type this
// package did not have, and its shape is the interesting part of M11: a plugin
// contributes an *extra* rule to the rights matrix, and the one thing it cannot do
// is relax a rule that is already there.
//
// That is enforced structurally rather than by convention, and the mechanism is that
// [Policies.Apply] ANDs the plugin's answer with the core's. A policy that returns
// [Granted] for a player gets nothing, because nothing is what the core decided and
// nothing AND anything is nothing. There is no ordering in which a plugin runs first
// and the core takes its answer, and there is no interface a plugin could implement
// to get there.
//
// # Why the narrowing happens here and not in the store
//
// The read predicate is SQL ([internal/store/acl.go]), and a policy expressed in Go
// is not part of it. That is a real gap and it is why `Policies.Filter` exists rather
// than `Policies.Apply` being the only method: the *rows* a listing is built from
// come back from the store already filtered by the core predicate, and this type
// narrows that list afterwards.
//
// The consequence, stated plainly because a reader will want to know: **a policy
// narrows what is served and what is listed, not what the store's `WHERE` clause
// admits.** A store method called directly by something other than this codebase —
// a plugin with a store handle, a future command — sees the core predicate and
// nothing else. The invariant this package protects is the one the SQL enforces; a
// policy is a second, stricter layer on top of it, never a looser one.
//
// # The failure direction is not the render's
//
// A panicking *render hook* is logged and skipped, because the render already has a
// correct answer without the hook. A panicking or erroring *policy* denies, because
// the answer it was going to give is the one keeping a page hidden. The two are
// opposite on purpose, and the asymmetry is the whole reason the failure cases are
// written down rather than shared.

// Policy is one extra rule a plugin contributes to the rights matrix.
//
// It is handed the decision [For] already reached and returns a decision, and the
// two are combined with AND by [Policies.Apply]. A policy that wants to hide
// something turns a field off; a policy cannot turn one on.
//
// The error is for a policy that could not decide — a lookup that failed, a
// principal it does not recognise — and the answer to one is a refusal, not a
// default. A rule that could not run is a rule that did not happen, and a rule that
// did not happen must not be read as a rule that said yes.
type Policy func(ctx context.Context, p Principal, page PageMeta, decided Decision) (Decision, error)

// rule is one policy with the name of the plugin that registered it, because the
// recovery and the log line need both and a bare function value has neither.
type rule struct {
	plugin string
	policy Policy
}

// Policies is an ordered set of plugin policies, composed with [For] rather than
// replacing it.
//
// The zero value is not usable — a nil *Policies is, and every method handles it, so
// `http.Config{Policies: nil}` is the same application as one with no plugins. That
// is what lets the HTTP layer ask the question unconditionally rather than testing
// for a field it usually did not set.
//
// It is built once at startup and read afterwards. [Policies.Add] is not safe to call
// concurrently with a render, and it does not need to be: a registry is built before
// the listener is up.
type Policies struct {
	log   *slog.Logger
	rules []rule
}

// NewPolicies returns an empty set that logs through log. A nil logger means
// [slog.Default], for the same reason everywhere else in this project does.
func NewPolicies(log *slog.Logger) *Policies {
	if log == nil {
		log = slog.Default()
	}
	return &Policies{log: log}
}

// Add appends a policy, and refuses one with no name or no function.
//
// A refusal here is a startup failure rather than a log line, and that is the whole
// of the loud-startup guarantee reaching this package: a policy that is not in the set
// is a page the DM believes is hidden and is not.
func (ps *Policies) Add(plugin string, policy Policy) error {
	if ps == nil {
		return fmt.Errorf("access: adding %q to a nil policy set", plugin)
	}
	if plugin == "" {
		return fmt.Errorf("access: a policy needs the name of the plugin that registered it")
	}
	if policy == nil {
		return fmt.Errorf("access: policy from %q is nil", plugin)
	}

	ps.rules = append(ps.rules, rule{plugin: plugin, policy: policy})
	return nil
}

// Len is how many policies are in the set.
//
// It is what the HTTP layer asks before it does the work of building a
// [PageMeta] for every row in a listing — see the note on ownership in
// [internal/http/decision.go], which is the only caller that cares.
func (ps *Policies) Len() int {
	if ps == nil {
		return 0
	}
	return len(ps.rules)
}

// IsEmpty reports whether there is nothing to apply, so a caller can skip the whole
// question rather than asking it of an empty set.
func (ps *Policies) IsEmpty() bool { return ps.Len() == 0 }

// Apply runs every policy over one decision and returns the narrowest answer.
//
// The order is the plugins' `(Priority, Name)` order, put there by the registry
// before it built this. It matters only in that all the results are ANDed, so two
// policies that disagree cannot both win — which is the point, and is why there is
// no "the last policy decides" anywhere in this function.
func (ps *Policies) Apply(
	ctx context.Context, p Principal, page PageMeta, decided Decision,
) Decision {
	if ps == nil || len(ps.rules) == 0 {
		return decided
	}

	narrowed := decided
	for _, r := range ps.rules {
		narrowed = narrow(narrowed, ps.call(ctx, r, p, page, narrowed))
	}
	return narrowed
}

// call runs one policy and recovers its panic.
//
// The fallback is [Nothing] and not `decided`, and that is the asymmetry the package
// comment is about: a policy that crashed was one whose job was to take a right away,
// and honouring the pre-crash decision would honour it by accident.
func (ps *Policies) call(
	ctx context.Context, r rule, p Principal, page PageMeta, decided Decision,
) Decision {
	// The named return is the fallback. `safe.Guard` recovers and the function
	// returns whatever `answer` holds, which is Nothing because that is what it was
	// initialised to.
	var answer Decision

	func() {
		defer safe.Guard(ps.log, r.plugin, "access.Policy")

		produced, err := r.policy(ctx, p, page, decided)
		if err != nil {
			ps.log.LogAttrs(ctx, slog.LevelError, "plugin access policy failed; denying",
				slog.String("plugin", r.plugin),
				slog.String("error", err.Error()),
			)
			return
		}
		answer = produced
	}()

	return answer
}

// narrow is the whole of "composition, not override", in one function.
//
// Five fields, ANDed, and no branch: a decision field that is false in either input
// is false in the output. A plugin cannot grant a right and a plugin cannot
// re-grant one that an earlier plugin in the same run took away, because the
// earlier narrowing is already in the value being narrowed.
//
// It is spelled out rather than written as a loop over a list of field names because
// a loop needs somewhere to hold the answers, and a `map[string]bool` of field names
// would make a new `Decision` field silently default to *allowed* — the opposite of
// this function's purpose. Adding a sixth field to [Decision] is a compile error
// here, which is the same argument `Granted` makes.
func narrow(decided, plugin Decision) Decision {
	return Decision{
		CanRead:       decided.CanRead && plugin.CanRead,
		CanEdit:       decided.CanEdit && plugin.CanEdit,
		CanReveal:     decided.CanReveal && plugin.CanReveal,
		CanSeeSecrets: decided.CanSeeSecrets && plugin.CanSeeSecrets,
		ReadsAll:      decided.ReadsAll && plugin.ReadsAll,
	}
}

// MetaFor is the [PageMeta] for a page, from the two things a caller knows about it.
//
// It is here rather than written as a struct literal at each of the three call sites
// (`decisionFor`, the page tree and the search candidates) because a fourth one would
// arrive with a plugin, and a `PageMeta` that leaves a field at its zero value is a
// page that is owned by nobody and archived, which is a decision the caller did not
// make.
//
// `owned` is an argument because it is the one fact the store knows and the caller
// does not, and because the three callers get it three different ways: a query for the
// single page, a memoised lookup for a listing, and a memoised lookup again for a
// search. See [internal/http/decision.go].
func MetaFor(page domain.Page, owned bool) PageMeta {
	return PageMeta{
		Type:       page.Type,
		Visibility: page.Visibility,
		Owned:      owned,
		Archived:   page.IsDeleted,
		Path:       page.Path,
	}
}
