package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	nethttp "net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/auth"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/config"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/datadir"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/edit"
	wiki "github.com/popinjayjohn/dine-and-dash-semiplane/internal/http"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/index"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/logfmt"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/sse"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/vault"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/version"
)

// serveOptions is what `wiki serve` takes.
type serveOptions struct {
	dataDir  string
	streams  int
	prod     bool
	noWatch  bool
	debounce time.Duration

	// addr, baseURL and trustedProxies are the three settings config.yaml holds, and
	// they are resolved in [runServe] rather than defaulted here, because a flag
	// default is a *precedence decision*: an empty default is "I did not choose",
	// which is what lets the environment and the file have a say.
	addr           string
	baseURL        string
	trustedProxies string

	// lan, certFile and keyFile are the LAN path, and `--lan` is a **first-class
	// deployment** rather than a debugging flag: ADR 0011 says so, and
	// `docs/security.md` lists self-signed TLS as the control for "the network, on a
	// LAN". A DM playing at a table on somebody else's wifi is the case this
	// application exists for, and it is not the case that works least well by
	// default.
	lan      bool
	certFile string
	keyFile  string

	// logFormat is the encoding the log is written in: `text` or `json`, and the
	// empty string is the environment's decision or the default's.
	//
	// It is a field rather than a `--log-format` on the shared parse because
	// `logfmt.Resolve` is where the precedence lives and a command that resolved it
	// itself would be a third place with its own idea of the order.
	logFormat string
}

// firstNonEmpty is the flag-over-settings precedence, in one place.
//
// It is the same three-rule shape as `internal/config.firstNonEmpty` and it is a
// separate function because the *sources* differ: this one is the flag against a
// resolved setting, and that one is the environment against a file against a default.
// Two rules, one each, rather than one rule with four sources and a caller that has
// to know which order to pass them in.
func firstNonEmpty(flag, setting string) string {
	if flag != "" {
		return flag
	}
	return setting
}

// splitProxies is the `--trusted-proxies` flag's value as a list.
//
// The flag is comma-separated and the file is a YAML list, for the reason
// `internal/config.resolveProxies` gives: each is the shape a person writes that form
// in. An empty item between two commas is dropped rather than becoming an empty
// trusted address, which would match nothing and read as a mistake in the log.
func splitProxies(flag string) []string {
	items := strings.Split(flag, ",")
	proxies := make([]string, 0, len(items))
	for _, one := range items {
		if trimmed := strings.TrimSpace(one); trimmed != "" {
			proxies = append(proxies, trimmed)
		}
	}
	return proxies
}

// defaultAddr is loopback and not `0.0.0.0`, deliberately.
//
// This is a wiki on a DM's own machine, and the default of every Go HTTP server is
// every interface -- which is the right default for a service and the wrong one
// for a thing holding a campaign's secrets with no authentication in front of it
// but a share link. A DM who wants it on their LAN types `--addr 0.0.0.0:8080`,
// which is a decision they make rather than one they inherit.
const defaultAddr = "127.0.0.1:8080"

// defaultDebounce is how long the watcher waits for a directory to stop changing
// before it syncs.
//
// It is longer than the reindex debounce's zero and shorter than a person typing,
// because the thing being watched here is a DM editing markdown in Obsidian, and
// Obsidian's save is several writes: a temp file, a rename, a metadata touch. A
// watcher with no debounce syncs three times for one keystroke, and every one of
// those syncs pushes a fresh frame to every open page.
const defaultDebounce = 400 * time.Millisecond

// gracefulTimeout is how long a shutdown waits for open requests.
//
// It is generous because a stream is an open request that never ends by design, and
// a short one would cut every live page in the campaign at the moment the DM
// restarted. The hub is closed first, which is what ends the streams, and by the
// time this timer is running there is nothing left to wait for but a slow page.
const gracefulTimeout = 5 * time.Second

