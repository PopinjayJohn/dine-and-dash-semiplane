// Package datadir works out where a DM's data directory is.
//
// ADR 0011 makes the data directory the backup unit, and makes one data
// directory per server. That makes its location the first thing any command
// that touches a database has to agree on: two commands that disagree about
// it will migrate one database and serve another, and nothing will say so.
//
// The precedence is the one ADR 0011 states — an explicit path beats the
// environment, and the environment beats the platform default — and it is
// three rules rather than a configuration language. config.yaml is not read
// yet, and until it is, the flag and DDSP_DATA_DIR are the whole surface.
// That is a deliberate gap rather than an oversight: a half-implemented
// config loader that silently ignored a config.yaml a DM had written would
// be worse than one that does not exist.
package datadir

import (
	"fmt"
	"os"
	"path/filepath"
)

// EnvVar is the environment variable ADR 0011 names for the data directory.
const EnvVar = "DDSP_DATA_DIR"

// appName is the directory the data directory is created inside, and is part
// of the path a DM has to remember.
const appName = "dine-and-dash-semiplane"

// Resolve returns the data directory to use, in ADR 0011's order of precedence:
// the path a command was given, then DDSP_DATA_DIR, then the platform
// default. An empty explicit path is not an error — most commands pass the
// zero value of their flag — and is resolved by the later rules.
func Resolve(explicit string) (string, error) {
	if explicit != "" {
		return filepath.Clean(explicit), nil
	}

	if fromEnv := os.Getenv(EnvVar); fromEnv != "" {
		return filepath.Clean(fromEnv), nil
	}

	return Default()
}

// Default returns the platform's data directory: the XDG-ish
// ~/.local/share/dine-and-dash-semiplane on Unix, and
// %LOCALAPPDATA%\dine-and-dash-semiplane on Windows, which is the same idea
// for the same reason — somewhere per-user that is not a working directory,
// so a DM never has to think about where it is.
func Default() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding the home directory, and %s is not set: %w", EnvVar, err)
	}

	// Windows keeps per-user application data somewhere of its own, and a
	// database in a home directory is a habit worth not forming.
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		return filepath.Join(local, appName), nil
	}

	return filepath.Join(home, ".local", "share", appName), nil
}

// DatabaseFile returns the path of the campaign database inside a data
// directory. It is a function rather than a constant because a Windows path
// joins with backslashes, and a hand-written join is a bug that only shows up
// on the platform nobody tested on.
func DatabaseFile(dir string) string {
	return filepath.Join(dir, "campaigns.db")
}
