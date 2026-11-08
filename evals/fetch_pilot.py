"""Fetch only pinned public inputs; never download or read model credentials."""
from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
CACHE = ROOT / ".cache/eval-sources"
OUTPUT = ROOT / ".eval/pilot-five"
PYTHON_FIELDS = "base_commit created_at difficulty environment_setup_commit eval_type image instance_id log_parser repo version patch test_patch eval_script problem_statement hints_text FAIL_TO_PASS PASS_TO_PASS".split()


def fetch(opener, url: str, path: Path) -> None:
    if path.is_file():
        return
    path.parent.mkdir(parents=True, exist_ok=True)
    with opener.open(url, timeout=120) as response:
        data = response.read()
    path.write_bytes(data)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--proxy", help="optional HTTP proxy for public downloads and Git")
    parser.add_argument("--skip-docker", action="store_true", help="fetch data on another OS before pulling images on Linux")
    parser.add_argument("--manifest",type=Path,default=ROOT/"evals/pilot-five.json")
    parser.add_argument("--records-dir",type=Path,default=OUTPUT/"source-records",help="where pinned source records are verified into")
    args = parser.parse_args()
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({"http":args.proxy,"https":args.proxy})) if args.proxy else urllib.request.build_opener()
    manifest = json.loads(args.manifest.read_text(encoding="utf-8"))
    records = args.records_dir.resolve()
    git = ["git"] + (["-c", "http.proxy=" + args.proxy] if args.proxy else [])
    for name, repo, revision in [
        ("multi-swe-bench", "multi-swe-bench/multi-swe-bench", manifest["harness_revisions"]["multi_swe_bench"]),
        ("swe-bench", "SWE-bench/SWE-bench", manifest["harness_revisions"]["swebench"]),
    ]:
        destination = CACHE/name
        if not destination.exists():
            destination.parent.mkdir(parents=True, exist_ok=True)
            subprocess.run(git + ["clone", "--no-checkout", "https://github.com/"+repo+".git", str(destination)], check=True)
            subprocess.run(git + ["-C", str(destination), "checkout", "--detach", revision], check=True)
        actual = subprocess.check_output(git + ["-C", str(destination), "rev-parse", "HEAD"], text=True).strip()
        if actual != revision:
            raise RuntimeError("cached upstream revision differs: " + name)
    python_rows = None
    for task in manifest["tasks"]:
        if task["language"] == "python":
            if python_rows is None:
                import pyarrow.parquet as pq
                path = CACHE/"datasets/verified-pinned.parquet"
                fetch(opener, f"https://huggingface.co/datasets/{task['dataset']}/resolve/{task['dataset_revision']}/data/test-00000-of-00001.parquet", path)
                if hashlib.sha256(path.read_bytes()).hexdigest() != "030cfd7f2a704c4c0226e7f104c725a3b41230b1d3517f9c915ad7ea5be3fa25":
                    raise RuntimeError("pinned Verified parquet checksum mismatch")
                python_rows = {x["instance_id"]:x for x in pq.read_table(path).to_pylist()}
            row = python_rows[task["id"]]
            row = {key:row[key] for key in PYTHON_FIELDS}
            for key in ["FAIL_TO_PASS", "PASS_TO_PASS"]:
                if isinstance(row[key], str): row[key] = json.loads(row[key])
            revision = manifest["harness_revisions"]["swebench_tasks"]
            for filename in ["eval.sh", "task.yaml", "tests.json", "gold.patch", "test.patch", "problem_statement.md"]:
                fetch(opener, f"https://raw.githubusercontent.com/SWE-bench/swe-bench-tasks/{revision}/tasks/{task['id']}/{filename}", CACHE/"swe-tasks"/task["id"]/filename)
        else:
            folder = "go" if task["language"] == "go" else "js"
            path = CACHE/"datasets"/task["dataset_file"]
            fetch(opener, f"https://huggingface.co/datasets/{task['dataset']}/resolve/{task['dataset_revision']}/{folder}/{task['dataset_file']}", path)
            rows = [json.loads(x) for x in path.read_text(encoding="utf-8").splitlines() if x.strip()]
            number = int(task["id"].rsplit("-",1)[1])
            row = next(x for x in rows if x["number"] == number)
        # Preserve the original pilot's Windows JSON serialization on every OS.
        data = json.dumps(row, indent=2, ensure_ascii=False).replace("\n", "\r\n").encode("utf-8")
        if hashlib.sha256(data).hexdigest() != task["record_sha256"]:
            raise RuntimeError("pinned source record checksum mismatch: " + task["id"])
        destination = records/(task["id"]+".json")
        destination.parent.mkdir(parents=True, exist_ok=True)
        if destination.exists() and destination.read_bytes() != data:
            raise RuntimeError("refusing to replace a different source record")
        destination.write_bytes(data)
        if not args.skip_docker:
            subprocess.run(["docker", "pull", task["image_digest"]], check=True)
        print("verified " + task["id"], flush=True)


if __name__ == "__main__":
    main()
