# Evaluation results

Every verdict below is the grader's own, taken from each harbor
trial's `result.json` (`verifier_result.rewards.reward`). A task counts
as resolved only when a hidden test that failed before the patch passes
after it; the agent's claim of completion carries no weight.

`no verdict` means no trial produced a graded result — the harness
raised, or the run died before any patch existed. Those tasks are
excluded from the fraction and counted separately rather than scored as
wrong answers, because a provider error says nothing about the model.

Generated 2026-11-15 by `python evals/export_results.py`.

## The controlled three-arm comparison

These three arms share one task set, one budget and one binary
(`f8033d16…`); only the collaboration topology differs. Nothing outside
this table is comparable to it — other rounds used different binaries,
different task sets, or no completion gate at all.

| Task | A — single agent, no review | B — single agent + cross-model review | C — planner / developer / reviewer pipeline |
|---|---|---|---|
| `cli__cli-405` | pass | pass | pass |
| `cli__cli-474` | pass | pass | pass |
| `cli__cli-477` | fail | fail | fail |
| `django__django-10880` | pass | pass | pass |
| `django__django-10914` | pass | pass | fail |
| `iamkun__dayjs-589` | pass | pass | pass |
| `iamkun__dayjs-591` | pass | pass | pass |
| `iamkun__dayjs-602` | fail | fail | fail |
| `sympy__sympy-13031` | fail | fail | pass |
| `sympy__sympy-13091` | fail | fail | fail |

| **Resolved** | **6/10** | **6/10** | **6/10** |
| No verdict | 0 | 0 | 0 |

The three arms land on the same total, but not on the same tasks:

- `django__django-10914` is resolved only by arm **A, B**.
- `sympy__sympy-13031` is resolved only by arm **C**.

Read that honestly: on this task set the topology made no difference
to the total. Each task is a single trial, so the per-task differences
above are not a measured effect; claiming one topology beats another
would need several trials per cell.

Within these failures, some runs never got as far as an edit:

- Arm **A** — no patch was written at all: `cli__cli-477`, `sympy__sympy-13031`, `sympy__sympy-13091`.
- Arm **B** — a patch was written and graded: `cli__cli-477`, `sympy__sympy-13091`; no patch was written at all: `iamkun__dayjs-602`, `sympy__sympy-13031`.
- Arm **C** — a patch was written and graded: `iamkun__dayjs-602`; no patch was written at all: `cli__cli-477`, `django__django-10914`, `sympy__sympy-13091`.

The verdicts stand as recorded. But a blank patch means the run
died before editing, so those tasks measure the budget ceiling or
an outage, not a wrong fix — the same tasks would need rerunning
on a larger budget before anyone attributes them to the model.

## All runs