// runServe is `wiki serve`: the HTTP server, the index watchers, and the lock that
// keeps two of either off one data directory.
func runServe(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	opts := &serveOptions{}
	flags.StringVar(&opts.dataDir, "data-dir", "", "the data directory ("+datadir.EnvVar+" overrides the default)")
	flags.StringVar(&opts.addr, "addr", "",
		"the address to listen on ("+config.EnvListen+" and config.yaml override the default)")
	flags.StringVar(&opts.baseURL, "base-url", "",
		"the origin share links are built against, as scheme and host ("+config.EnvBaseURL+
			" and config.yaml override it); defaults to the address served on")
	flags.BoolVar(&opts.lan, "lan", false,
		"serve on every interface, for playing at a table; implies --production and self-signed TLS")
	flags.StringVar(&opts.certFile, "tls-cert", "", "a certificate file for --lan, instead of a generated one")
	flags.StringVar(&opts.keyFile, "tls-key", "", "a key file for --lan, beside --tls-cert")
	flags.StringVar(&opts.trustedProxies, "trusted-proxies", "",
		"comma-separated proxy addresses whose X-Forwarded-For is believed ("+
			config.EnvTrustedProxies+" and config.yaml override it)")
	flags.IntVar(&opts.streams, "streams", wiki.DefaultStreams, "how many live page streams to hold open")
	flags.BoolVar(&opts.prod, "production", false,
		"mark the deployment as production: the session cookie gets Secure, which needs HTTPS")
	flags.BoolVar(&opts.noWatch, "no-watch", false,
		"do not watch the vaults for changes; `wiki sync` is the only way the index moves")
	flags.DurationVar(&opts.debounce, "debounce", defaultDebounce, "how long to wait for a vault to stop changing")
	flags.StringVar(&opts.logFormat, "log-format", "",
		"the log encoding, text or json ("+logfmt.EnvVar+" overrides the default)")

	if err := parseFlags(flags, args, "wiki serve"); err != nil {
		return err
	}
	if opts.streams < 1 {
		return errors.New("wiki serve: --streams must be at least 1")
	}

	format, err := logfmt.Resolve(opts.logFormat)
	if err != nil {
		return fmt.Errorf("wiki serve: %w", err)
	}
	encoding, err := logfmt.New(stderr, format, slog.LevelInfo)
	if err != nil {
		return fmt.Errorf("wiki serve: %w", err)
	}
	logger := slog.New(encoding)

	dir, err := datadir.Resolve(opts.dataDir)
	if err != nil {
		return fmt.Errorf("finding the data directory: %w", err)
	}

	// The three settings, with ADR 0011's precedence applied: the flag wins because a
	// DM who typed it meant it, the environment beats the file because a deployment
	// that sets it once should not set it on every subcommand, and the file beats
	// the default because it is the only one of the three a DM edits on purpose.
	settings, err := config.Load(dir)
	if err != nil {
		return err
	}
	// `--lan` is the one flag that implies another, and it implies the one that
	// matters: a session cookie without `Secure` on a network is a cookie every other
	// machine on the wifi can read. ADR 0003's attribute is not "on localhost".
	if opts.lan {
		opts.prod = true
	}

	addr := firstNonEmpty(opts.addr, settings.Listen)
	if opts.lan && !strings.Contains(addr, ":") || (opts.lan && isLoopbackAddr(addr)) {
		// A DM who typed `--lan` and left the default has asked for the LAN and got
		// loopback, which is the failure that looks like the feature not working. The
		// message says which of the two it was.
		addr = defaultLANAddr
	}
	baseURL := firstNonEmpty(opts.baseURL, settings.BaseURL)
	trustedProxies := settings.TrustedProxies
	if opts.trustedProxies != "" {
		trustedProxies = splitProxies(opts.trustedProxies)
	}

	s, err := openCampaignStore(ctx, dir)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	campaigns, err := campaignDirs(filepath.Join(dir, "vault"))
	if err != nil {
		return err
	}
	if len(campaigns) == 0 {
		return fmt.Errorf("no campaigns under %s; create one, or run `wiki sync` against a vault that has one", filepath.Join(dir, "vault"))
	}

	// The listener comes up before the index is read, so that the port a DM is
	// told about is a port that is already accepting connections. Binding late is
	// how a server prints an address and then refuses connections for two
	// seconds.
	// The listen config rather than `net.Listen` so that a Ctrl-C during the bind
	// is a Ctrl-C, not a socket that outlives the process that was asked to stop.
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}
	defer func() { _ = listener.Close() }()

	// The certificate, before the handler is built, because the origin share links are
	// built from it and a link with an `http` origin is a link a player's browser
	// will refuse before it ever reaches the redemption.
	lanConfig, err := lanTLS(ctx, opts, dir, stdout)
	if err != nil {
		_ = listener.Close()
		return err
	}

	hub := sse.NewHub(opts.streams)
	defer hub.Close()

	// Every campaign the server knows about is synced once at startup, so that a
	// fresh clone of a vault is servable without a second command. `wiki sync` is
	// still the way to do it deliberately; this is the server refusing to serve
	// an empty wiki and saying so.
	served, release, err := openCampaigns(ctx, s, dir, logger)
	if err != nil {
		return err
	}
	// The lock is held for as long as the process runs and released on the way
	// out. A server that died without releasing it leaves a lock that goes stale
	// after `staleLock` and is taken over then, which is right for a crash and
	// wrong for a Ctrl-C: a DM who stops the wiki to fix something and starts it
	// again should not have to wait two minutes.
	defer release()

	// The plugins, built once and handed to everything that takes a capability.
	// A failure here is a failure to start: a plugin that cannot register is a page
	// rendered without its contribution, and a DM has no way to tell that from a
	// plugin that was never written.
	registry, err := buildRegistry(s, logger)
	if err != nil {
		return err
	}

	handler, err := wiki.New(wiki.Config{
		Store:          s,
		Hooks:          registry.RenderHooks(),
		TrustedProxies: trustedProxies,
		Policies:       registry.Policies(),
		Routes:         registry.Routes(),
		Events:         registry.Events(),
		Redeemer: auth.Redeemer{
			Backend: s,
			Config:  authConfigFor(baseURLOf(baseURL, listener, opts.prod)),
		},
		// The writers this server already has open. `openCampaigns` holds a vault
		// per campaign for the life of the process, and an editor is a vault plus a
		// store plus a campaign -- so the table is the answer and opening a second
		// vault per campaign would be two handles on one directory.
		EditorFor: func(campaign domain.Campaign) (*edit.Editor, error) {
			for _, open := range served {
				if open.row.Slug == campaign.Slug {
					return edit.NewWith(open.syncer.Vault(), s, campaign, edit.Options{
						Hooks:    registry.RenderHooks(),
						Policies: registry.Policies(),
						Events:   registry.Events(),
					}), nil
				}
			}
			// A campaign the server was not started for: a DM has added a folder
			// since. It is opened on demand rather than refused, because a wiki that
			// cannot serve a campaign somebody just added is a wiki that needs a
			// restart for every new session.
			opened, openErr := vault.Open(filepath.Join(dir, "vault", campaign.Slug.String()))
			if openErr != nil {
				return nil, fmt.Errorf("opening the vault of %s: %w", campaign.Slug, openErr)
			}
			return edit.NewWith(opened, s, campaign, edit.Options{
				Hooks:    registry.RenderHooks(),
				Policies: registry.Policies(),
				Events:   registry.Events(),
			}), nil
		},
		Hub:           hub,
		Logger:        logger,
		Version:       version.Get(),
		Now:           time.Now,
		Production:    opts.prod,
		AllowedOrigin: baseURLOf(baseURL, listener, opts.prod),
	})
	if err != nil {
		return err
	}

	server := &nethttp.Server{
		Handler: handler,
		// The timeouts are the standard library's advice and the standard
		// library's advice is about servers that serve a lot of different clients.
		// This one serves a handful of people at a table. The two that matter:
		//
		// - ReadHeaderTimeout stops a connection that opens and says nothing, which
		//   on a machine on a table's wifi is a phone that went to sleep.
		// - WriteTimeout is *not* set, because a stream is a response that never
		//   ends, and a WriteTimeout would cut every live page at exactly the
		//   timeout.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
		// Non-nil only for `--lan`, and the difference is the whole of that flag: a
		// nil TLSConfig serves plaintext, which is right on loopback and wrong on a
		// network. `docs/security.md` lists this as the control for "the network, on
		// a LAN", and a control that is a nil field is not one.
		TLSConfig: lanConfig,
	}

	// The signals, before anything is served. A Ctrl-C has to be a graceful
	// shutdown and not a kill, because a kill leaves a lock file behind and the
	// next start has to wait out the stale window before it can run.
	stopped, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	watcherCtx, stopWatchers := context.WithCancel(context.Background())
	var watchers sync.WaitGroup
	if !opts.noWatch {
		watchers.Add(len(served))
		for _, c := range served {
			go func(c openCampaign) {
				defer watchers.Done()
				watchVault(watcherCtx, c, opts, hub, logger)
			}(c)
		}
	}

	serveErr := make(chan error, 1)
	go func() {
		// `ServeTLS` rather than `Serve` plus a wrapped listener, because
		// `ServeTLS` is the one that loads the certificate onto the server's own
		// `TLSConfig` and answers `https://` on the same listener, so a `--lan`
		// server and a loopback server are one code path rather than two.
		//
		// The certificate is already in `server.TLSConfig`, so the second argument
		// is empty -- and passing the files here as well would be the older way of
		// saying the same thing with a second chance to get it wrong.
		if lanConfig != nil {
			serveErr <- server.ServeTLS(listener, "", "")
			return
		}
		serveErr <- server.Serve(listener)
	}()

	// The URL, and the scheme it is actually served on. Printing `http://` for a
	// `--lan` server would be a link that does not work, and the DM would find that
	// out from a player at the table rather than from this line.
	scheme := "http"
	if lanConfig != nil {
		scheme = "https"
	}
	fmt.Fprintf(stdout, "wiki: serving %s on %s://%s\n", dir, scheme, listener.Addr())

	if opts.lan {
		// **The warning, before the server is announced.** `docs/security.md` is
		// honest that "click through the warning is a poor security story", and the
		// thing a DM needs at that moment is which warning, and what it should say to
		// the three people about to open it.
		fmt.Fprintf(stdout, `wiki: --lan is serving every interface with a self-signed certificate.
wiki:   players will see a browser warning. That is expected, and the certificate
wiki:   above is the one to check. The alternative is plaintext over the network,
wiki:   which is not a trade worth making with a campaign on it.
`)
	}
	fmt.Fprintf(stdout, "wiki: %d campaign(s); share links start at http://%s/c/<campaign>/?k=<token>\n",
		len(served), listener.Addr())
	if !opts.prod {
		fmt.Fprintf(stdout, "wiki: not a production deployment, so the session cookie is not Secure; pass --production behind HTTPS\n")
	}

	select {
	case err := <-serveErr:
		stopWatchers()
		watchers.Wait()
		if errors.Is(err, nethttp.ErrServerClosed) {
			return nil
		}
		return err

	case <-stopped.Done():
		fmt.Fprintf(stdout, "wiki: shutting down\n")
	}

	// The order of the shutdown is the reverse of the startup, and the first step
	// is the one that matters: the hub is closed, which ends every open stream, so
	// `Shutdown` has nothing long-running left to wait for.
	hub.Close()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), gracefulTimeout)
	defer cancel()
	shutdownErr := server.Shutdown(shutdownCtx)

	stopWatchers()
	watchers.Wait()

	return shutdownErr
}

