**English** | [简体中文](README.zh-CN.md)

# XCode

XCode is a multi-agent coding tool written in Go. Give it a task — say, "fix this bug and get the tests passing" — and it reads code, edits code, and runs commands in the workspace you specify, then tells you what it did and how it went.

## Multi-agent design

The architecture is orchestrator-worker. The main agent aligns on the goal with you, decomposes the work, and hands it out; the actual execution goes to role-specific subagents:

```text
Orchestrator (main agent)
  ├─ delegate → planner    decomposes the task, edits no files
  ├─ delegate → developer  edits code, runs tests
  └─ delegate → reviewer   independent check, returns pass / issues
```

**Role separation.** The three built-in roles each carry their own instructions: the planner only decomposes and never edits; the developer edits code and runs verification; the reviewer only looks at the code and the final state of the workspace. Roles can be customized via `.xcode/skills/<role>.md`.

**Context isolation.** Every delegate creates a brand-new subagent: its own conversation history, its own step budget, no inherited parent context, and only a summary of conclusions is returned. This is deliberate — the reviewer cannot see the developer's reasoning, which is what makes its judgment genuinely independent.

**Communication and traces.** Downward goes a task packet (role + task description), upward comes a conclusion summary; chat history is never shared. Every subagent's behavior events carry an `agent_id` into the same JSONL trace, so who did what is always auditable.

**Completion gate.** When a completion contract is supplied, XCode runs the verification commands whenever the model claims it is done; if the contract enables `review`, it also dispatches a reviewer with fresh context that emits `GATE-VERDICT: PASS` or `ISSUES`. A rejection sends the work back to be fixed, until it passes or the budget is exhausted. Real-world outcomes from compilation and tests are the final source of truth.

Want to check whether multi-agent actually pays off? `xcode eval --no-delegate` turns delegation off in one flag, so you can score the same task set against a single agent.

Tool execution has safety constraints: files are snapshotted before being written (and can be restored), commands are confined to the workspace by default, and sensitive operations require your confirmation.

## Quick start

Go 1.26+ is required. Build and run it directly; on first launch a model-selection menu appears in the terminal (if you would rather skip the menu, edit the config file directly — see the next section):

```bash
go build -o bin/xcode.exe ./cmd/xcode
cp .xcode/config.example .xcode/config    # copy the template and fill in your model key (this file is not committed)
bin/xcode.exe               # interactive mode (REPL, multi-turn conversation)
bin/xcode.exe "fix a bug"   # one-shot task
```

If you would rather not type `bin/xcode.exe` every time, you can optionally install it to your user PATH (entirely optional):

```bash
bin/xcode.exe install       # copies into your home directory and updates PATH; open a new terminal and type xcode
```

With no real model configured, a demo fake model is used: deterministic logic hard-coded in the source (it reads the last word of the prompt as a file name, once). It lets you exercise the whole pipeline at zero cost, and gives automated tests and offline evals a reproducible target. It will not understand your task and will not actually fix bugs — **for real work, configure a real model as described in the next section**.

## Configuring models

All model configuration lives in one file: **the global `~/.xcode/config`** (on Windows, `C:\Users\<you>\.xcode\config`). That is what the first-run wizard writes; editing it by hand is exactly equivalent. A project-level `.xcode/config` can override it as needed. Precedence: command-line flags > project config > global config > environment variables > built-in defaults.

```ini
# Default provider: demo / bailian / deepseek
provider=bailian

# Qwen (Alibaba Cloud Bailian)
bailian-api-key=sk-xxxxxxxx
bailian-base-url=https://dashscope.aliyuncs.com/compatible-mode/v1
bailian-model=qwen-plus

# DeepSeek (base-url has a default; usually just key and model)
deepseek-api-key=sk-xxxxxxxx
deepseek-base-url=https://api.deepseek.com
deepseek-model=deepseek-flash
```

Key points:

- base-url and model both have defaults, so an api-key alone is enough to run; the keys are fully symmetric across the three providers.
- Put keys in the global config only (outside the repo, so they cannot be committed by accident); be aware that a key written to the project `.xcode/config` may be committed with the repo.
- Environment variables (`DASHSCOPE_API_KEY`, `DEEPSEEK_API_KEY`, etc.) still work, but only as an injection fallback for scenarios without a config file, such as CI.
- If a key is configured but the provider is still `demo`, startup asks whether to switch instead of silently running the fake model.
- The `--provider` flag is retained for temporary overrides and scripted scenarios.

