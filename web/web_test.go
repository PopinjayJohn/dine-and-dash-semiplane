package web_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/web"
)

// TestTheVendoredClientIsTheVersionThePinNames is the guard on ADR 0008.
//
// The pin is a decision somebody made about a file that is now in the binary. If
// somebody upgrades the vendored client and forgets the constant -- or the ADR --
// then the two disagree and the one anybody reads at 2am is the constant. So the
// constant is checked against the bytes.
func TestTheVendoredClientIsTheVersionThePinNames(t *testing.T) {
	t.Parallel()

	script, err := web.Bytes("datastar.js")
	if err != nil {
		t.Fatalf("Bytes(datastar.js): %v", err)
	}

	marker := []byte("// Datastar v" + web.DatastarVersion)
	if !strings.HasPrefix(string(script), string(marker)) {
		t.Errorf("the vendored client does not start with %q, so it is not the pinned version", marker)
	}
}

// TestNothingIsFetchedFromTheNetworkAtRuntime: a script whose bytes are in the
// binary cannot be swapped by whoever answers a hostname, and a stylesheet that
// names a remote URL is a request this application makes on every page view.
// Neither is acceptable on a machine with no network, and neither is acceptable
// in the path of a campaign's secrets.
//
// The two files are checked for different things, and the difference is not
// fussiness. A stylesheet has exactly one way to reach the network -- a remote
// URL in a `url()` or an `@import` -- so any URL at all is a finding. A JavaScript
// bundle is full of strings that merely look like URLs, XML namespaces being the
// obvious one, so the question for it is whether it *imports* from one: a vendored
// ES module with a remote specifier is a script that does not exist offline.
func TestNothingIsFetchedFromTheNetworkAtRuntime(t *testing.T) {
	t.Parallel()

	t.Run("the stylesheet names no remote URL", func(t *testing.T) {
		t.Parallel()

		css, err := web.Bytes("wiki.css")
		if err != nil {
			t.Fatalf("Bytes(wiki.css): %v", err)
		}

		// A protocol-relative URL is the one that looks local and is not.
		for _, remote := range []string{"http://", "https://", "//", "@import"} {
			if strings.Contains(string(css), remote) {
				t.Errorf("the stylesheet contains %q, so the page will fetch something at runtime", remote)
			}
		}
	})

	t.Run("the client imports nothing remote", func(t *testing.T) {
		t.Parallel()

		script, err := web.Bytes("datastar.js")
		if err != nil {
			t.Fatalf("Bytes(datastar.js): %v", err)
		}

		// The bundle is minified, so an import specifier is `from"..."` with no
		// space. Both quote styles and the `import("...")` form are checked,
		// because a remote dynamic import is the same failure as a remote
		// static one.
		for _, specifier := range []string{`from"http`, `from"//`, `from'http`, `from'//`, `import("http`, `import("//`} {
			if strings.Contains(string(script), specifier) {
				t.Errorf("the vendored client contains %q, so it is not self-contained", specifier)
			}
		}
	})
}

// TestTheClientIsServedWithATypeABrowserWillRun: the HTTP layer sends
// `X-Content-Type-Options: nosniff`, so a module served as `text/plain` is a
// script the browser refuses. This is a correctness test and not a polish one.
func TestTheClientIsServedWithATypeABrowserWillRun(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		web.DatastarPath:   "text/javascript; charset=utf-8",
		web.StylesheetPath: "text/css; charset=utf-8",
	}

	for path, want := range tests {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			got := get(t, web.Handler("/static/"), path)

			if got.header.Get("Content-Type") != want {
				t.Errorf("Content-Type is %q, want %q", got.header.Get("Content-Type"), want)
			}
			if len(got.body) == 0 {
				t.Error("the response is empty")
			}
		})
	}
}

// TestTheStylesheetIsServedFromWhereTheLayoutLinksIt: the two are constants in
// two packages, and a layout that linked to a path the handler does not serve
// would be a wiki with no styling and no error.
func TestTheStylesheetIsServedFromWhereTheLayoutLinksIt(t *testing.T) {
	t.Parallel()

	for _, path := range []string{web.DatastarPath, web.StylesheetPath} {
		if !strings.HasPrefix(path, "/static/") {
			t.Fatalf("%q is not under the mount point the handler is given", path)
		}
		if got := get(t, web.Handler("/static/"), path); got.status != http.StatusOK {
			t.Errorf("GET %s is %d, want 200", path, got.status)
		}
	}
}

