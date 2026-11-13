"""Core library for the lightweight (lite) evaluation line.

This line is deliberately independent of the Harbor/SWE line: no Docker, no
Harbor, no repository mirrors, no image digests. A task is a handful of small
files whose acceptance tests finish in milliseconds to seconds, so a set of
100-200 tasks stays cheap enough to run on every ablation cell.

Grading discipline shared with the SWE line:

- the agent only ever sees ``starter/``; ``hidden/`` is never copied into the
  workspace the agent can read or write;
- grading always runs on a FRESH copy of ``starter/`` with the candidate patch
  applied and the hidden overlay copied in afterwards, so an agent that edits
  the visible tests cannot influence the verdict;
- a task is usable only after calibration proves the gold overlay passes and
  the empty patch fails;
- results are written once, never deleted, and never rerun over.

A task directory looks like this::

    tasks/<task-id>/
      task.json     metadata, prompts, commands
      starter/      files the agent receives (including visible tests)
      solution/     reference files overlaid for the gold calibration
      hidden/       grading-only tests, copied in at verify time
"""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import tempfile
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
LITE_DIR = ROOT / "evals" / "lite"
TASKS_DIR = LITE_DIR / "tasks"
POLICIES_DIR = LITE_DIR / "policies"
RUN_ROOT = ROOT / ".eval" / "lite"

SCHEMA_VERSION = 1
LANGUAGES = ("python", "go")
KINDS = ("algo", "module", "bugfix")
MAX_RECORDED_OUTPUT = 8000


class TaskError(RuntimeError):
    """A task directory is missing or malformed."""


def default_python() -> str:
    """Return the interpreter name for generated commands on this platform."""
    return "python" if os.name == "nt" else "python3"


def load_task(task_dir: Path) -> dict:
    """Load and validate one task directory; raises TaskError on any defect."""
    task_dir = Path(task_dir)
    meta_path = task_dir / "task.json"
    if not meta_path.is_file():
        raise TaskError(f"{task_dir} has no task.json")
    try:
        meta = json.loads(meta_path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as error:
        raise TaskError(f"{task_dir}/task.json is not valid JSON: {error}") from None
    if meta.get("schema_version") != SCHEMA_VERSION:
        raise TaskError(f"{task_dir}: unsupported schema_version {meta.get('schema_version')!r}")
    for key in ("id", "language", "kind", "prompt", "verify_commands", "hidden_command"):
        if not meta.get(key):
            raise TaskError(f"{task_dir}: task.json requires a non-empty {key!r}")
    if meta["id"] != task_dir.name:
        raise TaskError(f"{task_dir}: id {meta['id']!r} must equal the directory name")
    if meta["language"] not in LANGUAGES:
        raise TaskError(f"{task_dir}: unsupported language {meta['language']!r}")
    if meta["kind"] not in KINDS:
        raise TaskError(f"{task_dir}: unsupported kind {meta['kind']!r}")
    for group in ("verify_commands", "hidden_command"):
        for name, command in commands_of(meta, group):
            if not isinstance(command, list) or not command or not all(isinstance(x, str) for x in command):
                raise TaskError(f"{task_dir}: {group} entry {name!r} must be a non-empty string array")
    for name in ("starter", "solution", "hidden"):
        if not (task_dir / name).is_dir():
            raise TaskError(f"{task_dir}: missing {name}/ directory")
    meta["_dir"] = task_dir
    return meta


def commands_of(meta: dict, field: str) -> list[tuple[str, list]]:
    """Return (name, command) pairs; both command fields accept one flat array."""
    value = meta[field]
    if value and isinstance(value[0], list):
        return [(f"{field}-{i + 1}", command) for i, command in enumerate(value)]
    return [(field, value)]


def iter_tasks(tasks_dir: Path = TASKS_DIR) -> list[dict]:
    """Load every task directory in stable id order."""
    if not tasks_dir.is_dir():
        return []
    return [load_task(path) for path in sorted(p for p in tasks_dir.iterdir() if p.is_dir())]


def expand_command(command: list[str], python_exe: str) -> list[str]:
    """Substitute the ``{python}`` placeholder so the same task runs on any host."""
    return [part.replace("{python}", python_exe) for part in command]


def write_contract(meta: dict, path: Path, python_exe: str, review: bool = False) -> Path:
    """Write the completion-gate contract: goal plus the VISIBLE verification commands.

    The gate deliberately verifies only what the agent can run itself. Hidden
    grading stays entirely outside the agent's loop, so a passing gate is a
    fairness signal, never the score.
    """
    contract = {
        "goal": meta["prompt"],
        "acceptance": list(meta.get("acceptance", [])),
        "verify_commands": [
            {"name": name, "command": expand_command(command, python_exe)}
            for name, command in commands_of(meta, "verify_commands")
        ],
        "review": bool(review),
    }
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(contract, ensure_ascii=False, indent=2), encoding="utf-8")
    return path


