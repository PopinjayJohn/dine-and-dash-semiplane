// Package edit is the writer.
//
// # What it is for
//
// Everything else in this project reads files. This package is the one that
// writes them, and M9 is the milestone that introduced it — the editor, the
// renamer, the restorer and the archiver all go through here, and so does
// anything that later needs to write a page programmatically.
//
// # The order of a save, and why it is this order
//
//  1. check the path is a path a page can have;
//  2. read the file that is there now;
//  3. compare its hash with the one the caller last saw, and refuse on a
//     mismatch;
//  4. ask the store's write gate about the content being saved, before any of it
//     is a file;
//  5. write the file, atomically;
//  6. re-derive the row through `internal/index`, as the caller;
//  7. record the previous text as a revision, in the database and in
//     `_history`.
//
// Steps 4 and 5 are in that order because the watcher writes rows as the DM. A
// player's file on disk is a file the watcher will index, as the DM, into a page
// the gate refused — so the gate has to run while the content is still bytes, and
// the file has to be the last thing that changes. A save that wrote first and
// rolled back would be a window rather than a guarantee, and the window is exactly
// as long as the next filesystem event takes to arrive.
//
// Step 6 is the same derivation a sync does, by the same function, so a row
// written by an editor and a row written by a watcher cannot be two different
// derivations of one file. That is the whole reason step 6 exists instead of a
// call to `UpsertPage`.
//
// # The conflict check is a hash, and it is the ETag
//
// A caller says which text it was looking at, as the content hash of the file it
// was served. That is the same string as the HTTP `ETag` on the page, and it is
// the right thing to compare because it is a fact about the file rather than about
// the row: a row can be stale and a hash cannot, and the file is the truth
// (ADR 0001).
//
// The comparison is not the interesting part. The interesting part is what the
// caller can do with a mismatch, and that is `Versions`: the three texts a
// three-way diff needs, fetched on demand rather than carried on the error, so
// that a save that is not in conflict does not pay for a diff nobody looks at.
package edit

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/access"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/index"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/render"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/vault"
)

// Editor writes pages in one campaign.
//
// It is one per campaign rather than one per server, because a page is written
// through a vault, and a vault is a directory.
type Editor struct {
	vault    *vault.Vault
	store    *store.Store
	campaign domain.Campaign
	sync     *index.Syncer

	// hooks are the plugins' render hooks, and they are here so that a preview is
	// the page. A preview that rendered without a plugin's contribution would be a
	// preview that disagreed with the save it precedes, which is the one thing a
	// preview must not do.
	hooks render.Hooks

	// policies are the plugins' access rules, and they are asked here rather than
	// only in the HTTP layer because a save is a POST and a POST is a thing a
	// player can send without ever loading a form.
	//
	// The store's write gate is the invariant and it is still asked -- this is a
	// second, stricter layer, never a looser one. `CheckWritableContentAs` answers
	// "is this content allowed here"; a policy answers "is this principal allowed
	// here at all", which is a different question and the one a plugin has.
	policies *access.Policies

	// renderers is one renderer for this campaign's previews, built on first use
	// for the same reason the HTTP layer keeps one per campaign: a renderer holds a
	// link resolver and a resolver belongs to a campaign.
	renderersMu sync.Mutex
	renderers   map[domain.Slug]*render.Renderer
}

// New returns an editor for one campaign. The syncer is built here rather than
// taken, because it is the editor's own view of the index: one derivation, one
// campaign, and a caller cannot pair an editor with another campaign's syncer by
// accident.
func New(v *vault.Vault, s *store.Store, campaign domain.Campaign) *Editor {
	return NewWith(v, s, campaign, Options{})
}

// NewWith is [New] with the plugins' capabilities, for the caller that has them.
func NewWith(v *vault.Vault, s *store.Store, campaign domain.Campaign, opts Options) *Editor {
	return &Editor{
		vault:     v,
		store:     s,
		campaign:  campaign,
		sync:      index.New(v, s, campaign),
		hooks:     opts.Hooks,
		policies:  opts.Policies,
		renderers: map[domain.Slug]*render.Renderer{},
	}
}

// Campaign is the campaign this editor writes in.
func (e *Editor) Campaign() domain.Campaign { return e.campaign }

