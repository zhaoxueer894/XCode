"""Materialise lite task families into evals/lite/tasks/<id>/.

Running this again rewrites the starter/solution/hidden trees from the family
definitions. It refuses to overwrite a task directory that is already reserved
by a manifest, so a generated set cannot silently drift from a frozen one.
Use --force only while authoring, before a task is frozen.
"""

from __future__ import annotations

import argparse
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import families_go  # noqa: E402
import families_python  # noqa: E402

LITE_DIR = Path(__file__).resolve().parents[1]
TASKS_DIR = LITE_DIR / "tasks"

# Directories the materialiser owns inside a task.
OWNED = ("starter", "solution", "hidden")

# A nested module boundary keeps the task trees (whose starter/ and hidden/ each
# hold partial Go packages) out of the repository's own `go build ./...`, where
# the hidden overlays would otherwise look like broken packages.
MODULE_MARKER = "module lite/tasks\n\ngo 1.21\n"


def write_tree(root: Path, files: dict[str, str]) -> None:
    for relative, content in files.items():
        path = root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        # Write LF endings explicitly: the task must be byte-identical on any host.
        path.write_bytes(content.encode("utf-8"))


def materialise(task: dict, force: bool) -> str:
    directory = TASKS_DIR / task["id"]
    if directory.exists() and not force:
        return f"skip {task['id']} (exists; pass --force while authoring)"
    for name in OWNED:
        target = directory / name
        if target.exists():
            for child in sorted(target.rglob("*"), reverse=True):
                child.rmdir() if child.is_dir() else child.unlink()
            target.rmdir()
        write_tree(target, task[name])
    (directory / "task.json").write_text(_task_json(task), encoding="utf-8")
    return f"wrote {task['id']} ({task['language']}/{task['kind']})"


def _task_json(task: dict) -> str:
    import json

    meta = {
        "schema_version": 1,
        "id": task["id"],
        "language": task["language"],
        "kind": task["kind"],
        "prompt": task["prompt"],
        "acceptance": task["acceptance"],
        "tags": task["tags"],
        "verify_commands": task["verify_commands"],
        "hidden_command": task["hidden_command"],
    }
    return json.dumps(meta, ensure_ascii=False, indent=2) + "\n"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--force", action="store_true", help="overwrite existing task directories")
    parser.add_argument("--only", default="", help="generate only tasks whose id contains this text")
    args = parser.parse_args()

    produced = families_python.tasks() + families_go.tasks()
    seen = set()
    for task in produced:
        if task["id"] in seen:
            raise SystemExit(f"duplicate task id {task['id']}")
        seen.add(task["id"])
    TASKS_DIR.mkdir(parents=True, exist_ok=True)
    (TASKS_DIR / "go.mod").write_text(MODULE_MARKER, encoding="utf-8")
    for task in produced:
        if args.only and args.only not in task["id"]:
            continue
        print(materialise(task, args.force))
    print(f"total definitions: {len(produced)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
