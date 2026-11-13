"""Run the lite task set against the real XCode binary and grade in isolation.

Unlike the Harbor/SWE line this needs no Docker and no image: each task is a
temp git workspace with a handful of small files, so a full run of even 200
tasks is cheap. The runner is deliberately separate from drive_ablation.sh and
never writes into .eval/ablation/.

Flow per task:

1. materialise ``starter/`` into a temp git workspace (baseline commit);
2. write the prompt and a completion-gate contract whose verify commands are
   the task's VISIBLE suite only;
3. run ``bin/xcode`` non-interactively with the task's scoped policy;
4. export the candidate patch;
5. grade the patch on a FRESH tree in another temp directory using the hidden
   suite, and record the verdict.

Resume: a task whose grade.json already exists in this run root is skipped.
Results are never deleted or rerun in place; use a new --run-id to repeat.
"""

from __future__ import annotations

import argparse
import concurrent.futures
import hashlib
import json
import os
import shutil
import subprocess
import sys
import threading
import time
from datetime import datetime, timezone
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import lite_lib  # noqa: E402

ROOT = lite_lib.ROOT
ENV_PREFIX = {"deepseek": "DEEPSEEK", "glm": "GLM", "bailian": "DASHSCOPE"}
PRINT_LOCK = threading.Lock()


# --------------------------------------------------------------------------- #
# credentials
# --------------------------------------------------------------------------- #


def read_config(path: Path) -> dict:
    """Parse the project .xcode/config key=value file into a dict."""
    values = {}
    for number, line in enumerate(path.read_text(encoding="utf-8-sig").splitlines(), 1):
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        key, separator, value = line.partition("=")
        if not separator:
            raise SystemExit(f"{path}:{number}: expected key=value")
        values[key.strip()] = value.strip()
    return values


def model_environment(config: dict, role: str) -> tuple[str, dict, str]:
    """Resolve one role's model slot into (slot name, environment, model id)."""
    slot = config.get(role, "").strip()
    if not slot:
        raise SystemExit(f".xcode/config has no {role}= selector")
    prefix = f"model.{slot}."
    key, base_url, model_id = (config.get(prefix + name, "") for name in ("key", "base-url", "id"))
    if not all((key, base_url, model_id)):
        raise SystemExit(f".xcode/config requires {prefix}key, base-url and id")
    if slot not in ENV_PREFIX:
        raise SystemExit(f"unsupported {role} slot {slot!r}: expected one of {', '.join(ENV_PREFIX)}")
    environment = {
        f"{ENV_PREFIX[slot]}_API_KEY": key,
        f"{ENV_PREFIX[slot]}_BASE_URL": base_url,
        f"{ENV_PREFIX[slot]}_MODEL": model_id,
    }
    return slot, environment, model_id

def preflight(environment: dict, role: str) -> None:
    """Fail fast when a role's model slot cannot answer a one-token request."""
    import urllib.error
    import urllib.request

    prefix = next(name.split("_")[0] for name in environment if name.endswith("_API_KEY"))
    payload = json.dumps({
        "model": environment[f"{prefix}_MODEL"],
        "messages": [{"role": "user", "content": "Reply with OK only."}],
        "max_tokens": 16,
    }).encode()
    request = urllib.request.Request(
        environment[f"{prefix}_BASE_URL"].rstrip("/") + "/chat/completions",
        data=payload,
        headers={"Authorization": f"Bearer {environment[f'{prefix}_API_KEY']}", "Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            body = json.load(response)
    except (urllib.error.HTTPError, urllib.error.URLError, OSError, ValueError) as error:
        raise SystemExit(f"{role} preflight failed: {error}") from None
    if not isinstance(body.get("choices"), list):
        raise SystemExit(f"{role} preflight returned no chat completion")


# --------------------------------------------------------------------------- #
# one task run
# --------------------------------------------------------------------------- #


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1 << 20), b""):
            digest.update(block)
    return digest.hexdigest()