// Save is one request to write a page.
//
// The fields are the six things a save needs and no more, and each of them is
// there because a save without it would be guessing.
type Save struct {
	// Path is the page's identity, without an extension. A save may not change
	// it: that is `Rename`, and it is a separate operation because a rename has to
	// rewrite inbound links and a save does not.
	Path string

	// Markdown is the whole file: frontmatter and body, as the DM would have it on
	// disk. Not the body alone, and not a struct of fields — a save that took
	// fields would have to decide what to do with the keys it does not understand,
	// and the only answer that does not lose a DM's own YAML is to take their
	// bytes.
	Markdown string

	// Expect is the content hash of the file the caller was looking at, and it is
	// the ETag the page was served with. A mismatch is a conflict and nothing is
	// written.
	//
	// It is required for an update and must be empty for a create, and the two
	// cases are told apart by `Creating` rather than by the emptiness of this
	// field — a caller that forgot the ETag should be refused, not mistaken for
	// somebody creating a page.
	Expect string

	// Creating says this is a new page, which is a different request: there is
	// nothing to conflict with, and a page that is already there is a conflict
	// rather than an update.
	Creating bool

	// Message is the revision's message, and it is optional because a player
	// saving their own notes is not writing a changelog.
	Message string

	// As is who is writing. There is no default: a writer that defaulted to
	// anything would be a writer that could be left out of a call.
	As domain.Principal
}

// ErrConflict says the page is not what the caller last saw.
//
// It is a named error and not a type carrying the three texts, and the reason is
// cost: a save that is not in conflict does not pay for a diff nobody looks at.
// A caller that gets this calls `Versions` and gets the three texts, and the
// conflict is then a *view* rather than an error that every save has to build.
var ErrConflict = errors.New("edit: the page changed since you last saw it")

// ErrNoSuchPage says there is no file to save over. It is distinct from
// `ErrConflict` because the fix is different: a create, rather than a refresh.
var ErrNoSuchPage = errors.New("edit: there is no page at that path")

// Save writes a page's markdown and brings the index into step with it.
//
// It returns the row as it now is, and the revision number the *previous* text was
// kept under — which is zero for a create, because a page that did not exist has
// no previous text to keep.
//
// The revision number is returned rather than left to be looked up because a save's
// caller is a person being told what happened, and "saved as revision 4" is the
// sentence. It is a second return value rather than a struct because the row is
// the important half and a caller that wants a third thing can ask the store.
func (e *Editor) Save(ctx context.Context, in Save) (page domain.Page, previousRev int, err error) {
	checked, err := vault.CheckPagePath(in.Path)
	if err != nil {
		return domain.Page{}, 0, fmt.Errorf("saving %s: %w", in.Path, err)
	}

	current, currentHash, exists, err := e.read(checked)
	if err != nil {
		return domain.Page{}, 0, err
	}

	if conflictErr := e.checkConflict(in, checked, currentHash, exists); conflictErr != nil {
		return domain.Page{}, 0, conflictErr
	}

	// The gate, asked about the content while it is still bytes. See the package
	// comment for why it cannot come after the write.
	if gateErr := e.sync.CheckWritableContentAs(ctx, checked, []byte(in.Markdown), in.As); gateErr != nil {
		return domain.Page{}, 0, fmt.Errorf("saving %s: %w", checked, gateErr)
	}

	doc, err := vault.Parse([]byte(in.Markdown))
	if err != nil {
		// Unreachable in practice: the gate's derivation parsed the same bytes. It
		// is here because a parse that fails is a DM's typo and the message has to
		// be the parse's.
		return domain.Page{}, 0, fmt.Errorf("saving %s: %w", checked, err)
	}

	// The previous text becomes a revision *before* it is overwritten, and the
	// number comes from the store so that the database and `_history` agree about
	// the order of a page's history. Zero means there was no previous text.
	//
	// **Not when the text is the same.** Re-saving a page without changing it is
	// what an editor does when somebody opens it and types a space and takes it
	// back, and a history of identical copies is a history nobody can read and
	// nobody can restore from. A save that changes nothing still goes through the
	// gate and still writes the file; it just does not grow the history.
	previousRev = 0
	if exists && currentHash != vault.Hash([]byte(in.Markdown)) {
		stored, getErr := e.store.GetPage(ctx, e.campaign.ID, checked, store.AsDM(e.campaign.ID))
		if getErr != nil {
			return domain.Page{}, 0, fmt.Errorf("saving %s: looking it up to keep its history: %w", checked, getErr)
		}
		previousRev, err = e.keep(ctx, stored, current, currentHash, in)
		if err != nil {
			return domain.Page{}, 0, err
		}
	}

	if writeErr := e.vault.Write(checked, doc); writeErr != nil {
		return domain.Page{}, 0, fmt.Errorf("saving %s: %w", checked, writeErr)
	}

	// The row, by the same derivation the watcher uses, as the caller. The gate
	// runs again in here and the only thing that can have changed since step 4 is
	// somebody else's save of the same page, which is a conflict this call loses.
	if _, syncErr := e.sync.SyncPathAs(ctx, checked, in.As); syncErr != nil {
		return domain.Page{}, 0, fmt.Errorf("saving %s: %w", checked, syncErr)
	}

	page, err = e.store.GetPage(ctx, e.campaign.ID, checked, store.AsDM(e.campaign.ID))
	if err != nil {
		return domain.Page{}, previousRev, fmt.Errorf("saving %s: reading it back: %w", checked, err)
	}
	return page, previousRev, nil
}

