import asyncio
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest

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
