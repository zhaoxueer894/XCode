"""Mechanically select the unseen-task audit pool from pinned cached datasets.

The rule is fixed before any calibration or model run and only reads the
already-pinned dataset files, so it cannot see results or difficulty:

1. Repos in scope are the four pilot repos whose environment qualified.
   grpc/grpc-go stays excluded: its frozen environment audit recorded an
   expired test certificate and a missing host hardware file.
2. Per repo, only tasks with a PR number strictly greater than the highest
   PR already consumed by any earlier run in this project are eligible
   (go-zero 1969, express 3695, flask 5014, requests 6028).
3. A task must carry a non-empty official fail-to-pass set
   (f2p_tests / FAIL_TO_PASS).
4. The pinned datasets cannot fill the pool from the pilot repos alone
   (flask has one Verified instance and it is used; requests tops out at
   the used PR; go-zero and express have few unused F2P tasks). Scope
   therefore extends in this fixed order until the pool reaches 12:
   cli/cli (go, 2), axios (js, 2), dayjs (js, 2), sympy (python, 2),
   django (python, remainder). Extension repos contribute their first
   unused qualifying tasks by ascending PR number from the dataset start.
   The audit order is round-robin over scope order; within a repo the
   ascending PR order applies. The frozen evaluation set is later formed
   by this order among tasks that pass gold/empty calibration, capped at
   TEN.

record_sha256 uses the exact serialization fetch_pilot.py pins, so the
selected rows can be verified byte-for-byte later.

Run inside the Linux Harbor environment (needs pyarrow for python rows).
"""
from __future__ import annotations

import hashlib
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DATASETS = ROOT / ".cache/eval-sources/datasets"
PYTHON_FIELDS = "base_commit created_at difficulty environment_setup_commit eval_type image instance_id log_parser repo version patch test_patch eval_script problem_statement hints_text FAIL_TO_PASS PASS_TO_PASS".split()
FROZEN_SET_CAP = 10

MSB = "ByteDance-Seed/Multi-SWE-bench"
VER = "SWE-bench/SWE-bench_Verified"

# (repo key, dataset_file_or_None, parquet repo, workspace, language, dataset, used_pr_floor, take)
# Round "ten" (2026-11-09 第一轮): pilot 四仓库先用尽,再按固定顺序扩展到 12。
# Round "r2" (2026-11-09 第二轮): 只用已环境合格的四仓库,各取门槛之上前 3 道;
#   go-zero/express 候选已在第一轮用尽,axios/grpc 因环境审计排除,不再进入范围。
ROUNDS = {
    "ten": [
        ("zeromicro__go-zero", "zeromicro__go-zero_dataset.jsonl", "", "/home/go-zero", "go", MSB, 1969, 3),
        ("expressjs__express", "expressjs__express_dataset.jsonl", "", "/home/express", "javascript", MSB, 3695, 3),
        ("cli__cli", "cli__cli_dataset.jsonl", "", "/home/cli", "go", MSB, 0, 2),
        ("axios__axios", "axios__axios_dataset.jsonl", "", "/home/axios", "javascript", MSB, 0, 2),
        ("iamkun__dayjs", "iamkun__dayjs_dataset.jsonl", "", "/home/dayjs", "javascript", MSB, 0, 2),
        ("sympy__sympy", None, "sympy/sympy", "/testbed", "python", VER, 0, 2),
        ("django__django", None, "django/django", "/testbed", "python", VER, 0, 1),
    ],
    "r2": [
        ("cli__cli", "cli__cli_dataset.jsonl", "", "/home/cli", "go", MSB, 302, 3),
        ("iamkun__dayjs", "iamkun__dayjs_dataset.jsonl", "", "/home/dayjs", "javascript", MSB, 337, 3),
        ("sympy__sympy", None, "sympy/sympy", "/testbed", "python", VER, 12096, 3),
        ("django__django", None, "django/django", "/testbed", "python", VER, 7530, 3),
    ],
}
ROUND_DIRS = {"ten": "unseen-ten", "r2": "unseen-r2"}


def pinned_record(row: dict) -> tuple[bytes, str]:
    data = json.dumps(row, indent=2, ensure_ascii=False).replace("\n", "\r\n").encode("utf-8")
    return data, hashlib.sha256(data).hexdigest()


