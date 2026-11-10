#!/bin/sh
# A/B/C ablation driver. Runs AFTER user approval only.
# Every cell records: manifest, console log, git rev, binary sha, full harbor
# trials (events, results, patches, grades). Nothing is deleted or rerun
# selectively; failures stay on disk and are classified during analysis.
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

step "prepare cells"
$PY evals/prepare_ablation.py

GIT_REV=$(git rev-parse HEAD)
BIN_SHA=$(sha256sum bin/xcode-linux-amd64 | awk '{print $1}')

for CELL in a-single b-single-review c-pipeline; do
  step "cell $CELL"
  RUN=.eval/ablation/$CELL
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
  echo "cell=$CELL orchestrate=$ORCH reviewer=$RP/$RM binary=$BIN_SHA"
  set +e
  $HARBOR run -p "$RUN/tasks" -a evals.harbor_agent:XCodeAgent -n 2 --max-retries 0 \
    -o "$RUN/harbor" --job-name xcode-v1 \
    --ak binary_path="$RUN/build/xcode-linux-amd64" \
    --ak provider=deepseek --ak credentials_file="$CREDS" \
    --ak workspace=auto --ak conda_env=auto --ak no_delegate=false \
    --ak context_strategy=bounded --ak context_budget=6000 \
    --ak max_model_calls=100 --ak max_tokens=200000 --ak max_steps=100 --ak timeout_sec=1200 \
    --ak orchestrate="$ORCH" --ak reviewer_provider="$RP" --ak reviewer_model="$RM" \
    > "$RUN/console.log" 2>&1
  RC=$?
  set -e
  echo "cell $CELL finished with exit=$RC (log: $RUN/console.log)"
done

step "ablation runs complete — analysis next (no reruns, no deletions)"
