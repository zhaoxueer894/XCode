"""Audit gold/empty calibration and freeze the final unseen-task manifest.

Applies the frozen selection rule (first TEN tasks in frozen round-robin
order whose gold passes AND empty fails) to one round's harbor jobs and
writes evals/unseen-<round>.json. Tasks that fail calibration stay
documented in the audit block; nothing is replaced. Also writes the
round's calibration-audit.json. Gold and empty each accept a
comma-separated job list: later jobs are infrastructure fixups (e.g. a
re-run after a capture-tooling bug); the first job holding a trial for a
task wins, and superseded trials stay on disk.
"""
from __future__ import annotations

import datetime as dt
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def trial_status(run: Path, jobs: list[str], task_id: str) -> dict | None:
    for job in jobs:
        for path in (run / "harbor" / job).glob(f"{task_id}__*/result.json"):
            result = json.loads(path.read_text(encoding="utf-8"))
            grade_path = path.parent / "verifier" / "grade.json"
            grade = json.loads(grade_path.read_text(encoding="utf-8")) if grade_path.is_file() else None
            return {"trial_dir": str(path.parent.relative_to(ROOT)), "exception": result.get("exception_info"),
                    "task_checksum": result.get("task_checksum"), "grade": grade}
    return None


def main() -> None:
    import argparse

    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--round", choices=["ten", "r2", "r3"], required=True)
    parser.add_argument("--gold-job", default="gold-v1", help="comma-separated job list; first hit wins")
    parser.add_argument("--empty-job", default="empty-v1", help="comma-separated job list; first hit wins")
    args = parser.parse_args()
    round_dirs = {"ten": ("unseen-ten", ["gold-v3"], ["empty-v1"]),
                  "r2": ("unseen-r2", args.gold_job.split(","), args.empty_job.split(",")),
                  "r3": ("unseen-r3", args.gold_job.split(","), args.empty_job.split(","))}
    run_dir_name, gold_jobs, empty_jobs = round_dirs[args.round]
    RUN = ROOT / ".eval" / run_dir_name
    candidates = json.loads((ROOT / f"evals/unseen-{args.round}-candidates.json").read_text(encoding="utf-8"))
    audit = []
    frozen = []
    for index, task in enumerate(candidates["tasks"]):
        gold = trial_status(RUN, gold_jobs, task["id"])
        empty = trial_status(RUN, empty_jobs, task["id"])
        gold_ok = bool(gold and gold["grade"] and gold["grade"].get("status") == "passed" and not gold["exception"])
        empty_ok = bool(empty and empty["grade"] and empty["grade"].get("status") == "failed" and not empty["exception"])
        checksum_match = bool(gold and empty and gold.get("task_checksum") and gold["task_checksum"] == empty.get("task_checksum"))
        calibrated = gold_ok and empty_ok and checksum_match
        entry = {"id": task["id"], "order": index + 1, "calibrated": calibrated,
                 "gold_passed": gold_ok, "empty_failed": empty_ok, "same_task_checksum": checksum_match,
                 "gold": gold, "empty": empty}
        audit.append(entry)
        if calibrated and len(frozen) < 10:
            frozen.append(task)
    missing_empty = [e["id"] for e in audit if not e["empty"]]
    if missing_empty:
        raise RuntimeError("empty job has unfinished trials: " + ", ".join(missing_empty))
    manifest = dict(candidates)
    manifest["tasks"] = frozen
    manifest["frozen_at_utc"] = dt.datetime.now(dt.timezone.utc).isoformat()
    manifest["calibration"] = {
        "gold_jobs": gold_jobs,
        "empty_jobs": empty_jobs,
        "rule": "first TEN tasks in frozen order whose gold passes and empty fails; no replacement",
        "excluded": [{"id": e["id"], "order": e["order"],
                      "reason": ("gold patch did not pass in the no-network verifier"
                                 if not e["gold_passed"] else "empty patch unexpectedly passed or errored")}
                     for e in audit if not e["calibrated"]],
        "audit_file": f".eval/{run_dir_name}/calibration-audit.json",
    }
    output = ROOT / f"evals/unseen-{args.round}.json"
    if output.exists():
        raise RuntimeError("final manifest already frozen; do not regenerate")
    output.write_text(json.dumps(manifest, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    (RUN / "calibration-audit.json").write_text(json.dumps(audit, indent=2, ensure_ascii=False), encoding="utf-8")
    print(json.dumps({"frozen": [t["id"] for t in frozen], "excluded": [e["id"] for e in audit if not e["calibrated"]]}, indent=2))


if __name__ == "__main__":
    main()
