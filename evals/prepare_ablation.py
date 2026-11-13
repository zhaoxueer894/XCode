"""Prepare the A/B/C ablation cells from the frozen round-three set.

Cells (same 10 tasks, same budget, same binary — only the collaboration
topology differs):
  A single          : orchestrator=developer, no gate review
  B single-review   : A + completion gate with a cross-model reviewer
  C pipeline        : forced planner -> developer -> cross-model reviewer

Per cell this writes .eval/ablation/<cell>/ with:
  manifest.json  full frozen config incl. orchestrate + reviewer fields
  tasks/         copies of the calibrated round-three task dirs
  meta.txt       git revision and binary sha256 (filled by driver)

Everything the later analysis and slides need stays under the cell dir:
harbor trials (events.jsonl, result.json, model.patch, verifier grades),
console logs, and the exact manifest that produced them.
"""
from __future__ import annotations

import argparse
import json
import shutil
import sys
from pathlib import Path

from evals.ablation_config import CONFIG_PATH, load_models, probe_model

ROOT = Path(__file__).resolve().parents[1]
SOURCE_MANIFEST = ROOT / "evals/unseen-r3.json"
SOURCE_TASKS = ROOT / ".eval/unseen-r3/eligible-tasks"

CELLS = {
    "a-single": "single",
    "b-single-review": "single-review",
    "c-pipeline": "pipeline",
}