// checkConflict is the whole of the optimistic-concurrency rule, and it is one
// function so that the four cases cannot disagree.
//
// The cases, and what each is a conflict *about*:
//
//   - creating and the file is there: somebody else made this page. A create is
//     never an update, so the caller's "I am making a new page" is wrong.
//   - not creating and the file is not there: somebody archived it. The fix is to
//     create it, which is a different request.
//   - not creating and the hash differs: somebody saved it. This is the case the
//     ETag exists for, and the three-way diff is what resolves it.
//   - otherwise: fine.
func (e *Editor) checkConflict(in Save, path, currentHash string, exists bool) error {
	switch {
	case in.Creating && exists:
		return fmt.Errorf("%w: %s is already a page, so this is an update and not a create", ErrConflict, path)
	case !in.Creating && !exists:
		return fmt.Errorf("%w: %w, so it has to be created rather than saved", ErrConflict, ErrNoSuchPage)
	case in.Creating:
		return nil
	case in.Expect == "":
		// A caller that did not say what it was looking at has not said whether it
		// would mind an overwrite. Refusing is the direction where nothing is
		// destroyed, and the error says how to fix it.
		return fmt.Errorf("%w: no ETag was sent, so this save would overwrite whatever is there now", ErrConflict)
	case in.Expect != currentHash:
		return fmt.Errorf("%w: it is now %s and you last saw %s", ErrConflict, short(in.Expect), short(currentHash))
	default:
		return nil
	}
}

// read is the file that is there now, and its hash, and whether it is there.
func (e *Editor) read(path string) (text, hash string, exists bool, err error) {
	data, err := e.vault.ReadBytes(path)
	switch {
	case err == nil:
		return string(data), vault.Hash(data), true, nil
	case errors.Is(err, vault.ErrNotFound):
		return "", "", false, nil
	default:
		return "", "", false, fmt.Errorf("reading %s: %w", path, err)
	}
}

