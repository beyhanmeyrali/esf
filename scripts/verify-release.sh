#!/usr/bin/env bash

set -euo pipefail

release_dir=${1:-dist}
version=${2:-}

if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]]; then
  echo "usage: $0 RELEASE_DIRECTORY vMAJOR.MINOR.PATCH" >&2
  exit 2
fi

release_name=${version#v}
commit=$(git rev-parse HEAD)
targets=(
  linux/amd64
  linux/arm64
  darwin/amd64
  darwin/arm64
)

(
  cd "$release_dir"
  shasum -a 256 -c checksums.txt
)
grep -F '"version": "'"$version"'"' "$release_dir/release-manifest.json"
grep -F '"commit": "'"$commit"'"' "$release_dir/release-manifest.json"

verify_dir=$(mktemp -d)
trap 'rm -rf "$verify_dir"' EXIT
host_target="$(go env GOOS)/$(go env GOARCH)"

for target in "${targets[@]}"; do
  goos=${target%/*}
  goarch=${target#*/}
  archive="$release_dir/machinist_${release_name}_${goos}_${goarch}.tar.gz"
  target_dir="$verify_dir/${goos}_${goarch}"

  test -f "$archive"
  mkdir -p "$target_dir"
  contents=$(tar -tzf "$archive" | LC_ALL=C sort)
  expected=$(printf '%s\n' LICENSE README.md machinist factory | LC_ALL=C sort)
  test "$contents" = "$expected"
  tar -C "$target_dir" -xzf "$archive"
  test -x "$target_dir/machinist"
  test -x "$target_dir/factory"
  go version -m "$target_dir/machinist" | grep -F "GOOS=$goos"
  go version -m "$target_dir/machinist" | grep -F "GOARCH=$goarch"

  if [[ "$target" == "$host_target" ]]; then
    test "$("$target_dir/machinist" version)" = "$version"
  fi
done
