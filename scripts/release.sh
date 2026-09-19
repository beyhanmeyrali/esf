#!/usr/bin/env bash

set -euo pipefail

version=${1:-}
output_dir=${2:-dist}

if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]]; then
  echo "usage: $0 vMAJOR.MINOR.PATCH [output-directory]" >&2
  exit 2
fi

mkdir -p "$output_dir"
if find "$output_dir" -mindepth 1 -maxdepth 1 -print -quit | grep -q .; then
  echo "release output directory must be empty: $output_dir" >&2
  exit 1
fi

build_dir=$(mktemp -d)
trap 'rm -rf "$build_dir"' EXIT

release_name=${version#v}
commit=$(git rev-parse HEAD)
targets=(
  linux/amd64
  linux/arm64
  darwin/amd64
  darwin/arm64
)

for target in "${targets[@]}"; do
  goos=${target%/*}
  goarch=${target#*/}
  target_dir="$build_dir/${goos}_${goarch}"
  archive="$output_dir/machinist_${release_name}_${goos}_${goarch}.tar.gz"

  mkdir -p "$target_dir"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -buildvcs=false -trimpath -ldflags="-s -w -X main.version=$version" \
    -o "$target_dir/machinist" ./cmd/machinist
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -buildvcs=false -trimpath -ldflags="-s -w" \
    -o "$target_dir/factory" ./cmd/factory
  cp LICENSE README.md "$target_dir/"
  chmod 0644 "$target_dir/LICENSE" "$target_dir/README.md"
  touch -t 198001010000 "$target_dir/LICENSE" "$target_dir/README.md" "$target_dir/machinist" "$target_dir/factory"
  COPYFILE_DISABLE=1 tar -C "$target_dir" -cf - LICENSE README.md machinist factory | gzip -n > "$archive"
done

printf '{\n  "version": "%s",\n  "commit": "%s",\n  "targets": ["linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64"]\n}\n' \
  "$version" "$commit" > "$output_dir/release-manifest.json"

(
  cd "$output_dir"
  shasum -a 256 machinist_*.tar.gz release-manifest.json > checksums.txt
)
