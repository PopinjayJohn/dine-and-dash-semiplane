// Package web is the static half of the front end: the stylesheet, the pinned
// Datastar client, and anything else a browser fetches from this server.
//
// # Nothing is fetched at runtime
//
// ADR 0006 requires it and ADR 0011 explains why: a DM plays this at a table,
// and a table is a room with more than one device on it and no reason to have a
// working internet connection. A CDN is also a third party in the path of a
// campaign's secrets -- a `datastar.js` fetched at runtime is a script the
// application did not write running with the session cookie -- so embedding is
// the security answer as well as the offline one.
//
// `datastar.js` is vendored verbatim at the version ADR 0008 pins, and the test
// in this package reads the version out of the embedded bytes so an upgrade that
// forgets to update ADR 0008 fails rather than passing quietly. The source map
// it names at the end is not vendored: it is a developer convenience, and a
// browser that cannot find it says so in a console and otherwise reads the file
// the application actually serves.
//
// # One handler
//
// `Handler` is the whole surface. It is a handler rather than a
// `fs.FS` because two things have to be right that a raw `http.FileServer` gets
// wrong for embedded content, and both of them are correctness rather than
// polish:
//
//   - **The content type.** The HTTP layer sends `X-Content-Type-Options:
//     nosniff` on every response, so a `.js` served as `text/plain` is a script
//     the browser refuses to run. `mime.TypeByExtension` plus an explicit table
//     is what keeps that from happening on a machine whose `/etc/mime.types` is
//     not what the build machine's was.
//   - **Revalidation.** A DM replaces the binary and the CSS changes with it,
//     while the browser still has the old one. Embedded files have a zero
//     modification time, so `http.FileServer`'s `If-Modified-Since` never
//     matches and a browser with a heuristic cache would keep the old stylesheet
//     until it expired. An ETag computed from the bytes is what makes
//     revalidation work, and the response says `no-cache` so it is asked for
//     every time.
package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

// DatastarVersion is the client this binary embeds, which is the one ADR 0008
// pins.
//
// It is a constant and not a value read out of the file because the point of
// the pin is that it is a decision somebody made, and a version discovered at
// runtime is a version nobody chose.
const DatastarVersion = "1.0.4"

// files is the embedded tree, rooted at `static/`. `all:` includes files
// beginning with `_` and `.`, which the default pattern would skip, and a
// front end that quietly loses an asset because somebody named it `_fonts` is
// the kind of surprise this package exists to prevent.
//
//go:embed all:static
var files embed.FS

// contentTypes is the extension-to-type table for the files this package
// serves, because the system's table is not the build machine's table and a
// `nosniff` response with the wrong type is a broken page.
//
// `text/javascript` rather than `application/javascript` is the current
// standard spelling; both are accepted by every browser that matters and only one
// is in the registry.
var contentTypes = map[string]string{
	".css":   "text/css; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".webp":  "image/webp",
	".woff2": "font/woff2",
	".ico":   "image/x-icon",
	".map":   "application/json",
	".txt":   "text/plain; charset=utf-8",
}

// Handler serves the embedded assets beneath prefix.
//
// The prefix is stripped by this handler rather than by `http.StripPrefix` so
// that a request which does not carry it is a 404 from the same place that knows
// the answer, rather than a 404 from the mux. It is the only argument because it
// is the only thing the mount point and the asset tree can disagree about.
func Handler(prefix string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, prefix) {
			http.NotFound(w, r)
			return
		}

		name := strings.TrimPrefix(r.URL.Path, prefix)
		name = strings.TrimPrefix(name, "/")
		serve(w, r, name)
	})
}

func serve(w http.ResponseWriter, r *http.Request, name string) {
	if name == "" {
		http.NotFound(w, r)
		return
	}

	// path.Clean first, then the fs.ValidPath check: a name with a `..` in it is
	// either a traversal attempt or a mistake, and an embedded FS refuses to
	// open both, so this is about answering with a 404 rather than a 500.
	name = path.Clean(name)
	if !fs.ValidPath(name) || strings.HasPrefix(name, "..") {
		http.NotFound(w, r)
		return
	}

	contents, err := files.ReadFile(path.Join("static", name))
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// The ETag is the content, so two binaries with the same stylesheet agree
	// and two with a different one do not. It is quoted because that is what
	// `If-None-Match` carries.
	sum := sha256.Sum256(contents)
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`

	if match := r.Header.Get("If-None-Match"); match != "" && etchesMatch(match, etag) {
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Type", contentType(name))
	// `no-cache` rather than `no-store`: the bytes may be cached, they just may
	// not be reused without asking. That is what makes the ETag above do
	// anything, and it is the difference between a DM's browser re-checking a
	// 2 KiB stylesheet and silently rendering a campaign with last month's
	// layout.
	w.Header().Set("Cache-Control", "no-cache")

	// http.ServeContent for the range, last-modified and length handling, with
	// the zero time that embedded files have.
	http.ServeContent(w, r, name, time.Time{}, strings.NewReader(string(contents)))
}

// etchesMatch reports whether an `If-None-Match` header covers a tag.
//
// `*` matches anything, and a list is scanned rather than compared whole because
// a browser sends back every tag it has for the URL and a byte comparison would
// re-download the file on every navigation. The weak form is stripped because
// this content is a byte-for-byte embed, so a strong tag and a weak one mean
// the same thing here.
func etchesMatch(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" {
			return true
		}
		if strings.TrimPrefix(candidate, "W/") == strings.TrimPrefix(etag, "W/") {
			return true
		}
	}
	return false
}

// contentType is the type for a name, from the table above and then from the
// system. Unknown extensions are served as bytes rather than as a guessed type,
// because a wrong type under `nosniff` is a refusal and a missing one is only a
// missing feature.
func contentType(name string) string {
	if known, ok := contentTypes[strings.ToLower(path.Ext(name))]; ok {
		return known
	}
	if guessed := mime.TypeByExtension(path.Ext(name)); guessed != "" {
		return guessed
	}
	return "application/octet-stream"
}

// The one error this package has, for a caller that wants the file rather than a
// handler. It is exported because `wiki` needs to report a missing asset at
// startup rather than on the first request, and a missing asset in a binary
// that has been built is a bug in the build.
var ErrNoAsset = errors.New("web: no such embedded asset")

// Bytes returns one embedded file, for a caller embedding it in something else
// -- an inline stylesheet, a test, a build check. `ErrNoAsset` is returned for
// anything not in the tree, and a name that tries to leave it is treated as not
// being there rather than as an error about the name.
func Bytes(name string) ([]byte, error) {
	clean := path.Clean(strings.TrimPrefix(name, "/"))
	if !fs.ValidPath(clean) || strings.HasPrefix(clean, "..") {
		return nil, ErrNoAsset
	}

	contents, err := files.ReadFile(path.Join("static", clean))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNoAsset
	}
	if err != nil {
		return nil, err
	}
	return contents, nil
}

// DatastarPath is where the vendored client is served from, which the layout
// links to and the CSP allows.
const DatastarPath = "/static/datastar.js"

// StylesheetPath is where the stylesheet is served from.
const StylesheetPath = "/static/wiki.css"
