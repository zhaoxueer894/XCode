"""Uniform post-run diagnostic: replay identical patches with protected upstream tests.

This does not replace the frozen first-run score and makes no model calls.
Positive/negative controls are rerun for every calibrated task before reporting.
"""
from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
from pathlib import Path
import shutil
import subprocess

from evals.run_pilot import ROOT, OUTPUT, trials, calibration_ready


def main() -> None:
    destination=OUTPUT/"regrade-v2"
    if destination.exists(): raise RuntimeError("diagnostic already exists; do not overwrite evidence")
    gold,empty,agent=[trials(x) for x in ["gold-v5","empty-v4","xcode-v1"]]
    manifest=json.loads((ROOT/"evals/pilot-five.json").read_text(encoding="utf-8"))
    tasks=[task for task in manifest["tasks"] if calibration_ready(gold.get(task["id"]),empty.get(task["id"]))]
    if len(agent)!=len(tasks): raise RuntimeError("wait for every calibrated agent trial")
    destination.mkdir()
    empty_patch=destination/"empty.patch";empty_patch.write_bytes(b"")
    plan={"kind":"post_run_uniform_diagnostic_not_original_frozen_score",
          "policy":"protected-upstream","no_new_model_calls":True,
          "policy_changed_after_observing_first_run":True,"tasks":[]}
    jobs=[]
    for task in tasks:
        key=task["id"]
        bundle=destination/"task-bundles"/key
        shutil.copytree(OUTPUT/"tasks"/key/"tests",bundle)
        shutil.copyfile(ROOT/"evals/pilot_verifier.py",bundle/"verify.py")
        if agent[key]["task_checksum"]!=gold[key]["task_checksum"]:
            raise RuntimeError("agent task differs from its calibration")
        item={"id":key,"image":task["image_digest"],"original_task_checksum":gold[key]["task_checksum"],
              "verifier_sha256":hashlib.sha256((bundle/"verify.py").read_bytes()).hexdigest(),"patches":{}}
        for variant,patch in [("gold",ROOT/gold[key]["trial_dir"]/"artifacts/model.patch"),
                              ("empty",empty_patch),
                              ("candidate",ROOT/agent[key]["trial_dir"]/"artifacts/model.patch")]:
            item["patches"][variant]={"path":str(patch.relative_to(ROOT)),"sha256":hashlib.sha256(patch.read_bytes()).hexdigest()}
            jobs.append((task,bundle,variant,patch))
        plan["tasks"].append(item)
    (destination/"plan.json").write_text(json.dumps(plan,indent=2),encoding="utf-8")
    network="xcode-pilot-regrade-v2"
    subprocess.run(["docker","network","create","--internal",network],check=True,stdout=subprocess.DEVNULL)
    try:
        def execute(job):
            task,bundle,variant,patch=job
            logs=destination/task["id"]/variant;logs.mkdir(parents=True)
            name="xcode-pilot-v2-"+task["id"].replace("_","-")+"-"+variant
            args=["docker","run","--rm","--name",name,"--network",network,"--cpus","2","--memory","4g",
                  "-e","GOMAXPROCS=2","-e","GOFLAGS=-p=2",
                  "-v",str(bundle)+":/tests:ro","-v",str(patch)+":/candidate.patch:ro",
                  "-v",str(logs)+":/logs/verifier",task["image_digest"],
                  "/usr/bin/python3","/tests/verify.py","--patch","/candidate.patch","--test-policy","protected-upstream"]
            with (logs/"runner.txt").open("w",encoding="utf-8") as stream:
                try:
                    result=subprocess.run(args,stdout=stream,stderr=subprocess.STDOUT,timeout=1250)
                    status={"process_exit_code":result.returncode}
                except subprocess.TimeoutExpired:
                    subprocess.run(["docker","rm","-f",name],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
                    status={"runner_error":"timeout"}
            (logs/"runner.json").write_text(json.dumps(status),encoding="utf-8")
            grade=logs/"grade.json"
            outcome=json.loads(grade.read_text(encoding="utf-8")) if grade.is_file() else {"status":"error"}
            print(task["id"]+" "+variant+": "+outcome["status"],flush=True)
            return task["id"],variant,outcome
        with ThreadPoolExecutor(max_workers=2) as pool:
            outcomes=list(pool.map(execute,jobs))
    finally:
        subprocess.run(["docker","network","rm",network],check=True,stdout=subprocess.DEVNULL)
    groups={task["id"]:{} for task in tasks}
    for key,variant,outcome in outcomes: groups[key][variant]=outcome
    for group in groups.values():
        group["calibrated"]=group["gold"]["status"]=="passed" and group["empty"]["status"]=="failed"
        group["resolved"]=group["calibrated"] and group["candidate"]["status"]=="passed"
    report={"plan":plan,"resolved":sum(x["resolved"] for x in groups.values()),"tasks":groups}
    (destination/"report.json").write_text(json.dumps(report,indent=2),encoding="utf-8")
    print(json.dumps({"resolved":report["resolved"],"tasks":len(groups),"new_model_calls":0}),flush=True)


if __name__=="__main__":
    main()