def run_task(task: dict, cell: str, run_dir: Path, binary: Path, args, environment: dict) -> dict:
    """Run one task in one cell and grade it; return the recorded outcome."""
    task_dir = run_dir / cell / task["id"]
    task_dir.mkdir(parents=True, exist_ok=True)
    grade_path = task_dir / "grade.json"

    prompt_file = task_dir / "prompt.txt"
    prompt_file.write_text(task["prompt"] + "\n", encoding="utf-8")
    contract = lite_lib.write_contract(task, task_dir / "contract.json", args.python_exe, review=args.review)
    record = {
        "id": task["id"],
        "cell": cell,
        "language": task["language"],
        "kind": task["kind"],
        "started_at": datetime.now(timezone.utc).isoformat(),
    }

    scratch = lite_lib.scratch_directory()
    try:
        workspace = scratch / "workspace"
        baseline = lite_lib.prepare_workspace(task, workspace)
        record["baseline_commit"] = baseline

        command = [
            str(binary),
            "--isolated", "--non-interactive",
            "--provider", args.provider,
            "--workspace", str(workspace),
            "--policy", str(lite_lib.policy_for(task)),
            "--contract", str(contract),
            "--prompt-file", str(prompt_file),
            "--event-log", str(task_dir / "events.jsonl"),
            "--artifact-dir", str(task_dir / "artifacts"),
            "--result-file", str(task_dir / "run.json"),
            "--mode", "workspace-write",
            "--sandbox", "host",
            "--context-strategy", args.context_strategy,
            "--context-budget", str(args.context_budget),
            "--max-steps", str(args.max_steps),
            "--max-tokens", str(args.max_tokens),
            "--max-model-calls", str(args.max_model_calls),
            "--provider-retries", "0",
            "--timeout", f"{args.timeout_sec}s",
            "--reviewer-provider", args.reviewer_provider,
        ]
        if args.orchestrate:
            command += ["--orchestrate", args.orchestrate]
        if args.review:
            command += ["--strict-reviewer-max-steps", str(args.strict_reviewer_max_steps)]

        started = time.time()
        timed_out = False
        try:
            completed = subprocess.run(
                command, cwd=str(workspace), env=environment, timeout=args.timeout_sec + 120,
                stdout=subprocess.PIPE, stderr=subprocess.STDOUT, check=False,
            )
            exit_code, output = completed.returncode, completed.stdout.decode("utf-8", errors="replace")
        except subprocess.TimeoutExpired as error:
            timed_out, exit_code = True, -1
            output = (error.stdout or b"").decode("utf-8", errors="replace") + "\n[runner timeout]"
        record.update({
            "exit_code": exit_code,
            "runner_timed_out": timed_out,
            "wall_time_sec": round(time.time() - started, 1),
            "command": command,
        })
        (task_dir / "console.log").write_text(output, encoding="utf-8")

        # The patch is evidence; the hidden suite decides the outcome.
        patch_path = task_dir / "model.patch"
        try:
            patch = lite_lib.capture_patch(workspace, patch_path)
        except lite_lib.TaskError as error:
            record["patch_error"] = str(error)
            patch = b""
        grade = lite_lib.grade(task, patch, scratch / "grade", args.python_exe, args.timeout_sec)
    finally:
        shutil.rmtree(scratch, ignore_errors=True)

    record["grade_status"] = grade["status"]
    record["resolved"] = grade["status"] == "passed"
    record["finished_at"] = datetime.now(timezone.utc).isoformat()
    grade_path.write_text(json.dumps({"record": record, "grade": grade}, ensure_ascii=False, indent=2), encoding="utf-8")
    with PRINT_LOCK:
        mark = {"passed": "PASS", "failed": "fail", "error": "ERR "}[grade["status"]]
        print(f"[{mark}] cell={cell} task={task['id']} exit={exit_code} {record['wall_time_sec']}s", flush=True)
    return record


# --------------------------------------------------------------------------- #
# reporting
# --------------------------------------------------------------------------- #


def summarize(run_dir: Path, cells: list[str]) -> dict:
    """Merge every cell's grade.json into a report; nothing is dropped."""
    rows = []
    for cell in cells:
        for path in sorted((run_dir / cell).glob("*/grade.json")):
            payload = json.loads(path.read_text(encoding="utf-8"))
            rows.append({**payload["record"], "grade_reason": payload["grade"].get("reason")})
    by_cell = {}
    for cell in cells:
        cell_rows = [row for row in rows if row["cell"] == cell]
        by_cell[cell] = {
            "total": len(cell_rows),
            "resolved": sum(1 for row in cell_rows if row["resolved"]),
            "errors": sum(1 for row in cell_rows if row["grade_status"] == "error"),
            "by_kind": _count_by(cell_rows, "kind"),
            "by_language": _count_by(cell_rows, "language"),
        }
    return {
        "kind": "lite_task_outcome_not_a_leaderboard_score",
        "generated_at": datetime.now(timezone.utc).isoformat(),
        "cells": by_cell,
        "tasks": rows,
    }


