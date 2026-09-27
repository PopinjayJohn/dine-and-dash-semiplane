// Package config reads the data directory's `config.yaml`, and applies ADR 0011's
// precedence: **environment over file**, with a flag above both.
//
// # Why this package exists at all
//
// ADR 0011 decided the shape and the precedence and said nothing about the
// implementation:
//
//	> "Precedence is environment over file, decided here because it was
//	> otherwise undecided. One source of truth per setting: the environment
//	> overrides, it does not merge field by field with a different precedence per
//	> key."
//
// `internal/datadir` was the first setting to exist and it implemented the
// precedence for itself. This package is that precedence for the other three, and it
// exists as a package rather than as functions in `cmd/wiki` because a setting read
// by two commands and answered differently is a setting nobody can debug.
//
// # One source of truth per setting
//
// Each setting is resolved in exactly one function here, and the order is always
// flag, then environment, then file, then the built-in default. There is no merging
// of a file's value for one key with a different precedence for another, and that is
// ADR 0011's sentence rather than an addition to it.
//
// # A missing file is not an error, and a broken one is
//
// A DM who has never written a `config.yaml` gets every default, and a DM who has
// written one gets it. A file that is not valid YAML is an **error**, and it names
// the file: a config loader that silently ignored a file a DM had written is the
// failure `internal/datadir` was careful to avoid when it declined to read one at
// all, and "the setting I changed had no effect" is the worst possible outcome for a
// file whose entire purpose is to be read.
//
// # Unknown keys are refused
//
// A `config.yaml` with a misspelled key is an error rather than a shrug. The setting
// was not applied and the DM would not know, and the whole reason for a file is that
// a DM can set something without reading this source.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// FileName is the config file's name inside a data directory.
//
// It is the name the spec's deployment layout gives it (spec §13) and it is a
// constant rather than a convention so that a command which writes it and a command
// which reads it cannot disagree.
const FileName = "config.yaml"

// The environment variables §13 names, and the settings they select.
//
// They are exported as a group of names because the four settings share one
// precedence function and a table of "which variable selects which setting" is a
// second answer to the same question.
const (
	// EnvListen is DDSP_LISTEN: the address to serve on, default `127.0.0.1:8080`.
	EnvListen = "DDSP_LISTEN"

	// EnvBaseURL is DDSP_BASE_URL: the origin share links are built against, as
	// scheme and host.
	EnvBaseURL = "DDSP_BASE_URL"

	// EnvTrustedProxies is DDSP_TRUSTED_PROXIES: a comma-separated list of
	// addresses whose `X-Forwarded-For` is believed.
	EnvTrustedProxies = "DDSP_TRUSTED_PROXIES"

	// EnvLogFormat is DDSP_LOG_FORMAT, which `internal/logfmt` resolves rather than
	// this package. It is named here so that a reader of §13's list finds all four
	// in one place, and it is *not* in [File]'s schema for the same reason a
	// setting resolved elsewhere is not resolved twice.
	EnvLogFormat = "DDSP_LOG_FORMAT"
)

// Config is what a data directory's config file holds, and nothing more.
//
// Every field is a string because every one of these settings is one: an address, an
// origin, a list of addresses. A typed field would need a parse and an error message
// here, and the parse has to happen anyway at the point of use, so this is a
// transcription of a file and not a validation of it.
type Config struct {
	// Listen is the address to bind, as host:port. Empty means the default.
	Listen string `yaml:"listen"`

	// BaseURL is the origin share links are built against, as scheme and host, with
	// no trailing slash. Empty means "the address this server is served on".
	BaseURL string `yaml:"base_url"`

	// TrustedProxies is the list of proxy addresses whose `X-Forwarded-For` is
	// believed, one per line in the file and comma-separated in the environment.
	//
	// **This is a list of addresses the DM has decided to trust, and the default
	// trusts nothing.** The consequence is that behind a reverse proxy a share link's
	// rate limit is applied per proxy rather than per player, and the alternative --
	// believing `X-Forwarded-For` from anywhere -- is a rate limit a stranger can
	// remove by sending a header. An empty value is the safe answer and the doc for
	// the field says what a DM gives up by setting it.
	TrustedProxies []string `yaml:"trusted_proxies"`
}

// Settings is a resolved configuration: the answer for one process, from every
// source, with the precedence already applied.
//
// It is a struct rather than four functions because a caller that resolved them one
// at a time could resolve them at different times, and `X-Forwarded-For` and
// `Listen` disagreeing about which file was read is not a thing anybody can debug
// from a log line.
type Settings struct {
	Listen         string
	BaseURL        string
	TrustedProxies []string
}

