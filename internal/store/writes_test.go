package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
)

// The write side of the rights matrix, and the shape of these tests is different
// from the read side on purpose.
//
// A read that is too permissive is tested by looking for a page that should not
// come back. A write that is too permissive is tested by looking for a page that
// should not have been *created* — which means the assertion is about the
// filesystem as much as the database, because a refused write that still left a
// file behind would be a refusal in name only. The vault is the source of truth
// (ADR 0001) and the store is the gate in front of it, so the gate is what is
// being tested here and the file writing is M9's.

// writeFixture is a character page, a player bound to it, and a second player who
// is not.
func writeFixture(t *testing.T) (*store.Store, domain.Campaign, domain.Page, domain.Principal, domain.Principal) {
	t.Helper()

	ctx := context.Background()
	s := newStore(t)
	c := mustCreateCampaign(t, s)

	character, err := s.UpsertPage(ctx, domain.Page{
		CampaignID:  c.ID,
		Path:        "characters/aria",
		Title:       "Aria",
		Type:        domain.PageTypeCharacter,
		Visibility:  domain.VisibilityDMAndOwner,
		Frontmatter: "title: Aria\n",
		Body:        "A lockpicker.\n",
		ContentHash: "hash-of-aria",
	}, store.AsDM(c.ID))
	if err != nil {
		t.Fatalf("UpsertPage for the character: %v", err)
	}

	// A character page is its own owner, which is the second upsert.
	character, err = s.UpsertPage(ctx, domain.Page{
		CampaignID:           c.ID,
		Path:                 character.Path,
		Title:                character.Title,
		Type:                 character.Type,
		Visibility:           character.Visibility,
		OwnerCharacterPageID: character.ID,
		Frontmatter:          character.Frontmatter,
		Body:                 character.Body,
		ContentHash:          "hash-of-aria-owned",
	}, store.AsDM(c.ID))
	if err != nil {
		t.Fatalf("UpsertPage for the owned character: %v", err)
	}

	owner := mustCreatePrincipal(t, s, c.ID, "Alice (Ranger)", "hash-of-alice")
	if err := s.ReplacePrincipalCharacters(ctx, owner.ID, []string{character.ID}); err != nil {
		t.Fatalf("ReplacePrincipalCharacters: %v", err)
	}

	stranger := mustCreatePrincipal(t, s, c.ID, "Bob", "hash-of-bob")

	return s, c, character, owner, stranger
}

// A player may write within their own character and nowhere else, and "nowhere
// else" is the half worth testing: a page in somebody else's character, a
// DM-owned page, and a page owned by nobody.
func TestWriteIsScopedToTheCharactersOwn(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s, c, character, owner, stranger := writeFixture(t)

	// A page under the owner's own character, which the owner may write.
	own := domain.Page{
		CampaignID:           c.ID,
		Path:                 "characters/aria/spells",
		Title:                "Aria's spells",
		Type:                 domain.PageTypeNote,
		OwnerCharacterPageID: character.ID,
		Frontmatter:          "title: Aria's spells\n",
		Body:                 "Misty step.\n",
		ContentHash:          "hash-of-spells",
	}
	if _, err := s.UpsertPage(ctx, own, owner); err != nil {
		t.Errorf("the owner could not write a page in their own character: %v", err)
	}

	// The same page, from somebody else.
	tests := map[string]struct {
		principal domain.Principal
		page      domain.Page
	}{
		"somebody else's character": {
			principal: stranger,
			page:      own,
		},
		"a DM-owned page": {
			// The common case on every ordinary write, and the one a
			// player-authored feature would hit first.
			principal: owner,
			page: domain.Page{
				CampaignID:  c.ID,
				Path:        "locations/rivergate",
				Title:       "Rivergate",
				Type:        domain.PageTypeLocation,
				Frontmatter: "title: Rivergate\n",
				Body:        "A fortified town.\n",
				ContentHash: "hash-of-rivergate",
			},
		},
		"a page owned by nobody": {
			// The other common case: a page with no owner at all. The audience
			// is not consulted, because ownership is the only thing that opens a
			// write to a player and a page nobody owns is a page they may not write.
			principal: owner,
			page: domain.Page{
				CampaignID:  c.ID,
				Path:        "notes/loose",
				Title:       "A loose note",
				Type:        domain.PageTypeNote,
				Frontmatter: "title: A loose note\n",
				Body:        "Nothing under anybody.\n",
				ContentHash: "hash-of-loose",
			},
		},
		"somebody who is nobody": {
			principal: store.Nobody(),
			page:      own,
		},
		"a role this build does not know": {
			principal: domain.Principal{ID: "p-1", Role: domain.Role("editor")},
			page:      own,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := s.UpsertPage(ctx, tt.page, tt.principal); !errors.Is(err, store.ErrNotAllowed) {
				t.Errorf("UpsertPage returned %v, want an error matching ErrNotAllowed", err)
			}
		})
	}

	// And none of those wrote anything. This is the assertion that makes the
	// refusals real rather than a status code: a refused write that left a row
	// would pass the checks above on the next read.
	paths, err := s.ListPages(ctx, c.ID, store.AsDM(c.ID))
	if err != nil {
		t.Fatalf("ListPages: %v", err)
	}
	for _, page := range paths {
		switch page.Path {
		case "characters/aria/spells":
		case "locations/rivergate", "notes/loose":
			t.Errorf("a refused write left %q in the index", page.Path)
		}
	}
}