def _count_by(rows: list[dict], field: str) -> dict:
    counts = {}
    for row in rows:
        bucket = counts.setdefault(row[field], {"total": 0, "resolved": 0})
        bucket["total"] += 1
        bucket["resolved"] += int(row["resolved"])
    return counts


def write_report(run_dir: Path, report: dict, context: dict) -> None:
    (run_dir / "report.json").write_text(json.dumps({**context, **report}, ensure_ascii=False, indent=2), encoding="utf-8")
    lines = ["# Lite task run", ""]
    lines.append("Small local tasks graded by hidden tests. This is not a leaderboard score.")
    lines.append("")
    lines.append(f"Binary sha256: `{context['binary_sha256']}`")
    lines.append(f"Git revision: `{context['git_revision']}`")
    lines.append(f"Budget per task: {context['max_tokens']} tokens, {context['max_model_calls']} calls, {context['timeout_sec']}s")
    lines.append("")
    lines.append("| cell | resolved | total | infra errors |")
    lines.append("|---|---:|---:|---:|")
    for cell, stats in report["cells"].items():
        lines.append(f"| {cell} | {stats['resolved']} | {stats['total']} | {stats['errors']} |")
    lines.append("")
    for cell, stats in report["cells"].items():
        lines.append(f"## {cell}")
        lines.append("")
        lines.append("By kind: " + ", ".join(f"{kind} {v['resolved']}/{v['total']}" for kind, v in sorted(stats["by_kind"].items())))
        lines.append("")
        lines.append("By language: " + ", ".join(f"{lang} {v['resolved']}/{v['total']}" for lang, v in sorted(stats["by_language"].items())))
        lines.append("")
    (run_dir / "report.md").write_text("\n".join(lines) + "\n", encoding="utf-8")


# --------------------------------------------------------------------------- #
# entry point
# --------------------------------------------------------------------------- #


