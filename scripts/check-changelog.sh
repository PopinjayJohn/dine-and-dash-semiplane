#!/bin/sh
#
# Fails if a commit changed code without changing CHANGELOG.md in the same
# commit. The rule is in AGENTS.md and CONTRIBUTING.md; this is the enforcement
# named in docs/spec.md §15.
#
# Usage:  scripts/check-changelog.sh [base-ref]
#
# The base ref is detected when it is not given, in this order:
#   1. the argument
#   2. $GITHUB_BASE_REF, on a pull request
#   3. $GITHUB_EVENT_BEFORE, on a workflow_dispatch or push
#   4. origin/main, main, or HEAD~1, whichever resolves
#
# Exits 0 when the rule holds, 1 when it does not, 2 when it cannot tell.

set -eu

CHANGELOG=CHANGELOG.md

# The paths whose changes a human reading release notes cares about. A commit
# that touches one of these and not $CHANGELOG is the thing being caught.
PROTECTED='^(internal|cmd|plugins|migrations|web)/'

die() {
	printf 'check-changelog: %s\n' "$1" >&2
	exit "${2:-2}"
}

resolve_base() {
	if [ $# -ge 1 ] && [ -n "$1" ]; then
		printf '%s\n' "$1"
		return
	fi
	if [ -n "${GITHUB_BASE_REF:-}" ]; then
		printf 'origin/%s\n' "$GITHUB_BASE_REF"
		return
	fi
	if [ -n "${GITHUB_EVENT_BEFORE:-}" ] && [ "$GITHUB_EVENT_BEFORE" != "0000000000000000000000000000000000000000" ]; then
		printf '%s\n' "$GITHUB_EVENT_BEFORE"
		return
	fi
	for candidate in origin/main main HEAD~1; do
		if git rev-parse --verify --quiet "$candidate" >/dev/null 2>&1; then
			printf '%s\n' "$candidate"
			return
		fi
	done
	die "cannot determine a base commit; pass one as an argument"
}

base=$(resolve_base "$@")
git rev-parse --verify --quiet "$base" >/dev/null 2>&1 ||
	die "base ref $base does not exist"

if git rev-parse --verify --quiet "$base^{commit}" >/dev/null 2>&1; then
	merge_base=$(git merge-base "$base" HEAD) || die "no common ancestor with $base"
else
	merge_base=$(git rev-parse "$base")
fi

# The empty commit is what a fresh repository has, and what an initial push to
# a new branch has. There is nothing to check.
if [ "$merge_base" = "$(git rev-parse HEAD)" ]; then
	printf 'check-changelog: nothing to check, %s is the first commit\n' "$(git rev-parse --short HEAD)"
	exit 0
fi

# A merge commit is not the change; its second parent's history is.
commits=$(git rev-list --no-merges "$merge_base"..HEAD)

if [ -z "$commits" ]; then
	printf 'check-changelog: no non-merge commits since %s\n' "$(git rev-parse --short "$merge_base")"
	exit 0
fi

# Collect the commits that touched code, and whether each one also touched the
# changelog. Shell rather than a pipeline so that a commit cannot be reported
# for a neighbour's changelog edit.
missing=''

for commit in $commits; do
	short=$(git rev-parse --short "$commit")
	subject=$(git log -1 --format=%s "$commit")

	files=$(git diff-tree --no-commit-id --name-only -r "$commit")

	# shellcheck disable=SC2086 # deliberate word splitting over $files
	if ! printf '%s\n' "$files" | grep -qE "$PROTECTED"; then
		continue
	fi

	if printf '%s\n' "$files" | grep -qx "$CHANGELOG"; then
		continue
	fi

	missing="$missing  $short $subject
"
done

if [ -n "$missing" ]; then
	printf 'check-changelog: these commits changed code but not %s\n' "$CHANGELOG" >&2
	printf '%s' "$missing" >&2
	printf '\n' >&2
	printf 'Every commit touching internal/, cmd/, plugins/, migrations/ or web/ must\n' >&2
	printf 'update %s in the same commit. Add an entry under "## [Unreleased]".\n' "$CHANGELOG" >&2
	exit 1
fi

printf 'check-changelog: ok, %s commit(s) since %s\n' \
	"$(printf '%s\n' "$commits" | wc -l | tr -d ' ')" \
	"$(git rev-parse --short "$merge_base")"
