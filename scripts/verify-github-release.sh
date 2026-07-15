#!/usr/bin/env bash

# Fail-closed GitHub Release transaction checks. Requires GH_TOKEN and gh/jq.

set -euo pipefail

export LC_ALL=C

die() {
  echo "GitHub release verification failed: $*" >&2
  exit 1
}

usage() {
  cat >&2 <<'EOF'
usage:
  verify-github-release.sh preflight-state REPOSITORY TAG PRERELEASE
  verify-github-release.sh preflight-upload REPOSITORY TAG PRERELEASE ASSET_DIR
  verify-github-release.sh verify REPOSITORY RELEASE_ID TAG DRAFT PRERELEASE ASSET_DIR LATEST_POLICY
  verify-github-release.sh publish REPOSITORY RELEASE_ID TAG PRERELEASE ASSET_DIR LATEST_POLICY

PRERELEASE and DRAFT are true or false. LATEST_POLICY is ignore, must, or must-not.
preflight-upload prints release_status and release_id key/value lines for GITHUB_OUTPUT.
publish re-seals the exact release, converges draft/Latest state when needed, and verifies the result.
EOF
  exit 2
}

require_bool() {
  [[ "$1" == true || "$1" == false ]] || die "expected boolean, got $1"
}

local_assets_json() {
  local asset_dir=$1
  local output=$2
  local ndjson
  ndjson=$(mktemp)
  : >"$ndjson"
  local path name digest size
  while IFS= read -r -d '' path; do
    name=${path##*/}
    digest="sha256:$(sha256sum "$path" | awk '{print $1}')"
    size=$(wc -c <"$path")
    jq -cn --arg name "$name" --arg digest "$digest" --argjson size "$size" \
      '{name: $name, digest: $digest, size: $size}' >>"$ndjson"
  done < <(find "$asset_dir" -mindepth 1 -maxdepth 1 -type f ! -lname '*' -print0)
  jq -s 'sort_by(.name)' "$ndjson" >"$output"
  rm -f "$ndjson"
  jq -e 'length == 22 and (map(.name) | unique | length == 22)' "$output" >/dev/null \
    || die "local publish input is not the exact 22-file set"
}

tag_releases_json() {
  local repository=$1
  local tag=$2
  local output=$3
  local all
  all=$(mktemp)
  gh api --paginate "repos/$repository/releases?per_page=100" --jq '.[]' >"$all"
  jq -s --arg tag "$tag" '[.[] | select(.tag_name == $tag)]' "$all" >"$output"
  rm -f "$all"
}

fetch_assets() {
  local repository=$1
  local release_id=$2
  local output=$3
  gh api "repos/$repository/releases/$release_id/assets?per_page=100" >"$output"
}

assert_asset_subset() {
  local remote=$1
  local local_json=$2
  jq -e --slurpfile local "$local_json" '
    INDEX($local[0][]; .name) as $expected
    |
    (map(.name) | unique | length) == length
    and all(.state == "uploaded" and .size > 0 and (.digest | type == "string"))
    and all(.[];
      $expected[.name] != null
      and .size == $expected[.name].size
      and .digest == $expected[.name].digest)
  ' "$remote" >/dev/null || die "existing draft contains an unexpected, incomplete, or different-byte asset"
}

assert_exact_assets() {
  local remote=$1
  local local_json=$2
  jq -e --slurpfile local "$local_json" '
    length == 22
    and (map(.name) | unique | length == 22)
    and all(.state == "uploaded" and .size > 0 and (.digest | type == "string"))
    and ([.[] | {name, size, digest}] | sort_by(.name)) == $local[0]
  ' "$remote" >/dev/null || die "remote release assets do not exactly match the verified 22 local files"
}

assert_release_metadata() {
  local release_json=$1
  local release_id=$2
  local tag=$3
  local draft=$4
  local prerelease=$5
  jq -e --argjson id "$release_id" --arg tag "$tag" --argjson draft "$draft" --argjson prerelease "$prerelease" '
    .id == $id
    and .tag_name == $tag
    and .draft == $draft
    and .prerelease == $prerelease
  ' "$release_json" >/dev/null || die "release ID/tag/draft/prerelease metadata is not exact"
}

read_latest_tag() {
  local repository=$1
  local error
  local latest
  error=$(mktemp)
  if latest=$(gh api "repos/$repository/releases/latest" --jq .tag_name 2>"$error"); then
    rm -f "$error"
    printf '%s' "$latest"
    return 0
  fi
  if grep -q 'HTTP 404' "$error"; then
    rm -f "$error"
    return 0
  fi
  cat "$error" >&2
  rm -f "$error"
  die "could not read GitHub Latest"
}

assert_latest_policy() {
  local repository=$1
  local tag=$2
  local policy=$3
  [[ "$policy" == ignore ]] && return 0
  local latest
  latest=$(read_latest_tag "$repository")
  case "$policy" in
    must) [[ "$latest" == "$tag" ]] || die "Latest is ${latest:-absent}, expected $tag" ;;
    must-not) [[ "$latest" != "$tag" ]] || die "$tag unexpectedly replaced GitHub Latest" ;;
    *) die "invalid Latest policy: $policy" ;;
  esac
}

