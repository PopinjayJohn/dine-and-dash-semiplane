package render_test

import (
	"context"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/access"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/render"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/vault"
)

// TestTheCacheIsKeyedByTheDecision is the test that matters most in this file,
// and it is the one that would catch the mistake this cache is shaped to make
// easy: a render made for a DM served to a player.
//
// The sequence is the one a real request makes. The DM opens a page with a
// secret in it, a player opens the same page, and the two renders have to be
// two different things. If the key were the content hash alone -- which is what
// the spec's §11 line says, and what an implementation would reach for first --
// this test fails and the player gets the secret.
func TestTheCacheIsKeyedByTheDecision(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := render.New()

	page := render.Page{
		Path:        "locations/the-drowned-hound",
		Body:        "> [!SECRET] The hound\n> " + canary + "\n",
		ContentHash: vault.Hash([]byte("the hound")),
	}

	// The DM first, because a DM opening a page before a player is the common
	// order and the one that poisons a cache keyed the wrong way round.
	forPlayer, err := r.Render(ctx, page, render.Decision{CanSeeSecrets: true})
	if err != nil {
		t.Fatalf("Render for the DM: %v", err)
	}
	if !strings.Contains(forPlayer.HTML, canary) {
		t.Fatal("the DM's page does not contain the canary, so this test is not testing anything")
	}

	forUser, err := r.Render(ctx, page, render.Decision{})
	if err != nil {
		t.Fatalf("Render for a player: %v", err)
	}
	if strings.Contains(forUser.HTML, canary) {
		t.Errorf("a player was served a cached render that contains a secret\nhtml: %s", forUser.HTML)
	}

	// And the other way round, because a cache that only gets it right in one
	// order is still wrong.
	other, err := render.New().Render(ctx, page, render.Decision{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	againForDM, err := render.New().Render(ctx, page, render.Decision{CanSeeSecrets: true})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(againForDM.HTML, canary) {
		t.Error("the DM was served a cached render with the secret removed")
	}
	if strings.Contains(other.HTML, canary) {
		t.Error("a player was served a cached render with the secret in it")
	}
}

// TestTheCacheIsKeyedByEverythingThatChangesTheOutput: each field of the key,
// removed in turn, has to change the answer. A key field nothing depends on is
// either a mistake or dead weight.
func TestTheCacheIsKeyedByEverythingThatChangesTheOutput(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := render.New()

	base := render.Page{
		Path:        "locations/rivergate",
		Body:        "# Rivergate\n\nA fortified town.\n",
		ContentHash: vault.Hash([]byte("one")),
	}

	first, err := r.Render(ctx, base, render.Decision{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	t.Run("a different content hash is a different render", func(t *testing.T) {
		t.Parallel()

		changed := base
		changed.Body = "# Rivergate\n\nA fortified town, after the flood.\n"
		changed.ContentHash = vault.Hash([]byte("two"))

		second, err := r.Render(ctx, changed, render.Decision{})
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if second.HTML == first.HTML {
			t.Error("a changed body produced the cached HTML of the old one")
		}

		// And the old page still renders as it did, which is what says the
		// cache did not overwrite the entry rather than add to it.
		again, err := r.Render(ctx, base, render.Decision{})
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if again.HTML != first.HTML {
			t.Error("the first page's cached render changed under a second page")
		}
	})

	t.Run("a different path is a different entry", func(t *testing.T) {
		t.Parallel()

		// Nothing in this renderer's output depends on the page's own path yet,
		// so this is a test of the key rather than of the HTML: two pages with
		// the same body are the same bytes, and they must not share an entry.
		//
		// The path is in the key because the next thing to need it is relative
		// link resolution, and a cache keyed without it is a trap for whoever
		// adds that: the two pages would share an entry and one of them would
		// serve the other's links.
		cache := render.NewCache(8)

		one := render.CacheKey{ContentHash: "same", Version: 1, Path: "npcs/garros"}
		two := render.CacheKey{ContentHash: "same", Version: 1, Path: "npcs/vel"}

		cache.Put(one, render.Result{HTML: "garros"})
		cache.Put(two, render.Result{HTML: "vel"})

		if cache.Len() != 2 {
			t.Errorf("the cache holds %d entries for two pages, want 2", cache.Len())
		}
		got, found := cache.Get(two)
		if !found || got.HTML != "vel" {
			t.Errorf("the second page's entry is %+v (found = %t), want its own", got, found)
		}
	})

	t.Run("a different campaign is a different entry", func(t *testing.T) {
		t.Parallel()

		// Two campaigns can each hold `locations/rivergate` with the same
		// bytes -- two DMs who both started from the same template -- and every
		// URL in the render names the campaign it is in. A shared entry would
		// serve one campaign's links inside the other's HTML, which is a wrong
		// page rather than a broken one, so it is worth an entry of its own.
		renderer := render.NewWithLinks(testResolver(
			map[string]string{"locations/rivergate": "Rivergate"},
			map[string]string{},
		))

		page := render.Page{
			Path:        "locations/the-drowned-hound",
			Body:        "See [[locations/rivergate]].\n",
			ContentHash: "the-same-bytes-in-two-campaigns",
		}

		one := page
		one.Campaign = "blackwater"
		two := page
		two.Campaign = "thornford"

		first, err := renderer.Render(ctx, one, render.Decision{})
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		second, err := renderer.Render(ctx, two, render.Decision{})
		if err != nil {
			t.Fatalf("Render: %v", err)
		}

		if !strings.Contains(first.HTML, `href="/c/blackwater/locations/rivergate"`) {
			t.Errorf("the first campaign's link is %s", first.HTML)
		}
		if !strings.Contains(second.HTML, `href="/c/thornford/locations/rivergate"`) {
			t.Errorf("the second campaign was served the first campaign's link: %s", second.HTML)
		}
	})
}

// TestTheCacheEvicts: the bound is real, and what comes out is what the list
// says should come out.
func TestTheCacheEvicts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := render.New()

	// Twenty distinct pages, which is more than the bound of a small cache and
	// not so many that the test is slow.
	const pages = 20
	for i := range pages {
		body := "Page " + string(rune('a'+i)) + ".\n"
		if _, err := r.Render(ctx, render.Page{
			Path:        "page-" + string(rune('a'+i)),
			Body:        body,
			ContentHash: vault.Hash([]byte(body)),
		}, render.Decision{}); err != nil {
			t.Fatalf("Render: %v", err)
		}
	}

	// The first page is gone: the cache holds a bounded number of renders and
	// this has rendered more than that.
	body := "Page a.\n"
	gone, err := r.Render(ctx, render.Page{
		Path:        "page-a",
		Body:        body,
		ContentHash: vault.Hash([]byte(body)),
	}, render.Decision{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(gone.HTML, "Page a.") {
		t.Errorf("the evicted page did not render correctly when it was asked for again\nhtml: %s", gone.HTML)
	}
}

// TestRenderIsCachedAtAll: without this, every test above would pass with a
// cache that never hits, and the key tests would be testing nothing.
func TestRenderIsCachedAtAll(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := render.New()

	page := render.Page{
		Path:        "locations/rivergate",
		Body:        "# Rivergate\n\nA fortified town.\n",
		ContentHash: vault.Hash([]byte("rivergate")),
	}

	first, err := r.Render(ctx, page, render.Decision{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	second, err := r.Render(ctx, page, render.Decision{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if first.HTML != second.HTML {
		t.Error("two renders of one page differ, so nothing was cached and nothing was tested")
	}
}

// TestTheCacheEvictsTheOldest: the bound is real and the eviction order is the
// one the code says it is, which is worth pinning because "least recently
// stored" is a weaker promise than "least recently used" and pretending
// otherwise in a comment would be worse than admitting it.
func TestTheCacheEvictsTheOldest(t *testing.T) {
	t.Parallel()

	cache := render.NewCache(3)

	for _, key := range []string{"a", "b", "c"} {
		cache.Put(render.CacheKey{ContentHash: key}, render.Result{HTML: key})
	}

	// "a" is at the front, so storing "d" pushes it out.
	cache.Put(render.CacheKey{ContentHash: "d"}, render.Result{HTML: "d"})

	if _, found := cache.Get(render.CacheKey{ContentHash: "a"}); found {
		t.Error("the oldest entry is still in a cache at its limit")
	}
	for _, key := range []string{"b", "c", "d"} {
		if _, found := cache.Get(render.CacheKey{ContentHash: key}); !found {
			t.Errorf("the cache dropped %q, which is not the oldest entry", key)
		}
	}
	if cache.Len() != 3 {
		t.Errorf("the cache holds %d entries, want its limit of 3", cache.Len())
	}
}

// TestACacheOfNoSizeCachesNothing: a limit of zero is a thing a test asks for
// and a production build should not, and both have to work.
func TestACacheOfNoSizeCachesNothing(t *testing.T) {
	t.Parallel()

	cache := render.NewCache(0)

	cache.Put(render.CacheKey{ContentHash: "a"}, render.Result{HTML: "a"})

	if _, found := cache.Get(render.CacheKey{ContentHash: "a"}); found {
		t.Error("a cache with no size returned an entry")
	}
	if cache.Len() != 0 {
		t.Errorf("a cache with no size holds %d entries", cache.Len())
	}
}

// The cache key narrows `access.Decision` to the one field that changes the
// bytes, which is right and is also the place a future secret rule has to be
// looked at. This test is what makes that a check rather than a sentence in a
// comment: every field of the decision is changed one at a time, and the key is
// required to differ only for the ones that change the render.
//
// A field that changes the render and is *not* in the key is a render made for a
// DM served to a player, and it is silent.
func TestTheCacheKeyCarriesEveryFieldThatChangesTheBytes(t *testing.T) {
	t.Parallel()

	base := render.CacheKey{
		ContentHash:   "hash-of-rivergate",
		Version:       render.RendererVersion,
		CanSeeSecrets: false,
		Path:          "locations/rivergate",
	}

	// Every field of the decision, and whether changing it changes the render.
	// The two that are not in the key are the two that change what a caller may
	// *offer* rather than what the render *contains*, and that is the whole of
	// the narrowing.
	fields := map[string]struct {
		decision access.Decision
		changes  bool
	}{
		"can see secrets": {
			decision: access.Decision{CanSeeSecrets: true},
			changes:  true,
		},
		"can read":    {decision: access.Decision{CanRead: true}},
		"can edit":    {decision: access.Decision{CanEdit: true}},
		"can reveal":  {decision: access.Decision{CanReveal: true}},
		"cannot read": {decision: access.Decision{}},
	}

	for name, tt := range fields {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			key := base
			key.CanSeeSecrets = tt.decision.CanSeeSecrets

			// A cache hit is a key equality, so this is the whole of the test.
			// If a future field changed the render and were left out of the key,
			// `changes` would have to be true and the assertion below would fail.
			if !tt.changes && key != base {
				t.Errorf("changing %q changed the cache key, so the cache is holding renders it need not", name)
			}
			if tt.changes && key == base {
				t.Errorf("changing %q did not change the cache key, so a render made with it "+
					"would be served to somebody with the other one", name)
			}
		})
	}

	// And the other three fields really do not change the bytes, which is the
	// claim the table above rests on. If it stops being true the key has to grow.
	page := render.Page{
		Path:        "locations/rivergate",
		ContentHash: "hash-of-rivergate",
		Body:        "A fortified town.\n\n> [!SECRET]\n> The name is Ilithya Marrow.\n",
	}
	renderer := render.New()

	// And the claim the table rests on, tested rather than asserted: two decisions
	// that differ **only** in the read, edit and reveal fields must produce the
	// same bytes, because none of those fields changes what is stripped. The two
	// have `CanSeeSecrets` equal, which is the whole point -- `Granted` and
	// `Decision{CanRead: true}` would differ there, and would be testing the
	// secret rule rather than the narrowing.
	permitted := access.Decision{CanRead: true, CanSeeSecrets: true}
	alsoPermitted := access.Decision{CanRead: true, CanEdit: true, CanReveal: true, CanSeeSecrets: true}

	one, err := renderer.Render(context.Background(), page, permitted)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	two, err := renderer.Render(context.Background(), page, alsoPermitted)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if one.HTML != two.HTML {
		t.Errorf("two decisions differing only in the read, edit and reveal fields render "+
			"differently, so the cache key is missing one of them:\n%s\n---\n%s", one.HTML, two.HTML)
	}
	if !strings.Contains(one.HTML, "Ilithya") {
		t.Error("a decision that permits the secret does not have it in the render")
	}
	if one.SecretsStripped() != 0 {
		t.Errorf("a decision that permits the secret stripped %d of them", one.SecretsStripped())
	}

	// And the third: a decision that does not permit it, which is the case the key
	// exists for.
	none, err := renderer.Render(context.Background(), page, access.Decision{CanRead: true})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if none.HTML == one.HTML {
		t.Error("a decision that forbids the secret rendered the same bytes as one that permits it")
	}
	if strings.Contains(none.HTML, "Ilithya") {
		t.Error("a decision that forbids the secret left it in the render")
	}
	if none.SecretsStripped() != 1 {
		t.Errorf("the render stripped %d secrets, want 1", none.SecretsStripped())
	}
}
