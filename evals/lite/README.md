# Lite evaluation line

A second, independent evaluation line alongside the Harbor/SWE benchmark. It
replaces "clone a real repository, build a container, patch a bug, run an
upstream grader" with "a handful of small files whose acceptance tests finish in
milliseconds", so a set of 100-200 tasks can be re-run for every ablation cell
without a Docker daemon, an image pull or a ten-minute timeout.

The two lines answer different questions and are not comparable to each other:

| | Harbor/SWE line (`evals/`, `.eval/ablation/`) | Lite line (`evals/lite/`, `.eval/lite/`) |
|---|---|---|
| Task | real repository at a fixed commit | one small file, or a small bug to fix |
| Environment | Docker image pinned by digest | host `python` / `go` |
| Per-task time | minutes | milliseconds to a few seconds |
| Grading | upstream tests, separate verifier container | hidden unit tests on a fresh tree |
| Purpose | "does it work on real code?" | "which topology wins, and by how much?" |
| Sample size | 10 per frozen round | 100-200, low variance |

Neither line's numbers are a leaderboard score. The SWE line proves the agent
works against real repositories; the lite line is where the A/B/C ablation gets
enough samples for its conclusion to hold.

## Layout

```
evals/lite/
  lite_lib.py          task loading, workspace setup, patch capture, grading
  calibrate.py         gold passes / empty fails gate; run before any model round
  run_lite.py          drive bin/xcode, capture the patch, grade in isolation
  policies/            per-language command allowlists (python.json, go.json)
  generate/            task family definitions + materialiser
  tasks/<task-id>/     task.json, starter/, solution/, hidden/
.eval/lite/            calibration reports and runs (never deleted)
```

## Task format

Each task is a directory. The agent sees only `starter/`.

```
tasks/<task-id>/
  task.json      id, language, kind, prompt, acceptance, verify_commands, hidden_command
  starter/       files given to the agent, including the visible tests it may run
  solution/      reference files overlaid to prove the task is solvable
  hidden/        the authoritative visible suite plus extra grading tests
```

`verify_commands` (visible) are what the agent can run and what the completion
gate checks. `hidden_command` is what decides the score. `hidden/` restates the
visible suite as well, so an agent that edits or deletes the visible tests
cannot change its grade — grading always runs on a fresh tree with the candidate
patch applied and the hidden overlay copied in afterwards.

Prompt style: state the required behaviour and keep signatures unchanged. Do not
hint at the fix (a bug-fix prompt says "run the tests to see the failure", not
where the off-by-one is).

## Workflow

```bash
# 1. Materialise task directories from the family definitions.
python evals/lite/generate/gen_tasks.py

# 2. Calibrate: every task must pass the gold overlay and fail the empty patch,
#    on BOTH the visible and hidden suites. No model is called.
python evals/lite/calibrate.py --label seed-v1

# 3. Optional plumbing check that spends no budget (demo model, all tasks fail).
python evals/lite/run_lite.py --demo --limit 3 --run-id smoke

# 4. Real run. --yes is required because it spends model budget.
python evals/lite/run_lite.py --yes --cells single,single-review,pipeline \
  --run-id ablation-lite-v1 --concurrency 2
```

`bin/xcode.exe` must be rebuilt from current source first
(`go build -o bin/xcode.exe ./cmd/xcode`), or the run records a stale binary.

Credentials come from the project `.xcode/config` slot selectors
(`developer=` / `reviewer=`), exactly as the SWE line reads them. A preflight
one-token request runs against each needed slot before any task starts; pass
`--skip-preflight` only when you know the endpoint is reachable.

## Grading rules

- A task is `passed` only when every hidden command exits zero on a fresh tree.
- An empty or unappliable patch is `failed`, not an infrastructure error.
- Under-constrained or environment failures are recorded as separate outcomes,
  never folded into a pass.
- A run root skips tasks that already have a `grade.json`. To repeat a cell, use
  a new `--run-id`; existing results are never overwritten or deleted.
- `report.json` keeps every task's exit code, wall time, patch path and grade
  reason, so a failure can be attributed later without rerunning anything.

## Scaling to 100-200 tasks

Tasks come from parameterised families in `generate/families_python.py` and
`generate/families_go.py` rather than hand-copied directories. To add a variant,
add one entry to the family's `tasks()` list; to systematically expand, give a
family a parameter sweep (different constraints, sizes or edge cases) that
yields many concrete tasks from one template. Because grading is a fresh hidden
suite rather than a fixed upstream test file, a generated variant is only usable
once `calibrate.py` reports it as calibrated.

Public problems (LeetCode and similar) are deliberately avoided: a model may
have memorised them, which inflates the pass rate and contaminates the ablation
comparison. Every task here is original or generated.
