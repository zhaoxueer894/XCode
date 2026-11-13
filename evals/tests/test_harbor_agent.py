import asyncio
import json
import os
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from harbor.models.agent.context import AgentContext
from evals.harbor_agent import XCodeAgent


class FakeEnvironment:
    def __init__(self):
        self.uploads = {}
        self.commands = []

    async def upload_file(self, source_path, target_path):
        self.uploads[target_path] = Path(source_path).read_bytes()

    async def exec(self, **kwargs):
        self.commands.append(kwargs)
        return SimpleNamespace(return_code=0, stdout="a" * 40 + "\n", stderr="")


class HarborAdapterTests(unittest.TestCase):
    def test_real_harbor_adapter_keeps_credentials_out_of_commands_and_files(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / "xcode"
            binary.write_bytes(b"\x7fELFfixture-not-executable")
            credentials = root / "credentials.txt"
            secret = "sk-not-a-real-key-00000000000000000000"
            credentials.write_text("model: deepseek-flash\napi-key:\n" + secret)
            env = FakeEnvironment()
            agent = XCodeAgent(
                logs_dir=root, binary_path=str(binary), provider="deepseek",
                credentials_file=str(credentials), no_delegate="true", timeout_sec=30,
            )
            context = AgentContext()
            asyncio.run(agent.install(env))
            asyncio.run(agent.run("fix the bug; $(do-not-execute-this)", env, context))
            command = env.commands[-1]["command"]
            self.assertIn("--non-interactive", command)
            self.assertIn("--no-delegate", command)
            self.assertIn("--provider-retries 0", command)
            self.assertIn("capture_patch.py", command)
            self.assertNotIn(secret, command)
            self.assertNotIn("$(do-not-execute-this)", command)
            self.assertEqual(env.commands[-1]["env"]["DEEPSEEK_API_KEY"], secret)
            self.assertFalse(any(secret.encode() in data for data in env.uploads.values()))
            self.assertEqual(env.uploads["/logs/agent/task.txt"], b"fix the bug; $(do-not-execute-this)")

    def test_ablation_uses_only_the_selected_config_slots(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / "xcode"
            binary.write_bytes(b"\x7fELFfixture")
            config = root / "config"
            config.write_text(
                "developer=deepseek\nreviewer=glm\n"
                "model.deepseek.key=developer-fixture\n"
                "model.deepseek.base-url=https://developer.example/v1\n"
                "model.deepseek.id=developer-model\n"
                "model.glm.key=reviewer-fixture\n"
                "model.glm.base-url=https://reviewer.example/v1\n"
                "model.glm.id=reviewer-model\n",
                encoding="utf-8",
            )
            for mode in ("single", "single-review", "pipeline"):
                with self.subTest(mode=mode), patch.dict(os.environ, {"GLM_API_KEY": "ambient"}):
                    review = mode != "single"
                    agent = XCodeAgent(
                        logs_dir=root, binary_path=str(binary), xcode_config=str(config),
                        orchestrate=mode, reviewer_provider="glm" if review else "",
                        reviewer_model="reviewer-model" if review else "",
                        strict_reviewer_max_steps=32 if review else 0,
                        reviewer_max_output_tokens=32768 if review else 0,
                        allocate_stage_budget=review,
                    )
                    environment = FakeEnvironment()
                    asyncio.run(agent.run("Connectivity fixture", environment, AgentContext()))
                    call = environment.commands[-1]
                    expected = {
                        "DEEPSEEK_API_KEY": "developer-fixture",
                        "DEEPSEEK_BASE_URL": "https://developer.example/v1",
                        "DEEPSEEK_MODEL": "developer-model",
                    }
                    if review:
                        expected.update(
                            GLM_API_KEY="reviewer-fixture",
                            GLM_BASE_URL="https://reviewer.example/v1",
                            GLM_MODEL="reviewer-model",
                        )
                    self.assertEqual(call["env"], expected)
                    self.assertEqual("--reviewer-model reviewer-model" in call["command"], review)
                    self.assertEqual("--strict-reviewer-max-steps 32" in call["command"], review)
                    self.assertEqual("--reviewer-max-output-tokens 32768" in call["command"], review)
                    self.assertEqual("--allocate-stage-budget" in call["command"], review)
                    self.assertNotIn("developer-fixture", call["command"])
                    self.assertNotIn("reviewer-fixture", call["command"])
                    self.assertNotIn(config.read_bytes(), environment.uploads.values())

    def test_frozen_runtime_profile_uploads_a_contract_for_the_current_trial(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / "xcode"
            binary.write_bytes(b"\x7fELFfixture")
            manifest = root / "manifest.json"
            command = ["python", "bin/test", "sympy/core/tests/test_basic.py"]
            manifest.write_text(json.dumps({"task_runtime": {
                "sympy__sympy-13091": {"external_node_modules": False, "verify_command": command},
            }}))
            for mode in ("single", "single-review", "pipeline"):
                with self.subTest(mode=mode):
                    agent = XCodeAgent(logs_dir=root, binary_path=str(binary), provider="demo",
                                       orchestrate=mode, functional_repair_once=True,
                                       task_runtime_manifest=str(manifest))
                    agent.session_id = "sympy__sympy-13091__fixture__agent"
                    environment = FakeEnvironment()
                    asyncio.run(agent.run("Fix equality.", environment, AgentContext()))
                    contract = json.loads(environment.uploads["/installed-agent/gate-contract.json"])
                    self.assertEqual(contract["goal"], "Fix equality.")
                    self.assertEqual(contract["verify_commands"][0]["command"], command)
                    self.assertEqual(contract["review"], mode != "single")
                    cli_command = environment.commands[-1]["command"]
                    self.assertIn("--contract /installed-agent/gate-contract.json", cli_command)
                    self.assertIn("--functional-repair-once", cli_command)

    def test_unknown_usage_and_cost_stay_unknown(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / "xcode"
            binary.write_bytes(b"\x7fELFfixture")
            agent = XCodeAgent(logs_dir=root, binary_path=str(binary), provider="demo")
            (root / "result.json").write_text(json.dumps({"model":{"missing_usage":1,"usage":{}}}))
            context = AgentContext()
            agent.populate_context_post_run(context)
            self.assertIsNone(context.cost_usd)
            self.assertIsNone(context.n_input_tokens)
            self.assertIn("xcode", context.metadata)


if __name__ == "__main__":
    unittest.main()
