#!/bin/sh
# A/B/C ablation driver. Runs AFTER user approval only.
# Every cell records: manifest, console log, git rev, binary sha, full harbor
# trials (events, results, patches, grades). Nothing is deleted; completed
# trials are never rerun. Failures stay on disk and are classified later.
#
# RESUME: safe to relaunch after any interruption (agent died, WSL restart,
# machine reboot). Completed cells are skipped; a partially-run cell continues
# its REMAINING tasks under a fresh job name (xcode-v2, ...) — the additive
# fixup pattern used throughout earlier rounds. Analysis must merge trials
# from all xcode-v* jobs per cell.
set -eu
cd /mnt/d/Projects/XCode
export PYTHONPATH="$PWD" DOCKER_CONFIG="$PWD/.cache/docker-config" PYTHONUNBUFFERED=1
PY=.cache/harbor-linux/bin/python
HARBOR=.cache/harbor-linux/bin/harbor
# Single source of credentials: the project .xcode/config (gitignored).
CONFIG="$PWD/.xcode/config"
RUN_ROOT=.eval/ablation
CELLS="a-single b-single-review c-pipeline"
PREPARE_MODE=
PRESSURE_TASK=
PRESSURE_RUN=1
CONCURRENCY=2
if [ "${1:-}" = "--staged" ]; then
  PREPARE_MODE=--staged
  RUN_ROOT=.eval/ablation/staged
  shift
  if [ "${1:-}" = "--pressure-task" ] && [ "$#" -ge 2 ] && [ -n "$2" ]; then
    PRESSURE_TASK=$2
    RUN_ROOT=".eval/ablation/staged-pressure/$PRESSURE_TASK"
    CELLS="c-pipeline"
    CONCURRENCY=1
    shift 2
    if [ "${1:-}" = "--pressure-run" ] && [ "$#" -eq 2 ]; then
      case "$2" in
        ''|0*|*[!0-9]*) echo "invalid pressure run number: $2" >&2; exit 2 ;;
      esac
      PRESSURE_RUN=$2
      if [ "$PRESSURE_RUN" -ne 1 ]; then RUN_ROOT="$RUN_ROOT/run-$PRESSURE_RUN"; fi
      shift 2
    fi
  fi
elif [ "${1:-}" = "--strict-bc" ]; then
  RUN_ROOT=.eval/ablation/strict-bc
  CELLS="b-single-review c-pipeline"
  PREPARE_MODE=--strict-bc
  shift
fi
if [ "$#" -ne 0 ]; then
  echo "usage: $0 [--strict-bc | --staged [--pressure-task TASK_ID [--pressure-run NUMBER]]]" >&2
  exit 2
fi
exec 9>"$PWD/.eval/ablation-driver.lock"
flock -n 9 || { echo "FATAL: an ablation driver is already running"; exit 1; }

step() { echo; echo "===== [$(date -u +%Y-%m-%dT%H:%M:%SZ)] $1 ====="; }

step "preflight: credentials and binary"
test -s "$CONFIG" || { echo "FATAL: $CONFIG missing"; exit 1; }
# Preflight stdout contains only the validated egress hostnames. Apply the
# same agent-only allowlist in every cell without editing the frozen tasks.
HOSTS=$("$PY" -m evals.prepare_ablation --preflight)
set --
for HOST in $HOSTS; do
  set -- "$@" --allow-agent-host "$HOST"
done

step "binary freshness check"
# WSL has no Go toolchain: the binary MUST be rebuilt on the Windows side
# (Git Bash) before this driver starts. Refuse to run a stale binary —
# older builds predate the fingerprint fix and deadlock gates in B/C.
BIN_TIME=$(stat -c %Y bin/xcode-linux-amd64 2>/dev/null || echo 0)
HEAD_TIME=$(git log -1 --format=%ct)
if [ "$BIN_TIME" -lt "$HEAD_TIME" ]; then
  echo "FATAL: bin/xcode-linux-amd64 is older than HEAD. Rebuild on the Windows side first:"
  echo "  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/xcode-linux-amd64 ./cmd/xcode"
  exit 1
fi

step "prepare cells"
if [ -n "$PRESSURE_TASK" ]; then
  "$PY" -m evals.prepare_ablation --staged --pressure-task "$PRESSURE_TASK" --pressure-run "$PRESSURE_RUN"