// openCampaign is one campaign the server is serving: its row, its vault and the
// syncer the watcher drives.
type openCampaign struct {
	row    domain.Campaign
	vault  string
	syncer *index.Syncer
}

// openCampaigns reads every campaign's vault into the index and returns what the
// server will serve.
//
// A campaign that cannot be opened is a warning rather than a refusal: a DM with
// two campaigns and one unreadable directory still gets to play the other one, and
// a server that refuses to start because of a directory it was told about is a
// server that is hard to get running at all.
// It returns the campaigns and the function that releases everything it took: the
// per-campaign write locks, and the vault handles behind them. A caller that drops
// the second return value has leaked a lock, which is the kind of leak that only
// shows up as a confusing error two minutes later.
func openCampaigns(ctx context.Context, s *store.Store, dir string, logger *slog.Logger) ([]openCampaign, func(), error) {
	dirs, err := campaignDirs(filepath.Join(dir, "vault"))
	if err != nil {
		return nil, nil, err
	}

	var (
		opened   []openCampaign
		releases []func()
		skipped  int
	)
	for _, path := range dirs {
		slug, err := domain.NewSlug(filepath.Base(path))
		if err != nil {
			logger.Warn("a directory under vault/ is not a campaign name, so it is not served",
				slog.String("path", path), slog.String("error", err.Error()))
			skipped++
			continue
		}

		campaign, release, err := syncerFor(ctx, s, path, slug, filepath.Join(dir, "locks"), false)
		if err != nil {
			logger.Warn("a campaign could not be opened, so it is not served",
				slog.String("campaign", slug.String()), slog.String("error", err.Error()))
			skipped++
			continue
		}

		// The initial sync, so that a vault which has never been synced is
		// servable. A failure here is reported and the campaign is still served:
		// the watcher will pick the vault up as soon as a file changes, and a
		// server that refuses to start over one unreadable file is worse.
		report, syncErr := campaign.Sync(ctx)
		if syncErr != nil {
			logger.Warn("the first sync of a campaign failed; serving what is there",
				slog.String("campaign", slug.String()), slog.String("error", syncErr.Error()))
		} else {
			logger.Info("campaign indexed",
				slog.String("campaign", slug.String()),
				slog.Int("indexed", len(report.Indexed)),
				slog.Int("unchanged", report.Unchanged),
				slog.Int("passes", report.Passes))
		}

		opened = append(opened, openCampaign{row: campaign.Campaign(), vault: path, syncer: campaign})
		releases = append(releases, release)
	}

	releaseAll := func() {
		for _, releaseOne := range releases {
			releaseOne()
		}
	}

	if len(opened) == 0 {
		// Nothing was opened, so everything that was taken on the way to finding
		// that out has to go back.
		releaseAll()
		return nil, nil, fmt.Errorf("none of the %d director(ies) under vault/ could be opened as a campaign", skipped)
	}

	// The locks stay held for as long as the server runs, which is the point: one
	// server per data directory (ADR 0011) and one writer per campaign. A process
	// that is killed rather than stopped leaves a lock that goes stale after
	// `staleLock` and is taken over then, which is the right answer for a crash.
	return opened, releaseAll, nil
}

