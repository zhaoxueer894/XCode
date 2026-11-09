"""Run only inside the independent verifier; reuse pinned upstream graders."""
from __future__ import annotations

import argparse
from dataclasses import fields
import fnmatch
import importlib
import json
import os
from pathlib import Path
import subprocess
import sys
import time
import types


def namespace(name: str, path: str) -> None:
    """Load grading modules without importing unrelated orchestration backends."""
    module = types.ModuleType(name)
    module.__path__ = [path]
    sys.modules[name] = module


def load_native(tests: Path) -> None:
    archive = str(tests / "native.zip")
    sys.path.insert(0, archive)
    namespace("swebench.harness", archive + "/swebench/harness")
    namespace("multi_swe_bench.harness", archive + "/multi_swe_bench/harness")
    namespace("multi_swe_bench.harness.repos", archive + "/multi_swe_bench/harness/repos")
    for suffix in ["golang", "golang.cli", "golang.grpc", "golang.zeromicro",
                   "javascript", "javascript.axios", "javascript.expressjs", "javascript.iamkun"]:
        namespace("multi_swe_bench.harness.repos." + suffix, archive + "/multi_swe_bench/harness/repos/" + suffix.replace(".", "/"))


def grade_multi(row: dict, log: str) -> dict:
    from multi_swe_bench.harness.pull_request import PullRequest, Base, ResolvedIssue
    from multi_swe_bench.harness.image import Config
    from multi_swe_bench.harness.report import generate_report
    from multi_swe_bench.harness.test_result import TestResult

    modules = {
        "grpc": ("golang.grpc.grpc_go", "GrpcGo"),
        "zeromicro": ("golang.zeromicro.go_zero", "GoZero"),
        "cli": ("golang.cli.cli", "Cli"),
        "expressjs": ("javascript.expressjs.express", "Express"),
        "axios": ("javascript.axios.axios", "Axios"),
        "iamkun": ("javascript.iamkun.dayjs", "Dayjs"),
    }
    path, name = modules[row["org"]]
    cls = getattr(importlib.import_module("multi_swe_bench.harness.repos." + path), name)
    data = {field.name: row[field.name] for field in fields(PullRequest) if field.name in row}
    data["base"] = Base(**data["base"])
    data["resolved_issues"] = [ResolvedIssue(**x) for x in data["resolved_issues"]]
    instance = cls(PullRequest(**data), Config(need_clone=False, global_env=None, clear_env=True))
    parsed = instance.parse_log(log)
    if parsed.all_count == 0:
        raise RuntimeError("official parser found no test results")
    baseline = TestResult.schema().load(row["run_result"])
    failing = TestResult.schema().load(row["test_patch_result"])
    # Same report validity and membership checks as upstream gen_eval_reports.
    report = generate_report(instance, baseline, failing, parsed)
    missing = {}
    for group in ["p2p_tests", "f2p_tests", "s2p_tests", "n2p_tests"]:
        missing[group] = sorted(set(row[group]) - set(getattr(report, group)))
    return {"resolved": bool(report.valid) and not any(missing.values()), "valid": report.valid,
            "reason": report.error_msg, "missing": missing,
            "parsed": {"passed": parsed.passed_count, "failed": parsed.failed_count, "skipped": parsed.skipped_count}}


def grade_python(row: dict, task: dict, log_path: Path, patch: str) -> dict:
    from swebench.types import TestSpec
    from swebench.harness.grading import get_eval_report
    def names(value):
        return json.loads(value) if isinstance(value, str) else value
    spec = TestSpec(instance_id=task["id"], image=task["image"], eval_script_list=[],
                    repo=task["repository"], version=row["version"],
                    FAIL_TO_PASS=names(row["FAIL_TO_PASS"]), PASS_TO_PASS=names(row["PASS_TO_PASS"]),
                    # The pinned dataset row carries each repo's official parser
                    # (flask/requests/sympy/django differ); never guess from the id.
                    log_parser=row["log_parser"], eval_type="pass_and_fail")
    result = get_eval_report(spec, {"instance_id": task["id"], "model_patch": patch}, str(log_path), True)[task["id"]]
    if not result["patch_successfully_applied"]:
        raise RuntimeError("official grader could not find usable test output")
    return result


PROTECTED_PATTERNS = ["test/*", "tests/*", "*/test/*", "*/tests/*", "*_test.go",
                      "*.test.js", "*.spec.js", "*.test.ts", "*.spec.ts", "test_*.py", "*/test_*.py",
                      "conftest.py", "*/conftest.py"]


