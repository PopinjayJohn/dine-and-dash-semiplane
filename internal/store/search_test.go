package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/search"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
)

// The corpus is the spec's example vault, because a fixture nobody recognises is a
// fixture nobody can check a wrong answer against.
//
// It is also the corpus ADR 0009 is about, which is the point: one page a player
// may read, one page a player may not, and one name that lives inside a secret on
// a page a player may perfectly well open. The last one is the case that a
// page-level filter gets wrong, because the page is readable and the answer is
// still a disclosure.
const (
	secretCanary = "Ilithya Marrow"

	publicBody = "A fortified town at the confluence of the [[Blackwater]] and the [[Thorn]].\n" +
		"The toll-collector is [[npcs/garros-ironbar]].\n"
)

type corpusPage struct {
	path       string
	title      string
	kind       domain.PageType
	visibility domain.Visibility
	aliases    []string
	tags       []string
	bodyPublic string
	secretText string
}

func searchCorpus() []corpusPage {
	return []corpusPage{
		{
			path:       "locations/rivergate",
			title:      "Rivergate",
			kind:       domain.PageTypeLocation,
			visibility: domain.VisibilityPlayers,
			aliases:    []string{"the toll town", "Flussport"},
			tags:       []string{"location", "hub"},
			bodyPublic: publicBody,
			// The town page carries the secret, and it is a page every player may
			// read. This is the whole of ADR 0009 in one fixture.
			secretText: "Captain Vell is actually " + secretCanary + ", sworn to the Umbral Court.",
		},
		{
			path:       "npcs/garros-ironbar",
			title:      "Garros Ironbar",
			kind:       domain.PageTypeNPC,
			visibility: domain.VisibilityPlayers,
			tags:       []string{"npc"},
			bodyPublic: "Collects the toll, and has since the winter.\n",
		},
		{
			// A DM-only page whose *public* half says nothing about the secret, so
			// that a search for the canary finding it in the public index would be
			// a real failure rather than a fixture artefact.
			path:       "sessions/the-dragon-heist",
			title:      "The Dragon Heist",
			kind:       domain.PageTypeSessionLog,
			visibility: domain.VisibilityDMOnly,
			tags:       []string{"session"},
			bodyPublic: "Three of the four came back. The fourth is still out there.\n",
			secretText: "The fourth is " + secretCanary + ", who answers to the Umbral Court.",
		},
		{
			path:       "locations/the-drowned-hound",
			title:      "The Drowned Hound",
			kind:       domain.PageTypeLocation,
			visibility: domain.VisibilityPlayers,
			tags:       []string{"location"},
			bodyPublic: "A tavern in a town that has not been a tavern for a year.\n",
		},
	}
}

// searchableCampaign builds a campaign with the corpus in it, indexed.
// searchableDM is a DM principal for the tests that need one and no player, so
// that they do not have to throw one away.
var searchableDM = domain.Principal{ID: "principal-dm", Role: domain.RoleDM}

func searchableCampaign(t *testing.T) (*store.Store, domain.Campaign, domain.Principal, domain.Principal) {
	t.Helper()

	ctx := context.Background()
	s := newStore(t)

	campaign, err := s.CreateCampaign(ctx, domain.Campaign{
		Slug:     "blackwater",
		Name:     "The Blackwater",
		VaultDir: "vault/blackwater",
	})
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}

	for _, fixture := range searchCorpus() {
		stored, upsertErr := s.UpsertPage(ctx, domain.Page{
			CampaignID:  campaign.ID,
			Path:        fixture.path,
			Title:       fixture.title,
			Type:        fixture.kind,
			Visibility:  fixture.visibility,
			Frontmatter: "title: " + fixture.title + "\n",
			Body:        fixture.bodyPublic,
			ContentHash: "hash-of-" + fixture.path,
		}, store.AsDM(campaign.ID))
		if upsertErr != nil {
			t.Fatalf("UpsertPage(%q): %v", fixture.path, upsertErr)
		}

		entry := store.IndexEntry{
			PageID:     stored.ID,
			Title:      fixture.title,
			Aliases:    fixture.aliases,
			Tags:       fixture.tags,
			Kind:       fixture.kind.String(),
			BodyPublic: fixture.bodyPublic,
			SecretText: fixture.secretText,
		}
		if err := s.ReplacePageIndex(ctx, entry); err != nil {
			t.Fatalf("ReplacePageIndex(%q): %v", fixture.path, err)
		}
	}

	return s, campaign, searchableDM, domain.Principal{ID: "principal-player", Role: domain.RolePlayer}
}

