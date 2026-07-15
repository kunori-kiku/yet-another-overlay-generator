#!/usr/bin/env bash

# Revalidate that the remote release tag is still one annotated tag peeling to
# the commit approved at the start of the transaction. main may advance later;
# the release identity itself may not move.

set -euo pipefail

[[ $# -eq 2 ]] || {
  echo "usage: $0 RELEASE_TAG EXPECTED_COMMIT" >&2
  exit 2
}

tag=$1
expected_commit=$2
[[ "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(preview|beta|rc)\.(0|[1-9][0-9]*))?$ ]] \
  || { echo "release ref verification failed: invalid tag $tag" >&2; exit 1; }
[[ "$expected_commit" =~ ^[0-9a-f]{40}$ ]] \
  || { echo "release ref verification failed: invalid commit $expected_commit" >&2; exit 1; }

remote=$(mktemp)
trap 'rm -f "$remote"' EXIT
git ls-remote --tags origin "refs/tags/$tag" "refs/tags/$tag^{}" >"$remote"
[[ $(wc -l <"$remote") -eq 2 ]] \
  || { echo "release ref verification failed: $tag is absent, lightweight, or ambiguous on origin" >&2; exit 1; }
remote_commit=$(awk -v ref="refs/tags/$tag^{}" '$2 == ref {print $1}' "$remote")
[[ "$remote_commit" == "$expected_commit" ]] \
  || { echo "release ref verification failed: $tag now peels to $remote_commit, expected $expected_commit" >&2; exit 1; }

# actions/checkout@v4 fetches annotated tags at depth 0, then force-updates the
# triggering refs/tags/<name> to github.sha (the peeled commit). Fetch the exact
# remote tag into a private namespace so every job validates the tag object
# itself without trusting or rewriting checkout's synthetic local ref.
validation_ref="refs/yaog-release-tags/$tag"
git fetch --force --no-tags origin "refs/tags/$tag:$validation_ref"
[[ "$(git cat-file -t "$validation_ref")" == tag ]] \
  || { echo "release ref verification failed: remote $tag is not annotated" >&2; exit 1; }
[[ "$(git rev-list -n 1 "$validation_ref")" == "$expected_commit" ]] \
  || { echo "release ref verification failed: fetched $tag does not peel to $expected_commit" >&2; exit 1; }