| Run | What it is | Resolved | No verdict | Cut short | Empty patch | Trials | Binary |
|---|---|---:|---:|---:|---:|---:|---|
| `ablation/a-missing-scores-retry/a-single` | Retry for single-agent trials whose verdict was missing | 1/3 | 0 | 1 | 1 | 3 | `421828b6a1a1` |
| `ablation/a-single` | Early A - single agent (partial task set) | 3/7 | 3 | 1 | 3 | 10 | `421828b6a1a1` |
| `ablation/b-single-review` | Early B - single agent + review (partial task set) | 2/2 | 2 | 1 | 0 | 4 | `421828b6a1a1` |
| `ablation/paired-first-review/relaxed-two/review-feedback` | Paired study - review feedback (relaxed gate) | 0/1 | 0 | 0 | 0 | 1 | `f8033d163fab` |
| `ablation/paired-first-review/relaxed-two/self-check` | Paired study - unguided self-check (relaxed gate) | 0/1 | 0 | 0 | 0 | 1 | `f8033d163fab` |
| `ablation/paired-first-review/study/review-feedback` | Paired study - independent review feedback | 6/8 | 0 | 0 | 2 | 8 | `f8033d163fab` |
| `ablation/paired-first-review/study/self-check` | Paired study - unguided self-check | 6/8 | 0 | 0 | 2 | 8 | `f8033d163fab` |
| `ablation/paired-first-review/sympy-observed-pair/review-feedback` | Observed pair on sympy-13031 - review feedback | 1/1 | 0 | 0 | 0 | 1 | `f8033d163fab` |
| `ablation/paired-first-review/sympy-observed-pair/self-check` | Observed pair on sympy-13031 - unguided self-check | 0/1 | 0 | 0 | 0 | 1 | `f8033d163fab` |
| `ablation/paired-first-submission/a-single` | Paired study - plain submission (no review, no self-check) | 5/10 | 0 | 0 | 3 | 10 | `f8033d163fab` |
| `ablation/paired-review/smoke/review-feedback` | Smoke - review feedback | 1/1 | 0 | 0 | 0 | 1 | `f8033d163fab` |
| `ablation/paired-review/smoke/self-check` | Smoke - unguided self-check | 1/1 | 0 | 0 | 0 | 1 | `f8033d163fab` |
| `ablation/staged-a-aligned/a-single` | Staged ablation A - single agent | 6/10 | 0 | 3 | 3 | 10 | `f8033d163fab` |
| `ablation/staged-glm429-retry/c-pipeline` | Pipeline retry after the reviewer quota reset | 2/4 | 0 | 1 | 1 | 4 | `f8033d163fab` |
| `ablation/staged-pressure/sympy__sympy-13091/c-pipeline` | ablation/staged-pressure/sympy__sympy-13091/c-pipeline | 0/1 | 0 | 1 | 1 | 1 | `db9f74e3e6b5` |
| `ablation/staged-pressure/sympy__sympy-13091/run-10/b-single-review` | ablation/staged-pressure/sympy__sympy-13091/run-10/b-single-review | 0/1 | 0 | 1 | 1 | 1 | `b063872de713` |
| `ablation/staged-pressure/sympy__sympy-13091/run-11/b-single-review` | ablation/staged-pressure/sympy__sympy-13091/run-11/b-single-review | 0/1 | 0 | 1 | 1 | 1 | `2d39605074b3` |
| `ablation/staged-pressure/sympy__sympy-13091/run-12/b-single-review` | ablation/staged-pressure/sympy__sympy-13091/run-12/b-single-review | 0/1 | 0 | 0 | 0 | 1 | `f8033d163fab` |
| `ablation/staged-pressure/sympy__sympy-13091/run-2/c-pipeline` | ablation/staged-pressure/sympy__sympy-13091/run-2/c-pipeline | 0/1 | 0 | 1 | 1 | 1 | `672b28f59522` |
| `ablation/staged-pressure/sympy__sympy-13091/run-3/c-pipeline` | ablation/staged-pressure/sympy__sympy-13091/run-3/c-pipeline | 0/1 | 0 | 1 | 1 | 1 | `b94481496847` |
| `ablation/staged-pressure/sympy__sympy-13091/run-4/c-pipeline` | ablation/staged-pressure/sympy__sympy-13091/run-4/c-pipeline | 0/1 | 0 | 1 | 0 | 1 | `062ca8b10389` |
| `ablation/staged-pressure/sympy__sympy-13091/run-5/c-pipeline` | ablation/staged-pressure/sympy__sympy-13091/run-5/c-pipeline | 0/1 | 0 | 1 | 1 | 1 | `16bccd7bb99b` |
| `ablation/staged-pressure/sympy__sympy-13091/run-6/c-pipeline` | ablation/staged-pressure/sympy__sympy-13091/run-6/c-pipeline | 0/1 | 0 | 0 | 0 | 1 | `16bccd7bb99b` |
| `ablation/staged-pressure/sympy__sympy-13091/run-7/c-pipeline` | ablation/staged-pressure/sympy__sympy-13091/run-7/c-pipeline | 0/1 | 0 | 1 | 0 | 1 | `888e091db99a` |
| `ablation/staged-pressure/sympy__sympy-13091/run-8/c-pipeline` | ablation/staged-pressure/sympy__sympy-13091/run-8/c-pipeline | 0/1 | 0 | 0 | 0 | 1 | `b063872de713` |
| `ablation/staged-pressure/sympy__sympy-13091/run-9/c-pipeline` | ablation/staged-pressure/sympy__sympy-13091/run-9/c-pipeline | 0/1 | 0 | 0 | 0 | 1 | `b063872de713` |
| `ablation/staged/b-single-review` | Staged ablation B - single agent + cross-model review | 6/10 | 0 | 5 | 2 | 12 | `f8033d163fab` |
| `ablation/staged/c-pipeline` | Staged ablation C - planner / developer / reviewer pipeline | 6/10 | 0 | 8 | 3 | 10 | `f8033d163fab` |
| `ablation/strict-bc/b-single-review` | Strict-review B - single agent + cross-model review | 6/10 | 0 | 10 | 3 | 10 | `2602366d719c` |
| `ablation/strict-bc/c-pipeline` | Strict-review C - pipeline | 6/10 | 0 | 10 | 2 | 10 | `2602366d719c` |
| `pilot-five` | Pilot round - five repositories | 1/4 | 0 | 3 | 1 | 4 | `e51af418cebf` |
| `runtime-fix` | Runtime regression check (before fix) | 0/2 | 0 | 2 | 0 | 2 | `1dc56743947a` |
| `runtime-fix-v2` | Runtime regression check (after fix) | 2/2 | 0 | 2 | 0 | 2 | `1b2a4e524ab1` |
| `stage-budget-frozen-602-20261111T090149Z/b-single-review` | stage-budget-frozen-602-20261111T090149Z/b-single-review | 0/1 | 0 | 1 | 0 | 1 | `d22943954144` |
| `stage-budget-frozen-602-20261111T090149Z/c-pipeline` | stage-budget-frozen-602-20261111T090149Z/c-pipeline | 0/1 | 0 | 1 | 0 | 1 | `d22943954144` |
| `unseen-r2` | Round 2 - unseen tasks | 5/9 | 1 | 3 | 4 | 16 | `c9cf7d8bf052` |
| `unseen-r3` | Round 3 - unseen tasks | 3/8 | 2 | 1 | 3 | 10 | `7c452da9b5a8` |
| `unseen-ten` | Ten-task round | 4/9 | 0 | 0 | 3 | 10 | `6ce0d646f653` |
| `workflow-repair-602-v1/b-single-review` | workflow-repair-602-v1/b-single-review | 0/1 | 0 | 1 | 0 | 1 | `402127b9cce9` |
| `workflow-repair-602-v1/c-pipeline` | workflow-repair-602-v1/c-pipeline | 0/1 | 0 | 1 | 0 | 1 | `402127b9cce9` |
| `workflow-repair-cli405-v1/b-single-review` | workflow-repair-cli405-v1/b-single-review | 1/1 | 0 | 0 | 0 | 1 | `b6a96ccf7d12` |
| `workflow-repair-cli405-v1/c-pipeline` | workflow-repair-cli405-v1/c-pipeline | 1/1 | 0 | 0 | 0 | 1 | `b6a96ccf7d12` |