// mustQuery parses a query or fails the test. Every search test writes its query
// as the language rather than as a struct, so that a change to the language shows
// up here as a failing search rather than as a test that was quietly updated.
func mustQuery(t *testing.T, input string) search.Query {
	t.Helper()

	q, err := search.Parse(input)
	if err != nil {
		t.Fatalf("Parse(%q): %v", input, err)
	}
	return q
}

func pathsOf(hits []search.Hit) []string {
	paths := make([]string, 0, len(hits))
	for _, hit := range hits {
		paths = append(paths, hit.Path)
	}
	return paths
}

// A search finds what it says it finds. These are the relevance cases, and they
// are the reason the public body is empty today: a search that can only match a
// title, an alias, a tag or a type is a search that works, just not over prose.
func TestSearchPublicFindsPages(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s, campaign, dm, _ := searchableCampaign(t)

	tests := map[string]struct {
		query string
		want  []string
	}{
		"a title": {
			query: "rivergate",
			want:  []string{"locations/rivergate"},
		},
		"a title in another case": {
			query: "RIVERGATE",
			want:  []string{"locations/rivergate"},
		},
		"an alias": {
			query: "Flussport",
			want:  []string{"locations/rivergate"},
		},
		"an alias as written, with a space": {
			query: `"the toll town"`,
			want:  []string{"locations/rivergate"},
		},
		"a tag": {
			query: "npc",
			want:  []string{"npcs/garros-ironbar"},
		},
		"two words, both of which have to be there": {
			query: "fortified town",
			want:  []string{"locations/rivergate"},
		},
		"two words where only one is there": {
			query: "fortified dragon",
		},
		"a page type as a bare word": {
			query: "session",
			want:  []string{"sessions/the-dragon-heist"},
		},
		"a tag filter": {
			query: "tag:hub",
			want:  []string{"locations/rivergate"},
		},
		"two tag filters, either of which will do": {
			query: "tag:hub tag:npc",
			want:  []string{"locations/rivergate", "npcs/garros-ironbar"},
		},
		"a type filter": {
			query: "type:npc",
			want:  []string{"npcs/garros-ironbar"},
		},
		"a type filter and a text clause": {
			query: "toll type:npc",
			want:  []string{"npcs/garros-ironbar"},
		},
		"a filter with no text at all": {
			query: "type:location",
			want:  []string{"locations/rivergate", "locations/the-drowned-hound"},
		},
		"a word nobody wrote": {
			query: "draconary",
		},
		"an empty query asks for nothing": {
			query: "",
		},
		"whitespace alone asks for nothing": {
			query: "   ",
		},
		"a filter with no value asks for nothing": {
			query: "tag:",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			// As a DM, who sees the whole campaign. What a player gets is a subset
			// of this and nothing else, which is its own test below.
			got, err := s.SearchPublic(ctx, campaign.ID, dm, mustQuery(t, tt.query), search.DefaultLimit)
			if err != nil {
				t.Fatalf("SearchPublic(%q): %v", tt.query, err)
			}

			// Which pages, not in what order: the order is the ranking's business
			// and has its own test, and pinning it in every row here would make
			// every future weight change a test failure about weights.
			if !sameSet(pathsOf(got), tt.want) {
				t.Errorf("SearchPublic(%q) returned %v, want %v", tt.query, pathsOf(got), tt.want)
			}
		})
	}
}

