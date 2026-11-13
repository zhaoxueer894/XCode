"""Build the per-task difficulty table for the frozen evaluation sets.

Two label sources, both pinned and fixed before any model run:

1. Multi-SWE-bench ships human difficulty annotations under
   `docs/manual-verification/annotation-results/*.xlsx` in the harness repo
   revision recorded in each manifest's `harness_revisions`. Labels use the
   same four buckets as SWE-bench Verified: <15 min, 15 min - 1 hour,
   1-4 hours, >4 hours. Go and TS carry one rating per instance; JS carries
   two independent ratings that disagree often, so both are kept verbatim and
   a consensus bucket is derived separately.
2. SWE-bench Verified carries a `difficulty` field on the instance row itself.

Nothing here reads model output: the table is a property of the task set.

Run from the repository root.
"""
from __future__ import annotations

import json
import re
from collections import Counter
from pathlib import Path

import openpyxl

ROOT = Path(__file__).resolve().parents[1]
ANNOTATION_DIR = ROOT / ".cache/eval-sources/multi-swe-bench/docs/manual-verification/annotation-results"
ROUNDS = ["unseen-ten", "unseen-r2", "unseen-r3"]
# instance-id timestamp order is the round order; keep it explicit for the table.
ROUND_LABELS = {"unseen-ten": "round 1", "unseen-r2": "round 2", "unseen-r3": "round 3"}

# Ordered lightest to heaviest; rank is the index used for the consensus bucket.
BUCKETS = ["<15 min", "15 min - 1 hour", "1-4 hours", ">4 hours"]


def classify(cell: str | None) -> str | None:
    """Map one annotation cell to a canonical bucket."""
    if not cell:
        return None
    text = str(cell).strip()
    if text.startswith("<15"):
        return "<15 min"
    if text.startswith("15 分钟") or text.startswith("15分钟") or text.startswith("15 min"):
        return "15 min - 1 hour"
    if text.startswith("1-4"):
        return "1-4 hours"
    if text.startswith("4 小时以上") or text.startswith("4小时以上") or text.startswith(">4"):
        return ">4 hours"
    return None


def load_annotations() -> dict[tuple[str, int], dict]:
    """(repo_slug, pr_number) -> {"ratings": [...], "consensus": ...}."""
    out: dict[tuple[str, int], dict] = {}
    for filename in ("go.xlsx", "js.xlsx", "ts.xlsx"):
        workbook = openpyxl.load_workbook(ANNOTATION_DIR / filename, read_only=True)
        sheet = workbook[workbook.sheetnames[0]]
        rows = list(sheet.iter_rows(values_only=True))
        workbook.close()
        header = rows[0]
        diff_cols = [i for i, h in enumerate(header) if h and "需要多长时间" in str(h)]
        for row in rows[1:]:
            if not row or not row[1]:
                continue
            match = re.match(r"^\d+_(go|js|ts)_(.+)-(\d+)$", str(row[1]).strip())
            if not match:
                continue
            slug, number = match.group(2), int(match.group(3))
            ratings = [classify(row[i]) for i in diff_cols]
            ratings = [r for r in ratings if r]
            if not ratings:
                continue
            ranks = sorted(BUCKETS.index(r) for r in ratings)
            # Median rank keeps a single rating as-is and splits a disagreement in half.
            consensus = BUCKETS[ranks[len(ranks) // 2]]
            out[(slug, number)] = {"ratings": ratings, "consensus": consensus}
    return out


def python_difficulty(instance_id: str) -> str | None:
    """Read the Verifier `difficulty` field from any frozen record copy."""
    for pattern in (
        f".eval/*/records/{instance_id}.json",
        f".eval/*/tasks/{instance_id}/tests/record.json",
        f".eval/*/*/tasks/{instance_id}/tests/record.json",
    ):
        for path in sorted(ROOT.glob(pattern)):
            try:
                record = json.loads(path.read_text(encoding="utf-8"))
            except (json.JSONDecodeError, OSError):
                continue
            if record.get("difficulty"):
                return str(record["difficulty"]).strip()
    return None


def main() -> None:
    annotations = load_annotations()
    rows = []
    for round_name in ROUNDS:
        manifest = json.loads((ROOT / "evals" / f"{round_name}.json").read_text(encoding="utf-8"))
        for task in manifest["tasks"]:
            repo = task["repository"]
            language = task["language"]
            number = int(task["id"].rsplit("-", 1)[1])
            if language == "python":
                raw = python_difficulty(task["id"])
                label = classify(raw)
                source = "verified" if label else "missing"
                consensus = label
                ratings = [label] if label else []
            else:
                # Manifest repositories look like "cli/cli"; annotation slugs use "cli-cli".
                entry = annotations.get((repo.replace("/", "-"), number))
                if entry:
                    source = "msb"
                    ratings = entry["ratings"]
                    consensus = entry["consensus"]
                else:
                    source = "missing"
                    ratings, consensus = [], None
            rows.append({
                "round": ROUND_LABELS[round_name],
                "id": task["id"],
                "language": language,
                "repository": repo,
                "f2p_count": task.get("f2p_count"),
                "p2p_count": task.get("p2p_count"),
                "difficulty_ratings": ratings,
                "difficulty_consensus": consensus,
                "label_source": source,
            })

    payload = {
        "source": "Multi-SWE-bench manual annotation xlsx (go/js/ts) + SWE-bench Verified difficulty field",
        "buckets": BUCKETS,
        "notes": [
            "MSB JS instances carry two independent annotator ratings; both are kept and the consensus is the median bucket.",
            "Labels are a dataset property frozen before any model run, not a post-hoc judgement of model behaviour.",
        ],
        "tasks": rows,
    }
    (ROOT / "evals/task-difficulty.json").write_text(
        json.dumps(payload, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    counts = Counter(r["difficulty_consensus"] for r in rows)
    print(f"tasks={len(rows)} missing={counts[None]}")
    for bucket in BUCKETS:
        print(f"  {bucket:<16} {counts[bucket]}")
    print("json: evals/task-difficulty.json")

    lines = [
        "# Frozen evaluation sets: task difficulty",
        "",
        "Human difficulty labels for the 29 frozen tasks across the three unseen rounds.",
        "Labels come from the dataset, are fixed before any model run, and describe the",
        "reference fix, not the model's behaviour.",
        "",
        "Sources: Multi-SWE-bench `docs/manual-verification/annotation-results/*.xlsx`",
        "(Go / JS / TS, the same four buckets SWE-bench Verified uses) and the SWE-bench",
        "Verified `difficulty` field. JS instances were rated twice by independent",
        "annotators; both ratings are shown and the consensus is the median bucket.",
        "Generated by `evals/build_difficulty_table.py`; regenerate rather than hand-edit.",
        "",
        f"Totals: " + ", ".join(f"{b} = {counts[b]}" for b in BUCKETS) + f", unlabelled = {counts[None]}.",
        "",
        "| Round | Task | Language | F2P | P2P | Ratings (each annotator) | Consensus | Source |",
        "|---|---|---|---:|---:|---|---|---|",
    ]
    for r in rows:
        ratings = " / ".join(r["difficulty_ratings"]) or "-"
        lines.append("| %s | `%s` | %s | %s | %s | %s | %s | %s |" % (
            r["round"], r["id"], r["language"], r["f2p_count"], r["p2p_count"],
            ratings, r["difficulty_consensus"] or "-", r["label_source"]))
    lines.append("")
    (ROOT / "evals/task-difficulty.md").write_text("\n".join(lines), encoding="utf-8")
    print("md:  evals/task-difficulty.md")


if __name__ == "__main__":
    main()