def apply_production_patch(workspace: Path, patch: Path) -> subprocess.CompletedProcess:
    """Candidate tests stay in the artifact; only upstream tests affect the grade."""
    result = subprocess.run(["git", "apply", "--whitespace=nowarn",
                           *("--exclude=" + pattern for pattern in PROTECTED_PATTERNS), str(patch)],
                          cwd=workspace, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    if result.returncode == 0:
        # Also protect the source of a rename into a non-test directory.
        tracked = subprocess.check_output(["git", "ls-files", "-z"], cwd=workspace).decode("utf-8").split("\0")
        protected = [name for name in tracked if any(fnmatch.fnmatchcase(name, pattern) for pattern in PROTECTED_PATTERNS)]
        if protected:
            subprocess.run(["git", "restore", "--source=HEAD", "--worktree", "--", *protected], cwd=workspace, check=True)
    return result


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--tests", type=Path, default=Path(__file__).resolve().parent)
    # Harbor restores declared artifacts to their original source path.
    parser.add_argument("--patch", type=Path, default=Path("/logs/agent/model.patch"))
    parser.add_argument("--logs", type=Path, default=Path("/logs/verifier"))
    parser.add_argument("--test-policy", choices=["reject-existing", "protected-upstream"], default="reject-existing")
    args = parser.parse_args()
    args.logs.mkdir(parents=True, exist_ok=True)
    started = time.time()
    outcome = {"status": "error", "resolved": False}
    try:
        load_native(args.tests)
        task = json.loads((args.tests / "task.json").read_text())
        row = json.loads((args.tests / "record.json").read_text())
        if not args.patch.is_file():
            raise RuntimeError("candidate patch artifact is missing")
        patch = args.patch.read_text(encoding="utf-8")
        workspace = Path(task["workspace"])
        # Original image/history is accessible only to this verifier, never agent.
        subprocess.run(["git", "config", "--global", "--add", "safe.directory", str(workspace)], check=True)
        subprocess.run(["git", "reset", "--hard", task["base_commit"]], cwd=workspace, check=True, stdout=subprocess.DEVNULL)
        subprocess.run(["git", "clean", "-fd"], cwd=workspace, check=True, stdout=subprocess.DEVNULL)
        if patch.strip() and args.test_policy == "reject-existing":
            from unidiff import PatchSet
            for item in PatchSet(patch):
                name = item.path
                is_test = (name.startswith(("test/", "tests/")) or name.endswith(("_test.go", ".test.js", ".spec.js", ".test.ts", ".spec.ts")))
                if is_test and not item.is_added_file:
                    outcome = {"status": "failed", "resolved": False, "reason": "candidate changes an existing protected test: " + name}
                    break
            else:
                outcome = {}
        else:
            outcome = {}
        if not outcome and args.test_policy == "protected-upstream" and patch.strip():
            applied = apply_production_patch(workspace, args.patch)
            (args.logs / "candidate-apply.txt").write_bytes(applied.stdout)
            if applied.returncode:
                outcome = {"status":"failed", "resolved":False, "reason":"production patch does not apply"}
            else:
                # Serialize the filtered tree with Git, preserving binary data.
                # This index belongs only to the disposable verifier.
                subprocess.run(["git", "add", "-A"], cwd=workspace, check=True)
                production = subprocess.check_output(["git", "diff", "--cached", "--binary", "--full-index", task["base_commit"]], cwd=workspace)
                filtered = args.logs / "production.patch"
                filtered.write_bytes(production)
                args.patch = filtered
                patch = production.decode("utf-8")
                subprocess.run(["git", "reset", "--hard", task["base_commit"]], cwd=workspace, check=True, stdout=subprocess.DEVNULL)
                subprocess.run(["git", "clean", "-fd"], cwd=workspace, check=True, stdout=subprocess.DEVNULL)
        if not outcome:
            log_path = args.logs / "test-output.txt"
            with log_path.open("w", encoding="utf-8") as output:
                if task["language"] == "python":
                    if patch.strip():
                        applied = subprocess.run(["git", "apply", "--whitespace=nowarn", str(args.patch)], cwd=workspace, stdout=output, stderr=subprocess.STDOUT)
                        if applied.returncode:
                            outcome = {"status":"failed", "resolved":False, "reason":"candidate patch does not apply"}
                    command = ["bash", str(args.tests / "upstream-eval.sh")]
                else:
                    Path("/home/fix.patch").write_text(patch, encoding="utf-8")
                    # Empty patch must actually run the baseline tests, not fail git apply.
                    command = ["bash", "/home/fix-run.sh" if patch.strip() else "/home/test-run.sh"]
                if not outcome:
                    result = subprocess.run(command, cwd=workspace, stdout=output, stderr=subprocess.STDOUT, timeout=1100)
            if not outcome:
                if task["language"] == "python":
                    grade = grade_python(row, task, log_path, patch)
                else:
                    grade = grade_multi(row, log_path.read_text(errors="replace"))
                outcome = {"status": "passed" if grade["resolved"] else "failed", "resolved": grade["resolved"],
                           "test_command_exit_code": result.returncode, "native_grade": grade}
        (args.logs / "reward.txt").write_text("1" if outcome["resolved"] else "0")
    except Exception as exc:
        outcome = {"status":"error", "resolved":False, "error":f"{type(exc).__name__}: {exc}"}
        # Leave reward absent on infrastructure/parser failure: Harbor must report an error.
    outcome["wall_time_seconds"] = round(time.time() - started, 3)
    outcome["test_policy"] = args.test_policy
    (args.logs / "grade.json").write_text(json.dumps(outcome, ensure_ascii=False, indent=2), encoding="utf-8")
    print(json.dumps(outcome, ensure_ascii=False))
    if outcome["status"] == "error":
        raise SystemExit(2)


if __name__ == "__main__":
    main()