// A player sees a subset of what the DM sees, for every query, and the cases where
// the subset is smaller are the interesting ones. Running the whole language
// through both roles is cheaper than proving it pairwise and catches a predicate
// that admits for one role and not the other.
func TestSearchPublicForAPlayerIsASubsetOfTheDMs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s, campaign, dm, player := searchableCampaign(t)

	queries := []string{
		"rivergate", "Flussport", "toll", "tavern", "winter", "npc", "session",
		"tag:hub", "tag:npc", "type:location", "type:npc", "is:dm-only",
		"is:dm-only dragon", `"toll collector"`, secretCanary, "Umbral Court",
		"", "   ", "tag:", "draconary", "rivergate garros",
	}

	for _, query := range queries {
		t.Run(query, func(t *testing.T) {
			parsed := mustQuery(t, query)

			theirs, err := s.SearchPublic(ctx, campaign.ID, dm, parsed, search.MaxLimit)
			if err != nil {
				t.Fatalf("SearchPublic as a DM: %v", err)
			}
			mine, err := s.SearchPublic(ctx, campaign.ID, player, parsed, search.MaxLimit)
			if err != nil {
				t.Fatalf("SearchPublic as a player: %v", err)
			}

			theirsSet := map[string]bool{}
			for _, path := range pathsOf(theirs) {
				theirsSet[path] = true
			}
			for _, path := range pathsOf(mine) {
				if !theirsSet[path] {
					t.Errorf("a player found %q, which the DM did not: the audience scope is not one-directional",
						path)
				}
			}
		})
	}
}

// The audience scope, from the player's side, on the three ways a DM-only page
// could otherwise be found: by its title, by its tag, and by its type.
//
// `is:dm-only` is in the language at all so that a DM can ask "what have I marked
// for myself?", and for a player it is the empty intersection rather than an
// error: a filter that names something you may not see is not information, and an
// error would say the level exists and the page does not.
func TestSearchPublicAppliesTheAudienceScope(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s, campaign, _, player := searchableCampaign(t)

	tests := map[string]string{
		"by title":                          "The Dragon Heist",
		"by tag":                            "tag:session",
		"by type":                           "type:session-log",
		"by filter":                         "is:dm-only",
		"by word in its title and its body": "dragon",
	}

	for name, query := range tests {
		t.Run(name, func(t *testing.T) {
			hits, err := s.SearchPublic(ctx, campaign.ID, player, mustQuery(t, query), search.MaxLimit)
			if err != nil {
				t.Fatalf("SearchPublic(%q): %v", query, err)
			}
			if len(hits) != 0 {
				t.Errorf("a player found the dm-only page %v by searching for %q", pathsOf(hits), query)
			}
		})
	}

	// And the DM can, which is what makes the rows above a filter rather than a
	// broken search.
	hits, err := s.SearchPublic(ctx, campaign.ID, searchableDM, mustQuery(t, "is:dm-only"), search.MaxLimit)
	if err != nil {
		t.Fatalf("SearchPublic as a DM: %v", err)
	}
	if got := pathsOf(hits); !sameStrings(got, []string{"sessions/the-dragon-heist"}) {
		t.Errorf("the DM's is:dm-only search returned %v", got)
	}
}