// A DM writes everything, and the gate is an exemption rather than an exception:
// §8's matrix says yes six times for a DM and there is nothing to check.
func TestADMMayWriteEveryPage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s, c, _, _, _ := writeFixture(t)

	dm := store.AsDM(c.ID)
	for _, path := range []string{"locations/rivergate", "notes/loose", "sessions/one"} {
		_, err := s.UpsertPage(ctx, domain.Page{
			CampaignID:  c.ID,
			Path:        path,
			Title:       path,
			Type:        domain.PageTypeNote,
			Frontmatter: "title: " + path + "\n",
			Body:        "The DM wrote this.\n",
			ContentHash: "hash-of-" + path,
		}, dm)
		if err != nil {
			t.Errorf("the DM could not write %q: %v", path, err)
		}
	}
}

// A refusal is `ErrNotAllowed` and **not** `ErrNotFound`, and the asymmetry is
// deliberate: a read must not confirm that a page exists, and a write is a request
// about a page the caller already holds. A player who was told their link was dead
// would ask the DM, and the DM would not know why.
func TestAWriteRefusalIsNotNotFound(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s, c, _, owner, _ := writeFixture(t)

	_, err := s.UpsertPage(ctx, domain.Page{
		CampaignID:  c.ID,
		Path:        "locations/rivergate",
		Title:       "Rivergate",
		Type:        domain.PageTypeNote,
		Frontmatter: "title: Rivergate\n",
		Body:        "A fortified town.\n",
		ContentHash: "hash-of-rivergate",
	}, owner)

	if !errors.Is(err, store.ErrNotAllowed) {
		t.Fatalf("UpsertPage returned %v, want an error matching ErrNotAllowed", err)
	}
	if errors.Is(err, store.ErrNotFound) {
		t.Error("the refusal is also ErrNotFound, which would tell a player a page they can see does not exist")
	}
}