else
  "$PY" -m evals.prepare_ablation ${PREPARE_MODE:+"$PREPARE_MODE"}
fi
EXPECTED_TRIALS=10
if [ -n "$PRESSURE_TASK" ]; then EXPECTED_TRIALS=1; fi

GIT_REV=$(git rev-parse HEAD)
BIN_SHA=$(sha256sum bin/xcode-linux-amd64 | awk '{print $1}')

# count_trials CELL -> number of completed trials across all xcode-v* jobs.
# Layout: harbor/<job>/<task>__<suffix>/result.json -> depth 3 from harbor/.
count_trials() {
  if [ -d "$RUN_ROOT/$1/harbor" ]; then
    find "$RUN_ROOT/$1/harbor" -mindepth 3 -maxdepth 3 -name result.json -type f | wc -l
  else
    echo 0
  fi
}

# next_job_dir CELL -> the harbor output dir to use for a fresh run: the
# first xcode-vN that does not exist yet.
next_job_dir() {
  N=1
  while [ -e "$RUN_ROOT/$1/harbor/xcode-v$N" ] || [ -e "$RUN_ROOT/$1/tasks-resume-xcode-v$N" ]; do
    N=$((N+1))
  done
  echo "$RUN_ROOT/$1/harbor/xcode-v$N"
}

