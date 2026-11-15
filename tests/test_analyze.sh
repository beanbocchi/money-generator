#!/usr/bin/env bash
# lab/analyze.py measures both the live database and backtest exports. Its own
# arithmetic is checked here, because a bug in it would corrupt every conclusion drawn
# from the rig.
set -uo pipefail
cd "$(dirname "$0")/.."
python3 lab/analyze.py --self-test