[[ $# -ge 1 ]] || usage
mode=$1
shift

case "$mode" in
  preflight-state)
    [[ $# -eq 3 ]] || usage
    repository=$1 tag=$2 prerelease=$3
    require_bool "$prerelease"
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    tag_releases_json "$repository" "$tag" "$tmp/releases.json"
    count=$(jq 'length' "$tmp/releases.json")
    [[ "$count" -le 1 ]] || die "more than one release object exists for $tag"
    if [[ "$count" -eq 1 ]]; then
      jq -e --arg tag "$tag" --argjson prerelease "$prerelease" '
        .[0].tag_name == $tag
        and .[0].draft == true
        and .[0].prerelease == $prerelease
        and (.[0].id | type == "number")
      ' "$tmp/releases.json" >/dev/null \
        || die "existing release is public or has the wrong draft/prerelease classification"
    fi
    ;;

  preflight-upload)
    [[ $# -eq 4 ]] || usage
    repository=$1 tag=$2 prerelease=$3 asset_dir=${4%/}
    require_bool "$prerelease"
    [[ -d "$asset_dir" ]] || die "asset directory does not exist"
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    local_assets_json "$asset_dir" "$tmp/local.json"
    tag_releases_json "$repository" "$tag" "$tmp/releases.json"
    count=$(jq 'length' "$tmp/releases.json")
    [[ "$count" -le 1 ]] || die "more than one release object exists for $tag"
    if [[ "$count" -eq 0 ]]; then
      printf 'release_status=absent\nrelease_id=\n'
      exit 0
    fi
    jq -e --arg tag "$tag" --argjson prerelease "$prerelease" '
      length == 1
      and .[0].tag_name == $tag
      and .[0].draft == true
      and .[0].prerelease == $prerelease
      and (.[0].id | type == "number")
    ' "$tmp/releases.json" >/dev/null \
      || die "existing release is public or has the wrong draft/prerelease classification"
    release_id=$(jq -r '.[0].id' "$tmp/releases.json")
    fetch_assets "$repository" "$release_id" "$tmp/remote.json"
    assert_asset_subset "$tmp/remote.json" "$tmp/local.json"
    printf 'release_status=draft\nrelease_id=%s\n' "$release_id"
    ;;

  verify)
    [[ $# -eq 7 ]] || usage
    repository=$1 release_id=$2 tag=$3 draft=$4 prerelease=$5 asset_dir=${6%/} latest_policy=$7
    [[ "$release_id" =~ ^[0-9]+$ ]] || die "release ID is not numeric"
    require_bool "$draft"
    require_bool "$prerelease"
    [[ "$latest_policy" == ignore || "$latest_policy" == must || "$latest_policy" == must-not ]] \
      || die "invalid Latest policy: $latest_policy"
    [[ -d "$asset_dir" ]] || die "asset directory does not exist"
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    local_assets_json "$asset_dir" "$tmp/local.json"
    gh api "repos/$repository/releases/$release_id" >"$tmp/release.json"
    assert_release_metadata "$tmp/release.json" "$release_id" "$tag" "$draft" "$prerelease"
    fetch_assets "$repository" "$release_id" "$tmp/remote.json"
    assert_exact_assets "$tmp/remote.json" "$tmp/local.json"
    assert_latest_policy "$repository" "$tag" "$latest_policy"
    ;;

  publish)
    [[ $# -eq 6 ]] || usage
    repository=$1 release_id=$2 tag=$3 prerelease=$4 asset_dir=${5%/} latest_policy=$6
    [[ "$release_id" =~ ^[0-9]+$ ]] || die "release ID is not numeric"
    require_bool "$prerelease"
    [[ "$latest_policy" == must || "$latest_policy" == must-not ]] \
      || die "publish Latest policy must be must or must-not"
    [[ -d "$asset_dir" ]] || die "asset directory does not exist"
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    local_assets_json "$asset_dir" "$tmp/local.json"

    # Seal the exact object before every possible mutation. A retry may observe
    # either the private draft or an already-public release whose Latest update
    # was lost/undone after the earlier PATCH response.
    gh api "repos/$repository/releases/$release_id" >"$tmp/release-before.json"
    draft=$(jq -er '.draft as $draft
      | if ($draft | type) == "boolean" then ($draft | tostring)
        else error("release draft state is not boolean") end' "$tmp/release-before.json") \
      || die "release draft state is not boolean"
    assert_release_metadata "$tmp/release-before.json" "$release_id" "$tag" "$draft" "$prerelease"
    fetch_assets "$repository" "$release_id" "$tmp/remote-before.json"
    assert_exact_assets "$tmp/remote-before.json" "$tmp/local.json"

    latest=$(read_latest_tag "$repository")
    needs_patch=false
    [[ "$draft" == true ]] && needs_patch=true
    if [[ "$latest_policy" == must && "$latest" != "$tag" ]]; then
      needs_patch=true
      make_latest=true
    elif [[ "$latest_policy" == must-not && "$latest" == "$tag" ]]; then
      needs_patch=true
      make_latest=false
    elif [[ "$latest_policy" == must ]]; then
      make_latest=true
    else
      make_latest=false
    fi

    if [[ "$needs_patch" == true ]]; then
      gh api --method PATCH "repos/$repository/releases/$release_id" \
        -F draft=false \
        -F prerelease="$prerelease" \
        -f make_latest="$make_latest" >"$tmp/published.json"
      assert_release_metadata "$tmp/published.json" "$release_id" "$tag" false "$prerelease"
    fi

    gh api "repos/$repository/releases/$release_id" >"$tmp/release-after.json"
    assert_release_metadata "$tmp/release-after.json" "$release_id" "$tag" false "$prerelease"
    fetch_assets "$repository" "$release_id" "$tmp/remote-after.json"
    assert_exact_assets "$tmp/remote-after.json" "$tmp/local.json"
    assert_latest_policy "$repository" "$tag" "$latest_policy"
    ;;

  *) usage ;;
esac
