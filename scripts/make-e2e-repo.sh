#!/usr/bin/env bash
# Materialise the disposable end-to-end fixture repository.
#
# The fixture is intentionally NOT a real project and never touches a remote:
# it exists so the acceptance test can prove "one task in, one verified patch
# out" without any risk to an important repository.
#
# It writes a git repository to testdata/e2e-repo-work/ and prints its path and
# the baseline revision, which the demo script passes to `factory run --rev`.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC="${ROOT}/testdata/e2e-repo"
WORK="${ROOT}/testdata/e2e-repo-work"

if [ ! -d "${SRC}" ]; then
  echo "fixture source not found: ${SRC}" >&2
  exit 1
fi

rm -rf "${WORK}"
mkdir -p "${WORK}"

# Copy the fixture, excluding anything git-related.
(cd "${SRC}" && tar --exclude='.git' -cf - .) | (cd "${WORK}" && tar -xf -)

cd "${WORK}"
git init -q
git config user.email "factory@localhost"
git config user.name "Factory Fixture"
git add -A
git commit -qm "fixture baseline: greeting returns hello"

SHA="$(git rev-parse HEAD)"

echo "fixture_path=${WORK}"
echo "fixture_sha=${SHA}"