def policy_for(meta: dict) -> Path:
    """Return the scoped allowlist shipped for this task's language."""
    return POLICIES_DIR / f"{meta['language']}.json"


# --------------------------------------------------------------------------- #
# git plumbing
# --------------------------------------------------------------------------- #


def git(args: list[str], cwd: Path) -> subprocess.CompletedProcess:
    """Run git with an isolated config so host settings cannot change results."""
    return subprocess.run(
        ["git", "-c", "core.autocrlf=false", "-c", "core.safecrlf=false", "-c", "commit.gpgsign=false", *args],
        cwd=str(cwd), stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False,
    )


def init_repo(directory: Path) -> str:
    """Initialise a repository with one baseline commit; return the commit sha."""
    directory.mkdir(parents=True, exist_ok=True)
    for args in (
        ["init", "-q"],
        ["config", "user.email", "lite@xcode.local"],
        ["config", "user.name", "xcode-lite"],
        ["add", "-A"],
        ["commit", "-q", "-m", "baseline", "--no-verify"],
    ):
        result = git(args, directory)
        if result.returncode:
            raise TaskError(f"git {' '.join(args)} failed in {directory}: {result.stderr.decode(errors='replace')}")
    return git(["rev-parse", "HEAD"], directory).stdout.decode().strip()


def copy_tree(source: Path, destination: Path) -> None:
    """Copy a task directory into a workspace, keeping the layout flat."""
    shutil.copytree(source, destination, dirs_exist_ok=True)


# The agent's own runtime state must never enter the captured patch.
WORKSPACE_GITIGNORE = ".xcode-state/\n.xcode/\n__pycache__/\n*.pyc\n"


def prepare_workspace(meta: dict, destination: Path) -> str:
    """Materialise the agent-visible workspace and record its baseline commit."""
    copy_tree(meta["_dir"] / "starter", destination)
    ignore = destination / ".gitignore"
    if not ignore.exists():
        ignore.write_text(WORKSPACE_GITIGNORE, encoding="utf-8")
    return init_repo(destination)


def capture_patch(workspace: Path, output: Path) -> bytes:
    """Export tracked edits, deletions and new files as a candidate patch.

    The patch is evidence, never a verdict: it is replayed and graded elsewhere.
    """
    add = git(["add", "-A"], workspace)
    if add.returncode:
        raise TaskError(f"git add failed: {add.stderr.decode(errors='replace')}")
    patch = git(["diff", "--cached", "--binary", "--full-index", "HEAD", "--"], workspace)
    git(["reset", "-q"], workspace)
    if patch.returncode:
        raise TaskError(f"git diff failed: {patch.stderr.decode(errors='replace')}")
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_bytes(patch.stdout)
    return patch.stdout


def overlay(source: Path, destination: Path) -> None:
    """Copy an overlay tree over a workspace, letting task files win."""
    if source.is_dir():
        shutil.copytree(source, destination, dirs_exist_ok=True)


def build_overlay_patch(meta: dict, overlay_dir: Path, workdir: Path) -> bytes:
    """Produce the patch that applying ``overlay_dir`` to starter would create."""
    prepare_workspace(meta, workdir)
    overlay(overlay_dir, workdir)
    return capture_patch(workdir, workdir.parent / "overlay.patch")


# --------------------------------------------------------------------------- #
# execution and grading
# --------------------------------------------------------------------------- #


def run_command(command: list[str], cwd: Path, python_exe: str, timeout: int = 600) -> dict:
    """Run one command in a workspace and record its exit code and output."""
    argv = expand_command(command, python_exe)
    environment = dict(os.environ)
    environment["PYTHONDONTWRITEBYTECODE"] = "1"
    started = time.time()
    try:
        result = subprocess.run(
            argv, cwd=str(cwd), env=environment, timeout=timeout,
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, check=False,
        )
        exit_code, output = result.returncode, result.stdout.decode("utf-8", errors="replace")
    except subprocess.TimeoutExpired as error:
        exit_code = -1
        output = (error.stdout or b"").decode("utf-8", errors="replace") + f"\n[timeout after {timeout}s]"
    except OSError as error:
        exit_code = -1
        output = f"[could not start {argv[0]}: {error}]"
    return {
        "command": argv,
        "exit_code": exit_code,
        "duration_ms": int((time.time() - started) * 1000),
        "output": output[-MAX_RECORDED_OUTPUT:],
    }


