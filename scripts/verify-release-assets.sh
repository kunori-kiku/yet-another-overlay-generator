#!/usr/bin/env bash

# Verify the complete publishable YAOG release set. This verifier is deliberately
# fail-closed: exact outer names, safe archive structure, every target's Go build
# metadata/version, and byte-identical bundled/standalone agents are all required.

set -euo pipefail

export LC_ALL=C

usage() {
  echo "usage: $0 <asset-directory> <release-tag> <source-commit>" >&2
  exit 2
}

fail() {
  echo "release asset verification failed: $*" >&2
  exit 1
}

[[ $# -eq 3 ]] || usage

asset_dir=${1%/}
release_tag=$2
source_commit=$3

[[ -d "$asset_dir" ]] || fail "asset directory does not exist: $asset_dir"
[[ "$release_tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(preview|beta|rc)\.(0|[1-9][0-9]*))?$ ]] \
  || fail "unsupported release tag: $release_tag"
[[ "$source_commit" =~ ^[0-9a-f]{40}$ ]] || fail "source commit must be a full lowercase Git SHA"

targets=(
  linux-amd64
  linux-arm64
  linux-386
  linux-armv7
  windows-amd64
  windows-arm64
  windows-386
)

declare -A target_goos=(
  [linux-amd64]=linux [linux-arm64]=linux [linux-386]=linux [linux-armv7]=linux
  [windows-amd64]=windows [windows-arm64]=windows [windows-386]=windows
)
declare -A target_goarch=(
  [linux-amd64]=amd64 [linux-arm64]=arm64 [linux-386]=386 [linux-armv7]=arm
  [windows-amd64]=amd64 [windows-arm64]=arm64 [windows-386]=386
)
declare -A target_goarm=([linux-armv7]=7)

expected_assets=()
for target in "${targets[@]}"; do
  if [[ "$target" == windows-* ]]; then
    archive_ext=zip
    executable_ext=.exe
  else
    archive_ext=tar.gz
    executable_ext=
  fi
  expected_assets+=(
    "yaog-bundle-${target}.${archive_ext}"
    "yaog-agent-${target}${executable_ext}"
    "yaog-agent-${target}${executable_ext}.sha256"
  )
done
expected_assets+=("yaog-local-design-${release_tag}.zip")

[[ ${#expected_assets[@]} -eq 22 ]] || fail "internal allowlist error: expected 22 names"

actual_count=$(find "$asset_dir" -mindepth 1 -maxdepth 1 -printf . | wc -c)
[[ "$actual_count" -eq 22 ]] || fail "expected exactly 22 assets, found $actual_count"

expected_list=$(mktemp)
actual_list=$(mktemp)
work_dir=$(mktemp -d)
trap 'rm -f "$expected_list" "$actual_list"; rm -rf "$work_dir"' EXIT

printf '%s\n' "${expected_assets[@]}" | sort >"$expected_list"
find "$asset_dir" -mindepth 1 -maxdepth 1 -printf '%f\n' | sort >"$actual_list"
if ! diff -u "$expected_list" "$actual_list"; then
  fail "asset names do not match the 22-file allowlist"
fi

for name in "${expected_assets[@]}"; do
  path="$asset_dir/$name"
  [[ -f "$path" && ! -L "$path" ]] || fail "asset is not a regular non-symlink file: $name"
  [[ -s "$path" ]] || fail "asset is empty: $name"
done

for target in "${targets[@]}"; do
  executable_ext=
  [[ "$target" == windows-* ]] && executable_ext=.exe
  agent="yaog-agent-${target}${executable_ext}"
  sidecar="$asset_dir/$agent.sha256"
  sidecar_bytes=$(wc -c <"$sidecar")
  sidecar_lines=$(wc -l <"$sidecar")
  expected_digest=$(<"$sidecar")
  [[ "$sidecar_bytes" -eq 65 && "$sidecar_lines" -eq 1 ]] \
    || fail "$agent.sha256 must contain exactly 64 hexadecimal bytes and one newline"
  [[ "$expected_digest" =~ ^[0-9a-f]{64}$ ]] \
    || fail "$agent.sha256 is not a lowercase SHA-256 digest"
  actual_digest=$(sha256sum "$asset_dir/$agent" | awk '{print $1}')
  [[ "$actual_digest" == "$expected_digest" ]] \
    || fail "$agent does not match its SHA-256 sidecar"
done

archive_auditor="$work_dir/release-archive-audit"
go build -trimpath -o "$archive_auditor" ./scripts/release_archive_audit.go

audit_and_extract() {
  local archive=$1
  local destination=$2
  local manifest=$3
  shift 3
  local args=(-archive "$archive" -extract-dir "$destination" -manifest "$manifest")
  local member
  for member in "$@"; do
    args+=(-require "$member")
  done
  "$archive_auditor" "${args[@]}"
}

assert_binary_metadata() {
  local executable=$1
  local label=$2
  local main_package=$3
  local expected_goos=$4
  local expected_goarch=$5
  local expected_goarm=${6:-}
  local metadata="$work_dir/${label}.buildinfo"
  local strings_file="$work_dir/${label}.strings"

  go version -m "$executable" >"$metadata" \
    || fail "$label is not a readable Go executable"
  grep -Fqx $'\tpath\t'"github.com/kunorikiku/yet-another-overlay-generator/cmd/$main_package" "$metadata" \
    || fail "$label has the wrong Go main package"
  grep -Fqx $'\tbuild\tGOOS='"$expected_goos" "$metadata" \
    || fail "$label has the wrong GOOS (expected $expected_goos)"
  grep -Fqx $'\tbuild\tGOARCH='"$expected_goarch" "$metadata" \
    || fail "$label has the wrong GOARCH (expected $expected_goarch)"
  grep -Fqx $'\tbuild\tvcs.revision='"$source_commit" "$metadata" \
    || fail "$label was not built from source revision $source_commit"
  grep -Fqx $'\tbuild\tvcs.modified=false' "$metadata" \
    || fail "$label was built from a modified VCS worktree"
  grep -Fqx $'\tbuild\tCGO_ENABLED=0' "$metadata" \
    || fail "$label was not built with CGO_ENABLED=0"
  grep -Fqx $'\tbuild\t-trimpath=true' "$metadata" \
    || fail "$label was not built with trimpath"
  if [[ -n "$expected_goarm" ]]; then
    grep -Fqx $'\tbuild\tGOARM='"$expected_goarm" "$metadata" \
      || fail "$label has the wrong GOARM (expected $expected_goarm)"
  fi

  strings -a "$executable" >"$strings_file"
  [[ $(grep -Fxc -- "$release_tag" "$strings_file" || true) -eq 1 ]] \
    || fail "$label must contain exactly one statically embedded release tag"
}

assert_exact_native_version() {
  local executable=$1
  local label=$2
  local output="$work_dir/${label}.version"
  chmod 0755 "$executable"
  "$executable" version >"$output"
  cmp -s "$output" <(printf '%s\n' "$release_tag") \
    || fail "$label does not report the exact release tag when executed"
}

wasm_reference=
wasm_exec_reference=
frontend_manifest_reference=
for target in "${targets[@]}"; do
  executable_ext=
  archive_ext=tar.gz
  if [[ "$target" == windows-* ]]; then
    executable_ext=.exe
    archive_ext=zip
  fi
  archive="$asset_dir/yaog-bundle-${target}.${archive_ext}"
  extracted="$work_dir/extracted-$target"
  member_manifest="$work_dir/$target.members"
  audit_and_extract "$archive" "$extracted" "$member_manifest" \
    "bin/yaog-server${executable_ext}" \
    "bin/yaog-compiler${executable_ext}" \
    "bin/yaog-agent${executable_ext}" \
    frontend/index.html \
    frontend/yaog.wasm \
    frontend/wasm_exec.js

  awk -F '\t' 'NF != 5 { exit 1 }
    $5 != "bin" && $5 !~ /^bin\// && $5 != "frontend" && $5 !~ /^frontend\// { exit 1 }
  ' "$member_manifest" || fail "$target bundle has an unexpected archive root"
  awk -F '\t' '
    $5 == "frontend" && $1 == "d" { next }
    $5 == "frontend/assets" && $1 == "d" { next }
    $5 ~ /^frontend\/assets\/[^/]+$/ && $1 == "f" { next }
    $5 == "frontend/index.html" && $1 == "f" { next }
    $5 == "frontend/yaog.wasm" && $1 == "f" { next }
    $5 == "frontend/wasm_exec.js" && $1 == "f" { next }
    $5 == "frontend/favicon.svg" && $1 == "f" { next }
    $5 == "frontend/icons.svg" && $1 == "f" { next }
    $5 == "bin" || $5 ~ /^bin\// { next }
    { exit 1 }
  ' "$member_manifest" || fail "$target controller frontend tree contains an unexpected member"
  grep -Eq $'^f\t[0-7]{4}\t[1-9][0-9]*\t[0-9a-f]{64}\tfrontend/assets/' "$member_manifest" \
    || fail "$target controller frontend has no regular built asset"
  awk -F '\t' '$1 == "f" && $5 ~ /^bin\// { print $5 }' "$member_manifest" | sort \
    >"$work_dir/$target.bin-files"
  printf '%s\n' \
    "bin/yaog-agent${executable_ext}" \
    "bin/yaog-compiler${executable_ext}" \
    "bin/yaog-server${executable_ext}" | sort >"$work_dir/$target.expected-bin-files"
  cmp "$work_dir/$target.expected-bin-files" "$work_dir/$target.bin-files" \
    || fail "$target bundle must contain exactly the three expected bin files"
  awk -F '\t' '$5 == "bin" { next } $5 ~ /^bin\// && $1 != "f" { exit 1 }' "$member_manifest" \
    || fail "$target bundle contains an unexpected bin directory or non-file member"
  while IFS=$'\t' read -r _kind mode _size _digest name; do
    [[ "$name" == bin/* ]] || continue
    mode_value=$((8#$mode))
    (( (mode_value & 0500) == 0500 )) \
      || fail "$target executable $name must be owner-readable and owner-executable (mode $mode)"
  done <"$member_manifest"

  awk -F '\t' '$5 == "frontend" || $5 ~ /^frontend\// { print $1 "\t" $3 "\t" $4 "\t" $5 }' \
    "$member_manifest" >"$work_dir/$target.frontend-members"
  if [[ -z "$frontend_manifest_reference" ]]; then
    frontend_manifest_reference="$work_dir/$target.frontend-members"
  else
    cmp "$frontend_manifest_reference" "$work_dir/$target.frontend-members" \
      || fail "complete controller frontend tree or bytes differ in $target bundle"
  fi

  goos=${target_goos[$target]}
  goarch=${target_goarch[$target]}
  goarm=${target_goarm[$target]:-}
  assert_binary_metadata "$extracted/bin/yaog-server${executable_ext}" \
    "$target-bundle-server" server "$goos" "$goarch" "$goarm"
  assert_binary_metadata "$extracted/bin/yaog-compiler${executable_ext}" \
    "$target-bundle-compiler" compiler "$goos" "$goarch" "$goarm"
  assert_binary_metadata "$extracted/bin/yaog-agent${executable_ext}" \
    "$target-bundle-agent" agent "$goos" "$goarch" "$goarm"

  standalone="$asset_dir/yaog-agent-${target}${executable_ext}"
  assert_binary_metadata "$standalone" "$target-standalone-agent" agent "$goos" "$goarch" "$goarm"
  cmp "$extracted/bin/yaog-agent${executable_ext}" "$standalone" \
    || fail "$target standalone agent differs from the agent in its bundle"

  if [[ -z "$wasm_reference" ]]; then
    wasm_reference="$extracted/frontend/yaog.wasm"
    wasm_exec_reference="$extracted/frontend/wasm_exec.js"
  else
    cmp "$wasm_reference" "$extracted/frontend/yaog.wasm" \
      || fail "frontend/yaog.wasm differs between platform bundles"
    cmp "$wasm_exec_reference" "$extracted/frontend/wasm_exec.js" \
      || fail "frontend/wasm_exec.js differs between platform bundles"
  fi

  if [[ "$target" == linux-amd64 ]]; then
    assert_exact_native_version "$extracted/bin/yaog-server" linux-amd64-bundle-server
    assert_exact_native_version "$extracted/bin/yaog-compiler" linux-amd64-bundle-compiler
    assert_exact_native_version "$extracted/bin/yaog-agent" linux-amd64-bundle-agent
    assert_exact_native_version "$standalone" linux-amd64-standalone-agent
  fi
done

local_design="$asset_dir/yaog-local-design-${release_tag}.zip"
local_extract="$work_dir/local-design"
local_manifest="$work_dir/local-design.members"
audit_and_extract "$local_design" "$local_extract" "$local_manifest" index.html yaog.wasm wasm_exec.js
awk -F '\t' '
  NF != 5 { exit 1 }
  $5 == "assets" && $1 == "d" { next }
  $5 ~ /^assets\/[^/]+$/ && $1 == "f" { next }
  $5 == "index.html" && $1 == "f" { next }
  $5 == "yaog.wasm" && $1 == "f" { next }
  $5 == "wasm_exec.js" && $1 == "f" { next }
  $5 == "favicon.svg" && $1 == "f" { next }
  $5 == "icons.svg" && $1 == "f" { next }
  { exit 1 }
' "$local_manifest" || fail "local-design archive contains an unexpected tree member"
grep -Eq $'^f\t[0-7]{4}\t[1-9][0-9]*\t[0-9a-f]{64}\tassets/' "$local_manifest" \
  || fail "local-design archive has no regular built asset"
cmp "$wasm_reference" "$local_extract/yaog.wasm" \
  || fail "local-design yaog.wasm differs from the bundled controller frontend"
cmp "$wasm_exec_reference" "$local_extract/wasm_exec.js" \
  || fail "local-design wasm_exec.js differs from the bundled controller frontend"

echo "Verified exact YAOG release asset set: 22 files for $release_tag at $source_commit"