`No verdict` tasks never reached the grader and leave the fraction.
`Cut short` tasks ended early (budget exhausted, agent error) but were
still graded. `Empty patch` tasks were graded as failures without the
agent having edited anything at all, so they measure the budget ceiling
or an outage rather than a wrong fix. Both of the latter two stay in the
fraction, because a verdict from the grader is the honest record of what
happened — but they are shown so nobody reads them as model capability.

## Against the published round reports

The numbers above are recomputed from raw trial records, so they can
differ from the reports written when each round finished. Where they
differ, the cause is a difference in attribution, not in data:

| Round | Published | Recomputed | Why they differ |
|---|---|---|---|
| Round 2 (`evals/UNSEEN_R2_REPORT.md`) | 5/6 | 5/9 (1 no verdict) | Same passes, different denominator. The report scores the 6 tasks that ran to completion and sets the 3 cli tasks aside as provider cut-offs. Here the denominator is every task the grader actually scored, so those 3 are counted (as failures) and flagged as cut-short. One further task never reached the grader and is listed as no-verdict. |
| Round 3 (`evals/UNSEEN_R3_REPORT.md`) | 3/8 | 3/8 (2 no verdict) | Matches. Both sides exclude the 2 tasks that lost their patch and count the killed sympy-13031 run as a failure. |
| Pilot (`evals/PILOT_REPORT.md`) | 1/5 first pass | 1/4 | The frozen first pass ran 4 of 5 tasks (grpc-go never called the model) and resolved 1, which is the 1/4 shown above. A later regrade, after the rule that forbade touching existing test files was corrected, resolved 2 more from the same patches with no new model calls. |

