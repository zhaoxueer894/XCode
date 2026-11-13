"""Calibrate the lite tasks: gold must pass, empty must fail.

A task enters the model round only after this passes for both its visible and
its hidden suites. Two properties matter and both are checked:

- the visible suite the agent can run fails on the starter and passes on the
  reference solution, so the task gives real signal;
- the hidden grading suite does the same, so an agent that "fixes" only the
  visible tests still fails.

The report is written to .eval/lite/calibration/<timestamp>.json and, with
--label, to a stable name. Nothing is deleted; re-running appends a new file.
"""

from __future__ import annotations

import argparse
import json
import shutil
import sys
from datetime import datetime, timezone
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import lite_lib  # noqa: E402


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--python", dest="python_exe", default=lite_lib.default_python(),
                        help="interpreter used for {python} placeholders")
    parser.add_argument("--timeout", type=int, default=600, help="per-command timeout in seconds")
    parser.add_argument("--label", default="", help="stable report name under calibration/")
    parser.add_argument("--only", default="", help="calibrate only tasks whose id contains this text")
    parser.add_argument("--json", action="store_true", help="print the report as JSON only")
    args = parser.parse_args()

    tasks = [task for task in lite_lib.iter_tasks() if args.only in task["id"]]
    if not tasks:
        print("no tasks matched", file=sys.stderr)
        return 2

    stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    report_dir = lite_lib.RUN_ROOT / "calibration"
    report_dir.mkdir(parents=True, exist_ok=True)

    results = []
    for task in tasks:
        scratch = lite_lib.scratch_directory()
        try:
            result = lite_lib.calibrate_task(task, scratch / "work", args.python_exe, args.timeout)
        finally:
            shutil.rmtree(scratch, ignore_errors=True)
        results.append(result)
        mark = "ok  " if result["calibrated"] else "FAIL"
        print(f"[{mark}] {result['id']} ({result['language']}/{result['kind']})")
        if not result["calibrated"]:
            for name, ok in result["checks"].items():
                if not ok:
                    print(f"         {name} not satisfied")

    calibrated = sum(1 for item in results if item["calibrated"])
    report = {
        "kind": "lite_calibration_not_a_capability_score",
        "timestamp_utc": stamp,
        "python": args.python_exe,
        "timeout_sec": args.timeout,
        "total": len(results),
        "calibrated": calibrated,
        "failed": [item["id"] for item in results if not item["calibrated"]],
        "tasks": results,
    }
    name = args.label or stamp
    (report_dir / f"{name}.json").write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding="utf-8")
    if args.json:
        print(json.dumps(report, ensure_ascii=False, indent=2))
    else:
        print(f"\ncalibrated {calibrated}/{len(results)}; report: {report_dir / (name + '.json')}")
    return 0 if calibrated == len(results) else 1


if __name__ == "__main__":
    raise SystemExit(main())