// TestTheAssetsRevalidate: a DM replaces the binary and the stylesheet changes
// with it, while the browser still has the old one. Embedded files have a zero
// modification time, so this has to be the ETag or it is nothing.
func TestTheAssetsRevalidate(t *testing.T) {
	t.Parallel()

	handler := web.Handler("/static/")

	first := get(t, handler, web.StylesheetPath)
	if first.ETag() == "" {
		t.Fatal("the response carries no ETag, so a browser cannot revalidate it")
	}
	if got := first.header.Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control is %q, want no-cache", got)
	}

	revalidated := getWith(t, handler, web.StylesheetPath, map[string]string{"If-None-Match": first.ETag()})
	if revalidated.status != http.StatusNotModified {
		t.Errorf("a request with If-None-Match was answered %d, want 304", revalidated.status)
	}
	if len(revalidated.body) != 0 {
		t.Errorf("a 304 carried %d bytes", len(revalidated.body))
	}

	// A stale tag is a full answer, not a 304, or a DM would be left with a
	// stylesheet from a binary they are no longer running.
	stale := getWith(t, handler, web.StylesheetPath, map[string]string{"If-None-Match": `"not-this-one"`})
	if stale.status != http.StatusOK {
		t.Errorf("a request with a stale ETag was answered %d, want 200", stale.status)
	}
}

// TestNothingElseIsServed: the handler answers for the tree it embeds and not for
// the machine it runs on. A traversal out of `static/`, a path under it that is
// not in the embed, and a request for the mount point itself are all 404s.
func TestNothingElseIsServed(t *testing.T) {
	t.Parallel()

	handler := web.Handler("/static/")

	tests := map[string]struct {
		path string
		want int
	}{
		"a file that is not embedded":         {path: "/static/config.yaml", want: http.StatusNotFound},
		"a traversal out of the tree":         {path: "/static/../web.go", want: http.StatusNotFound},
		"an encoded traversal":                {path: "/static/%2e%2e/web.go", want: http.StatusNotFound},
		"the mount point itself":              {path: "/static/", want: http.StatusNotFound},
		"a path outside the mount point":      {path: "/assets/wiki.css", want: http.StatusNotFound},
		"a directory inside the tree":         {path: "/static/static", want: http.StatusNotFound},
		"a file the pin names, with a prefix": {path: "/static/datastar.js.map", want: http.StatusNotFound},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := get(t, handler, tt.path); got.status != tt.want {
				t.Errorf("GET %s is %d, want %d", tt.path, got.status, tt.want)
			}
		})
	}
}

// TestBytesRefusesToLeaveTheTree: `Bytes` is what a caller embedding an asset in
// something else uses, and the answer for a name that tries to climb out is "no
// such asset" rather than an error about the name, so a caller that passes one
// gets the same answer it would get for a typo.
func TestBytesRefusesToLeaveTheTree(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"../web.go", "/../go.mod", "nothing-here.css", ""} {
		if _, err := web.Bytes(name); err == nil {
			t.Errorf("Bytes(%q) returned no error", name)
		}
	}
}

// TestEveryStaticFileIsReachable: the embed pattern and the handler are two
// pieces of code that have to agree, and the failure is a binary that builds and
// serves a page with no stylesheet. This walks the directory on disk -- the same
// directory the embed reads -- and asks the handler for every file in it.
func TestEveryStaticFileIsReachable(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir("static")
	if err != nil {
		t.Fatalf("reading static/: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("static/ is empty, so this binary would serve a page with nothing on it")
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := "/static/" + entry.Name()
		if got := get(t, web.Handler("/static/"), path); got.status != http.StatusOK {
			t.Errorf("GET %s is %d, want 200", path, got.status)
		}
	}
}

// TestTheVendoredFilesAreTheOnesOnDisk: vendoring is a copy, and a copy that
// somebody edits in place is a file the ADR's version no longer describes. This
// is the check that makes "vendored verbatim" a fact about the repository rather
// than an intention.
func TestTheVendoredFilesAreTheOnesOnDisk(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir("static")
	if err != nil {
		t.Fatalf("reading static/: %v", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		onDisk, err := os.ReadFile(filepath.Join("static", entry.Name()))
		if err != nil {
			t.Fatalf("reading %s: %v", entry.Name(), err)
		}
		embedded, err := web.Bytes(entry.Name())
		if err != nil {
			t.Fatalf("Bytes(%s): %v", entry.Name(), err)
		}
		if string(onDisk) != string(embedded) {
			t.Errorf("%s is embedded differently from the file on disk", entry.Name())
		}
	}
}

// response is what a test reads back: the bytes, the headers, and the status,
// which are three things a handler test always wants together.
type response struct {
	body   []byte
	header http.Header
	status int
}

// ETag is a convenience for the one header the revalidation test cares about.
func (r response) ETag() string { return r.header.Get("ETag") }

func get(t *testing.T, handler http.Handler, path string) response {
	t.Helper()

	return getWith(t, handler, path, nil)
}

func getWith(t *testing.T, handler http.Handler, path string, headers map[string]string) response {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	for name, value := range headers {
		req.Header.Set(name, value)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	result := rec.Result()
	return response{body: body, header: result.Header, status: result.StatusCode}
}