// keep records the previous text as a revision, in both histories.
//
// Two stores, on purpose, and the comment on `vault.ArchiveRevision` is the
// reason: the rows are this application's history and the files are the DM's, the
// files are the ones Obsidian's File Recovery panel reads, and a DM who has just
// lost an edit wants the one their editor shows them.
func (e *Editor) keep(ctx context.Context, page domain.Page, text, hash string, in Save) (int, error) {
	// No `CreatedAt`: the store stamps it, and it has the one clock a running
	// application has. A second clock here is a second answer to "what time is
	// it", and the two are the same to the minute only by luck.
	revision, err := e.store.AppendRevision(ctx, domain.PageRevision{
		PageID:            page.ID,
		Markdown:          text,
		ContentHash:       hash,
		AuthorPrincipalID: in.As.ID,
		Message:           in.Message,
	})
	if err != nil {
		return 0, fmt.Errorf("keeping the history of %s: %w", page.Path, err)
	}

	// The DM's own history, in the vault. A failure here is logged as a warning
	// rather than failing the save: the row is the authoritative history and the
	// file is the DM's convenience, so refusing to save a page because a
	// convenience could not be written is the wrong trade. The revision number is
	// the store's, so the two histories order a page's edits identically.
	previous, readErr := e.vault.Read(page.Path)
	if readErr != nil {
		// The page has just been read and is about to be overwritten, so this
		// cannot happen; and if it somehow does, the row is the history and the
		// save should go on.
		return revision.Rev, nil //nolint:nilerr // the row is the history; the file is the DM's convenience
	}
	if _, archiveErr := e.vault.ArchiveRevision(page.Path, revision.Rev, revision.CreatedAt, previous); archiveErr != nil {
		// Same trade, and it is a real cost: this application's history has the
		// text and Obsidian's File Recovery panel does not, until the next
		// successful save of this page. Failing the save would lose the *edit* to
		// keep a copy of the *old* one somewhere nicer, which is the wrong way
		// round.
		return revision.Rev, nil //nolint:nilerr // see above: the row is the history
	}

	return revision.Rev, nil
}

// Versions is the three texts a three-way diff needs, for a caller that has just
// been refused a save.
//
//   - base is the text the caller's edit was based on, found by the ETag it sent.
//     It is the empty string when this application has no revision with that hash,
//     which is the ordinary case for a page the DM wrote in Obsidian: there is no
//     base, and a diff that pretended otherwise would be inventing one.
//   - current is what the file says now.
//   - incoming is what the caller wanted to write.
//
// Three strings rather than a type carrying them, so that a package's public
// surface stays small, and because a caller that has been refused a save is a
// caller that is about to render a diff and has no use for a revision's id.
func (e *Editor) Versions(ctx context.Context, path, expect, incoming string) (base, current string, err error) {
	checked, err := vault.CheckPagePath(path)
	if err != nil {
		return "", "", fmt.Errorf("the three versions of %s: %w", path, err)
	}

	text, hash, exists, err := e.read(checked)
	if err != nil {
		return "", "", err
	}
	if !exists {
		return "", incoming, fmt.Errorf("%w: %w", ErrConflict, ErrNoSuchPage)
	}

	if expect != "" && expect != hash {
		base, err = e.revisionAt(ctx, checked, expect)
		if err != nil {
			return "", text, err
		}
	}
	return base, text, nil
}

// revisionAt is the text of the revision with a given content hash.
//
// An empty result and a nil error mean "this application has not kept that text",
// which is not a failure: the caller is going to say so in the diff, and an error
// here would be a page that cannot be saved because its history is incomplete.
func (e *Editor) revisionAt(ctx context.Context, path, hash string) (string, error) {
	stored, err := e.store.GetPage(ctx, e.campaign.ID, path, store.AsDM(e.campaign.ID))
	if err != nil {
		return "", fmt.Errorf("looking up %s: %w", path, err)
	}

	revisions, err := e.store.ListRevisions(ctx, stored.ID)
	if err != nil {
		return "", fmt.Errorf("listing the history of %s: %w", path, err)
	}

	// Newest first, because a hash that appears twice is two saves of the same
	// text and the later one is the base the caller was actually looking at.
	for i := len(revisions) - 1; i >= 0; i-- {
		if revisions[i].ContentHash == hash {
			return revisions[i].Markdown, nil
		}
	}
	return "", nil
}

// Read is the whole file as text, for the editor's textarea.
//
// It goes through the vault and not through the row, because the file is the
// source of truth (ADR 0001) and an editor that loaded `page.Frontmatter +
// page.Body` would be editing a reconstruction. The reconstruction is byte-identical
// today and will not be after the next change to either, and an editor that
// silently reformats a DM's file on save is the worst thing this application could
// do to somebody's vault.
func (e *Editor) Read(ctx context.Context, path string) (text string, hash string, err error) {
	checked, err := vault.CheckPagePath(path)
	if err != nil {
		return "", "", fmt.Errorf("reading %s: %w", path, err)
	}

	data, err := e.vault.ReadBytes(checked)
	if err != nil {
		return "", "", fmt.Errorf("reading %s: %w", checked, err)
	}
	return string(data), vault.Hash(data), nil
}

