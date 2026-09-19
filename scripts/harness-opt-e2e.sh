#!/usr/bin/env bash
# End-to-end factory test against a REAL remote repository.
#
#   repository : https://github.com/mitkox/harness-opt
#   revision   : an exact commit SHA (pinned, never "whatever HEAD is")
#   agent      : an operator-registered harness name
#   gate       : the repository's own test suite, run inside the Cube microVM
#
# Nothing is pushed. The factory clones, changes, verifies and reports; the
# remote repository is untouched.
#
# Usage: scripts/harness-opt-e2e.sh <agent-harness> [revision]
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

AGENT="${1:-harness-opt-fix}"
REVISION="${2:-4326ec81aaf5a06d6f180714db79cc17a0ef21f5}"
REPO="https://github.com/mitkox/harness-opt"
PROFILE="harness-opt"
WORKER_LOG="${ROOT}/.factory/worker-harness-opt.log"

TASK='Two tests read committed M1 evidence from runs/, but runs/ is listed in .gitignore, so a fresh clone has no such directory and both tests fail for a reason unrelated to the code under test:
  tests/test_m2_cli.py::test_migration_keeps_m1_runs_readable
  tests/test_rename_compat.py::test_historical_m1_runs_readable
Make scripts/migrate_m1_to_m2.py treat an absent runs directory as expected (report an empty set and exit zero) and make each affected test skip when the evidence is unavailable. Do not weaken any other assertion.'

mkdir -p "${ROOT}/.factory"
make --no-print-directory build >/dev/null

echo "== starting factory worker =="
./bin/factory worker >"${WORKER_LOG}" 2>&1 &
WORKER_PID=$!
trap 'kill "${WORKER_PID}" 2>/dev/null || true' EXIT

for _ in $(seq 1 60); do
  grep -q "factory worker starting" "${WORKER_LOG}" 2>/dev/null && break
  kill -0 "${WORKER_PID}" 2>/dev/null || { echo "worker died:"; tail -30 "${WORKER_LOG}"; exit 1; }
  sleep 1
done
echo "   ready"
echo

echo "== repository =="
echo "   ${REPO} @ ${REVISION}"
echo "   agent: ${AGENT}   profile: ${PROFILE}"
echo

set +e
./bin/factory run \
  --repo "${REPO}" \
  --rev "${REVISION}" \
  --task "${TASK}" \
  --agent "${AGENT}" \
  --verification "${PROFILE}" \
  --wait
RUN_EXIT=$?
set -e

echo
echo "== leakage check =="
set +e
./bin/factory sandboxes
SANDBOX_EXIT=$?
set -e

echo
echo "run_exit=${RUN_EXIT} sandbox_exit=${SANDBOX_EXIT}"
if [ "${RUN_EXIT}" -eq 0 ] && [ "${SANDBOX_EXIT}" -eq 0 ]; then
  echo "HARNESS-OPT E2E (${AGENT}): SUCCEEDED"
  exit 0
fi
echo "HARNESS-OPT E2E (${AGENT}): FAILED"
exit 1
