package migrations_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/popinjayjohn/dine-and-dash-semiplane/migrations"
)

func TestLoadEmbeddedMigrations(t *testing.T) {
	t.Parallel()

	loaded, err := migrations.Load(migrations.FS())
	if err != nil {
		t.Fatalf("Load(FS()) returned an error: %v", err)
	}

	if len(loaded) != 1 {
		t.Fatalf("got %d migrations, want exactly the one M1 ships: %+v", len(loaded), loaded)
	}

	got := loaded[0]
	if got.Version != 1 {
		t.Errorf("version = %d, want 1", got.Version)
	}
	if got.Name != "init" {
		t.Errorf("name = %q, want %q", got.Name, "init")
	}
	if strings.TrimSpace(got.Up) == "" {
		t.Error("the up migration is empty")
	}
	if strings.TrimSpace(got.Down) == "" {
		t.Error("the down migration is empty")
	}

	latest, err := migrations.Latest()
	if err != nil {
		t.Fatalf("Latest() returned an error: %v", err)
	}
	if latest != got.Version {
		t.Errorf("Latest() = %d, want %d", latest, got.Version)
	}
}

func TestLoad(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		fsys    fstest.MapFS
		want    []int
		wantErr string
	}{
		{
			name: "one complete migration",
			fsys: fstest.MapFS{
				"0001_init.up.sql":   &fstest.MapFile{Data: []byte("CREATE TABLE a (id TEXT);")},
				"0001_init.down.sql": &fstest.MapFile{Data: []byte("DROP TABLE a;")},
			},
			want: []int{1},
		},
		{
			name: "several migrations come back in version order",
			fsys: fstest.MapFS{
				"0002_access.up.sql":   &fstest.MapFile{Data: []byte("SELECT 2;")},
				"0002_access.down.sql": &fstest.MapFile{Data: []byte("SELECT 2;")},
				"0001_init.up.sql":     &fstest.MapFile{Data: []byte("SELECT 1;")},
				"0001_init.down.sql":   &fstest.MapFile{Data: []byte("SELECT 1;")},
			},
			want: []int{1, 2},
		},
		{
			name:    "an empty directory has no migrations and no error",
			fsys:    fstest.MapFS{},
			want:    nil,
			wantErr: "",
		},
		{
			name: "a migration with no down file is refused: there would be no way back",
			fsys: fstest.MapFS{
				"0001_init.up.sql": &fstest.MapFile{Data: []byte("CREATE TABLE a (id TEXT);")},
			},
			wantErr: `version 1 (init) has no .down.sql file`,
		},
		{
			name: "a migration with no up file is refused",
			fsys: fstest.MapFS{
				"0001_init.down.sql": &fstest.MapFile{Data: []byte("DROP TABLE a;")},
			},
			wantErr: `version 1 (init) has no .up.sql file`,
		},
		{
			name: "a gap in the versions is refused, because the rest would build a schema nobody described",
			fsys: fstest.MapFS{
				"0001_init.up.sql":     &fstest.MapFile{Data: []byte("SELECT 1;")},
				"0001_init.down.sql":   &fstest.MapFile{Data: []byte("SELECT 1;")},
				"0003_search.up.sql":   &fstest.MapFile{Data: []byte("SELECT 3;")},
				"0003_search.down.sql": &fstest.MapFile{Data: []byte("SELECT 3;")},
			},
			wantErr: "no gaps: found version 3 where 2 was expected",
		},
		{
			name: "a set that does not start at 1 is refused",
			fsys: fstest.MapFS{
				"0002_access.up.sql":   &fstest.MapFile{Data: []byte("SELECT 2;")},
				"0002_access.down.sql": &fstest.MapFile{Data: []byte("SELECT 2;")},
			},
			wantErr: "no gaps: found version 2 where 1 was expected",
		},
		{
			name: "an empty migration is refused",
			fsys: fstest.MapFS{
				"0001_init.up.sql":   &fstest.MapFile{Data: []byte("  \n\t\n")},
				"0001_init.down.sql": &fstest.MapFile{Data: []byte("DROP TABLE a;")},
			},
			wantErr: `migration "0001_init.up.sql" is empty`,
		},
		{
			name: "two names for one version is refused",
			fsys: fstest.MapFS{
				"0001_init.up.sql":     &fstest.MapFile{Data: []byte("SELECT 1;")},
				"0001_schema.down.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
			},
			wantErr: `version 1 is named both "init" and "schema"`,
		},
		{
			name: "a stray file in the directory is refused rather than ignored",
			fsys: fstest.MapFS{
				"0001_init.up.sql":       &fstest.MapFile{Data: []byte("SELECT 1;")},
				"0001_init.down.sql":     &fstest.MapFile{Data: []byte("SELECT 1;")},
				"notes-for-next-time.md": &fstest.MapFile{Data: []byte("remember the FTS5 tokenizer")},
			},
			wantErr: `migration file "notes-for-next-time.md" does not follow`,
		},
		{
			name: "a name with three digits is not a migration file",
			fsys: fstest.MapFS{
				"001_init.up.sql":   &fstest.MapFile{Data: []byte("SELECT 1;")},
				"001_init.down.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
			},
			wantErr: `does not follow`,
		},
		{
			name: "a name with five digits is not a migration file",
			fsys: fstest.MapFS{
				"00001_init.up.sql":   &fstest.MapFile{Data: []byte("SELECT 1;")},
				"00001_init.down.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
			},
			wantErr: `does not follow`,
		},
		{
			name: "an unknown direction is not a migration file",
			fsys: fstest.MapFS{
				"0001_init.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
			},
			wantErr: `does not follow`,
		},
		{
			name: "an upper-case direction is not a migration file",
			fsys: fstest.MapFS{
				"0001_init.UP.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
			},
			wantErr: `does not follow`,
		},
		{
			name: "a version with no name is not a migration file",
			fsys: fstest.MapFS{
				"0001_.up.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
			},
			wantErr: `does not follow`,
		},
		{
			name: "a subdirectory is ignored, so a test fixture can sit beside the migrations",
			fsys: fstest.MapFS{
				"0001_init.up.sql":   &fstest.MapFile{Data: []byte("SELECT 1;")},
				"0001_init.down.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
				"testdata/copy.txt":  &fstest.MapFile{Data: []byte("a copy, not a migration")},
			},
			want: []int{1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := migrations.Load(tt.fsys)

			if tt.wantErr != "" {
				assertErrorContains(t, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("Load returned an unexpected error: %v", err)
			}

			versions := make([]int, 0, len(got))
			for _, m := range got {
				versions = append(versions, m.Version)
			}
			if len(versions) != len(tt.want) {
				t.Fatalf("got versions %v, want %v", versions, tt.want)
			}
			for i, version := range versions {
				if version != tt.want[i] {
					t.Errorf("migration %d has version %d, want %d", i, version, tt.want[i])
				}
			}
		})
	}
}

func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()

	switch {
	case err == nil:
		t.Fatalf("no error, want one containing %q", want)
	case !strings.Contains(err.Error(), want):
		t.Fatalf("error %q, want one containing %q", err, want)
	}
}
