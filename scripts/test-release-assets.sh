#!/usr/bin/env bash

# End-to-end synthetic proof for scripts/verify-release-assets.sh. It builds a
# clean seven-target fixture, proves the positive path, then proves missing/extra,
# copied-wrong-arch, wrong-version, and dev-version negatives. The Go unit tests
# invoked first cover ZIP/TAR links/special types, traversal, separator, case,
# prefix, member-count, expanded-size, and outer-size adversarial archives.

set -euo pipefail

export LC_ALL=C

repo=$(git rev-parse --show-toplevel)
source_commit=$(git rev-parse HEAD)
tag=v9.9.9-rc.7
wrong_tag=v9.9.9-rc.8
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

go test ./scripts
git clone --quiet --no-local --no-hardlinks "$repo" "$tmp/source"

assets="$tmp/assets"
mkdir -p "$assets"

targets=(
  'linux-amd64 linux amd64 '
  'linux-arm64 linux arm64 '
  'linux-386 linux 386 '
  'linux-armv7 linux arm 7'
  'windows-amd64 windows amd64 '
  'windows-arm64 windows arm64 '
  'windows-386 windows 386 '
)

for target_row in "${targets[@]}"; do
  read -r target goos goarch goarm <<<"$target_row"
  executable_ext=
  archive_ext=tar.gz
  [[ "$goos" == windows ]] && executable_ext=.exe archive_ext=zip
  build="$tmp/build-$target"
  bundle="$tmp/bundle-$target"
  mkdir -p "$build" "$bundle/bin" "$bundle/frontend"
  for command in server compiler agent; do
    (
      cd "$tmp/source"
      CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch GOARM=$goarm \
        go build -trimpath -ldflags "-s -w -X main.BuildVersion=$tag" \
        -o "$build/yaog-$command$executable_ext" "./cmd/$command"
    )
    cp "$build/yaog-$command$executable_ext" "$bundle/bin/"
  done
  cp -R "$repo/frontend/dist/." "$bundle/frontend/"
  if [[ "$archive_ext" == tar.gz ]]; then
    tar -czf "$assets/yaog-bundle-$target.tar.gz" -C "$bundle" bin frontend
  else
    (cd "$bundle" && zip -qr "$assets/yaog-bundle-$target.zip" bin frontend)
  fi
  cp "$build/yaog-agent$executable_ext" "$assets/yaog-agent-$target$executable_ext"
  sha256sum "$assets/yaog-agent-$target$executable_ext" | awk '{print $1}' \
    >"$assets/yaog-agent-$target$executable_ext.sha256"
done
(cd "$repo/frontend/dist" && zip -qr "$assets/yaog-local-design-$tag.zip" ./*)

bash scripts/verify-release-assets.sh "$assets" "$tag" "$source_commit"

expect_failure() {
  local name=$1
  local fixture=$2
  if bash scripts/verify-release-assets.sh "$fixture" "$tag" "$source_commit" \
    >"$tmp/$name.stdout" 2>"$tmp/$name.stderr"; then
    echo "negative release-verifier case unexpectedly passed: $name" >&2
    exit 1
  fi
}

cp -al "$assets" "$tmp/missing"
rm "$tmp/missing/yaog-agent-linux-386.sha256"
expect_failure missing-asset "$tmp/missing"

cp -al "$assets" "$tmp/extra"
printf 'unexpected\n' >"$tmp/extra/unexpected.txt"
expect_failure extra-asset "$tmp/extra"

cp -al "$assets" "$tmp/unexpected-root"
printf 'rogue\n' >"$tmp/bundle-linux-amd64/rogue.txt"
rm "$tmp/unexpected-root/yaog-bundle-linux-amd64.tar.gz"
tar -czf "$tmp/unexpected-root/yaog-bundle-linux-amd64.tar.gz" \
  -C "$tmp/bundle-linux-amd64" bin frontend rogue.txt
expect_failure unexpected-archive-root "$tmp/unexpected-root"
rm "$tmp/bundle-linux-amd64/rogue.txt"

cp -al "$assets" "$tmp/non-executable"
chmod 0644 "$tmp/bundle-linux-amd64/bin/yaog-agent"
rm "$tmp/non-executable/yaog-bundle-linux-amd64.tar.gz"
tar -czf "$tmp/non-executable/yaog-bundle-linux-amd64.tar.gz" \
  -C "$tmp/bundle-linux-amd64" bin frontend
expect_failure non-executable-archive-mode "$tmp/non-executable"
chmod 0755 "$tmp/bundle-linux-amd64/bin/yaog-agent"

# Any execute bit is not sufficient: Unix applies the owner's class to an
# owner-extracted file, so mode 0401 still cannot be executed by that owner.
cp -al "$assets" "$tmp/non-owner-executable"
chmod 0401 "$tmp/bundle-linux-amd64/bin/yaog-agent"
rm "$tmp/non-owner-executable/yaog-bundle-linux-amd64.tar.gz"
tar -czf "$tmp/non-owner-executable/yaog-bundle-linux-amd64.tar.gz" \
  -C "$tmp/bundle-linux-amd64" bin frontend
expect_failure non-owner-executable-archive-mode "$tmp/non-owner-executable"
chmod 0755 "$tmp/bundle-linux-amd64/bin/yaog-agent"

cp -al "$assets" "$tmp/frontend-drift"
printf '\nsynthetic drift\n' >>"$tmp/bundle-linux-arm64/frontend/index.html"
rm "$tmp/frontend-drift/yaog-bundle-linux-arm64.tar.gz"
tar -czf "$tmp/frontend-drift/yaog-bundle-linux-arm64.tar.gz" \
  -C "$tmp/bundle-linux-arm64" bin frontend
expect_failure complete-frontend-drift "$tmp/frontend-drift"

cp -al "$assets" "$tmp/wrong-arch"
rm "$tmp/wrong-arch/yaog-agent-linux-arm64" "$tmp/wrong-arch/yaog-agent-linux-arm64.sha256"
cp "$assets/yaog-agent-linux-amd64" "$tmp/wrong-arch/yaog-agent-linux-arm64"
sha256sum "$tmp/wrong-arch/yaog-agent-linux-arm64" | awk '{print $1}' \
  >"$tmp/wrong-arch/yaog-agent-linux-arm64.sha256"
expect_failure copied-wrong-arch "$tmp/wrong-arch"

build_negative_agent() {
  local output=$1
  local version=$2
  (
    cd "$tmp/source"
    if [[ "$version" == dev ]]; then
      CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o "$output" ./cmd/agent
    else
      CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
        go build -trimpath -ldflags "-s -w -X main.BuildVersion=$version" -o "$output" ./cmd/agent
    fi
  )
}

for kind in wrong-version dev-version; do
  cp -al "$assets" "$tmp/$kind"
  rm "$tmp/$kind/yaog-agent-linux-arm64" "$tmp/$kind/yaog-agent-linux-arm64.sha256"
  version=$wrong_tag
  [[ "$kind" == dev-version ]] && version=dev
  build_negative_agent "$tmp/$kind/yaog-agent-linux-arm64" "$version"
  sha256sum "$tmp/$kind/yaog-agent-linux-arm64" | awk '{print $1}' \
    >"$tmp/$kind/yaog-agent-linux-arm64.sha256"
  expect_failure "$kind" "$tmp/$kind"
done

echo "Verified positive and adversarial synthetic release-asset fixtures"
