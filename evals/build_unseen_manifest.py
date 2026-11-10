"""Assemble the frozen candidate manifest from selection + resolved digests.

Inputs are the two mechanical artifacts of this round:
- .eval/unseen-ten/selection.json  (pool chosen by select_unseen.py)
- .eval/unseen-ten/digests.json    (repo digests from pull_unseen_images.py)

A task whose image could not be pulled keeps digest null: it stays in the
pool and is reported as an infrastructure failure, never silently dropped
or replaced. Harness revisions and the agent budget mirror runtime-two-v2
so the only intended variable of this round is the unseen task set plus
the early-wrap-up runtime.
"""
from __future__ import annotations

import datetime as dt
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]

HARNESS_REVISIONS = {
    "multi_swe_bench": "24f493f8a103e72312ded4f6b9c89f081d69cb09",
    "swebench": "02e7a74ffd0b707aab73d203fe87bdc7c76afc8e",
    "swebench_tasks": "3d07b464b7b311a0cbfb5ed5b2d8a3b96f84a33d",
    "harbor": "0.25.0",
}

AGENT = {
    "model": "deepseek-flash",
    "thinking": "enabled",
    "reasoning_effort": "high",
    "no_delegate": True,
    "max_model_calls": 100,
    "max_tokens": 200000,
    "max_steps": 100,
    "timeout_sec": 1200,
    "max_output_tokens": 8192,
    "provider_retries": 0,
    "context_strategy": "bounded",
    "context_budget": 6000,
    "runtime_budget_guidance": True,
    "runtime_progress_guidance": True,
    "early_wrap_up_and_final_request_tools_off": True,
}

SELECTION_POLICIES = {
    "ten": (
        "Unseen-task round selected mechanically before any calibration or model run. "
        "Pilot repos contribute ALL unused tasks past their consumed PR floors with a "
        "non-empty official FAIL_TO_PASS (go-zero>1969 cap 3, express>3695 cap 3); the "
        "pinned datasets cannot fill 12 from pilot repos alone (flask has one used "
        "instance, requests tops out at the used PR), so scope extends in a fixed order "
        "until the pool reaches 12: cli/cli(2), axios(2), dayjs(2), sympy(2), django(1). "
        "grpc/grpc-go stays excluded by its frozen environment audit (expired test "
        "certificate, missing host hardware file). Audit order is round-robin over scope "
        "order with ascending PR within a repo; the frozen evaluation set is the first "
        "TEN tasks of that order that pass gold/empty calibration, capped at 10, with "
        "no replacement after calibration begins. The grading rule (protected-upstream) "
        "is fixed before the run. This round is the first model exposure to every task "
        "in the pool."
    ),
    "r2": (
        "Second unseen round (validating the exploration gate), selected mechanically "
        "before any calibration or model run. Scope is the four repos that qualified in "
        "the first round: cli, dayjs, sympy, django (go-zero and express are exhausted "
        "in the pinned datasets; axios and grpc stay excluded by their frozen "
        "environment audits). Each contributes its first THREE unused tasks past the "
        "round-one consumed floors (cli>302, dayjs>337, sympy>12096, django>7530) with a "
        "non-empty official FAIL_TO_PASS. Audit order is round-robin [cli, dayjs, sympy, "
        "django]; the frozen set is the first TEN that pass gold/empty calibration, no "
        "replacement after calibration begins. Grading rule protected-upstream is fixed "
        "before the run. First model exposure to every task in the pool."
    ),
    "r3": (
        "Third unseen round: FIRST real-repo exposure of the multi-agent design. "
        "Same mechanical scope as round two (the four qualified repos cli/dayjs/"
        "sympy/django), first THREE unused F2P tasks past the round-two floors "
        "(cli>402, dayjs>569, sympy>12489, django>10554), pool 12, frozen set = "
        "first TEN passing gold/empty calibration, no replacement. Delegate "
        "(planner/developer/reviewer) is ENABLED for the model runs; known "
        "limitation recorded: the exploration gate applies per agent on its own "
        "progress snapshot, not aggregated across sub-agents. Single-agent "
        "baseline for directional comparison is round two on a different frozen "
        "set. Grading rule protected-upstream fixed before the run."
    ),
}


def main() -> None:
    import argparse

    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--round", choices=sorted(SELECTION_POLICIES), required=True)
    args = parser.parse_args()
    round_dirs = {"ten": "unseen-ten", "r2": "unseen-r2", "r3": "unseen-r3"}
    run_dir = ROOT / ".eval" / round_dirs[args.round]
    selection = json.loads((run_dir / "selection.json").read_text(encoding="utf-8"))
    digests = {item["id"]: item for item in json.loads((run_dir / "digests.json").read_text(encoding="utf-8"))}
    tasks = []
    for entry in selection["pool"]:
        pull = digests.get(entry["id"])
        digest = pull["digest"] if pull else None
        org, repo = entry["repo_key"].split("__", 1)
        task = {
            "id": entry["id"],
            "language": entry["language"],
            "dataset": entry["dataset"],
            "dataset_revision": ("56ff018c04a38e27ada1e9d0a6d5839a51f88f0d" if entry["language"] != "python"
                                 else "78f471bf655a3137b2e8a75af1501690ec009ec3"),
            "repository": f"{org}/{repo}",
            "base_commit": entry["base_commit"],
            "workspace": entry["workspace"],
            "record_sha256": entry["record_sha256"],
            "f2p_count": entry["f2p_count"],
            "p2p_count": entry["p2p_count"],
        }
        if "dataset_file" in entry:
            task["dataset_file"] = entry["dataset_file"]
        if digest:
            task["image"] = pull["image"]
            task["image_digest"] = f'{pull["image"].split(":")[0]}@{digest}'
        else:
            task["image"] = entry["image"]
            task["image_digest"] = None
            task["image_unavailable"] = True
        tasks.append(task)
    agent = dict(AGENT)
    if args.round == "r3":
        agent["no_delegate"] = False
        agent["delegate"] = "enabled (planner/developer/reviewer, shared run budget)"
    manifest = {
        "schema_version": 1,
        "execution_environment": {
            "cpus": 2,
            "memory_mb": 4096,
            "go_env": {"GOMAXPROCS": "2", "GOFLAGS": "-p=2"},
            "reason": "Align pre-cgroup-aware Go runtimes and package parallelism to the actual container CPU quota; tests and upstream graders are unchanged.",
        },
        "selection_policy": SELECTION_POLICIES[args.round],
        "harness_revisions": HARNESS_REVISIONS,
        "agent": agent,
        "frozen_at_utc": dt.datetime.now(dt.timezone.utc).isoformat(),
        "tasks": tasks,
    }
    output = ROOT / f"evals/unseen-{args.round}-candidates.json"
    if output.exists():
        raise RuntimeError("candidate manifest already frozen; do not regenerate")
    output.write_text(json.dumps(manifest, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    unavailable = [t["id"] for t in tasks if not t["image_digest"]]
    print(json.dumps({"tasks": len(tasks), "image_unavailable": unavailable, "output": str(output)}, indent=2))


if __name__ == "__main__":
    main()
