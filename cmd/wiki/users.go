package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/auth"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/config"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/datadir"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
)

// `wiki users` is the M9 buttons' command form, and the reason it exists at all is
// the deployment shape rather than convenience.
//
// M9 shipped "New player link" and "Revoke" as buttons in the DM's own browser,
// which is the right way to do it and is **the only way it could be done**: the
// plaintext token exists exactly once, in the page that mints it, and a command that
// printed it to a terminal would be printing a credential to a place it is far more
// likely to be read from. The buttons stay; this is for the three cases they cannot
// reach:
//
//   - **a scripted onboarding**, where a DM's first session with a new player is
//     `wiki users new <name> --role player` rather than opening a browser;
//   - **a headless box**, which is what `wiki serve --lan` plus a Dockerfile is; and
//   - **a link that has to be handed over out of band**, where pasting a page is not
//     possible and the token has to travel through something the DM controls.
//
// The last case is why the token is printed and why the command says so. Printing a
// credential to stdout is a hazard the HTTP page does not have — the page is
// `no-store` and this is not — so the command says it once, loudly, and does not offer
// to put it in a file or an environment variable.

// runUsers is the dispatcher for `wiki users <new|revoke|list>`, and it is separate
// from the top-level dispatch because a command with subcommands is a different shape
// and pretending otherwise gives every subcommand a prefix.
func runUsers(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError{name: "users", flags: flagsForSubcommandHelp(stderr)}
	}

	switch args[0] {
	case "new":
		return runUsersNew(ctx, args[1:], stdout, stderr)
	case "revoke":
		return runUsersRevoke(ctx, args[1:], stdout, stderr)
	case "list":
		return runUsersList(ctx, args[1:], stdout, stderr)
	case "-h", "--help", "help":
		_, err := io.WriteString(stdout, `usage: wiki users <command>

  new <name>      mint a share link and print it once
  revoke <id>     end a principal's sessions and stop their link working
  list            list principals, by id, with their role and label

A share link is a credential. "wiki users new" prints the plaintext because
that is the only copy that will ever exist, and it is not in any log.
`)
		return err
	default:
		return fmt.Errorf("wiki users: %q is not one of new, revoke or list", args[0])
	}
}

