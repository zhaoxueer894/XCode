#!/bin/sh
# r3 calibration driver: select -> pull -> manifest -> fetch -> prepare -> gold -> empty.
# Stops at the first failure; every step's output is on the console log.
set -eu
cd /mnt/d/Projects/XCode
export PYTHONPATH="$PWD" DOCKER_CONFIG="$PWD/.cache/docker-config" PYTHONUNBUFFERED=1
PY=.cache/harbor-linux/bin/python
HARBOR=.cache/harbor-linux/bin/harbor
GW=$(ip route show default | awk '{print $3}')

step() { echo; echo "===== [$(date -u +%H:%M:%S)] $1 ====="; }

step "select r3 pool"
$PY evals/select_unseen.py --round r3

step "pull images"
python3 evals/pull_unseen_images.py --round r3

step "build candidates manifest"
$PY evals/build_unseen_manifest.py --round r3

step "fetch + verify pinned records"
$PY evals/fetch_pilot.py --manifest evals/unseen-r3-candidates.json --records-dir .eval/unseen-r3/records --skip-docker --proxy "http://$GW:7897"

step "prepare harbor tasks"
$PY evals/prepare_pilot.py --output .eval/unseen-r3 --manifest evals/unseen-r3-candidates.json --records-dir .eval/unseen-r3/records

step "gold calibration (n=2)"
$HARBOR run -p .eval/unseen-r3/tasks -a oracle -n 2 --max-retries 0 -o .eval/unseen-r3/harbor --job-name gold-v1

step "empty calibration (n=2)"
$PY evals/run_pilot.py empty --run-root .eval/unseen-r3 --manifest evals/unseen-r3-candidates.json --empty-job empty-v1 --concurrency 2

step "finalize frozen set"
$PY evals/finalize_unseen_manifest.py --round r3 --gold-job gold-v1 --empty-job empty-v1

step "DONE — calibration complete, ready for the multi-agent run"
