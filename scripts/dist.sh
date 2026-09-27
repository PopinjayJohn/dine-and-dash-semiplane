#!/usr/bin/env bash
#
# Cross-compile the release archives.
#
# # Why a script and not a Makefile recipe
#
# CI's rule is "every job runs a make target, not a raw go command", and
# `make dist` runs this — so the rule holds. The rule is *not* "the logic
# lives in the Makefile", and a shell loop inside a recipe is a place where
# `$${var}` and `$(var)` are quietly different things: the first version of
# this in the Makefile produced `wiki_6f13943-dirty__` because a `$(DIST_DIR)`
# expanded next to a `$${target}` in a way that made the second one empty. That
# is not a Makefile bug, it is Makefile doing what Makefile does, and the
# answer is to stop asking it to be a shell.
#
# The third version of this recipe was correct and this script is where it
# lives.

set -euo pipefail

readonly module="github.com/popinjayjohn/dine-and-dash-semiplane"

# The platforms `internal/store` is tested on in CI's matrix, plus the two
# darwin builds a DM on a Mac needs. A platform nobody asked for is a binary
# nobody has run, and this project is one a single person installs.
readonly platforms=(
	"darwin amd64"
	"darwin arm64"
	"linux amd64"
	"linux arm64"
	"windows amd64"
)

readonly root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly dist="${root}/dist"

# `git describe` rather than a version in a file, because §15 says the version
# comes from the git tag and a version that has to be bumped in two places is a
# version that is wrong in one of them.
version="$(git -C "$root" describe --tags --always --dirty 2>/dev/null || echo dev)"
commit="$(git -C "$root" rev-parse --short HEAD 2>/dev/null || echo unknown)"
built="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# The same ldflags `make build` uses, so a release is stamped the same way a
# local build is. A bug report against a version string has to be traceable to
# a commit, and that is the whole of `-ldflags`.
ldflags="-s -w"
ldflags+=" -X ${module}/internal/version.Version=${version}"
ldflags+=" -X ${module}/internal/version.Commit=${commit}"
ldflags+=" -X ${module}/internal/version.Date=${built}"

mkdir -p "$dist"

for platform in "${platforms[@]}"; do
	read -r target machine <<<"$platform"

	extension=""
	if [ "$target" = "windows" ]; then
		extension=".exe"
	fi

	name="wiki_${version}_${target}_${machine}"
	stage="$(mktemp -d)"
	trap 'rm -rf "$stage"' RETURN

	echo "dist: ${target}/${machine}"
	GOOS="$target" GOARCH="$machine" go build -C "$root" -trimpath \
		-ldflags "$ldflags" \
		-o "${stage}/wiki${extension}" ./cmd/wiki

	# The two files a DM needs who is not going to read a git repository: what
	# this is, and what it holds. `docs/security.md` is the second because the
	# first question about a self-hosted wiki holding a campaign is what it is
	# protected by, and the answer being in the download rather than on a website
	# is the point of "one binary, one data directory".
	cp "$root/README.md" "${stage}/README.md"
	if [ -d "$root/docs" ]; then
		mkdir -p "${stage}/docs"
		cp "$root/docs/security.md" "${stage}/docs/security.md"
	fi

	# A tar and not a zip, because every platform's tools can make one and not
	# every platform's tools can unzip one. `tar -xzf` on the far end is the
	# ADR 0011 property: a restore that needs this project to be working.
	tar -C "$stage" -czf "${dist}/${name}.tar.gz" .

	echo "dist:   ${dist}/${name}.tar.gz"
done

echo "dist: $(find "$dist" -name '*.tar.gz' | wc -l | tr -d ' ') archive(s) in ${dist}"
echo "dist: version ${version} (${commit})"
