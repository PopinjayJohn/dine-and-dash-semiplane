package migrations_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/clock"
	"github.com/popinjayjohn/dine-and-dash-semiplane/migrations"
)

// fixedClock is the clock every test in this file uses. The instant is a real
// session date from the spec's example vault, and the step means two
// migrations applied to the same database get two different timestamps.
func fixedClock() *clock.Fixed {
	return clock.NewFixed(time.Date(2026, 2, 14, 19, 3, 0, 0, time.UTC), time.Minute)
}

// TestUpFromZero is the named migration test: an empty file becomes a working
// schema, the state is reported accurately, and the timestamps are the ones the
// injected clock produced.
func TestUpFromZero(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := newTestDB(t)
	clk := fixedClock()

	before, err := migrations.Version(ctx, db)
	if err != nil {
		t.Fatalf("Version on an empty database: %v", err)
	}
	if before.Version != 0 {
		t.Errorf("an unmigrated database reports version %d, want 0", before.Version)
	}

	state, err := migrations.Up(ctx, db, clk)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}

	// The latest version, by name -- the version number is the migration
	// set's business and a test that spells it out has to be edited every time
	// a migration is added.
	latest, err := migrations.Latest()
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	loaded, err := migrations.Load(migrations.FS())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if state.Version != latest {
		t.Errorf("Up left the schema at version %d, want the latest, %d", state.Version, latest)
	}
	if state.Name != loaded[latest-1].Name {
		t.Errorf("Up left the schema named %q, want %q", state.Name, loaded[latest-1].Name)
	}
	if state.Dirty {
		t.Error("Up left the schema dirty after a clean run")
	}

	// Every version's recorded timestamp is one the clock produced, written in
	// the layout the store writes every other timestamp in, and they increase
	// with the version. The instants themselves are not asserted: the test clock
	// steps a minute per call, so which migration gets which instant is a
	// property of how many migrations there are and of nothing else.
	stamps, err := appliedAtByVersion(ctx, db)
	if err != nil {
		t.Fatalf("reading the recorded migration times: %v", err)
	}
	if len(stamps) != latest {
		t.Errorf("the schema recorded %d timestamps, want one per version, %d", len(stamps), latest)
	}

	start := time.Date(2026, 2, 14, 19, 3, 0, 0, time.UTC)
	previous := start.Add(-time.Hour)
	for version := 1; version <= latest; version++ {
		appliedAt, ok := stamps[version]
		if !ok {
			t.Errorf("version %d has no recorded timestamp", version)
			continue
		}

		stamp, parseErr := time.Parse(migrations.TimeLayout, appliedAt)
		if parseErr != nil {
			t.Errorf("version %d recorded %q, which is not a timestamp in the store's layout: %v",
				version, appliedAt, parseErr)
			continue
		}
		if stamp.Before(start) {
			t.Errorf("version %d recorded %v, which is before the test clock started at %v", version, stamp, start)
		}
		if !stamp.After(previous) {
			t.Errorf("version %d recorded %v, which is not after version %d's %v", version, stamp, version-1, previous)
		}
		previous = stamp
	}

	after, err := migrations.Version(ctx, db)
	if err != nil {
		t.Fatalf("Version after migrating: %v", err)
	}
	if after != state {
		t.Errorf("Version reported %v, want the state Up reported %v", after, state)
	}
}

// TestUpCreatesTheSchema is the list of objects a fresh database has after
// every migration has been applied.
//
// It is an allow-list of names rather than a comparison against a dump, because
// the question it answers is "is anything missing", and a missing table is a
// migration that was written and not shipped.
func TestUpCreatesTheSchema(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := newTestDB(t)

	if _, err := migrations.Up(ctx, db, fixedClock()); err != nil {
		t.Fatalf("Up: %v", err)
	}

	want := []string{
		// 0001: the tables of docs/spec.md §5 and §10.
		"campaigns",
		"pages",
		"page_revisions",
		"page_links",
		"principals",
		"sessions",
		"audit_log",
		// The runner's own bookkeeping.
		"schema_migrations",
		// 0001's indexes, without which nothing would be fast.
		"page_links_dst",
		"audit_log_campaign_at",
		"principals_campaign",
		"sessions_principal",
		// 0002: the names a page answers to, so a wiki link can resolve by
		// alias or file name the way it does in Obsidian.
		"page_targets",
		"page_targets_page",
	}

	got := tableNames(t, db)
	for _, name := range want {
		if !slices.Contains(got, name) {
			t.Errorf("after migrating, %q does not exist; the schema has %v", name, got)
		}
	}
}