// watchVault is the index watcher, which is the reason a page being read in a
// browser updates when the DM saves it in Obsidian.
//
// Every campaign gets its own goroutine and its own watcher, and a watcher that
// fails -- a directory that was deleted, an inotify limit -- logs and returns
// rather than taking the server with it. A wiki whose live updates stopped is still
// a wiki that can be read, and that is the right thing to be left with.
func watchVault(ctx context.Context, c openCampaign, opts *serveOptions, hub *sse.Hub, logger *slog.Logger) {
	watcher, err := index.Watch(c.syncer, opts.debounce)
	if err != nil {
		logger.Error("watching a vault for changes",
			slog.String("campaign", c.row.Slug.String()),
			slog.String("vault", c.vault),
			slog.String("error", err.Error()))
		return
	}
	defer func() { _ = watcher.Close() }()

	logger.Info("watching a vault for changes",
		slog.String("campaign", c.row.Slug.String()),
		slog.String("vault", c.vault),
		slog.Duration("debounce", opts.debounce))

	err = watcher.Run(ctx, func(report index.Report) {
		if report.Changed() == 0 {
			// A sync that changed nothing is the common case and it is not a
			// reason to send every open page a fresh copy of itself.
			return
		}

		logger.Info("indexed",
			slog.String("campaign", c.row.Slug.String()),
			slog.Int("indexed", len(report.Indexed)),
			slog.Int("archived", len(report.Archived)),
			slog.Int("passes", report.Passes))

		// The pages whose rows moved, and only those, become pushes. An archived
		// page is in the list too: a reader looking at a page the DM has just
		// deleted should be told, and their stream's own read will find it gone
		// and end rather than leaving them with the last copy they were allowed
		// to see.
		wiki.PageChanged(hub, c.row.ID, append(report.Indexed, report.Archived...)...)
	}, func(watchErr error) {
		logger.Error("watching a vault",
			slog.String("campaign", c.row.Slug.String()),
			slog.String("error", watchErr.Error()))
	})

	if ctx.Err() == nil && err != nil {
		logger.Error("a watcher stopped", slog.String("campaign", c.row.Slug.String()), slog.String("error", err.Error()))
	}
}

