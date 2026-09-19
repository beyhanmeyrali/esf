#!/bin/sh
# Deterministic CONFORMANCE agent for the harness-opt end-to-end test.
#
# WHY THIS EXISTS
#
# The factory's contract with a coding agent is narrow and testable: receive a
# task on stdin, modify the repository in the working directory, and exit zero on
# success. This script satisfies exactly that contract without a language model,
# so the end-to-end test can prove the FACTORY — real remote clone from GitHub,
# sandbox lifecycle, patch extraction, deterministic verification, cleanup —
# independently of model availability.
#
# It is NOT a substitute for a real coding agent. The factory treats it as an
# ordinary operator-registered harness with no special casing; the OpenCode
# harness is implemented alongside it and is selected with `--agent opencode2`.
#
# THE DEFECT IT FIXES
#
# In github.com/mitkox/harness-opt, two tests read committed M1 evidence under
# `runs/`, but `runs/` is listed in .gitignore. A fresh clone therefore has no
# such directory, so both tests fail for a reason that has nothing to do with the
# code under test:
#
#   tests/test_m2_cli.py::test_migration_keeps_m1_runs_readable
#   tests/test_rename_compat.py::test_historical_m1_runs_readable
#
# The fix is the ordinary engineering one: a missing runs directory is EXPECTED
# on a fresh clone (report an empty set, exit zero), and each test skips rather
# than fails when the gitignored evidence is unavailable.
set -eu

# The harness runs the agent with `--version` once, at provision time, to prove
# the agent is executable.
if [ "${1:-}" = "--version" ]; then
  echo "harness-opt-conformance-agent 1.0.0"
  exit 0
fi

# Read the task from stdin, exactly as the OpenCode harness delivers it.
TASK="$(cat)"
if [ -z "${TASK}" ]; then
  echo "conformance agent: no task provided on stdin" >&2
  exit 2
fi
echo "conformance agent: received task (${#TASK} bytes)"

if [ ! -f scripts/migrate_m1_to_m2.py ]; then
  echo "conformance agent: not a harness-opt checkout (cwd=$(pwd))" >&2
  exit 3
fi

python3 - <<'PY'
import pathlib
import sys

# ── 1. The migration tool must tolerate an absent runs directory ──────────────
# A fresh clone has no runs/ directory because it is gitignored. Absence is
# expected, not an error: report an empty set and succeed.
path = pathlib.Path("scripts/migrate_m1_to_m2.py")
source = path.read_text()
anchor = "    rows = []\n    for name in sorted(os.listdir(args.runs_dir)):"
guard = (
    "    rows = []\n"
    "    if not os.path.isdir(args.runs_dir):\n"
    '        # Absent on a fresh clone: runs/ is gitignored, so its absence is\n'
    "        # expected rather than an error. Report an empty set and succeed.\n"
    "        print(json.dumps(rows, indent=2))\n"
    "        return 0\n"
    "    for name in sorted(os.listdir(args.runs_dir)):"
)
if anchor in source:
    path.write_text(source.replace(anchor, guard, 1))
    print("patched scripts/migrate_m1_to_m2.py")
elif "if not os.path.isdir(args.runs_dir):" in source:
    print("scripts/migrate_m1_to_m2.py already patched")
else:
    sys.exit("migration anchor not found: refusing to make a partial change")

# ── 2. Each affected test skips when the gitignored evidence is absent ────────
skip = (
    '    if not os.path.isdir("runs"):\n'
    '        pytest.skip("legacy M1 evidence is gitignored and absent from a fresh clone")\n'
)
targets = [
    ("tests/test_m2_cli.py", "def test_migration_keeps_m1_runs_readable():\n    import subprocess\n"),
    ("tests/test_rename_compat.py", "def test_historical_m1_runs_readable():\n"),
]
for filename, anchor in targets:
    p = pathlib.Path(filename)
    s = p.read_text()
    if skip in s:
        print(f"{filename} already patched")
        continue
    if anchor not in s:
        sys.exit(f"{filename}: anchor not found: refusing to make a partial change")
    p.write_text(s.replace(anchor, anchor + skip, 1))
    print(f"patched {filename}")
PY

echo "conformance agent: done"
exit 0
