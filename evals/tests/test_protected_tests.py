from pathlib import Path
import subprocess
import tempfile
import unittest

from evals.pilot_verifier import apply_production_patch


class ProtectedTests(unittest.TestCase):
    def test_verifier_keeps_upstream_tests_and_applies_real_code_and_binary_changes(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory)
            def git(*args):
                return subprocess.check_output(["git",*args],cwd=root)
            git("init","-q");git("config","user.name","Test");git("config","user.email","test@example.invalid")
            (root/"tests").mkdir()
            (root/"tests/test_original.py").write_text("assert False\n",encoding="utf-8")
            (root/"app.py").write_text("result = 0\n",encoding="utf-8")
            (root/"image.bin").write_bytes(b"\x00old\xff")
            git("add",".");git("commit","-qm","base")
            (root/"tests/test_original.py").write_text("assert True\n",encoding="utf-8")
            (root/"tests/test_extra.py").write_text("assert True\n",encoding="utf-8")
            (root/"app.py").write_text("result = 42\n",encoding="utf-8")
            (root/"image.bin").write_bytes(b"\x00new\xfe")
            git("add",".")
            patch=root/"candidate.patch"
            patch.write_bytes(git("diff","--cached","--binary","HEAD"))
            git("reset","--hard","HEAD")
            result=apply_production_patch(root,patch)
            self.assertEqual(result.returncode,0,result.stdout)
            self.assertEqual((root/"tests/test_original.py").read_text(),"assert False\n")
            self.assertFalse((root/"tests/test_extra.py").exists())
            self.assertEqual((root/"app.py").read_text(),"result = 42\n")
            self.assertEqual((root/"image.bin").read_bytes(),b"\x00new\xfe")


if __name__=="__main__":
    unittest.main()