func TestUpIsIdempotent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := newTestDB(t)
	clk := fixedClock()

	first, err := migrations.Up(ctx, db, clk)
	if err != nil {
		t.Fatalf("first Up: %v", err)
	}

	// A second run with a clock that has moved on must not re-apply
	// anything: re-running an ALTER TABLE ADD COLUMN against an existing
	// column is exactly the failure `wiki reindex` on a live database would
	// otherwise cause.
	second, err := migrations.Up(ctx, db, clk)
	if err != nil {
		t.Fatalf("second Up: %v", err)
	}

	if second != first {
		t.Errorf("second Up reported %v, want the first result %v", second, first)
	}

	var appliedAt string
	if err := db.QueryRowContext(ctx, `SELECT applied_at FROM schema_migrations WHERE version = 1`).Scan(&appliedAt); err != nil {
		t.Fatalf("reading the recorded migration time: %v", err)
	}
	if want := "2026-02-14T19:03:00.000000000Z"; appliedAt != want {
		t.Errorf("applied_at = %q after a second Up, want the first run's %q: the migration ran again", appliedAt, want)
	}
}

// TestDownRollsBackTheVersionItIsGiven: a down migration undoes its own
// version and nothing else.
//
// The two halves matter in both directions. Everything the rolled-back version
// created is gone -- otherwise a later Up fails on an object that already
// exists -- and everything an earlier version created is still there, because a
// down migration that dropped `campaigns` would be dropping the campaign
// index along with it. This test said "nothing is left", which was true while
// there was one migration and stopped being true the moment there were two.
func TestDownRollsBackTheVersionItIsGiven(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := newTestDB(t)

	before, err := migrations.Up(ctx, db, fixedClock())
	if err != nil {
		t.Fatalf("Up: %v", err)
	}

	latest, err := migrations.Latest()
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if before.Version != latest {
		t.Fatalf("Up left the schema at %v, want the latest version %d", before, latest)
	}

	state, err := migrations.Down(ctx, db, 1)
	if err != nil {
		t.Fatalf("Down: %v", err)
	}
	if state.Version != latest-1 {
		t.Errorf("Down left the schema at version %d, want %d", state.Version, latest-1)
	}

	got := tableNames(t, db)

	// Gone: the objects the version just rolled back created.
	for _, name := range []string{"page_targets", "page_targets_page"} {
		if slices.Contains(got, name) {
			t.Errorf("%q survived the down migration", name)
		}
	}

	// Still here: what the version below it created, which this down migration
	// was never asked to touch.
	for _, name := range []string{"campaigns", "pages", "page_links", "schema_migrations"} {
		if !slices.Contains(got, name) {
			t.Errorf("the down migration removed %q, which belongs to an earlier version", name)
		}
	}

	// And up again works, which is the property that makes the pair usable in
	// a test.
	if _, err := migrations.Up(ctx, db, fixedClock()); err != nil {
		t.Fatalf("Up after Down: %v", err)
	}
}

// TestDownToZeroLeavesNothing: rolling all the way back leaves a database with
// no schema in it at all, which is what `reindex --full` leans on when it
// decides to start over.
func TestDownToZeroLeavesNothing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := newTestDB(t)

	latest, err := migrations.Latest()
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}

	if _, upErr := migrations.Up(ctx, db, fixedClock()); upErr != nil {
		t.Fatalf("Up: %v", upErr)
	}

	state, err := migrations.Down(ctx, db, latest)
	if err != nil {
		t.Fatalf("Down: %v", err)
	}
	if state.Version != 0 {
		t.Errorf("Down left the schema at version %d, want 0", state.Version)
	}

	for _, name := range tableNames(t, db) {
		if name == "schema_migrations" {
			continue
		}
		t.Errorf("%q survived rolling every migration back", name)
	}
}

func TestDownRefuses(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		steps   int
		migrate bool
		wantErr string
	}{
		"zero steps is not a rollback":     {steps: 0, migrate: true, wantErr: "want at least 1"},
		"negative steps is not a rollback": {steps: -1, migrate: true, wantErr: "want at least 1"},
		"nothing to roll back":             {steps: 1, migrate: false, wantErr: ""},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			db := newTestDB(t)

			if tt.migrate {
				if _, err := migrations.Up(ctx, db, fixedClock()); err != nil {
					t.Fatalf("Up: %v", err)
				}
			}

			state, err := migrations.Down(ctx, db, tt.steps)
			if tt.wantErr != "" {
				assertErrorContains(t, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("Down: %v", err)
			}
			if state.Version != 0 {
				t.Errorf("Down left the schema at version %d, want 0", state.Version)
			}
		})
	}
}

