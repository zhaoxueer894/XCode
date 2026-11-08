"""Prepare the frozen five-task pilot using pinned data and official test scripts.

Run with the Linux Harbor environment from the repository root. All generated
tasks, upstream answers, image locks and logs stay under the ignored .eval tree.
"""
from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import sys
import urllib.request
import zipfile

ROOT = Path(__file__).resolve().parents[1]
OUTPUT = ROOT / ".eval/pilot-five"
SOURCES = ROOT / ".cache/eval-sources"


def sha(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def download(url: str, path: Path) -> bytes:
    if not path.is_file():
        path.parent.mkdir(parents=True, exist_ok=True)
        with urllib.request.urlopen(url, timeout=90) as source:
            path.write_bytes(source.read())
    return path.read_bytes()


def native_archive(path: Path) -> None:
    # All dependencies below are pure Python and work with the system Python
    # already provided by the official images (3.10/3.11). No pip/network at grade time.
    site = Path(sys.prefix) / "lib/python3.13/site-packages"
    packages = ["dataclasses_json", "marshmallow", "unidiff", "typing_inspect.py", "typing_extensions.py", "mypy_extensions.py", "packaging"]
    with zipfile.ZipFile(path, "w", zipfile.ZIP_DEFLATED) as archive:
        for source, name in [(SOURCES/"multi-swe-bench/multi_swe_bench", "multi_swe_bench"), (SOURCES/"swe-bench/swebench", "swebench")]:
            for file in source.rglob("*.py"):
                if "__pycache__" not in file.parts:
                    archive.write(file, name + "/" + str(file.relative_to(source)).replace("\\", "/"))
        for name in packages:
            source = site / name
            if not source.exists():
                raise RuntimeError("missing grader dependency: " + str(source))
            for file in source.rglob("*.py") if source.is_dir() else [source]:
                archive.write(file, str(file.relative_to(site)).replace("\\", "/"))
        for repo in ["multi-swe-bench", "swe-bench"]:
            archive.write(SOURCES/repo/"LICENSE", "licenses/"+repo+".LICENSE")


def instruction(task: dict, row: dict) -> str:
    if task["language"] == "python":
        problem = row["problem_statement"]
    else:
        pieces = [row["title"], row.get("body") or ""]
        for issue in row["resolved_issues"]:
            pieces += ["Issue: " + issue["title"], issue.get("body") or ""]
        if row.get("hints"):
            pieces += ["Benchmark-provided interface requirements:", row["hints"]]
        problem = "\n\n".join(pieces)
    return (
        "Fix the following repository issue. Inspect the code, implement the fix, and run appropriate tests.\n"
        "Do not modify or delete existing tests. You may add new tests. Work only in the repository.\n"
        "External network access is restricted to the model API; dependencies are preinstalled.\n\n" + problem + "\n"
    )


def prepare(test_policy: str = "protected-upstream", manifest_path: Path | None = None) -> None:
    manifest = json.loads((manifest_path or ROOT/"evals/pilot-five.json").read_text(encoding="utf-8"))
    if (OUTPUT/"tasks").exists() and (OUTPUT/"harbor").exists():
        raise RuntimeError("task directory belongs to an existing run; use --output with a fresh directory")
    OUTPUT.mkdir(parents=True, exist_ok=True)
    for name, revision in [("multi-swe-bench",manifest["harness_revisions"]["multi_swe_bench"]), ("swe-bench",manifest["harness_revisions"]["swebench"])]:
        actual = subprocess.check_output(["git", "-C", str(SOURCES/name), "rev-parse", "HEAD"],text=True).strip()
        if actual != revision:
            raise RuntimeError("upstream source revision changed: " + name)
    archive = OUTPUT/"native.zip"
    native_archive(archive)
    locked_images = {}
    for task in manifest["tasks"]:
        name = task["id"]
        source_record = ROOT/".eval/pilot-five/source-records"/(name+".json")
        data = source_record.read_bytes()
        if sha(data) != task["record_sha256"]:
            raise RuntimeError("frozen source record changed: " + name)
        row = json.loads(data)
        digest = task["image_digest"]
        image = json.loads(subprocess.check_output(["docker", "image", "inspect", digest],text=True))[0]
        locked_images[name] = {"tag":task["image"], "digest":digest, "image_id":image["Id"]}
        directory = OUTPUT/"tasks"/name
        environment, tests, solution = [directory/x for x in ["environment", "tests", "solution"]]
        for path in [environment,tests,solution]: path.mkdir(parents=True,exist_ok=True)
        repo = task["workspace"]
        # The answer and hidden tests stay in the original verifier image. The
        # agent gets a new single-commit Git repository with those files removed.
        dockerfile = f'''FROM {digest}
USER root
ENV GOMAXPROCS=2 GOFLAGS=-p=2
WORKDIR {repo}
RUN git config --global --add safe.directory {repo} && git reset --hard {task['base_commit']} && git clean -fd && git ls-files -z > /tmp/pilot-tracked-files && rm -rf {repo}/.git && git init -q && git -c user.name=Pilot -c user.email=pilot@example.invalid add -f --pathspec-from-file=/tmp/pilot-tracked-files --pathspec-file-nul && git -c user.name=Pilot -c user.email=pilot@example.invalid commit -qm 'Evaluation base snapshot' && rm -f /tmp/pilot-tracked-files /home/fix.patch /home/test.patch /home/fix-run.sh /home/test-run.sh /home/prepare.sh /root/.bash_history /eval.sh /patch.diff
RUN /usr/bin/python3 --version && test $(git rev-list --all --count) -eq 1
'''
        (environment/"Dockerfile").write_text(dockerfile,encoding="utf-8")
        (directory/"instruction.md").write_text(instruction(task,row),encoding="utf-8")
        (tests/"task.json").write_text(json.dumps(task,indent=2),encoding="utf-8")
        shutil.copyfile(source_record,tests/"record.json")
        shutil.copyfile(ROOT/"evals/pilot_verifier.py",tests/"verify.py")
        shutil.copyfile(archive,tests/"native.zip")
        if task["language"] == "python":
            revision=manifest["harness_revisions"]["swebench_tasks"]
            source=SOURCES/"swe-tasks"/name
            for filename in ["eval.sh","task.yaml","tests.json","gold.patch","test.patch","problem_statement.md"]:
                download(f"https://raw.githubusercontent.com/SWE-bench/swe-bench-tasks/{revision}/tasks/{name}/{filename}",source/filename)
            # Use the exact official task evaluator, including its test patch.
            shutil.copyfile(source/"eval.sh",tests/"upstream-eval.sh")
        (tests/"test.sh").write_text(f"#!/bin/bash\nset -e\n/usr/bin/python3 /tests/verify.py --test-policy {test_policy}\n",encoding="utf-8")
        # Harbor treats verifier-specific images/builds as having bundled tests.
        # Bake /tests only into the verifier image, never the agent image.
        (tests/"Dockerfile").write_text(f"FROM {digest}\nUSER root\nENV GOMAXPROCS=2 GOFLAGS=-p=2\nCOPY . /tests\nWORKDIR {repo}\n",encoding="utf-8")
        (solution/"gold.patch").write_text(row["patch"] if task["language"]=="python" else row["fix_patch"],encoding="utf-8")
        shutil.copyfile(ROOT/"evals/capture_patch.py",solution/"capture_patch.py")
        (solution/"solve.sh").write_text(f'''#!/bin/bash
set -e
cd {repo}
base=$(git rev-parse HEAD)
git apply --whitespace=nowarn /solution/gold.patch
/usr/bin/python3 /solution/capture_patch.py --workspace {repo} --base-commit "$base" --output /logs/agent/model.patch
''',encoding="utf-8")
        config = f'''schema_version = "1.4"
artifacts = [{{source="/logs/agent/model.patch", destination="model.patch"}}]

[task]
name = "xcode-pilot/{name}"
version = "1.1.0"

[metadata]
source = "{task['dataset']}"
source_revision = "{task['dataset_revision']}"
source_base_commit = "{task['base_commit']}"
purpose = "five-task integration pilot; not a leaderboard score"
grading_policy = "{test_policy}"

[environment]
cpus = 2
memory_mb = 4096
storage_mb = 15360
build_timeout_sec = 600
network_mode = "public"

[agent]
timeout_sec = 1320
network_mode = "allowlist"
allowed_hosts = ["api.deepseek.com"]

[verifier]
environment_mode = "separate"
timeout_sec = 1200
network_mode = "no-network"

[verifier.environment]
cpus = 2
memory_mb = 4096
storage_mb = 15360
network_mode = "no-network"
'''
        (directory/"task.toml").write_text(config,encoding="utf-8")
    lock = OUTPUT/"images.lock.json"
    if lock.exists() and json.loads(lock.read_text()) != locked_images:
        raise RuntimeError("image digests changed; do not overwrite existing pilot evidence")
    lock.write_text(json.dumps(locked_images,indent=2)+"\n",encoding="utf-8")
    (OUTPUT/"manifest.json").write_text(json.dumps(manifest,indent=2)+"\n",encoding="utf-8")
    print(json.dumps({"tasks":len(manifest["tasks"]),"native_archive_sha256":sha(archive.read_bytes()),"output":str(OUTPUT)},indent=2))


if __name__=="__main__":
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output",type=Path,default=OUTPUT)
    parser.add_argument("--manifest",type=Path,default=ROOT/"evals/pilot-five.json")
    parser.add_argument("--test-policy",choices=["reject-existing","protected-upstream"],default="protected-upstream")
    args=parser.parse_args()
    OUTPUT=args.output.resolve()
    prepare(args.test_policy,args.manifest)