// Preview is what a page would render as, under a principal's decision, from
// content that is not a file yet.
//
// One function rather than a decision and a render done by the caller, and the
// reason is a bug this shape had: the caller had the whole file's bytes and the
// renderer wants the *body*, so a preview rendered the frontmatter as prose --
// an `<hr>` where the `---` fences were and a heading out of the `title:` line. The
// save stores `doc.Body()` and the page route renders that, so a preview built
// any other way is not a preview of the save.
//
// It writes nothing. A preview is a question and a save is an act, and a route
// that answered a question by doing the act would be a route a browser's prefetch
// could write to.
func (e *Editor) Preview(ctx context.Context, path string, markdown []byte, as domain.Principal) (render.Result, error) {
	checked, err := vault.CheckPagePath(path)
	if err != nil {
		return render.Result{}, fmt.Errorf("previewing %s: %w", path, err)
	}

	doc, err := vault.Parse(markdown)
	if err != nil {
		return render.Result{}, fmt.Errorf("previewing %s: %w", checked, err)
	}

	decision, err := e.sync.DecisionForContent(ctx, checked, markdown, as)
	if err != nil {
		return render.Result{}, fmt.Errorf("previewing %s: %w", checked, err)
	}

	return e.rendererFor(checked).Render(ctx, render.Page{
		Campaign:    e.campaign.Slug.String(),
		Path:        checked,
		Body:        doc.Body(),
		ContentHash: vault.Hash(markdown),
	}, decision)
}

// rendererFor is a renderer for this campaign, with the same link resolver the sync
// uses, so a preview resolves `[[links]]` the way the saved page will.
//
// A preview *is* the page, including the links in it, which is most of what makes
// one worth having: a DM writing a link to a page they have not written yet wants
// to know that the link is unresolved before they save, not after.
func (e *Editor) rendererFor(_ string) *render.Renderer {
	e.renderersMu.Lock()
	defer e.renderersMu.Unlock()

	if existing, found := e.renderers[e.campaign.Slug]; found {
		return existing
	}

	built := render.NewWith(render.Options{
		Links: index.NewResolver(e.store, e.campaign.ID),
		Hooks: e.hooks,
	})
	e.renderers[e.campaign.Slug] = built
	return built
}

// MayWrite answers whether a principal may write a page, for the Edit button.
//
// It is the same gate with the same derivation, asked about the file that is
// there — so the answer is about *this* page's current owner rather than about a
// guess, and a page whose owner is broken is an Edit button nobody is offered.
func (e *Editor) MayWrite(ctx context.Context, path string, as domain.Principal) error {
	checked, err := vault.CheckPagePath(path)
	if err != nil {
		return err
	}
	if err := e.policiesDenyEdit(ctx, checked, as); err != nil {
		return err
	}
	return e.sync.CheckWritableAs(ctx, checked, as)
}

// policiesDenyEdit asks the plugins whether this principal may write here, for a
// path that may not exist yet.
//
// It is asked *before* the store's gate and not instead of it, and the order is the
// same one `internal/http/policy_test.go` argues for: a plugin can only take a right
// away, so asking it first is asking a stricter question and the answer is the
// stricter of the two.
//
// A path that does not exist has no `PageMeta` to ask about, so the metadata is
// built from the path alone. A policy that narrows on a page's *stored* audience
// cannot narrow a page that has no row yet, which is the same limit the store's own
// gate has and the reason the two are asked together rather than one or the other.
func (e *Editor) policiesDenyEdit(ctx context.Context, path string, as domain.Principal) error {
	if e.policies.IsEmpty() {
		return nil
	}

	meta := access.PageMeta{Path: path}
	decided := access.For(access.PrincipalOf(as), meta)
	if e.policies.Apply(ctx, access.PrincipalOf(as), meta, decided).CanEdit {
		return nil
	}

	return store.ErrNotAllowed
}

// short is a content hash in a message. A full hash is 64 characters and a message
// with a 64-character identifier in it is a message nobody reads to the end.
func short(hash string) string {
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12] + "…"
}