// The results are ordered, and the order is a promise: a DM scrolling a result
// list and a test asserting on it both need it to be the same list twice.
func TestSearchPublicIsOrderedAndRanked(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s, campaign, _, player := searchableCampaign(t)

	// "town" appears in Rivergate's public body and in the Drowned Hound's, and
	// in neither title. The order between them is the ranking's business, but the
	// ranks handed to the fusion must be one, two, three in that order.
	hits, err := s.SearchPublic(ctx, campaign.ID, player, mustQuery(t, "town"), search.MaxLimit)
	if err != nil {
		t.Fatalf("SearchPublic: %v", err)
	}
	if len(hits) < 2 {
		t.Fatalf("SearchPublic returned %d hits, want at least two: %+v", len(hits), hits)
	}

	for i, hit := range hits {
		if hit.Rank != i+1 {
			t.Errorf("hit %d has rank %d, want %d", i, hit.Rank, i+1)
		}
		if hit.FromSecrets {
			t.Errorf("hit %d came from the private index", i)
		}
		if hit.Score != 0 {
			t.Errorf("hit %d carries the score %v, want none: a score from one index means nothing next to another",
				i, hit.Score)
		}
	}

	// And the same query twice gives the same list.
	again, err := s.SearchPublic(ctx, campaign.ID, player, mustQuery(t, "town"), search.MaxLimit)
	if err != nil {
		t.Fatalf("the second SearchPublic: %v", err)
	}
	if !sameStrings(pathsOf(hits), pathsOf(again)) {
		t.Errorf("the same query returned %v then %v", pathsOf(hits), pathsOf(again))
	}
}

// The limit is bounded, and a caller that asks for none of a page's worth of
// results is not asking the database to do that.
func TestSearchPublicBoundsTheLimit(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s, campaign, _, player := searchableCampaign(t)

	for name, limit := range map[string]int{
		"zero":         0,
		"below zero":   -5,
		"one":          1,
		"far too many": search.MaxLimit * 100,
	} {
		t.Run(name, func(t *testing.T) {
			hits, err := s.SearchPublic(ctx, campaign.ID, player, mustQuery(t, "town"), limit)
			if err != nil {
				t.Fatalf("SearchPublic with limit %d: %v", limit, err)
			}
			if len(hits) > search.MaxLimit {
				t.Errorf("a limit of %d returned %d hits, want at most %d",
					limit, len(hits), search.MaxLimit)
			}
			if limit == 1 && len(hits) > 1 {
				t.Errorf("a limit of 1 returned %d hits: %+v", len(hits), hits)
			}
		})
	}
}

// The private index is only reachable through the stricter scope, and the excerpt
// is built from the private index. Those two facts are the whole of the canary
// test below, so this asserts them separately first: a DM finds their own secret
// by content, which is a real need — the DM forgets which page they wrote a name
// on — and that it is only ever the DM.
func TestSearchSecretsFindsSecretText(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s, campaign, dm, player := searchableCampaign(t)

	hits, err := s.SearchSecrets(ctx, campaign.ID, dm, mustQuery(t, "Umbral Court"), search.DefaultLimit)
	if err != nil {
		t.Fatalf("SearchSecrets as a DM: %v", err)
	}

	if got := pathsOf(hits); !sameStrings(got, []string{
		"locations/rivergate", "sessions/the-dragon-heist",
	}) {
		t.Errorf("the DM's search for secret text returned %v", got)
	}

	// The excerpt comes from the private index, so it *is* the secret text. That
	// is the design (ADR 0009): an excerpt is a pointer into a document, and this
	// principal is entitled to it.
	for _, hit := range hits {
		if !hit.FromSecrets {
			t.Errorf("hit %q is not marked as coming from the private index", hit.Path)
		}
		if !strings.Contains(hit.Snippet, "Umbral Court") {
			t.Errorf("hit %q has the excerpt %q, want the matched secret text", hit.Path, hit.Snippet)
		}
	}

	// And nobody else.
	for who, as := range map[string]domain.Principal{
		"a player":                 player,
		"a principal with no role": {},
	} {
		none, err := s.SearchSecrets(ctx, campaign.ID, as, mustQuery(t, "Umbral Court"), search.DefaultLimit)
		if err != nil {
			t.Fatalf("SearchSecrets as %s: %v", who, err)
		}
		if len(none) != 0 {
			t.Errorf("SearchSecrets as %s returned %d hits: %v", who, len(none), pathsOf(none))
		}
	}
}