// DefaultListen is the address §13 specifies.
//
// Loopback and not `0.0.0.0`, and the reason is in `cmd/wiki/serve.go` where the flag
// default is: this is a wiki holding a campaign's secrets with a share link in front
// of it and no other authentication, and every interface is the right default for a
// service and the wrong one for this.
const DefaultListen = "127.0.0.1:8080"

// Load reads the settings for a data directory.
//
// The three steps are in the order they can fail, which is also the order a DM
// debugging a configuration wants them: the data directory is resolved first (so
// "not found" is about the directory), the file is read and parsed (so a typo is
// about the file), and the environment is applied (so a typo there is about the
// variable). Each error names which of the three it was.
func Load(dataDir string) (Settings, error) {
	settings := Settings{Listen: DefaultListen}

	// A file that is not there is the ordinary case and is not an error. Every
	// command resolves the data directory before this, so the path is already
	// absolute or already known-bad.
	path := filepath.Join(dataDir, FileName)
	fromFile, err := readFile(path)
	if err != nil {
		return Settings{}, err
	}

	// Highest precedence first, in the argument order [firstNonEmpty] is written
	// in: the environment beats the file, and the file beats the default.
	settings.Listen = firstNonEmpty(os.Getenv(EnvListen), fromFile.Listen, DefaultListen)
	settings.BaseURL = firstNonEmpty(os.Getenv(EnvBaseURL), fromFile.BaseURL, "")
	settings.TrustedProxies = resolveProxies(fromFile.TrustedProxies, os.Getenv(EnvTrustedProxies))

	return settings, nil
}

// readFile is one data directory's `config.yaml`, or the zero value when there is
// none.
//
// `os.IsNotExist` is the *only* "there is no file" answer, and anything else is
// reported. A file that cannot be read because it is a directory, or because the
// process may not read it, is a DM who wrote a config and is being ignored, and the
// alternative — carrying on with defaults — is the failure this package's doc comment
// is about.
func readFile(path string) (Config, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // the path is the data directory, not a request
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("reading %s: %w", path, err)
	}

	// `KnownFields` is the whole of the "unknown keys are refused" promise: a
	// misspelled `base_url` as `base_url_` is a setting that was not applied, and a
	// DM who cannot see that has a config file that does nothing.
	var parsed Config
	decoder := yaml.NewDecoder(strings.NewReader(string(raw)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&parsed); err != nil {
		return Config{}, fmt.Errorf("parsing %s: %w", path, err)
	}

	return parsed, nil
}

// resolveProxies is the trusted-proxy list, environment over file.
//
// The environment is comma-separated and the file is a YAML list, because that is how
// a person writes each of them: a YAML list is right for a file a human edits and
// wrong for a variable a shell sets. An empty result is an empty list and never nil,
// so a caller ranging over it does not have to test for nil to know it is empty.
func resolveProxies(fromFile []string, fromEnv string) []string {
	if fromEnv != "" {
		split := strings.Split(fromEnv, ",")
		proxies := make([]string, 0, len(split))
		for _, one := range split {
			if trimmed := strings.TrimSpace(one); trimmed != "" {
				proxies = append(proxies, trimmed)
			}
		}
		return proxies
	}

	proxies := make([]string, 0, len(fromFile))
	for _, one := range fromFile {
		if trimmed := strings.TrimSpace(one); trimmed != "" {
			proxies = append(proxies, trimmed)
		}
	}
	return proxies
}

// firstNonEmpty is the precedence for one setting, written once.
//
// **The arguments are in precedence order, highest first**, so that the call site
// reads the same way the rule does: `firstNonEmpty(os.Getenv(...), fromFile.X,
// Default)`. That is the only reason it is a function rather than a `switch` at the
// call site — a `switch` in [Load] would be four copies of ADR 0011's sentence, and
// the fifth setting would be the one with a different order in it.
func firstNonEmpty(highest, middle, lowest string) string {
	for _, candidate := range []string{highest, middle, lowest} {
		// Trimmed before the emptiness test, and the reason is a shell: a variable
		// set from an unset one is spaces rather than nothing, and
		// `DDSP_LISTEN="$LISTEN"` with `LISTEN` unset is the ordinary way to get
		// there. An address of spaces is a bind that fails with an error about a
		// string nobody recognises; falling through to the file or the default is
		// what the DM meant.
		//
		// The value that is used is the **trimmed** one, not the original, so a
		// trailing space in `config.yaml` is not carried into a listen address.
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
