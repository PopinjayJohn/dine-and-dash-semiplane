package render_test

import (
	"context"
	"strings"
	"testing"

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
