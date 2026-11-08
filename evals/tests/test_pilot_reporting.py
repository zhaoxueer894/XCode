import contextlib
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from evals import run_pilot


class PilotReportingTests(unittest.TestCase):
    def test_calibration_requires_real_positive_and_negative_controls(self):
        good={"grade":{"status":"passed"},"exception":None,"task_checksum":"fixed"}
        negative={"grade":{"status":"failed"},"exception":None,"task_checksum":"fixed"}
        errored={"grade":{"status":"failed"},"exception":{"exception_type":"RuntimeError"},"task_checksum":"fixed"}
        self.assertTrue(run_pilot.calibration_ready(good,negative))
        self.assertFalse(run_pilot.calibration_ready(good,good))
        self.assertFalse(run_pilot.calibration_ready(good,errored))
        self.assertFalse(run_pilot.calibration_ready(good,None))
        self.assertFalse(run_pilot.calibration_ready(good,{**negative,"task_checksum":"changed"}))

    def test_missing_tasks_stay_in_denominator_and_unknown_cost_stays_unknown(self):
        manifest=json.loads((run_pilot.ROOT/"evals/pilot-five.json").read_text())
        name=manifest["tasks"][0]["id"]
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory)
            for job,status in [("gold","passed"),("empty","failed"),("agent","passed")]:
                trial=root/"harbor"/job/(name+"__fixture")
                (trial/"verifier").mkdir(parents=True)
                (trial/"result.json").write_text(json.dumps({"exception_info":None,"task_checksum":"fixed"}))
                (trial/"verifier/grade.json").write_text(json.dumps({"status":status,"resolved":status=="passed"}))
                if job=="agent":
                    (trial/"agent").mkdir()
                    (trial/"agent/result.json").write_text(json.dumps({"answer":"中文运行结果", "model":{"calls":7,"missing_usage":0,"usage":{"total_tokens":12}}},ensure_ascii=False),encoding="utf-8")
            with patch.object(run_pilot,"OUTPUT",root),patch.object(Path,"relative_to",lambda self,*args:self),contextlib.redirect_stdout(io.StringIO()):
                result=run_pilot.report("gold","empty","agent")
            self.assertEqual(result["total_tasks"],5)
            self.assertEqual(result["calibrated_tasks"],1)
            self.assertEqual(result["resolved_tasks"],1)
            self.assertEqual(result["end_to_end_success_rate"],0.2)
            self.assertIsNone(result["cost_usd"])
            self.assertEqual(result["total_model_calls"],7)
            self.assertEqual(result["total_tokens"],12)


if __name__=="__main__":
    unittest.main()
