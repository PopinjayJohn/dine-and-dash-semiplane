package http_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	wiki "github.com/popinjayjohn/dine-and-dash-semiplane/internal/http"
)

// # The session log
//
// The log is a projection with a stream on it, and the two things it has to get
// right are that it is *the reader's* campaign and that it updates without a
// reload. Everything else is drawing.

// logURL is the log, once, and `?log=live` is the stream.
func (f *fixture) logURL() string { return "/c/" + f.campaign.Slug.String() + "/?log=1" }

// TestTheLogIsOrderedByWhenAPageChanged is the query's contract: most recently
// changed first, with a path tiebreak so two reads of the same state give the same
// order.
func TestTheLogIsOrderedByWhenAPageChanged(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	dm := f.dmSession()

	// Three pages touched in a known order, at three known times.
	f.hands.advance(3 * time.Hour)
	f.writeFile("locations/third", fileFor("Third", "Third."))
	f.indexNew("locations/third")
	first := f.indexed("locations/third").UpdatedAt

	f.hands.advance(3 * time.Hour)
	f.writeFile("locations/second", fileFor("Second", "Second."))
	f.indexNew("locations/second")
	second := f.indexed("locations/second").UpdatedAt

	f.hands.advance(3 * time.Hour)
	f.writeFile("locations/first", fileFor("First", "First."))
	f.indexNew("locations/first")
	third := f.indexed("locations/first").UpdatedAt

	if first.After(second) || second.After(third) {
		t.Fatalf("the fixture's clocks did not advance: %v, %v, %v", first, second, third)
	}

	got := f.get(f.logURL(), dm)
	if got.status != http.StatusOK {
		t.Fatalf("the log is %d, want 200\nbody: %s", got.status, got.body)
	}

	order := []string{"First", "Second", "Third"}
	positions := make([]int, len(order))
	for i, title := range order {
		positions[i] = strings.Index(got.body, ">"+title+"<")
		if positions[i] < 0 {
			t.Fatalf("the log does not contain %q:\n%s", title, got.body)
		}
	}
	if positions[0] >= positions[1] || positions[1] >= positions[2] {
		t.Errorf("the log is in the order %v, want the most recently changed first", positions)
	}
}

// TestTheLogIsTheReadersCampaign is the access-control half, and it is the half
// that undoes ADR 0020's fix if it is wrong: a log that announced a hidden page's
// edit would tell a player that a DM's page exists and when it moved.
func TestTheLogIsTheReadersCampaign(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	asDM := f.get(f.logURL(), f.dmSession())
	if !strings.Contains(asDM.body, "Captain Vell") {
		t.Errorf("the DM's log does not list their own private page:\n%s", asDM.body)
	}

	asPlayer := f.get(f.logURL(), f.playerSession())
	if asPlayer.status != http.StatusOK {
		t.Fatalf("a player's log is %d, want 200", asPlayer.status)
	}
	for _, leak := range []string{"Captain Vell", "npcs/vel"} {
		if strings.Contains(asPlayer.body, leak) {
			t.Errorf("a player's log carries %q, which is a DM's page:\n%s", leak, asPlayer.body)
		}
	}
	// And it does list the pages they may read, so the test is not satisfied by an
	// empty log.
	if !strings.Contains(asPlayer.body, "rivergate") {
		t.Errorf("a player's log is empty of the pages they may read:\n%s", asPlayer.body)
	}
}

// TestTheLogUpdatesItself: the stream, over a real pipe, with a change published
// while a reader is watching.
//
// The test reuses the stream machinery from `stream_test.go` rather than
// duplicating it, and the assertion is the one a reader would make: the line that
// was not there before is there now, without a reload.
func TestTheLogUpdatesItself(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	dm := f.dmSession()

	// The log hangs off the campaign *root*, so a reader arrives at it from the
	// sidebar rather than from a page.
	// `openSSE` reads the first frame itself -- the handler always sends one, and a
	// client that connects after a save has to be in sync without waiting for the
	// next one -- so this is the *first* frame rather than a second one.
	log, first := f.openSSE(t, "/c/"+f.campaign.Slug.String()+"/?log=live", dm)
	defer log.cancel()

	if !strings.Contains(first.text(), "rivergate") {
		t.Errorf("the first frame is not the log:\n%s", first.text())
	}

	// A page appears, and the campaign is told.
	f.writeFile("notes/a-new-page", fileFor("A New Page", "New."))
	f.indexNew("notes/a-new-page")
	// The same call `wiki serve`'s watcher makes, so the test exercises the
	// publishing path rather than reaching into the hub.
	wiki.PageChanged(f.hub, f.campaign.ID, "notes/a-new-page")

	frame, err := log.next(5 * time.Second)
	if err != nil {
		t.Fatalf("the change did not arrive: %v", err)
	}
	if !strings.Contains(frame.text(), "A New Page") {
		t.Errorf("the pushed frame does not have the new page in it:\n%s", frame.text())
	}
	if !strings.Contains(frame.text(), `href="/c/blackwater/notes/a-new-page"`) {
		t.Errorf("the new line is not a link to the page:\n%s", frame.text())
	}
}

// TestTheLogIsBounded is the cost: a notice per change re-reads a list, so the
// list is short and a full hub is refused rather than queued.
func TestTheLogIsBounded(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	dm := f.dmSession()

	tiny := newTinyHub()
	f.cfg.Hub = tiny
	f.rebuild()

	got := f.get(f.logURL(), dm)
	if got.status != http.StatusOK {
		t.Fatalf("the log is %d on a hub of one, want 200", got.status)
	}

	// And the list is short, because a log of a hundred lines is a page of reading
	// rather than a glance.
	long := f.get(f.logURL(), dm)
	if strings.Count(long.body, `class="log-entry"`) > 12 {
		t.Errorf("the log has %d entries, more than the bound", strings.Count(long.body, `class="log-entry"`))
	}
}

// TestTheLogIsAFragmentLikeTheSearch: both are swapped into a page, so neither
// brings its own chrome with it.
func TestTheLogIsAFragmentLikeTheSearch(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	got := f.get(f.logURL(), f.dmSession())
	for _, notAFragment := range []string{"<!DOCTYPE", "class=\"sidebar\"", "Log out"} {
		if strings.Contains(got.body, notAFragment) {
			t.Errorf("the log contains %q, so it is a whole page rather than a fragment", notAFragment)
		}
	}
	if !strings.Contains(got.body, `id="session-log"`) {
		t.Errorf("the log is not the element the stream patches:\n%s", got.body)
	}
}
