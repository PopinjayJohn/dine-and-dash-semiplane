package render

import (
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// One parser, one lock, one path into it.
//
// # What this is for, and what it is not for
//
// It is tempting to write this as "goldmark's parser is not goroutine-safe, so
// this is a bug fix". I believed that, wrote it, and then could not show it: with
// the lock removed, `go test -race` on eight goroutines rendering and extracting
// secrets stays quiet. Reading the source, the field I suspected of being
// per-parse state — `escapedSpace` — is written once inside a `sync.Once`, so the
// write is ordered against every reader, and the per-parse state lives in
// `parser.Context`, which is new each time. goldmark documents nothing either
// way.
//
// So the honest position is that this is **defensive**, and the comment says what
// it is defending:
//
//   - A parser this program shares across every render, every link resolution and
//     every secret extraction, where the only reason it is safe today is a
//     conclusion I reached by reading somebody else's parser rather than by
//     anything the library states. That conclusion is worth not depending on. If
//     it is wrong, the lock costs nothing; if it is right and the lock is
//     redundant, the lock still costs nothing.
//   - A mutex rather than a pool of parsers: a pool means a goldmark instance per
//     worker and a `sync.Pool` of them is a second thing to reason about. This
//     program runs on a DM's own machine with a handful of readers, so serialising
//     the parse costs nothing anybody would notice.
//   - One lock, in `Renderer`, and one path. An earlier version of this file had a
//     `pipelineMu` for the package-level pipeline *and* a `parseMu` inside
//     `Renderer` — and the package-level pipeline *is* a `*Renderer`, so there
//     were two locks guarding one parser and neither was guarding it against the
//     other. That one was a real defect in the fix rather than in goldmark, and
//     it is why the lock lives where it does.
//
// The tests beside this one pin the *behaviour*: concurrent renders and
// concurrent secret extractions each get the answer for their own input, and a
// secret is never in the public text. That is worth having whether or not the
// lock is load-bearing, and it is the part a future change can actually break.

// parseSource parses a source buffer with the renderer's pipeline, under the
// lock. It is the only way to reach a parser in this package.
func (r *Renderer) parseSource(source []byte) ast.Node {
	r.parseMu.Lock()
	defer r.parseMu.Unlock()

	return r.md.Parser().Parse(text.NewReader(source))
}

// parseWithPipeline parses with the package-level pipeline, for the three callers
// that want a tree and nothing else — the link resolver, `SecretText` and
// `PublicText`. It goes through `parseSource` rather than reaching for the
// pipeline's parser itself, because a lock two call sites have to remember is a
// lock one of them will not.
func parseWithPipeline(body string) ast.Node {
	return pipeline.parseSource([]byte(body))
}
