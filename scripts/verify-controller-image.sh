#!/usr/bin/env bash

# Verify one policy-non-overwritten controller image reference. The only stdout
# value on success is its multi-platform digest. Exit 3 means a proven-absent
# reference; all registry/auth/shape/content failures exit 1.

set -euo pipefail

die() {
  echo "controller image verification failed: $*" >&2
  exit 1
}

[[ $# -eq 3 || $# -eq 4 ]] || {
  echo "usage: $0 IMAGE_REF EXPECTED_VERSION EXPECTED_REVISION [EXPECTED_DIGEST]" >&2
  exit 2
}

image_ref=$1
expected_version=$2
expected_revision=$3
expected_digest=${4:-}

[[ "$expected_version" == edge || "$expected_version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(preview|beta|rc)\.(0|[1-9][0-9]*))?$ ]] \
  || die "invalid expected version"
[[ "$expected_revision" =~ ^[0-9a-f]{40}$ ]] || die "expected revision is not a full Git SHA"
[[ -z "$expected_digest" || "$expected_digest" =~ ^sha256:[0-9a-f]{64}$ ]] \
  || die "expected digest is invalid"

manifest=$(mktemp)
inspect_error=$(mktemp)
version_output=$(mktemp)
image_config=$(mktemp)
trap 'rm -f "$manifest" "$inspect_error" "$version_output" "$image_config"' EXIT

inspect_ok=false
absence_only=true
is_absence_error() {
  grep -Fqx "ERROR: $image_ref: not found" "$inspect_error" \
    || grep -Eqi '(manifest unknown|name unknown|no such manifest|HTTP 404|unexpected status[^:]*: 404)' "$inspect_error"
}
for attempt in 1 2 3 4 5; do
  : >"$inspect_error"
  if docker buildx imagetools inspect "$image_ref" --format '{{json .Manifest}}' >"$manifest" 2>"$inspect_error"; then
    inspect_ok=true
    break
  fi
  if ! is_absence_error; then
    absence_only=false
  fi
  (( attempt == 5 )) || sleep $((attempt * 2))
done

if [[ "$inspect_ok" != true ]]; then
  if [[ "$absence_only" == true ]]; then
    exit 3
  fi
  cat "$inspect_error" >&2
  die "could not inspect $image_ref after bounded retries"
fi

digest=$(jq -er '.digest | select(test("^sha256:[0-9a-f]{64}$"))' "$manifest") \
  || die "$image_ref did not return a valid index digest"
if [[ -n "$expected_digest" && "$digest" != "$expected_digest" ]]; then
  die "$image_ref resolves to $digest, expected $expected_digest"
fi
jq -e '
  [
    .manifests[]
    | select(.platform.os != "unknown")
    | (.platform.os + "/" + .platform.architecture)
  ]
  | sort == ["linux/amd64", "linux/arm64"]
' "$manifest" >/dev/null \
  || die "$image_ref does not contain exactly linux/amd64 and linux/arm64 runtime images"

# Read each runtime child config by digest, then execute both children through
# the verified parent index. No mutable tag lookup is reused for labels/runtime.
for architecture in amd64 arm64; do
  child_digest=$(jq -er --arg architecture "$architecture" '
    .manifests[]
    | select(.platform.os == "linux" and .platform.architecture == $architecture)
    | .digest | select(test("^sha256:[0-9a-f]{64}$"))
  ' "$manifest") || die "$image_ref has no valid linux/$architecture child digest"
  config_ok=false
  for attempt in 1 2 3 4 5; do
    if docker buildx imagetools inspect "$image_ref@$child_digest" --format '{{json .Image}}' >"$image_config"; then
      config_ok=true
      break
    fi
    (( attempt == 5 )) || sleep $((attempt * 2))
  done
  [[ "$config_ok" == true ]] || die "could not inspect the verified $architecture image config"
  revision=$(jq -er '.config.Labels["org.opencontainers.image.revision"] // empty' "$image_config") \
    || die "$image_ref linux/$architecture has no source revision label"
  version_label=$(jq -er '.config.Labels["org.opencontainers.image.version"] // empty' "$image_config") \
    || die "$image_ref linux/$architecture has no semantic version label"
  [[ "$revision" == "$expected_revision" ]] \
    || die "$image_ref linux/$architecture revision label is $revision, expected $expected_revision"
  [[ "$version_label" == "$expected_version" ]] \
    || die "$image_ref linux/$architecture version label is $version_label, expected $expected_version"
  runtime_ok=false
  for attempt in 1 2 3 4 5; do
    if docker run --rm --platform "linux/$architecture" "$image_ref@$digest" version >"$version_output"; then
      runtime_ok=true
      break
    fi
    (( attempt == 5 )) || sleep $((attempt * 2))
  done
  [[ "$runtime_ok" == true ]] || die "$image_ref linux/$architecture version command failed"
  cmp -s "$version_output" <(printf '%s\n' "$expected_version") \
    || die "$image_ref linux/$architecture runtime does not report exact version $expected_version"
done

printf '%s\n' "$digest"
