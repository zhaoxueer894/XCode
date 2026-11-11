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
CONFIG=/mnt/d/Projects/XCode/.xcode/config
CREDS=$CONFIG
GLM_KEY=$(sed -n 's/^glm-api-key=//p' "$CONFIG" | head -1)

step() { echo; echo "===== [$(date -u +%Y-%m-%dT%H:%M:%SZ)] $1 ====="; }

step "preflight: credentials and binary"
test -s "$CREDS" || { echo "FATAL: $CONFIG missing"; exit 1; }
if [ -z "$GLM_KEY" ]; then echo "FATAL: glm-api-key not set in .xcode/config (cells B/C need the cross-model reviewer)"; exit 1; fi
export GLM_API_KEY="$GLM_KEY"
# Preflight: one minimal chat call; abort on insufficient balance so cells
# B/C never run fully degraded (that would invalidate the ablation).
PREFLIGHT=$(curl -s --max-time 30 -x "http://$(ip route show default | awk '{print $3}'):7897" \
  "https://open.bigmodel.cn/api/paas/v4/chat/completions" \
  -H "Authorization: Bearer $GLM_KEY" -H "Content-Type: application/json" \
  -d '{"model":"glm-4.6","messages":[{"role":"user","content":"ok"}],"max_tokens":4}')
echo "glm preflight: $PREFLIGHT"
case "$PREFLIGHT" in
  *1113*|*error*) echo "FATAL: GLM balance insufficient or error — top up before the ablation"; exit 1;;
esac

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
if [ -d .eval/ablation ]; then
  # Resume mode: cells must all have been prepared together before any run.
  for CELL in a-single b-single-review c-pipeline; do
    test -d ".eval/ablation/$CELL/tasks" || { echo "FATAL: partial prepare state (.eval/ablation exists but $CELL/tasks missing)"; exit 1; }
  done
  echo "existing cells found — resume mode"
else
  $PY evals/prepare_ablation.py
fi

GIT_REV=$(git rev-parse HEAD)
BIN_SHA=$(sha256sum bin/xcode-linux-amd64 | awk '{print $1}')

# count_trials CELL -> number of completed trials across all xcode-v* jobs.
# Layout: harbor/<job>/<task>__<suffix>/result.json -> depth 3 from harbor/.
count_trials() {
  find ".eval/ablation/$1/harbor" -mindepth 3 -maxdepth 3 -name result.json 2>/dev/null | wc -l
}

# next_job_dir CELL -> the harbor output dir to use for a fresh run: the
# first xcode-vN that does not exist yet.
next_job_dir() {
  N=1
  while [ -d ".eval/ablation/$1/harbor/xcode-v$N" ]; do N=$((N+1)); done
  echo ".eval/ablation/$1/harbor/xcode-v$N"
}

for CELL in a-single b-single-review c-pipeline; do
  step "cell $CELL"
  RUN=.eval/ablation/$CELL
  TOTAL=$(count_trials "$CELL")
  if [ "$TOTAL" -ge 10 ]; then
    echo "cell $CELL already complete ($TOTAL trials) — skipping"
    continue
  fi
  # Snapshot the exact binary that ran this cell.
  mkdir -p "$RUN/build"
  cp bin/xcode-linux-amd64 "$RUN/build/xcode-linux-amd64"
  printf '%s\n%s\n' "$GIT_REV" "$BIN_SHA" > "$RUN/meta.txt"
  # Read orchestrate/reviewer from the cell manifest; args mirror the
  # recorded manifest exactly.
  ORCH=$($PY - "$RUN/manifest.json" <<'PYEOF'
import json, sys
agent = json.load(open(sys.argv[1]))["agent"]
print(agent.get("orchestrate", ""))
PYEOF
)
  RP=$($PY - "$RUN/manifest.json" <<'PYEOF'
import json, sys
print(json.load(open(sys.argv[1]))["agent"].get("reviewer_provider", ""))
PYEOF
)
  RM=$($PY - "$RUN/manifest.json" <<'PYEOF'
import json, sys
print(json.load(open(sys.argv[1]))["agent"].get("reviewer_model", ""))
PYEOF
)
  JOB=$(next_job_dir "$CELL")
  TASKS="$RUN/tasks"
  if [ "$TOTAL" -gt 0 ]; then
    # Resume: build a dir with only the tasks that have no trial yet.
    TASKS="$RUN/tasks-resume-$(basename "$JOB")"
    rm -rf "$TASKS"; mkdir -p "$TASKS"
    # Trial directory names land in done.tmp (task__suffix), one per line.
    find ".eval/ablation/$CELL/harbor" -mindepth 3 -maxdepth 3 -name result.json 2>/dev/null | awk -F/ '{print $(NF-1)}' > "$TASKS.done.tmp"
    for TASKDIR in "$RUN"/tasks/*/; do
      ID=$(basename "$TASKDIR")
      if ! grep -q "^${ID}__" "$TASKS.done.tmp"; then
        cp -r "$TASKDIR" "$TASKS/"
      fi
    done
    rm -f "$TASKS.done.tmp"
    REMAIN=$(find "$TASKS" -mindepth 1 -maxdepth 1 -type d | wc -l)
    echo "resume: $TOTAL done, running the remaining $REMAIN task(s) under $(basename "$JOB")"
    if [ "$REMAIN" -eq 0 ]; then echo "cell $CELL: nothing left to run"; continue; fi
  fi
  echo "cell=$CELL orchestrate=$ORCH reviewer=$RP/$RM binary=$BIN_SHA job=$(basename "$JOB")"
  set +e
  $HARBOR run -p "$TASKS" -a evals.harbor_agent:XCodeAgent -n 2 --max-retries 0 \
    -o "$RUN/harbor" --job-name "$(basename "$JOB")" \
    --ak binary_path="$RUN/build/xcode-linux-amd64" \
    --ak provider=deepseek --ak credentials_file="$CREDS" \
    --ak workspace=auto --ak conda_env=auto --ak no_delegate=false \
    --ak context_strategy=bounded --ak context_budget=6000 \
    --ak max_model_calls=100 --ak max_tokens=200000 --ak max_steps=100 --ak timeout_sec=1200 \
    --ak orchestrate="$ORCH" --ak reviewer_provider="$RP" --ak reviewer_model="$RM" \
    >> "$RUN/console.log" 2>&1
  RC=$?
  set -e
  echo "cell $CELL finished with exit=$RC (log: $RUN/console.log)"
done

step "ablation runs complete — analysis next (no reruns, no deletions)"
