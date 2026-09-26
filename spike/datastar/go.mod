// This is a separate module on purpose. The spike exists to answer the
// "known risk" in docs/spec.md §3 and ADR 0006, and it must not put
// datastar-go into the application's dependency graph before M10 decides
// whether it is worth the dependency.
//
// Delete this directory in M10, when internal/sse lands for real.
module github.com/popinjayjohn/dine-and-dash-semiplane/spike/datastar

go 1.24

require github.com/starfederation/datastar-go v1.2.2

require (
	github.com/CAFxX/httpcompression v0.0.9 // indirect
	github.com/andybalholm/brotli v1.2.0 // indirect
	github.com/klauspost/compress v1.18.0 // indirect
	github.com/valyala/bytebufferpool v1.0.0 // indirect
)
