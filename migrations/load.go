package migrations

import (
	"cmp"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"
)

// The shape of a migration file name: four digits, an underscore, a name, then
// .up.sql or .down.sql.
const (
	upSuffix   = ".up.sql"
	downSuffix = ".down.sql"

	// versionDigits is how many digits a version has. Four is enough for the
	// twelve milestones this project has, and enough that a new migration is
	// added rather than a digit borrowed.
	versionDigits = 4
)

// Load reads the migrations out of fsys and checks them against each other.
//
// The checks are the point. A migration set with a hole in it means someone
// deleted a file or failed to commit one, and a runner that applies the rest
// anyway builds a schema nobody described. A version with no down file means
// there is no way back, which is a decision to make deliberately rather than by
// forgetting a file. Both are refused here, at load time, before a single
// statement runs.
func Load(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("reading the migration directory: %w", err)
	}

	loaded := map[int]*Migration{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		fileName := entry.Name()
		version, name, direction, ok := parseFileName(fileName)
		if !ok {
			return nil, fmt.Errorf("migration file %q does not follow 0000_name%s or 0000_name%s", fileName, upSuffix, downSuffix)
		}

		contents, err := fs.ReadFile(fsys, fileName)
		if err != nil {
			return nil, fmt.Errorf("reading migration %q: %w", fileName, err)
		}
		if strings.TrimSpace(string(contents)) == "" {
			return nil, fmt.Errorf("migration %q is empty", fileName)
		}

		m, seen := loaded[version]
		if !seen {
			m = &Migration{Version: version, Name: name}
			loaded[version] = m
		}
		if m.Name != name {
			return nil, fmt.Errorf("version %d is named both %q and %q", version, m.Name, name)
		}

		if direction == directionUp {
			m.Up = string(contents)
		} else {
			m.Down = string(contents)
		}
	}

	ordered := make([]Migration, 0, len(loaded))
	for _, m := range loaded {
		ordered = append(ordered, *m)
	}
	slices.SortFunc(ordered, func(a, b Migration) int { return cmp.Compare(a.Version, b.Version) })

	for i, m := range ordered {
		switch {
		case m.Version != i+1:
			return nil, fmt.Errorf("migration versions must start at 1 and have no gaps: found version %d where %d was expected", m.Version, i+1)
		case m.Up == "":
			return nil, fmt.Errorf("version %d (%s) has no %s file", m.Version, m.Name, upSuffix)
		case m.Down == "":
			return nil, fmt.Errorf("version %d (%s) has no %s file", m.Version, m.Name, downSuffix)
		}
	}

	return ordered, nil
}

// The two directions a migration file can apply in.
const (
	directionUp   = "up"
	directionDown = "down"
)

// parseFileName splits NNNN_name.up.sql into its version, its name and the
// direction it applies in.
func parseFileName(fileName string) (version int, name, direction string, ok bool) {
	rest, isUp := strings.CutSuffix(fileName, upSuffix)
	switch {
	case isUp:
		direction = directionUp
	case strings.HasSuffix(fileName, downSuffix):
		rest, _ = strings.CutSuffix(fileName, downSuffix)
		direction = directionDown
	default:
		return 0, "", "", false
	}

	versionText, name, found := strings.Cut(rest, "_")
	if !found || len(versionText) != versionDigits {
		return 0, "", "", false
	}
	for _, r := range versionText {
		if r < '0' || r > '9' {
			return 0, "", "", false
		}
	}

	version, err := strconv.Atoi(versionText)
	if err != nil {
		return 0, "", "", false
	}

	// A name is a label, not an identifier, so it is only required to be
	// non-empty and free of separators: refusing more would surprise whoever
	// wrote the file, in a file nobody is currently looking at.
	if name == "" || strings.ContainsAny(name, " /\\.") {
		return 0, "", "", false
	}

	return version, name, direction, true
}
