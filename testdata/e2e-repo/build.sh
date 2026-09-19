#!/bin/sh
# Deterministic "build" gate: the module must import cleanly.
set -eu
python3 -c "import greeting; print('build ok')"
