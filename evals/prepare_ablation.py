"""Prepare the A/B/C ablation cells from the frozen round-three set.

Cells (same 10 tasks, same budget, same binary — only the collaboration
topology differs):
  A single          : orchestrator=developer, no gate review
  B single-review   : A + completion gate with a cross-model reviewer
  C pipeline        : forced planner -> developer -> cross-model reviewer

Per cell this writes .eval/ablation/<cell>/ with:
  manifest.json  full frozen config incl. orchestrate + reviewer fields
  tasks/         copies of the calibrated round-three task dirs
  meta.json      git revision, binary sha256, timestamps (filled by driver)

Everything the later analysis and slides need stays under the cell dir:
harbor trials (events.jsonl, result.json, model.patch, verifier grades),
console logs, and the exact manifest that produced them.
"""
from __future__ import annotations

import json
import shutil
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SOURCE_MANIFEST = ROOT / "evals/unseen-r3.json"
SOURCE_TASKS = ROOT / ".eval/unseen-r3/eligible-tasks"

CELLS = {
    "a-single": {"orchestrate": "single", "reviewer_provider": "", "reviewer_model": ""},
    "b-single-review": {"orchestrate": "single-review", "reviewer_provider": "glm", "reviewer_model": "glm-4.6"},
    "c-pipeline": {"orchestrate": "pipeline", "reviewer_provider": "glm", "reviewer_model": "glm-4.6"},
}


def main() -> None:
    manifest = json.loads(SOURCE_MANIFEST.read_text(encoding="utf-8"))
    for cell, overrides in CELLS.items():
        cell_dir = ROOT / ".eval/ablation" / cell
        if cell_dir.exists():
            raise RuntimeError(f"cell already prepared (refusing to overwrite): {cell_dir}")
        cell_manifest = dict(manifest)
        agent = dict(manifest["agent"])
        agent.update(overrides)
        agent["no_delegate"] = False
        agent["ablation_note"] = (
            "A/B/C ablation over the frozen round-three tasks; identical binary, budget "
            "and task order; only the collaboration topology differs. Ablation cells "
            "reuse tasks already seen by round three (delegate-advertised, mostly "
            "unused) — disclosed, same exposure across cells."
        )
        cell_manifest["agent"] = agent
        cell_dir.mkdir(parents=True)
        (cell_dir / "manifest.json").write_text(json.dumps(cell_manifest, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
        shutil.copytree(SOURCE_TASKS, cell_dir / "tasks")
        print(f"prepared {cell}: {len(cell_manifest['tasks'])} tasks, orchestrate={overrides['orchestrate']}")


if __name__ == "__main__":
    main()