def multi_swe_candidates(dataset_file: str, floor: int, take: int) -> list[dict]:
    rows = []
    for line in (DATASETS / dataset_file).read_text(encoding="utf-8").splitlines():
        if not line.strip():
            continue
        row = json.loads(line)
        f2p = row.get("f2p_tests") or {}
        p2p = row.get("p2p_tests") or {}
        if row["number"] > floor and f2p:
            rows.append((row["number"], row, len(f2p), len(p2p)))
    rows.sort(key=lambda item: item[0])
    return rows[:take]


def python_candidates(floor_repo: str, floor: int, take: int) -> list[dict]:
    import pyarrow.parquet as pq

    table = pq.read_table(DATASETS / "verified-pinned.parquet").to_pylist()
    rows = []
    for row in table:
        if row["repo"] != floor_repo:
            continue
        number = int(row["instance_id"].rsplit("-", 1)[1])
        f2p = row["FAIL_TO_PASS"]
        if isinstance(f2p, str):
            f2p = json.loads(f2p)
        p2p = row["PASS_TO_PASS"]
        if isinstance(p2p, str):
            p2p = json.loads(p2p)
        if number > floor and f2p:
            filtered = {key: row[key] for key in PYTHON_FIELDS}
            for key in ["FAIL_TO_PASS", "PASS_TO_PASS"]:
                if isinstance(filtered[key], str):
                    filtered[key] = json.loads(filtered[key])
            rows.append((number, filtered, len(f2p), len(p2p)))
    rows.sort(key=lambda item: item[0])
    return rows[:take]


def main() -> None:
    import argparse

    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--round", choices=sorted(ROUNDS), required=True)
    args = parser.parse_args()
    scope = ROUNDS[args.round]
    run_dir = ROOT / ".eval" / ROUND_DIRS[args.round]
    output = run_dir / "selection.json"
    if output.exists():
        raise RuntimeError(f"round {args.round} selection already frozen: {output}")
    pool = []
    for repo_key, dataset_file, parquet_repo, workspace, language, dataset, floor, take in scope:
        rows = (multi_swe_candidates(dataset_file, floor, take) if dataset_file
                else python_candidates(parquet_repo, floor, take))
        if not rows:
            raise RuntimeError(f"no unused qualifying candidate for {repo_key}")
        if len(rows) < take:
            raise RuntimeError(f"not enough candidates past floor {floor} for {repo_key}: {len(rows)}/{take}")
        for number, row, f2p_count, p2p_count in rows:
            data, record_sha = pinned_record(row)
            pool.append({
                "id": row["instance_id"],
                "number": number,
                "repo_key": repo_key,
                "language": language,
                "dataset": dataset,
                **({"dataset_file": dataset_file} if dataset_file else {}),
                "workspace": workspace,
                "base_commit": row["base"]["sha"] if language != "python" else row["base_commit"],
                "image": f"mswebench/{repo_key.replace('__', '_m_')}:pr-{number}" if language != "python" else row["image"],
                "record_sha256": record_sha,
                "f2p_count": f2p_count,
                "p2p_count": p2p_count,
            })
            (run_dir / "records").mkdir(parents=True, exist_ok=True)
            (run_dir / "records" / f"{row['instance_id']}.json").write_bytes(data)
    # 冻结顺序：按 scope 顺序轮转取各仓库第 1、2、3 号候选。
    order = {key: index for index, (key, *_) in enumerate(scope)}
    rank_in_repo = {}
    counters = {key: 0 for key in order}
    for task in sorted(pool, key=lambda t: (order[t["repo_key"]], t["number"])):
        counters[task["repo_key"]] += 1
        rank_in_repo[task["id"]] = counters[task["repo_key"]]
    pool.sort(key=lambda t: (rank_in_repo[t["id"]], order[t["repo_key"]]))
    output.write_text(json.dumps({"round": args.round, "cap": FROZEN_SET_CAP, "scope": [
        {"repo": key, "floor": floor, "take": take} for key, _, _, _, _, _, floor, take in scope
    ], "pool": pool}, indent=2), encoding="utf-8")
    print(json.dumps({"round": args.round, "pool_size": len(pool), "ids": [t["id"] for t in pool]}, indent=2))


if __name__ == "__main__":
    main()
