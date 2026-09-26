// Package version reports the build version of the wiki binary.
//
// Every field is a variable so that it can be overridden at link time:
//
//	go build -ldflags "-X github.com/popinjayjohn/dine-and-dash-semiplane/internal/version.Version=v1.2.3"
//
// The defaults are what `go run` and `go test` see, and they deliberately say
// "dev" rather than pretending to be a release.
package version

import (
	"fmt"
	"runtime"
)

// Build metadata, overridden at link time. See the package comment.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// Info is the full build description, as reported by `wiki version` and by the
// /healthz endpoint.
type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
	Go      string `json:"go"`
}

// Get returns the build information for the running binary.
func Get() Info {
	return Info{
		Version: Version,
		Commit:  Commit,
		Date:    Date,
		Go:      fmt.Sprintf("%s %s/%s", runtime.Version(), runtime.GOOS, runtime.GOARCH),
	}
}

// String renders the build information on one line.
func (i Info) String() string {
	return fmt.Sprintf("%s (commit %s, built %s, %s)", i.Version, i.Commit, i.Date, i.Go)
}