> **Upgrading from an older version**: `.env` and `--model-config` have been removed. If a legacy `.env` is detected at startup, a one-time migration notice is printed; move the key into the config file above.

## Optional: compression decision classifier (jev)

An advanced feature for compaction; leaving it unconfigured does not affect normal use.

Long conversations are compacted to save context. During compaction, XCode splits the history into "evidence units" and decides which to keep, truncate, or drop — by built-in rules by default. If you want a model to make those decisions, there are two backends:

- **local**: reuses the main model as the classifier by default, with no extra configuration; `typed-classifier-api-key/-endpoint/-model` can point at a dedicated classifier model (falls back to the Bailian key when no key is given).
- **jev**: calls TypeSafe's systemone classification service. Add three keys to the config file (`~/.xcode/config`):

  ```ini
  typesafe-api-key=your-key
  typesafe-endpoint=service-endpoint
  typesafe-model=model-name
  ```

  Then set `typed-classifier=jev` in `.xcode/config` (defaults to local) and enable it at runtime with `--typed-shadow`.

Shadow means classification results are recorded but not applied — real compaction still follows the rules, which makes it easy to compare model versus rule decisions.

## Usage

### One-shot tasks

The positional argument is the task description; wrap the whole sentence in quotes. The provider defaults to the config file:

```bash
bin/xcode.exe --workspace D:/path/to/repo "fix the login timeout bug, then get go test passing"
```

Each step is shown live while running: which files were read, which commands ran, policy decisions and approvals. When it finishes, the stop reason and this run's token usage are printed.

### Interactive mode (multi-turn conversation)

Launching without a task enters the REPL: you can converse continuously, and context persists across turns (the provider comes from the config file; if you installed via `xcode install`, just type `xcode`):

```bash
bin/xcode.exe
```

The REPL has several built-in slash commands:

```text
/status             current provider, turns, cumulative tokens, modified files, test status
/model              switch model provider: menu selection (or /model deepseek directly),
                     writes the config file and takes effect immediately, history preserved
/setup              same as /model
/compact            compact conversation history to free context (working notes preserved)
/trust go test      trust a command prefix; running it in this session no longer prompts
/clear              clear session history (project memory preserved)
/exit               quit
/help               list available commands
```

The session is saved automatically on exit, and the next launch asks whether to continue from where you left off.

### Crash recovery

If the process is killed mid-task (crash, power loss, terminal closed by accident), add `--resume` to continue from the last safe boundary; completed steps are not redone:

```bash
bin/xcode.exe --resume
```

Unfinished tool calls are handled according to their side effects: read-only ones are retried automatically; file-modifying ones are replayed only after checking the actual state; anything whose outcome is ambiguous (a single exec, for example) is listed for you to confirm — it never re-runs on its own initiative.

### Completion gate

By default, the run ends when the model says it is done. If a task has hard acceptance criteria, write a contract file and let the gate enforce it:

```json
{
  "goal": "fix the out-of-bounds access in utils.go",
  "acceptance": ["go test all pass"],
  "review": true,
  "verify_commands": [
    { "name": "go test", "command": ["go", "test", "./..."] }
  ]
}
```

```bash
bin/xcode.exe --contract task.json "fix the out-of-bounds issue in utils.go"
```

When the model claims completion, XCode actually runs `verify_commands` in the current workspace and dispatches a reviewer to double-check; a failure sends the work back to be fixed, until it passes or the budget is exhausted.

### Evaluation

The new external evaluation entry point and the staged implementation notes are in [evals/README.md](evals/README.md). It runs the real CLI, saves failure results, accounts for main/sub-agent and summary model budgets uniformly, and provides Harbor adaptation and full patch export.

Every graded run so far is tabulated in [evals/EVAL_RESULTS.md](evals/EVAL_RESULTS.md): per-task verdicts, the binary each arm used, and the known limits of each comparison. The raw trajectories, patches and grader output behind those numbers are published as a release asset.

The `xcode eval` below is an internal small-task regression; it cannot be used to claim real-repository capability or enterprise-grade reliability. The demo model only checks that the pipeline is wired up.

Score a fixed task set with multiple trials for mean and variance:

```bash
# Offline scoring (demo fake model, free, reproducible)
bin/xcode.exe eval --trials 3 --label demo-baseline

# Scoring with a real model
bin/xcode.exe eval --provider bailian --trials 3 --label qwen-full

# Single vs. multi-agent comparison: --no-delegate turns delegation off; run again and compare
bin/xcode.exe eval --provider bailian --trials 3 --label qwen-nodelegate --no-delegate

# Freeze a baseline and compare after every change (non-zero exit on regression, usable as a merge gate)
bin/xcode.exe eval baseline save qwen-full --run .eval/<run-dir>
bin/xcode.exe eval compare --baseline qwen-full --current .eval/<new-run-dir>
```

