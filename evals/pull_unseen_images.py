"""Pull the frozen audit-pool images and resolve their repo digests.

Selection is already fixed in .eval/unseen-ten/selection.json; pull order
or failures must not change which tasks are in the pool. A failed pull is
recorded with digest null and surfaces later in the environment audit.

Run inside WSL (docker daemon access): python3 evals/pull_unseen_images.py
"""
from __future__ import annotations

import json
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def main() -> None:
    selection = json.loads((ROOT / ".eval/unseen-ten/selection.json").read_text(encoding="utf-8"))
    results = []
    for task in selection["pool"]:
        image = task["image"]
        print(f"pulling {task['id']}: {image}", flush=True)
        digest = None
        try:
            subprocess.run(["docker", "pull", image], check=True)
            repo_digests = subprocess.check_output(
                ["docker", "image", "inspect", image, "--format", "{{json .RepoDigests}}"], text=True)
            entries = json.loads(repo_digests)
            if entries:
                digest = entries[0].split("@", 1)[1]
        except subprocess.CalledProcessError as error:
            print(f"pull failed for {image}: {error}", file=sys.stderr, flush=True)
        results.append({"id": task["id"], "image": image, "digest": digest})
    output = ROOT / ".eval/unseen-ten/digests.json"
    output.write_text(json.dumps(results, indent=2) + "\n", encoding="utf-8")
    ok = sum(1 for item in results if item["digest"])
    print(json.dumps({"pulled": ok, "total": len(results), "output": str(output)}))


if __name__ == "__main__":
    main()