for CELL in $CELLS; do
  step "cell $CELL"
  RUN="$RUN_ROOT/$CELL"
  TOTAL=$(count_trials "$CELL")
  if [ "$TOTAL" -gt "$EXPECTED_TRIALS" ]; then
    echo "FATAL: $CELL has more than $EXPECTED_TRIALS trial results"
    exit 1
  fi
  SNAPSHOT="$RUN/build/xcode-linux-amd64"
  if [ -e "$RUN/meta.txt" ] || [ -e "$SNAPSHOT" ] || [ "$TOTAL" -gt 0 ]; then
    EXPECTED_META=$(printf '%s\n%s\n' "$GIT_REV" "$BIN_SHA")
    if [ ! -f "$RUN/meta.txt" ] || [ "$(cat "$RUN/meta.txt")" != "$EXPECTED_META" ] ||
       [ ! -f "$SNAPSHOT" ] || [ "$(sha256sum "$SNAPSHOT" | awk '{print $1}')" != "$BIN_SHA" ]; then
      echo "FATAL: $CELL binary snapshot or metadata does not match this build"
      exit 1
    fi
  fi
  if [ "$TOTAL" -eq "$EXPECTED_TRIALS" ]; then
    echo "cell $CELL already complete ($TOTAL trials) — skipping"
    continue
  fi
  if [ ! -e "$SNAPSHOT" ]; then
    mkdir -p "$RUN/build"
    cp bin/xcode-linux-amd64 "$SNAPSHOT"
    printf '%s\n%s\n' "$GIT_REV" "$BIN_SHA" > "$RUN/meta.txt"
  fi
  # Read orchestrate/reviewer from the cell manifest; args mirror the
  # recorded manifest exactly.
  MODEL_ARGS=$("$PY" - "$RUN/manifest.json" <<'PYEOF'
import json, sys
agent = json.load(open(sys.argv[1]))["agent"]
for key in ("orchestrate", "provider", "model", "reviewer_provider", "reviewer_model", "strict_reviewer_max_steps", "reviewer_max_output_tokens"):
    print(agent.get(key, 0))
print(str(agent.get("allocate_stage_budget", False)).lower())
print(str(agent.get("functional_repair_once", False)).lower())
for key in ("max_model_calls", "max_tokens", "max_steps", "timeout_sec"):
    print(agent[key])
PYEOF
)
  ORCH=$(printf '%s\n' "$MODEL_ARGS" | sed -n '1p')
  PROVIDER=$(printf '%s\n' "$MODEL_ARGS" | sed -n '2p')
  MODEL=$(printf '%s\n' "$MODEL_ARGS" | sed -n '3p')
  RP=$(printf '%s\n' "$MODEL_ARGS" | sed -n '4p')
  RM=$(printf '%s\n' "$MODEL_ARGS" | sed -n '5p')
  STRICT_REVIEWER_MAX_STEPS=$(printf '%s\n' "$MODEL_ARGS" | sed -n '6p')
  REVIEWER_MAX_OUTPUT_TOKENS=$(printf '%s\n' "$MODEL_ARGS" | sed -n '7p')
  ALLOCATE_STAGE_BUDGET=$(printf '%s\n' "$MODEL_ARGS" | sed -n '8p')
  FUNCTIONAL_REPAIR_ONCE=$(printf '%s\n' "$MODEL_ARGS" | sed -n '9p')
  MAX_MODEL_CALLS=$(printf '%s\n' "$MODEL_ARGS" | sed -n '10p')
  MAX_TOKENS=$(printf '%s\n' "$MODEL_ARGS" | sed -n '11p')
  MAX_STEPS=$(printf '%s\n' "$MODEL_ARGS" | sed -n '12p')
  TIMEOUT_SEC=$(printf '%s\n' "$MODEL_ARGS" | sed -n '13p')
  TASK_RUNTIME_MANIFEST=
  if [ "$PREPARE_MODE" = "--staged" ]; then TASK_RUNTIME_MANIFEST="$RUN/manifest.json"; fi
  JOB=$(next_job_dir "$CELL")
  TASKS="$RUN/tasks"
  if [ "$TOTAL" -gt 0 ]; then
    # Resume: build a dir with only the tasks that have no trial yet.
    TASKS="$RUN/tasks-resume-$(basename "$JOB")"
    mkdir "$TASKS"
    DONE_TRIALS=$(find "$RUN/harbor" -mindepth 3 -maxdepth 3 -name result.json -type f | awk -F/ '{print $(NF-1)}')
    for TASKDIR in "$RUN"/tasks/*/; do
      ID=$(basename "$TASKDIR")
      if ! printf '%s\n' "$DONE_TRIALS" | grep -q "^${ID}__"; then
        cp -r "$TASKDIR" "$TASKS/"
      fi
    done
    REMAIN=$(find "$TASKS" -mindepth 1 -maxdepth 1 -type d | wc -l)
    echo "resume: $TOTAL done, running the remaining $REMAIN task(s) under $(basename "$JOB")"
    if [ "$((TOTAL+REMAIN))" -ne "$EXPECTED_TRIALS" ]; then
      echo "FATAL: $CELL completed and remaining trial counts do not match the frozen set"
      exit 1
    fi
  fi
  echo "cell=$CELL orchestrate=$ORCH reviewer=$RP/$RM binary=$BIN_SHA job=$(basename "$JOB")"
  set +e
  $HARBOR run -p "$TASKS" -a evals.harbor_agent:XCodeAgent -n "$CONCURRENCY" --max-retries 0 \
    -o "$RUN/harbor" --job-name "$(basename "$JOB")" \
    --ak binary_path="$SNAPSHOT" \
    --model "$MODEL" --ak provider="$PROVIDER" --ak xcode_config="$CONFIG" \
    --ak workspace=auto --ak conda_env=auto --ak no_delegate=false \
    --ak context_strategy=bounded --ak context_budget=6000 \
    --ak max_model_calls="$MAX_MODEL_CALLS" --ak max_tokens="$MAX_TOKENS" \
    --ak max_steps="$MAX_STEPS" --ak timeout_sec="$TIMEOUT_SEC" \
    --ak orchestrate="$ORCH" --ak reviewer_provider="$RP" --ak reviewer_model="$RM" \
    --ak strict_reviewer_max_steps="$STRICT_REVIEWER_MAX_STEPS" \
    --ak reviewer_max_output_tokens="$REVIEWER_MAX_OUTPUT_TOKENS" \
    --ak allocate_stage_budget="$ALLOCATE_STAGE_BUDGET" \
    --ak functional_repair_once="$FUNCTIONAL_REPAIR_ONCE" \
    --ak task_runtime_manifest="$TASK_RUNTIME_MANIFEST" "$@" \
    >> "$RUN/console.log" 2>&1
  RC=$?
  set -e
  echo "cell $CELL finished with exit=$RC (log: $RUN/console.log)" | tee -a "$RUN/console.log"
  TOTAL=$(count_trials "$CELL")
  if [ "$RC" -ne 0 ] || [ "$TOTAL" -ne "$EXPECTED_TRIALS" ]; then
    echo "FATAL: $CELL stopped with $TOTAL/$EXPECTED_TRIALS trial results; inspect $RUN/console.log before resuming"
    exit 1
  fi
done

step "ablation runs complete — analysis next (no reruns, no deletions)"