// Reveal is the owner moving a page one level less strict, and it is a level
// change and not a second mechanism (§8). Both halves matter: a player may open
// their own page's audience, and a player may not close anybody's.
func TestRevealOnlyLoosens(t *testing.T) {
	t.Parallel()

	// Each case gets its own fixture. An earlier version shared one and the
	// subtests ran in map order, which meant the answer to "may the owner reveal
	// their page" depended on whether the DM's case had already closed it: a
	// `dm-only` page is not readable by a player, so the owner got not-found and
	// the case failed for a reason that had nothing to do with what it was
	// testing. A test that depends on the order its siblings run in has stopped
	// meaning anything.
	tests := map[string]struct {
		as       string
		to       domain.Visibility
		wantErr  error
		wantHeld bool
	}{
		"the owner opens their own page to the players": {
			as: "owner", to: domain.VisibilityPlayers,
		},
		"somebody else cannot open it": {
			as: "stranger", to: domain.VisibilityPlayers, wantErr: store.ErrNotAllowed,
		},
		"nobody cannot open it either": {
			as: "nobody", to: domain.VisibilityPlayers, wantErr: store.ErrNotAllowed,
		},
		"the owner cannot close their own page further": {
			// `dm-and-owner` is the strictest a player's own page can be, and
			// setting `dm-only` is not a reveal: it is a DM-only page wearing a
			// player's hands, and §8 says `dm-only` is absolute.
			as: "owner", to: domain.VisibilityDMOnly, wantErr: store.ErrNotAllowed,
		},
		"the owner cannot re-set the level it already has": {
			// Which is not an error and not a reveal: it does nothing, and the
			// point of the check is that it is a no-op rather than a widening.
			as: "owner", to: domain.VisibilityDMAndOwner,
		},
		"a DM may close it": {
			as: "dm", to: domain.VisibilityDMOnly,
		},
		"a DM may open it": {
			as: "dm", to: domain.VisibilityPlayers,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			s, c, character, owner, stranger := writeFixture(t)

			as := map[string]domain.Principal{
				"owner":    owner,
				"stranger": stranger,
				"nobody":   store.Nobody(),
				"dm":       store.AsDM(c.ID),
			}[tt.as]

			err := s.SetPageVisibility(ctx, c.ID, character.Path, tt.to, as)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("SetPageVisibility returned %v, want an error matching %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("SetPageVisibility: %v", err)
			}

			// And the row really moved, rather than the call having quietly
			// decided it was already what was asked for.
			page, err := s.GetPage(ctx, c.ID, character.Path, store.AsDM(c.ID))
			if err != nil {
				t.Fatalf("GetPage: %v", err)
			}
			if page.Visibility != tt.to {
				t.Errorf("the page is %q, want %q", page.Visibility, tt.to)
			}
		})
	}

	// A level that is not one is refused before anything is read at all: it is a
	// programming error rather than a permission one, so it is not ErrNotAllowed,
	// and a caller that writes it has a bug rather than a denied request.
	t.Run("a level that is not one", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		s, c, character, _, _ := writeFixture(t)

		err := s.SetPageVisibility(ctx, c.ID, character.Path, domain.Visibility("everybody"), store.AsDM(c.ID))
		if err == nil {
			t.Fatal("a level that is not one was accepted")
		}
		if errors.Is(err, store.ErrNotAllowed) {
			t.Errorf("a bad level is reported as a permission: %v", err)
		}
	})
}

// The store and the resolver have to agree about who owns a page, and this is the
// seam between them: the gate asks the store's question, the read predicate asks
// its own, and a disagreement is a player who can write a page they cannot read
// or the reverse.
func TestTheWriteGateAgreesWithTheResolver(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s, c, character, owner, stranger := writeFixture(t)

	for name, principal := range map[string]domain.Principal{
		"the owner":     owner,
		"somebody else": stranger,
		"the DM":        store.AsDM(c.ID),
		"nobody":        store.Nobody(),
	} {
		page, err := s.GetPage(ctx, c.ID, character.Path, store.AsDM(c.ID))
		if err != nil {
			t.Fatalf("GetPage: %v", err)
		}

		meta, err := s.PageMetaOf(ctx, page, principal)
		if err != nil {
			t.Fatalf("PageMetaOf: %v", err)
		}
		decision, err := s.MayWrite(ctx, page, principal)
		if err != nil {
			t.Fatalf("MayWrite: %v", err)
		}

		_, writeErr := s.UpsertPage(ctx, page, principal)
		wrote := writeErr == nil

		if wrote != decision.CanEdit {
			t.Errorf("%s: the gate says %t and the resolver says %t",
				name, wrote, decision.CanEdit)
		}
		_ = meta
	}
}