// runUsersNew mints a link and prints it, once.
func runUsersNew(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("users new", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	opts := &newUserOptions{baseURL: ""}
	flags.StringVar(&opts.dataDir, "data-dir", "", "the data directory ("+datadir.EnvVar+" overrides the default)")
	flags.StringVar(&opts.role, "role", string(domain.RolePlayer),
		"the role this principal has: dm or player")
	flags.StringVar(&opts.campaign, "campaign", "", "the campaign, by slug")
	flags.StringVar(&opts.baseURL, "base-url", "",
		"the origin to build the link against; the address `wiki serve` was given, by default")
	flags.DurationVar(&opts.expiry, "expiry", 0,
		"how long the link stays valid, as 720h or 30d; 0 is auth's default")

	if err := parseFlagsAllowing(flags, args, "wiki users new", 1); err != nil {
		return err
	}

	label := strings.TrimSpace(flags.Arg(0))
	if label == "" {
		return errors.New("wiki users new: a link needs a name for the person it is for")
	}

	role := domain.Role(strings.TrimSpace(opts.role))
	if !role.Valid() {
		return fmt.Errorf("wiki users new: %q is not a role: it is dm or player", opts.role)
	}

	dir, err := datadir.Resolve(opts.dataDir)
	if err != nil {
		return fmt.Errorf("finding the data directory: %w", err)
	}

	// **The campaign before the origin**, and the order is what a DM debugging a
	// mistyped command wants: "no campaign called that" is a more basic answer than
	// "you have not configured an origin", and a command that reports the second for
	// a mistake of the first kind sends somebody to edit a config file that was fine.
	campaign, s, err := openOneCampaignIn(ctx, dir, opts.campaign)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	// A share link needs an origin, and there is **no default for it**: a link built
	// with no origin is a path a player cannot open, and a command that printed one
	// anyway would look like it worked. `--base-url` beats the setting because a DM
	// who typed it meant it.
	settings, err := config.Load(dir)
	if err != nil {
		return err
	}

	baseURL := firstNonEmpty(opts.baseURL, settings.BaseURL)
	if baseURL == "" {
		return errors.New("wiki users new: a share link needs an origin, and there is " +
			"none configured: pass --base-url, set " + config.EnvBaseURL +
			", or put base_url in " + config.FileName)
	}

	authCfg := authConfigFor(baseURL)
	if opts.expiry > 0 {
		authCfg.LinkExpiry = opts.expiry
	}

	issued, err := auth.Minter{Backend: s, Config: authCfg}.Issue(ctx, campaign, role, label)
	if err != nil {
		return fmt.Errorf("minting a link for %s: %w", label, err)
	}

	// The same audit row the HTTP route writes, because the fact a DM needs when a
	// player says "my link stopped working" is that the link was issued, and the
	// command is a way to issue one. A row written by the button and not by the
	// command would be an audit log with a hole exactly where the scripted path is.
	if _, auditErr := s.AppendAudit(ctx, domain.AuditEntry{
		CampaignID:  campaign.ID,
		PrincipalID: issued.Principal.ID,
		Action:      auditActionIssued,
		At:          time.Now().UTC(),
		Detail:      label,
	}); auditErr != nil {
		return fmt.Errorf("recording a link that was issued: %w", auditErr)
	}

	link := authCfg.BaseURL + "/c/" + campaign.Slug.String() + "/?k=" + issued.Token.Hex()

	// The plaintext, and the warning, in that order. The warning is on stderr so that
	// `wiki users new … > link.txt` writes only the link, which is the thing a DM
	// redirecting to a file actually wants, and `2>/dev/null` still says it.
	_, err = fmt.Fprintf(stderr,
		"wiki: this link is shown once and is not in any log. Its principal is %s.\n",
		issued.Principal.ID)
	if err != nil {
		return err
	}

	_, err = fmt.Fprintln(stdout, link)
	return err
}

// runUsersRevoke ends a principal's sessions and stops their link.
func runUsersRevoke(ctx context.Context, args []string, stdout, _ io.Writer) error {
	flags := flag.NewFlagSet("users revoke", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	opts := &revokeOptions{}
	flags.StringVar(&opts.dataDir, "data-dir", "", "the data directory ("+datadir.EnvVar+" overrides the default)")
	flags.StringVar(&opts.campaign, "campaign", "", "the campaign, by slug")

	if err := parseFlagsAllowing(flags, args, "wiki users revoke", 1); err != nil {
		return err
	}

	campaign, s, err := openOneCampaign(ctx, opts.dataDir, opts.campaign)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	id := strings.TrimSpace(flags.Arg(0))

	// **Two things, in this order, and the order is the point.** The *sessions* end
	// first, so a player who is already reading loses access immediately; the *link*
	// stops working second, so a player who has not opened the wiki since cannot open
	// it at all. Doing only the second would leave a revoked player reading for as
	// long as their cookie lasts, which is the failure a DM revoking somebody at the
	// table is actually trying to avoid.
	rotated, err := auth.Redeemer{Backend: s, Config: auth.Config{Now: time.Now}}.
		Rotate(ctx, domain.Principal{ID: id, CampaignID: campaign.ID}, auth.RotatedForRoleChange)
	if err != nil {
		return fmt.Errorf("revoking %s: %w", id, err)
	}
	if revokeErr := s.RevokePrincipal(ctx, id); revokeErr != nil {
		return fmt.Errorf("revoking %s: %w", id, revokeErr)
	}

	_, err = fmt.Fprintf(stdout,
		"revoked %s: %d session(s) ended, and their link no longer works\n", id, rotated)
	return err
}

// runUsersList is the three principals' worth of information a DM needs when somebody
// says their link stopped working.
func runUsersList(ctx context.Context, args []string, stdout, _ io.Writer) error {
	flags := flag.NewFlagSet("users list", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	opts := &listUsersOptions{}
	flags.StringVar(&opts.dataDir, "data-dir", "", "the data directory ("+datadir.EnvVar+" overrides the default)")
	flags.StringVar(&opts.campaign, "campaign", "", "the campaign, by slug")

	if err := parseFlags(flags, args, "wiki users list"); err != nil {
		return err
	}

	campaign, s, err := openOneCampaign(ctx, opts.dataDir, opts.campaign)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	principals, err := s.ListPrincipals(ctx, campaign.ID)
	if err != nil {
		return fmt.Errorf("listing the principals of %s: %w", campaign.Slug, err)
	}

	if len(principals) == 0 {
		_, err := fmt.Fprintf(stdout, "No principals in %s yet. `wiki users new <name>` makes one.\n",
			campaign.Slug)
		return err
	}

	for _, principal := range principals {
		state := "active"
		if !principal.RevokedAt.IsZero() {
			state = "revoked"
		}
		if _, err := fmt.Fprintf(stdout, "%-38s %-7s %-9s %s\n",
			principal.ID, principal.Role.String(), state, principal.Label); err != nil {
			return err
		}
	}
	return nil
}

// auditActionIssued is the audit action for a link being minted from the command
// line, and it is **the same string the HTTP button writes**.
//
// Sharing the constant is the point: a DM whose player's link stopped working reads
// the audit log, and a row written by the command and not by the button would be a
// hole exactly where the scripted path is — the one nobody clicked.
const auditActionIssued = "share_link_issued"

// newUserOptions is what `wiki users new` takes.
type newUserOptions struct {
	dataDir  string
	role     string
	baseURL  string
	expiry   time.Duration
	campaign string
}

// revokeOptions is what `wiki users revoke` takes.
type revokeOptions struct {
	dataDir  string
	campaign string
}

// listUsersOptions is what `wiki users list` takes.
type listUsersOptions struct {
	dataDir  string
	campaign string
}

// openOneCampaign is the data directory and one campaign in it, which is the shape
// every command that touches a campaign needs.
//
// It is a function because four commands need it and the "which campaign" argument is
// the one a user gets wrong: a command that defaulted it to the first one would
// revoke somebody in the wrong campaign without saying so.
func openOneCampaign(
	ctx context.Context, dataDir, slug string,
) (domain.Campaign, *store.Store, error) {
	dir, err := datadir.Resolve(dataDir)
	if err != nil {
		return domain.Campaign{}, nil, fmt.Errorf("finding the data directory: %w", err)
	}

	return openOneCampaignIn(ctx, dir, slug)
}

// openOneCampaignIn is [openOneCampaign] for a data directory the caller has already
// resolved, which is the shape `users new` needs because it reads `config.yaml` — and
// reading a config file needs the directory before the store is open.
func openOneCampaignIn(
	ctx context.Context, dir, slug string,
) (domain.Campaign, *store.Store, error) {
	if strings.TrimSpace(slug) == "" {
		return domain.Campaign{}, nil, errors.New("which campaign: pass --campaign <slug>")
	}

	s, err := openCampaignStore(ctx, dir)
	if err != nil {
		return domain.Campaign{}, nil, err
	}

	// **An existing campaign, and never a new one.** `campaignFor` creates a row for
	// a folder that is not in the database, because `wiki sync` is told to read a
	// vault and a vault is a thing a DM can make by creating a folder. A command
	// that *revokes* somebody has the opposite relationship with a missing row: the
	// principal being revoked is in a campaign that exists, and a campaign that does
	// not exist is a typo. So the lookup refuses rather than creating, and a
	// `wiki users revoke` against a mistyped slug changes nothing at all.
	parsed, err := domain.NewSlug(slug)
	if err != nil {
		_ = s.Close()
		return domain.Campaign{}, nil, fmt.Errorf("%q is not a campaign name: %w", slug, err)
	}

	campaign, err := s.CampaignBySlug(ctx, parsed)
	if err != nil {
		_ = s.Close()
		return domain.Campaign{}, nil, fmt.Errorf("no campaign %q in %s", slug, dir)
	}

	return campaign, s, nil
}