NODE_TEST = ["node", "./node_modules/jest/bin/jest.js", "--runInBand", "--coverage=false"]
STAGED_VERIFICATION = {
    "cli__cli-405": ["go", "test", "./command"],
    "cli__cli-474": ["go", "test", "./command"],
    "cli__cli-477": ["go", "test", "./command"],
    "iamkun__dayjs-589": [*NODE_TEST, "test/plugin/quarterOfYear.test.js"],
    "iamkun__dayjs-591": [*NODE_TEST, "test/plugin/quarterOfYear.test.js"],
    "iamkun__dayjs-602": [*NODE_TEST, "test/locale.test.js"],
    "sympy__sympy-13031": ["python", "bin/test", "-k", "stack", "sympy/matrices/tests/test_commonmatrix.py"],
    # Both files hold an F2P test; the upstream grader (eval.sh) runs both. A
    # narrower visible command left the agent with all-green evidence while the
    # hidden test still failed.
    "sympy__sympy-13091": ["python", "bin/test", "sympy/core/tests/test_basic.py", "sympy/core/tests/test_numbers.py"],
    "django__django-10880": ["python", "tests/runtests.py", "aggregation", "aggregation_regress", "--parallel", "1"],
    "django__django-10914": ["python", "tests/runtests.py", "file_storage.tests.FileStoragePermissions", "--parallel", "1"],
}


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--preflight", action="store_true", help="probe the configured models and print their egress hosts")
    modes = parser.add_mutually_exclusive_group()
    modes.add_argument("--strict-bc", action="store_true", help="prepare a separate strict-review B/C run")
    modes.add_argument("--staged", action="store_true", help="prepare a fresh A/B/C run with staged review budgets")
    parser.add_argument("--pressure-task", help="prepare only this frozen task in C, in a separate staged run")
    parser.add_argument("--pressure-run", type=int, default=1, help="separate archive number for a pressure revalidation")
    args = parser.parse_args()
    if args.pressure_task and not args.staged:
        parser.error("--pressure-task requires --staged")
    if args.pressure_run < 1 or (args.pressure_run != 1 and not args.pressure_task):
        parser.error("--pressure-run requires a positive archive number and a pressure task")
    try:
        developer, reviewer = load_models(CONFIG_PATH)
        if args.preflight:
            for model in (developer, reviewer):
                probe_model(model)
                print(f"{model.provider} preflight passed", file=sys.stderr)
            print(*dict.fromkeys((developer.host, reviewer.host)))
            return
    except (OSError, ValueError, RuntimeError) as error:
        raise SystemExit(f"FATAL: {error}") from None

    manifest = json.loads(SOURCE_MANIFEST.read_text(encoding="utf-8"))
    output = ROOT / (".eval/ablation/strict-bc" if args.strict_bc else ".eval/ablation")
    if args.staged:
        output = ROOT / ".eval/ablation/staged"
    if args.pressure_task:
        manifest["tasks"] = [task for task in manifest["tasks"] if task["id"] == args.pressure_task]
        if not manifest["tasks"]:
            parser.error("pressure task must be in the frozen manifest")
        output = ROOT / ".eval/ablation/staged-pressure" / args.pressure_task
        if args.pressure_run > 1:
            output /= f"run-{args.pressure_run}"
    baseline_hash = None
    if args.strict_bc:
        baseline = output.parent / "a-single"
        baseline_hash = (baseline / "meta.txt").read_text(encoding="utf-8").splitlines()[1]
    resume = output.exists()
    cells = {cell: mode for cell, mode in CELLS.items() if not args.strict_bc or mode != "single"}
    if args.pressure_task:
        cells = {"c-pipeline": CELLS["c-pipeline"]}
    for cell, topology in cells.items():
        cell_dir = output / cell
        review = topology != "single"
        cell_manifest = dict(manifest)
        agent = dict(manifest["agent"])
        agent.update(
            orchestrate=topology,
            provider=developer.provider,
            model=developer.model_id,
            base_url=developer.base_url,
            reviewer_provider=reviewer.provider if review else "",
            reviewer_model=reviewer.model_id if review else "",
            reviewer_base_url=reviewer.base_url if review else "",
            allowed_hosts=list(dict.fromkeys((developer.host, reviewer.host))),
            no_delegate=False,
        )
        if args.strict_bc:
            agent.update(
                strict_reviewer_max_steps=32,
                reused_baseline="../a-single",
                baseline_binary_sha256=baseline_hash,
            )
        agent["ablation_note"] = (
            "A/B/C ablation over the frozen round-three tasks; identical binary, budget "
            "and task order; only the collaboration topology differs. Ablation cells "
            "reuse tasks already seen by round three (delegate-advertised, mostly "
            "unused) — disclosed, same exposure across cells."
        )
        if args.staged:
            agent.update(
                max_tokens=500_000,
                allocate_stage_budget=review,
                strict_reviewer_max_steps=32 if review else 0,
                # A thinking reviewer spends output budget on reasoning before it
                # can emit its one-line verdict; 8192 left it truncated with no
                # GATE-VERDICT at all (pressure run 7).
                reviewer_max_output_tokens=32768 if review else 0,
                functional_repair_once=True,
                functional_repair_reserve={"token_percent": 25, "request_percent": 10, "max_attempts": 1},
            )
            cell_manifest["task_runtime"] = {
                task["id"]: {
                    "external_node_modules": task["language"] == "javascript",
                    "verify_command": STAGED_VERIFICATION[task["id"]],
                }
                for task in manifest["tasks"]
            }
            if args.pressure_task:
                agent["ablation_note"] = "Single-task pipeline pressure check, outside the formal A/B/C run."
        cell_manifest["agent"] = agent
        if resume:
            manifest_path = cell_dir / "manifest.json"
            if not manifest_path.is_file() or not (cell_dir / "tasks").is_dir():
                raise SystemExit(f"FATAL: partial prepare state in {cell_dir}")
            if json.loads(manifest_path.read_text(encoding="utf-8")) != cell_manifest:
                raise SystemExit(f"FATAL: {cell} manifest differs from the current ablation configuration")
            print(f"verified existing manifest for {cell}")
            continue
        cell_dir.mkdir(parents=True)
        (cell_dir / "manifest.json").write_text(json.dumps(cell_manifest, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
        for task in cell_manifest["tasks"]:
            task_id = task["id"]
            shutil.copytree(SOURCE_TASKS / task_id, cell_dir / "tasks" / task_id)
        print(f"prepared {cell}: {len(cell_manifest['tasks'])} tasks, orchestrate={topology}")


if __name__ == "__main__":
    main()
