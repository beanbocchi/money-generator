#!/usr/bin/env bash
# Bootstrap this project on a machine. Idempotent: safe to re-run.
set -euo pipefail
cd "$(dirname "$0")/.."

NFI_REPO="${NFI_REPO:-https://github.com/iterativv/NostalgiaForInfinity}"
GIT="${GIT:-git}"
COMPOSE="${COMPOSE:-docker compose}"

if [ ! -d NostalgiaForInfinity/.git ]; then
  echo "==> cloning NostalgiaForInfinity"
  # Partial clone: upstream's .git is ~295MB because its history holds thousands of
  # revisions of multi-megabyte generated .py files, against a ~17MB working tree.
  # blob:none fetches only the blobs the checkout needs and keeps the full commit
  # graph, so the updater's `git diff OLD NEW` still works. Shallow --depth is NOT
  # usable: it breaks exactly that diff between successive fetches.
  $GIT clone --filter=blob:none --single-branch -b main "$NFI_REPO" NostalgiaForInfinity
else
  echo "==> NostalgiaForInfinity already present, skipping clone"
fi

# This mkdir is load-bearing, not cosmetic. docker compose creates a missing
# bind-mount source as root the first time it starts a service that mounts it. If
# services/updater/state/ does not exist before that first `docker compose up`, you get a
# root-owned previous_sha (and directory) that you cannot delete from the host
# without sudo. This happened for real during Task 4's rehearsal. Creating the
# directory here, as the invoking user, before compose ever runs, avoids it.
mkdir -p user_data/logs user_data/data services/updater/state

if [ ! -f .env ]; then
  cp .env.example .env

  # .env.example ships this machine's HOST_UID/HOST_GID/DOCKER_GID (1000/1000/968).
  # Those are wrong on any other machine, so overwrite them here with values
  # computed from the actual host the moment .env is created.
  REAL_UID=$(id -u)
  REAL_GID=$(id -g)
  REAL_DOCKER_GID=$(getent group docker | cut -d: -f3 || true)

  sed -i "s/^HOST_UID=.*/HOST_UID=${REAL_UID}/" .env
  sed -i "s/^HOST_GID=.*/HOST_GID=${REAL_GID}/" .env
  if [ -n "$REAL_DOCKER_GID" ]; then
    sed -i "s/^DOCKER_GID=.*/DOCKER_GID=${REAL_DOCKER_GID}/" .env
    echo "==> set HOST_UID=${REAL_UID} HOST_GID=${REAL_GID} DOCKER_GID=${REAL_DOCKER_GID} in .env from this host"
  else
    echo "==> WARNING: 'getent group docker' found no docker group on this host."
    echo "    Leaving DOCKER_GID unset in .env is NOT safe to ignore: an empty"
    echo "    DOCKER_GID makes the updater unable to reach the docker socket."
    echo "    Add the docker group first, or set DOCKER_GID in .env by hand."
  fi

  echo
  echo "==> created .env from .env.example. Fill these in, then re-run bin/setup.sh:"
  grep -n '=$' .env || true
  echo
  echo "    FREQTRADE__DRY_RUN is true. Set it to false only when you mean real money."
  echo "    To move a live bot here, also copy its *.sqlite* and nfi*-profit_max-*.json"
  echo "    into user_data/ before starting (see README.md)."
  exit 1
fi

echo "==> starting stack"
$COMPOSE up -d --build
$COMPOSE ps
