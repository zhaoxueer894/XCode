"""Harbor 0.25 integration: install and execute the actual XCode CLI."""
from __future__ import annotations

import hashlib
import json
import os
import re
from pathlib import Path
import shlex
import tempfile

from harbor.agents.installed.base import BaseInstalledAgent, with_prompt_template
from harbor.environments.base import BaseEnvironment
from harbor.models.agent.context import AgentContext


def as_bool(value: bool | str) -> bool:
    if isinstance(value, bool):
        return value
    if value.lower() not in {"true", "false"}:
        raise ValueError("expected true or false")
    return value.lower() == "true"


class XCodeAgent(BaseInstalledAgent):
    def __init__(
        self, *args, binary_path: str = "bin/xcode-linux-amd64",
        workspace: str = "/app", provider: str = "deepseek", credentials_file: str | None = None,
        no_delegate: bool | str = False, max_model_calls: int = 100,
        max_tokens: int = 0, max_steps: int = 100, timeout_sec: int = 1200,
        context_strategy: str = "bounded", context_budget: int = 6000, **kwargs,
    ):
        self.conda_env = kwargs.pop("conda_env", None)
        super().__init__(*args, **kwargs)
        self.binary = Path(binary_path).resolve(strict=True)
        binary = self.binary.read_bytes()
        if not binary.startswith(b"\x7fELF"):
            raise ValueError("build a Linux binary for Harbor task containers")
        self.binary_hash = hashlib.sha256(binary).hexdigest()
        if workspace != "auto" and (not workspace.startswith("/") or workspace == "/"):
            raise ValueError("workspace must be an absolute repository directory")
        if provider not in {"bailian", "deepseek", "demo"}:
            raise ValueError("provider must be bailian, deepseek or demo")
        self.workspace, self.provider = workspace, provider
        self.credentials_file = Path(credentials_file) if credentials_file else None
        self.no_delegate = as_bool(no_delegate)
        self.max_model_calls, self.max_tokens = int(max_model_calls), int(max_tokens)
        self.max_steps, self.timeout_sec = int(max_steps), int(timeout_sec)
        self.context_strategy, self.context_budget = context_strategy, int(context_budget)
        if min(self.max_model_calls, self.max_steps, self.timeout_sec, self.context_budget) <= 0 or self.max_tokens < 0:
            raise ValueError("positive call, step, time and context limits are required")

    @staticmethod
    def name() -> str:
        return "xcode"

    def version(self) -> str:
        return self.binary_hash

    async def install(self, environment: BaseEnvironment) -> None:
        await self.exec_as_root(environment, "mkdir -p /installed-agent")
        await environment.upload_file(self.binary, "/installed-agent/xcode")
        await environment.upload_file(Path(__file__).with_name("capture_patch.py"), "/installed-agent/capture_patch.py")
        await self.exec_as_root(environment, "chmod 755 /installed-agent/xcode; command -v git; command -v python3")

    @with_prompt_template
    async def run(self, instruction: str, environment: BaseEnvironment, context: AgentContext) -> None:
        # No credentials in shell text, task files or logs. Only the agent process
        # receives them; task/verifier images must never bake credentials in.
        if self.workspace == "auto":
            detected = await environment.exec(command="git rev-parse --show-toplevel")
            root = (detected.stdout or "").strip()
            if detected.return_code or not root.startswith("/") or root == "/" or "\n" in root:
                raise RuntimeError("could not detect the task Git repository")
            self.workspace = root
        conda_env = "testbed" if self.conda_env == "auto" and self.workspace == "/testbed" else self.conda_env
        if conda_env == "auto":
            conda_env = None
        env = {}
        if self.provider != "demo":
            prefix = "DEEPSEEK" if self.provider == "deepseek" else "DASHSCOPE"
            key = self._extra_env.get(f"{prefix}_API_KEY") or os.environ.get(f"{prefix}_API_KEY")
            model = self.model_name
            if self.credentials_file:
                if self.provider != "deepseek":
                    raise ValueError("credentials_file is supported with provider deepseek")
                text = self.credentials_file.read_text(encoding="utf-8-sig")
                keys = re.findall(r"\bsk-[A-Za-z0-9_-]{16,}", text)
                models = re.findall(r'''(?im)["']?(?:model|model_name|DEEPSEEK_MODEL)["']?\s*[:=]\s*["']?([A-Za-z0-9_.:/-]+)''', text)
                if not keys or not models:
                    raise ValueError("credential document requires an API key and explicit model")
                if model and model != models[0]:
                    raise ValueError("--model conflicts with the credential document")
                key, model = keys[0], models[0]
            if not key:
                raise ValueError(f"{prefix}_API_KEY is required; no model calls were made")
            if not model:
                raise ValueError("pass the exact model ID with --model")
            env = {
                f"{prefix}_API_KEY": key,
                f"{prefix}_MODEL": model,
                f"{prefix}_BASE_URL": self._extra_env.get(f"{prefix}_BASE_URL") or os.environ.get(
                    f"{prefix}_BASE_URL", "https://api.deepseek.com" if self.provider == "deepseek" else "https://dashscope.aliyuncs.com/compatible-mode/v1"
                ),
            }
        logs = str(self.environment_logs_dir)
        prepared = await environment.exec(command=f"mkdir -p {shlex.quote(logs)}; git rev-parse HEAD", cwd=self.workspace)
        if prepared.return_code:
            raise RuntimeError("task workspace must be a prepared Git repository")
        base = (prepared.stdout or "").strip()
        with tempfile.TemporaryDirectory(prefix="xcode-prompt-") as directory:
            path = Path(directory) / "task.txt"
            path.write_text(instruction, encoding="utf-8")
            await environment.upload_file(path, f"{logs}/task.txt")
        args = [
            "/installed-agent/xcode", "--isolated", "--non-interactive",
            "--provider", self.provider, "--provider-retries", "0",
            "--thinking", "enabled", "--reasoning-effort", "high", "--max-output-tokens", "8192",
            "--workspace", self.workspace, "--sandbox", "host",
            # This permission is only for the disposable task container. Safety
            # suites must use a separate profile with explicit policy/approvals.
            "--mode", "full-access", "--context-strategy", self.context_strategy,
            "--context-budget", str(self.context_budget),
            "--max-model-calls", str(self.max_model_calls), "--max-tokens", str(self.max_tokens),
            "--max-steps", str(self.max_steps), "--timeout", f"{self.timeout_sec}s",
            "--event-log", f"{logs}/events.jsonl", "--artifact-dir", f"{logs}/artifacts",
            "--result-file", f"{logs}/result.json", "--prompt-file", f"{logs}/task.txt",
        ]
        if self.no_delegate:
            args.append("--no-delegate")
        capture = ["python3", "/installed-agent/capture_patch.py", "--workspace", self.workspace,
                   "--base-commit", base, "--output", f"{logs}/model.patch"]
        # CLI budget/timeout failures still produce a patch and proceed to grading.
        command = (
            (f"set -e\nsource /opt/miniconda3/bin/activate\nconda activate {shlex.quote(conda_env)}\n" if conda_env else "")
            + f"set +e\n{shlex.join(args)} >{shlex.quote(logs + '/stdout.txt')} 2>{shlex.quote(logs + '/stderr.txt')}\n"
            f"status=$?\nprintf '%s\\n' \"$status\" >{shlex.quote(logs + '/exit-code.txt')}\n"
            f"{shlex.join(capture)}\n"
        )
        result = await environment.exec(command=command, cwd=self.workspace, env=env, timeout_sec=self.timeout_sec + 60)
        if result.return_code:
            raise RuntimeError("failed to capture candidate patch; inspect runner logs")
        context.metadata = {"xcode_binary_sha256": self.binary_hash, "base_commit": base,
                            "no_delegate": self.no_delegate, "provider_retries": 0}

    def populate_context_post_run(self, context: AgentContext) -> None:
        path = self.logs_dir / "result.json"
        if not path.is_file():
            context.metadata = {**(context.metadata or {}), "xcode_result_missing": True}
            return
        record = json.loads(path.read_text(encoding="utf-8"))
        model = record["model"]
        if not model["missing_usage"]:
            context.n_input_tokens = model["usage"].get("input_tokens", 0)
            context.n_output_tokens = model["usage"].get("output_tokens", 0)
        # No fabricated dollar cost when provider pricing/cache billing is unknown.
        context.metadata = {**(context.metadata or {}), "xcode": record}


class EmptyPatchAgent(BaseInstalledAgent):
    """Calibration control: run the real verifier on untouched task code."""

    @staticmethod
    def name() -> str:
        return "xcode-empty-patch"

    async def install(self, environment: BaseEnvironment) -> None:
        pass

    async def run(self, instruction: str, environment: BaseEnvironment, context: AgentContext) -> None:
        result = await environment.exec(command=f"mkdir -p {shlex.quote(str(self.environment_logs_dir))}; : > {shlex.quote(str(self.environment_logs_dir / 'model.patch'))}")
        if result.return_code:
            raise RuntimeError("could not write empty calibration patch")