def grade(task: dict, patch: bytes, workdir: Path, python_exe: str, timeout: int = 600) -> dict:
    """Replay a candidate patch into a clean workspace and run the hidden tests.

    Returns a dict with ``status`` in {passed, failed, error}. Only ``passed``
    means the hidden acceptance tests went green; an empty or unappliable patch
    is a failure, not an infrastructure error.
    """
    if not patch.strip():
        return {"status": "failed", "reason": "empty patch: the agent changed no tracked file", "runs": []}
    patch_file = workdir.parent / "candidate.patch"
    patch_file.write_bytes(patch)
    try:
        prepare_workspace(task, workdir)
    except TaskError as error:
        return {"status": "error", "reason": f"could not prepare grading workspace: {error}", "runs": []}
    applied = git(["apply", "--whitespace=nowarn", "--binary", str(patch_file)], workdir)
    if applied.returncode:
        return {
            "status": "failed",
            "reason": "candidate patch did not apply to the clean starter tree",
            "detail": applied.stderr.decode("utf-8", errors="replace")[-2000:],
            "runs": [],
        }
    overlay(task["_dir"] / "hidden", workdir)
    runs = [run_command(command, workdir, python_exe, timeout) for _, command in commands_of(task, "hidden_command")]
    passed = all(run["exit_code"] == 0 for run in runs)
    return {"status": "passed" if passed else "failed", "reason": "hidden tests", "runs": runs}


def calibrate_task(task: dict, workdir: Path, python_exe: str, timeout: int = 600) -> dict:
    """Prove the task discriminates: gold must pass, empty must fail.

    Both the visible and the hidden suites are checked, because a task whose
    visible tests already pass on the starter gives the agent no signal.

    ``workdir`` is a scratch directory; it is reused between checks and cleaned
    by the caller. Nothing here touches recorded results.
    """
    base = workdir.parent
    gold_patch = build_overlay_patch(task, task["_dir"] / "solution", workdir)

    visible_gold = run_commands_after_patch(task, gold_patch, base / "gold-visible", python_exe, "verify_commands", timeout)
    hidden_gold = grade(task, gold_patch, base / "gold-hidden", python_exe, timeout)
    visible_empty = run_commands_after_patch(task, b"", base / "empty-visible", python_exe, "verify_commands", timeout)
    hidden_empty = grade(task, b"", base / "empty-hidden", python_exe, timeout)

    checks = {
        "gold_visible_passes": visible_gold["status"] == "passed",
        "gold_hidden_passes": hidden_gold["status"] == "passed",
        "empty_visible_fails": visible_empty["status"] == "failed",
        "empty_hidden_fails": hidden_empty["status"] == "failed",
    }
    return {
        "id": task["id"],
        "language": task["language"],
        "kind": task["kind"],
        "calibrated": all(checks.values()),
        "checks": checks,
        "gold_hidden": hidden_gold,
        "empty_hidden": hidden_empty,
        "empty_visible": visible_empty,
    }


def run_commands_after_patch(
    task: dict, patch: bytes, workdir: Path, python_exe: str, field: str, timeout: int = 600
) -> dict:
    """Apply a patch to a clean starter tree and run one of the task's commands."""
    try:
        prepare_workspace(task, workdir)
    except TaskError as error:
        return {"status": "error", "reason": str(error), "runs": []}
    if patch.strip():
        patch_file = workdir.parent / "candidate.patch"
        patch_file.write_bytes(patch)
        applied = git(["apply", "--whitespace=nowarn", "--binary", str(patch_file)], workdir)
        if applied.returncode:
            return {"status": "failed", "reason": "patch did not apply", "runs": []}
    runs = [run_command(command, workdir, python_exe, timeout) for _, command in commands_of(task, field)]
    return {
        "status": "passed" if all(run["exit_code"] == 0 for run in runs) else "failed",
        "reason": field,
        "runs": runs,
    }


def scratch_directory() -> Path:
    """Create a throwaway scratch root; evidence is kept elsewhere."""
    return Path(tempfile.mkdtemp(prefix="xcode-lite-"))