Each evaluation writes the JSONL traces of all trials plus reports in three formats — `report.json` / `report.md` / `report.html` (success rate, turns, tokens, repeated reads, variance, failure clustering).

### Auxiliary commands

```text
xcode install                 optional: install to user PATH, then just type xcode (uninstall = delete file + remove PATH entry)
xcode checkpoints                list past pre-write snapshots
xcode restore --id <snapshotID>  restore a specific snapshot (requires interactive confirmation)
xcode journal                    inspect the tool-call state machine (including unknowns left by crash windows)
xcode policy check --tool exec --arguments '{"command":["go","test"]}'
                                 dry-run one policy decision and print the full decision chain
xcode eval-fault                 fault-injection evaluation: compare compaction/recovery strategies under crashes
```

## Common configuration

Runtime precedence: command-line flags > project-level `.xcode/config` > global `~/.xcode/config` > built-in defaults. The most-used settings:

```text
developer=deepseek        # development model: a slot name from the model set (see below)
reviewer=glm              # review model: slot name; if unset, review degrades to the development model with a warning
model.deepseek.key=sk-... # model set: model.<name>.key / base-url / id, any number of entries
model.deepseek.base-url=https://api.deepseek.com
model.deepseek.id=deepseek-flash
orchestrate=              # empty (full-tool agent) / single / single-review / pipeline
mode=workspace-write      # plan / workspace-write / full-access
context-strategy=bounded  # bounded / none / sliding / handoff / compaction / decision-gated
context-budget=6000       # estimated token target for model-visible history; excludes tool descriptions
max-steps=8               # max model turns per task
sandbox=host              # host / docker / docker-wsl
timeout=2m
```

Other optional files: `.xcode/policy.json` (tool allow/deny rules), `.xcode/skills/<role>.md` (custom delegate roles), `.xcode/memory.md` (project-level memory, injected on every run).

The default `bounded` keeps the task, project requirements, and the most recent complete tool round-trips, collapsing older results into evidence records with artifact references. Full history is still stored; the current task or the latest tool round-trip may exceed the target when large, and a tool call and its result are never split apart. `none` explicitly disables compaction. Every model call also receives the latest shared request/token balance and remaining time; these notices are not written into the session history. When a production change is observed and a later command exits successfully, the notice asks for immediate wrap-up; when several consecutive calls after a modification produce no successful verification, the notice asks for one focused verification before finishing. When the budget only covers one last request (1 request left, or under 5% of tokens left), tools are disabled for that request so the model produces a final answer instead of issuing a tool call whose result nobody will ever consume. Past half the budget with no production change observed, an exploration gate engages: search and directory listing are disabled first, and below 30% remaining, command execution is disabled too, leaving only file reads, file edits, and diff, forcing a move into implementation; the gate lifts immediately once a production change is observed. Under delegation, each agent is subject to the same rules according to its own progress.

Pre-command snapshots support symlinks: the link itself is saved and restored, and its target is never read. Go caches, concurrency settings, and an activated Python environment needed by build tools are passed to commands; model API keys are not passed to command subprocesses.

## Orchestration modes and cross-model review

`orchestrate` selects the collaboration topology; the `--orchestrate` flag takes priority over the config file:

- **single**: a single agent; the model cannot see the delegate tool;
- **single-review**: a single agent plus a mandatory completion-gate review (automatically enables the review contract);
- **pipeline**: a pipeline orchestrator — the toolbox keeps only delegate and git_diff, and editing code itself is forbidden;
  a mandatory planner (structured diagnosis, budget capped at 30% of the whole run) → developer (the only editor) →
  completion gate. Station order is validated in code: a developer before the planner is rejected; an orchestrator
  claiming completion early is bounced back by the gate's precondition.

The completion gate's reviewer is the only mandatory independent agent: fresh context, seeing only the goal + diff + test results, never the development process. `reviewer-provider` configures cross-model review (e.g. glm); when it is unconfigured or fails at runtime (tokens exhausted, provider error), the gate **automatically degrades to the developer's model with a triple warning** (stderr banner + `review.degraded` event + run-record marker) — never silently. The exploration gate and runtime notices are decided from a run-level shared ledger — the main agent and all subagents share one account of "has anyone touched production code", so late-born subagents are not penalized by an empty ledger.

## Tests

```bash
go test ./...
go test -race ./...
```