def select_tasks(args) -> list[dict]:
    tasks = lite_lib.iter_tasks()
    if args.tasks:
        wanted = {name.strip() for name in args.tasks.split(",") if name.strip()}
        known = {task["id"] for task in tasks}
        missing = wanted - known
        if missing:
            raise SystemExit(f"unknown task ids: {', '.join(sorted(missing))}")
        tasks = [task for task in tasks if task["id"] in wanted]
    if args.language:
        tasks = [task for task in tasks if task["language"] == args.language]
    if args.kind:
        tasks = [task for task in tasks if task["kind"] == args.kind]
    if args.limit:
        tasks = tasks[: args.limit]
    return tasks


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--cells", default="single",
                        help="comma-separated cells: single,single-review,pipeline")
    parser.add_argument("--tasks", default="", help="comma-separated task ids (default: all)")
    parser.add_argument("--language", default="", choices=["", "python", "go"])
    parser.add_argument("--kind", default="", choices=["", "algo", "module", "bugfix"])
    parser.add_argument("--limit", type=int, default=0, help="run only the first N selected tasks")
    parser.add_argument("--run-id", default="", help="output directory name (default: timestamp)")
    parser.add_argument("--label", default="", help="recorded in the report for identification")
    parser.add_argument("--concurrency", type=int, default=2, help="tasks run at once")
    parser.add_argument("--binary", default=str(ROOT / "bin" / "xcode.exe"))
    parser.add_argument("--config", default=str(ROOT / ".xcode" / "config"))
    parser.add_argument("--python", dest="python_exe", default=lite_lib.default_python())
    parser.add_argument("--provider", default="", help="defaults to the developer slot name")
    parser.add_argument("--reviewer-provider", default="", help="defaults to the reviewer slot name")
    parser.add_argument("--orchestrate", default="", choices=["", "single", "single-review", "pipeline"])
    parser.add_argument("--review", action="store_true", help="request gate review (contract review=true)")
    parser.add_argument("--strict-reviewer-max-steps", type=int, default=8)
    parser.add_argument("--max-tokens", type=int, default=200000)
    parser.add_argument("--max-model-calls", type=int, default=60)
    parser.add_argument("--max-steps", type=int, default=40)
    parser.add_argument("--timeout-sec", type=int, default=300)
    parser.add_argument("--context-strategy", default="bounded")
    parser.add_argument("--context-budget", type=int, default=6000)
    parser.add_argument("--skip-preflight", action="store_true")
    parser.add_argument("--demo", action="store_true",
                        help="offline plumbing check with the demo model (expects every task to fail)")
    parser.add_argument("--yes", action="store_true", help="confirm the run may spend model budget")
    args = parser.parse_args()

    if not args.yes and not args.demo:
        raise SystemExit("refusing to run without --yes: this spends real model budget")
    binary = Path(args.binary)
    if not binary.is_file():
        raise SystemExit(f"binary not found: {binary} (build it first)")

    if args.demo:
        # Plumbing check only: the offline demo model changes no file, so every
        # task is expected to fail grading. This exercises workspace setup,
        # policy, the gate and patch capture without spending any budget.
        developer_slot, reviewer_slot = "demo", "demo"
        developer_env = reviewer_env = {}
        developer_model = reviewer_model = "demo"
        provider = reviewer_provider = "demo"
        # The CLI accepts only glm/deepseek/bailian as a reviewer provider; an
        # empty value simply leaves the reviewer unconfigured, which is correct
        # because the demo cell requests no review.
        reviewer_provider = ""
        # Cells only differ by orchestrate/review, which the demo model cannot
        # exercise; force a single cell so no mismatched evidence is recorded.
        args.cells, args.review, args.orchestrate = "single", False, ""
        environment = dict(os.environ)
    else:
        config = read_config(Path(args.config))
        developer_slot, developer_env, developer_model = model_environment(config, "developer")
        reviewer_slot, reviewer_env, reviewer_model = model_environment(config, "reviewer")
        environment = {**os.environ, **developer_env, **reviewer_env}
        provider = args.provider or developer_slot
        reviewer_provider = args.reviewer_provider or reviewer_slot
        if not args.skip_preflight:
            preflight(developer_env, "developer")
            if args.review or args.orchestrate in ("single-review", "pipeline"):
                preflight(reviewer_env, "reviewer")
    # run_task reads the resolved provider names off args.
    args.provider = provider
    args.reviewer_provider = reviewer_provider

    cells = [cell.strip() for cell in args.cells.split(",") if cell.strip()]
    run_id = args.run_id or datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    run_dir = lite_lib.RUN_ROOT / "runs" / run_id
    run_dir.mkdir(parents=True, exist_ok=True)

    tasks = select_tasks(args)
    if not tasks:
        raise SystemExit("no tasks selected")
    print(f"lite run {run_id}: {len(tasks)} tasks x {len(cells)} cell(s), concurrency {args.concurrency}")

    pending = [
        (task, cell) for cell in cells for task in tasks
        if not (run_dir / cell / task["id"] / "grade.json").exists()
    ]
    if not pending:
        print("nothing to do: every selected task already has a grade.json")
    with concurrent.futures.ThreadPoolExecutor(max_workers=args.concurrency) as pool:
        futures = [pool.submit(run_task, task, cell, run_dir, binary, args, environment) for task, cell in pending]
        for future in concurrent.futures.as_completed(futures):
            future.result()

    context = {
        "run_id": run_id,
        "label": args.label,
        "demo": args.demo,
        "binary": str(binary),
        "binary_sha256": sha256_file(binary),
        "git_revision": subprocess.run(
            ["git", "-C", str(ROOT), "rev-parse", "HEAD"], stdout=subprocess.PIPE, check=False
        ).stdout.decode().strip(),
        "developer": f"{developer_slot}/{developer_model}",
        "reviewer": f"{reviewer_slot}/{reviewer_model}",
        "cells": cells,
        "orchestrate": args.orchestrate,
        "review": args.review,
        "max_tokens": args.max_tokens,
        "max_model_calls": args.max_model_calls,
        "max_steps": args.max_steps,
        "timeout_sec": args.timeout_sec,
        "context_strategy": args.context_strategy,
        "context_budget": args.context_budget,
    }
    report = summarize(run_dir, cells)
    write_report(run_dir, report, context)
    print(f"\nreport: {run_dir / 'report.md'}")
    for cell, stats in report["cells"].items():
        print(f"  {cell}: {stats['resolved']}/{stats['total']} resolved, {stats['errors']} infra errors")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
