import os
from pathlib import Path
import subprocess
import tempfile
import unittest

from evals.capture_patch import capture_patch


class CapturePatchTests(unittest.TestCase):
    def test_replays_edits_deletions_and_new_files_without_changing_index(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "repo"
            root.mkdir()
            def git(*args):
                return subprocess.check_output(["git", "-C", str(root), *args])
            git("init", "-q")
            git("config", "user.email", "fixture@example.invalid")
            git("config", "user.name", "Fixture")
            (root / "changed.txt").write_text("old\n")
            (root / "deleted.txt").write_text("delete me\n")
            (root / ".gitignore").write_text(".env\n.xcode-state/\n")
            git("add", ".")
            git("commit", "-qm", "base")
            base = git("rev-parse", "HEAD").decode().strip()
            (root / "changed.txt").write_text("new\n")
            git("add", "changed.txt")
            original_index = (root / ".git/index").read_bytes()
            (root / "deleted.txt").unlink()
            (root / "new.bin").write_bytes(b"\x00\x01\xff")
            (root / "literal[1].txt").write_text("literal filename\n")
            (root / ".env").write_text("secret-must-not-leak")
            (root / ".xcode-state").mkdir()
            (root / ".xcode-state/log.json").write_text("private log")
            patch = Path(directory) / "model.patch"
            capture_patch(root, base, patch)
            self.assertEqual((root / ".git/index").read_bytes(), original_index)
            self.assertNotIn(b"secret-must-not-leak", patch.read_bytes())
            self.assertNotIn(b"private log", patch.read_bytes())
            clean = Path(directory) / "clean"
            subprocess.check_call(["git", "clone", "-q", str(root), str(clean)])
            subprocess.check_call(["git", "-C", str(clean), "apply", str(patch)])
            self.assertEqual((clean / "changed.txt").read_text(), "new\n")
            self.assertFalse((clean / "deleted.txt").exists())
            self.assertEqual((clean / "new.bin").read_bytes(), b"\x00\x01\xff")
            self.assertEqual((clean / "literal[1].txt").read_text(), "literal filename\n")


if __name__ == "__main__":
    unittest.main()