// baseURLOf is the origin share links are built against, which is configuration
// and never a request header: the whole question is "the host I would have written
// a link into", and a `Host` the caller chose answers nothing.
func baseURLOf(baseURL string, listener net.Listener, production bool) string {
	if baseURL != "" {
		return strings.TrimSuffix(baseURL, "/")
	}

	address := listener.Addr().String()
	if host, port, err := net.SplitHostPort(address); err == nil {
		// A wildcard bind is not a link a player can open, so it is spelled as
		// localhost -- which is what a DM on the machine meant, and the only answer
		// that is certainly right without asking them.
		if host == "" || host == "::" || host == "0.0.0.0" {
			host = "localhost"
		}
		address = net.JoinHostPort(host, port)
	}

	scheme := "http"
	if production {
		// Behind a TLS-terminating proxy the scheme is the proxy's, and the flag
		// that says so is the same flag that says the cookie is Secure.
		scheme = "https"
	}
	return scheme + "://" + address
}

// authConfigFor is `auth`'s configuration for this deployment.
//
// Thirty days for both the link and the session, and the same number for both,
// because they are one policy: how long does somebody's access last without the DM
// doing something. A DM can shorten it in the vault's configuration later; a
// number here is a starting point, not a policy.
func authConfigFor(baseURL string) auth.Config {
	return auth.Config{
		BaseURL:         baseURL,
		Now:             time.Now,
		LinkExpiry:      30 * 24 * time.Hour,
		SessionLifetime: 30 * 24 * time.Hour,
	}
}
