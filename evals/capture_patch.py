"""Capture tracked edits, deletions and new files without changing Git's index.

Run outside the agent as part of the evaluation runner. Output is a candidate
patch, never a correctness verdict. Regrade it in a clean verifier environment.
"""
import argparse
import os
import re
from pathlib import Path
import subprocess
import tempfile


def capture_patch(workspace, base_commit, output):
    workspace = workspace.resolve(strict=True)
    if not re.fullmatch(r"[0-9a-fA-F]{40}|[0-9a-fA-F]{64}", base_commit):
        raise ValueError("base_commit must be the pinned full commit hash")
    if output.resolve().is_relative_to(workspace):
        raise ValueError("store evaluation artifacts outside the task workspace")
    # A separate index prevents staged user/agent changes from being overwritten.
    with tempfile.TemporaryDirectory(prefix="xcode-patch-") as directory:
        env = dict(os.environ, GIT_INDEX_FILE=str(Path(directory) / "index"), GIT_LITERAL_PATHSPECS="1")

        def git(*args, input=None):
            result = subprocess.run(
                ["git", "-C", str(workspace), *args], env=env,
                input=input,
                stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False,
            )
            if result.returncode:
                raise RuntimeError(result.stderr.decode("utf-8", errors="replace"))
            return result.stdout

        git("read-tree", base_commit)
        # Explicit excluded pathspecs make `git add` error when those paths are
        # already ignored. Enumerate base-tracked and visible new files instead;
        # this also includes deleted tracked paths and treats odd names literally.
        names = git("ls-files", "--cached", "--others", "--exclude-standard", "-z").split(b"\0")
        allowed = sorted({name for name in names if name and name != b".env"
                          and name != b".xcode-state" and not name.startswith(b".xcode-state/")})
        if allowed:
            # -f: allowed is an explicit enumeration of base-tracked and visible
            # new files, so force-adding cannot pull in junk; without -f, tracked
            # paths covered by .gitignore are rejected by git.
            git("add", "-f", "-A", "--pathspec-from-file=-", "--pathspec-file-nul", input=b"\0".join(allowed)+b"\0")
        patch = git("diff", "--cached", "--binary", "--full-index", base_commit, "--")
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_bytes(patch)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--workspace", type=Path, required=True)
    parser.add_argument("--base-commit", required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    capture_patch(args.workspace, args.base_commit, args.output)


if __name__ == "__main__":
    main()