Rows whose count includes a retry:

- `ablation/staged/b-single-review`: `cli__cli-405`, `cli__cli-474` produced no verdict on the first attempt and were rerun.
- `unseen-r2`: `cli__cli-352`, `cli__cli-362`, `cli__cli-402` produced no verdict on the first attempt and were rerun.

## Verifier calibration

Each row applies a known patch and checks that the grader reacts
correctly: `empty-*` supplies no fix and must fail, `gold-*` supplies
the official fix and must pass. These validate the harness, not the
agent.

| Run | Variant | Resolved | Expected |
|---|---|---:|---|
| `pilot-five` | `empty-v1` | 0/0 | all fail |
| `pilot-five` | `empty-v2` | 0/0 | all fail |
| `pilot-five` | `empty-v3` | 0/4 | all fail |
| `pilot-five` | `empty-v4` | 0/5 | all fail |
| `pilot-five` | `gold-v1` | 0/0 | all pass |
| `pilot-five` | `gold-v2` | 0/0 | all pass |
| `pilot-five` | `gold-v3` | 0/0 | all pass |
| `pilot-five` | `gold-v4` | 3/4 | all pass |
| `pilot-five` | `gold-v5` | 4/5 | all pass |
| `runtime-fix` | `empty-v1` | 0/2 | all fail |
| `runtime-fix` | `gold-v1` | 2/2 | all pass |
| `runtime-fix-v2` | `empty-v1` | 0/2 | all fail |
| `runtime-fix-v2` | `gold-v1` | 2/2 | all pass |
| `unseen-r2` | `empty-v1` | 0/12 | all fail |
| `unseen-r2` | `gold-v1` | 8/9 | all pass |
| `unseen-r2` | `gold-v2` | 3/3 | all pass |
| `unseen-r2` | `gold-v3` | 11/12 | all pass |
| `unseen-r3` | `empty-v1` | 0/12 | all fail |
| `unseen-r3` | `gold-v1` | 12/12 | all pass |
| `unseen-ten` | `empty-v1` | 1/12 | all fail |
| `unseen-ten` | `gold-v1` | 0/0 | all pass |
| `unseen-ten` | `gold-v2` | 3/5 | all pass |
| `unseen-ten` | `gold-v3` | 10/12 | all pass |

**A negative control passed, which it must not.** An empty patch
resolving a task means that task's grader accepts no fix at all:

- `unseen-ten` / `empty-v1`: `django__django-7530`

Any agent score on such a task is meaningless, and the task needs
fixing before it is used again.

Gold-patch rows that did not fully pass. These are recorded as
harness or environment problems, exactly what the control exists
to expose, and the affected tasks carry no usable agent score:

- `pilot-five` / `gold-v4`: 3/4 passed — `grpc__grpc-go-2630`, `zeromicro__go-zero-1783`
- `pilot-five` / `gold-v5`: 4/5 passed — `grpc__grpc-go-2630`
- `unseen-r2` / `gold-v1`: 8/9 passed — `cli__cli-352`, `cli__cli-362`, `cli__cli-402`, `django__django-10097`
- `unseen-r2` / `gold-v3`: 11/12 passed — `django__django-10097`
- `unseen-ten` / `gold-v2`: 3/5 passed — `axios__axios-5085`, `axios__axios-5338`
- `unseen-ten` / `gold-v3`: 10/12 passed — `axios__axios-5085`, `axios__axios-5338`

