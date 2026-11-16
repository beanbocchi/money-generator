#!/usr/bin/env bash
# newsgate's offline self-test inside its own image: fake feeds, fake model, no network.
set -uo pipefail
cd "$(dirname "$0")/.."
docker build -q -t money-generator-newsgate services/newsgate >/dev/null || exit 1
docker run --rm --network none money-generator-newsgate --self-test