// TestSecretNeverAppearsInAResult is the named test, and it is deliberately
// blunt: a forbidden-substring assertion over everything a search can hand back.
//
// The two halves are the point, and the second is what stops the first from being
// vacuous. A player searching for the canary must get nothing, and a DM searching
// for the same word must get the page — because a test that only asserts the
// absence of the canary also passes against an index that holds no secret text at
// all, and that index would also stop the DM from finding their own name.
func TestSecretNeverAppearsInAResult(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s, campaign, dm, player := searchableCampaign(t)

	t.Run("a player searching for the canary gets nothing", func(t *testing.T) {
		hits, err := s.SearchPublic(ctx, campaign.ID, player, mustQuery(t, secretCanary), search.MaxLimit)
		if err != nil {
			t.Fatalf("SearchPublic: %v", err)
		}
		assertNoCanary(t, "the public index, a player", hits)

		private, err := s.SearchSecrets(ctx, campaign.ID, player, mustQuery(t, secretCanary), search.MaxLimit)
		if err != nil {
			t.Fatalf("SearchSecrets: %v", err)
		}
		assertNoCanary(t, "the private index, a player", private)
	})

	t.Run("a player searching for a fragment of the canary gets nothing", func(t *testing.T) {
		for _, fragment := range []string{"ilithya", "Marrow", `"Ilithya Marrow"`, "ilithya marrow"} {
			hits, err := s.SearchPublic(ctx, campaign.ID, player, mustQuery(t, fragment), search.MaxLimit)
			if err != nil {
				t.Fatalf("SearchPublic(%q): %v", fragment, err)
			}
			assertNoCanary(t, "the public index, a player, "+fragment, hits)
		}
	})

	t.Run("a player cannot list a dm-only page by searching for it", func(t *testing.T) {
		hits, err := s.SearchPublic(ctx, campaign.ID, player, mustQuery(t, "is:dm-only dragon"), search.MaxLimit)
		if err != nil {
			t.Fatalf("SearchPublic: %v", err)
		}
		if len(hits) != 0 {
			t.Errorf("a player found %v by searching for the dm-only page", pathsOf(hits))
		}
	})

	t.Run("a DM finds it, which is what makes the rows above meaningful", func(t *testing.T) {
		public, err := s.SearchPublic(ctx, campaign.ID, dm, mustQuery(t, secretCanary), search.MaxLimit)
		if err != nil {
			t.Fatalf("SearchPublic: %v", err)
		}
		if len(public) != 0 {
			t.Errorf("the canary was findable in the public index: %v", pathsOf(public))
		}

		private, err := s.SearchSecrets(ctx, campaign.ID, dm, mustQuery(t, secretCanary), search.MaxLimit)
		if err != nil {
			t.Fatalf("SearchSecrets: %v", err)
		}
		if len(private) == 0 {
			t.Fatal("the DM cannot find their own secret by content, so the assertions above prove nothing")
		}
		if !strings.Contains(private[0].Snippet, secretCanary) {
			t.Errorf("the DM's excerpt is %q, want it to contain %q", private[0].Snippet, secretCanary)
		}
	})
}

// assertNoCanary is the forbidden-substring assertion, over every field a hit
// carries rather than over its path alone. An excerpt is the field that leaks,
// and a title is the field that would leak a `dm-only` page's existence.
func assertNoCanary(t *testing.T, what string, hits []search.Hit) {
	t.Helper()

	for _, hit := range hits {
		for field, value := range map[string]string{
			"path":    hit.Path,
			"title":   hit.Title,
			"snippet": hit.Snippet,
			"id":      hit.PageID,
		} {
			if strings.Contains(value, secretCanary) {
				t.Errorf("%s leaked the secret in the %s: %q", what, field, value)
			}
		}
	}
}

// sameSet compares two path lists without regard to order or repetition.
func sameSet(got, want []string) bool {
	seen := map[string]int{}
	for _, path := range got {
		seen[path]++
	}
	if len(seen) != len(want) {
		return false
	}
	for _, path := range want {
		if seen[path] != 1 {
			return false
		}
	}
	return true
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