// TestUpRefusesADatabaseFromANewerBinary is the check that stops a rollback of
// the binary from reading a database it does not understand. Serving a stale
// binary against a newer schema is how a wiki ends up leaking a column it
// thinks does not exist.
func TestUpRefusesADatabaseFromANewerBinary(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := newTestDB(t)

	if err := migrations.Force(ctx, db, fixedClock(), 9); err != nil {
		t.Fatalf("Force: %v", err)
	}

	_, err := migrations.Up(ctx, db, fixedClock())
	assertErrorContains(t, err, "written by a newer build")
}

func TestForce(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := newTestDB(t)
	clk := fixedClock()

	if _, err := migrations.Up(ctx, db, clk); err != nil {
		t.Fatalf("Up: %v", err)
	}

	// Forcing to 0 forgets the schema, which is what an empty index is: the
	// tables are still there, but the runner no longer claims to know them.
	if forceErr := migrations.Force(ctx, db, clk, 0); forceErr != nil {
		t.Fatalf("Force(0): %v", forceErr)
	}
	state, err := migrations.Version(ctx, db)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if state.Version != 0 {
		t.Errorf("after Force(0) the schema reports version %d, want 0", state.Version)
	}

	// Forcing to a version records it, marked clean, without running SQL.
	if forceErr := migrations.Force(ctx, db, clk, 1); forceErr != nil {
		t.Fatalf("Force(1): %v", forceErr)
	}
	state, err = migrations.Version(ctx, db)
	if err != nil {
		t.Fatalf("Version after Force(1): %v", err)
	}
	if state.Version != 1 {
		t.Errorf("after Force(1) the schema reports version %d, want 1", state.Version)
	}
	if state.Dirty {
		t.Error("Force(1) left the schema dirty; the whole point is to clear the flag")
	}

	// A negative version is not a version.
	assertErrorContains(t, migrations.Force(ctx, db, clk, -1), "does not exist")
}

func TestVersionOnAnUnmigratedDatabase(t *testing.T) {
	t.Parallel()

	state, err := migrations.Version(context.Background(), newTestDB(t))
	if err != nil {
		t.Fatalf("Version on a database with no schema_migrations table: %v", err)
	}

	if state.Version != 0 || state.Dirty {
		t.Errorf("Version reported %v, want the zero state", state)
	}
}

func TestStateString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		state migrations.State
		want  string
	}{
		{name: "empty", state: migrations.State{}, want: "no migrations applied"},
		{name: "clean", state: migrations.State{Version: 1, Name: "init"}, want: "version 1 (init), clean"},
		{name: "dirty", state: migrations.State{Version: 7, Name: "search", Dirty: true}, want: "version 7 (search), dirty"},
		{name: "no name", state: migrations.State{Version: 3}, want: "version 3, clean"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.state.String(); got != tt.want {
				t.Errorf("State.String() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestTimeLayoutSortsAsText is the reason TimeLayout has a fixed-width
// fraction. A timestamp column is TEXT, so every comparison and every ORDER BY
// on one is a string comparison, and a layout that drops trailing zeros makes
// "…T19:03:00.4Z" sort after "…T19:03:00.45Z".
func TestTimeLayoutSortsAsText(t *testing.T) {
	t.Parallel()

	earlier := time.Date(2026, 2, 14, 19, 3, 0, 450000000, time.UTC)
	later := time.Date(2026, 2, 14, 19, 3, 0, 500000000, time.UTC)

	earlierText := earlier.Format(migrations.TimeLayout)
	laterText := later.Format(migrations.TimeLayout)

	if earlierText >= laterText {
		t.Errorf("%q does not sort before %q as text, so a WHERE on a timestamp column is wrong", earlierText, laterText)
	}
	if !strings.HasSuffix(earlierText, "Z") {
		t.Errorf("TimeLayout produced %q, want a UTC offset", earlierText)
	}

	// And the layout round-trips, which is what lets a row be read back into
	// a time.Time and compared with a fresh one.
	parsed, err := time.Parse(migrations.TimeLayout, earlierText)
	if err != nil {
		t.Fatalf("parsing %q: %v", earlierText, err)
	}
	if !parsed.Equal(earlier) {
		t.Errorf("parsing %q gave %v, want %v", earlierText, parsed, earlier)
	}
}
