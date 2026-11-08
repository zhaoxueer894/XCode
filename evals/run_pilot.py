"""Run and report the frozen pilot. No retries or replacement of failed tasks."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
OUTPUT = ROOT / ".eval/pilot-five"
MANIFEST = ROOT / "evals/pilot-five.json"


def trials(job: str) -> dict:
    directory = OUTPUT/"harbor"/job
    found = {}
    for path in directory.glob("*/result.json"):
        key = path.parent.name.rsplit("__",1)[0]
        result = json.loads(path.read_text(encoding="utf-8"))
        grade_path = path.parent/"verifier/grade.json"
        grade = json.loads(grade_path.read_text(encoding="utf-8")) if grade_path.is_file() else None
        record_path = path.parent/"agent/result.json"
        record = json.loads(record_path.read_text(encoding="utf-8")) if record_path.is_file() else None
        found[key] = {"trial_dir":str(path.parent.relative_to(ROOT)), "grade":grade,
                      "task_checksum":result.get("task_checksum"),
                      "exception":result.get("exception_info"), "record":record}
    return found


def calibration_ready(gold: dict | None, empty: dict | None) -> bool:
    return bool(gold and empty and not gold["exception"] and not empty["exception"]
                and gold.get("task_checksum") and gold.get("task_checksum")==empty.get("task_checksum")
                and gold["grade"] and empty["grade"]
                and gold["grade"].get("status")=="passed"
                and empty["grade"].get("status")=="failed")


def report(gold_job: str, empty_job: str, agent_job: str) -> dict:
    manifest=json.loads(MANIFEST.read_text(encoding="utf-8"))
    gold,empty,agent=[trials(x) for x in [gold_job,empty_job,agent_job]]
    audit_path=OUTPUT/"environment-audit.json"
    audit=json.loads(audit_path.read_text(encoding="utf-8")) if audit_path.is_file() else {}
    rows=[]
    for task in manifest["tasks"]:
        key=task["id"];g,e,a=gold.get(key),empty.get(key),agent.get(key)
        ready=calibration_ready(g,e)
        grade=a["grade"] if a else None
        record=a["record"] if a else None
        same_task = bool(a and g and a.get("task_checksum")==g.get("task_checksum"))
        rows.append({"id":key,"language":task["language"],"calibrated":ready,
                     "environment_audit":audit.get(key),
                     "gold":g,"empty":e,"agent":a,
                     "resolved":bool(ready and same_task and not a["exception"] and grade and grade.get("resolved")),
                     "model_calls":record["model"]["calls"] if record else None,
                     "missing_usage":record["model"]["missing_usage"] if record else None,
                     "tokens":record["model"]["usage"].get("total_tokens") if record else None})
    solved=sum(x["resolved"] for x in rows)
    calibrated=sum(x["calibrated"] for x in rows)
    result={"kind":"frozen_repository_integration_pilot_not_leaderboard_score",
            "jobs":{"gold":gold_job,"empty":empty_job,"agent":agent_job},"total_tasks":len(rows),
            "calibrated_tasks":calibrated,"resolved_tasks":solved,
            "end_to_end_success_rate":solved/len(rows),
            "total_model_calls":sum(x["model_calls"] or 0 for x in rows),
            "total_tokens":sum(x["tokens"] or 0 for x in rows),
            "usage_complete_for_attempted_tasks":all(x["agent"]["record"] and x["missing_usage"]==0 for x in rows if x["agent"]),
            "cost_usd":None,"tasks":rows}
    diagnostic_path=OUTPUT/"regrade-v2/report.json"
    if diagnostic_path.is_file():
        diagnostic=json.loads(diagnostic_path.read_text(encoding="utf-8"))
        result["post_run_diagnostic"]={"policy":"protected-upstream","policy_changed_after_first_run":True,
            "new_model_calls":0,"resolved_tasks":diagnostic["resolved"],
            "calibrated_tasks":sum(x["calibrated"] for x in diagnostic["tasks"].values()),
            "grades":{key:{"calibrated":value["calibrated"],"resolved":value["resolved"]} for key,value in diagnostic["tasks"].items()},
            "source":"regrade-v2/report.json","replaces_original_score":False}
    OUTPUT.mkdir(parents=True,exist_ok=True)
    (OUTPUT/"report.json").write_text(json.dumps(result,ensure_ascii=False,indent=2),encoding="utf-8")
    lines=[f"# {len(rows)} 道真实仓库题试跑", "", "这是小规模链路试跑，不是排行榜分数。没有把环境错误或未运行任务从总数中删掉。", "",
           f"校准通过 {calibrated}/{len(rows)}；独立验收通过 {solved}/{len(rows)}；已记录模型请求 {result['total_model_calls']} 次；已记录 token {result['total_tokens']}。费用未核算，不填零。", "",
           "| 题目 | 参考补丁 | 空补丁 | XCode 独立验收 | 请求数 | token |", "|---|---|---|---|---:|---:|"]
    for row in rows:
        def state(trial):
            if not trial:return "未运行"
            if trial["exception"]:return "运行错误"
            return {"passed":"通过","failed":"未通过","error":"评测错误"}.get(trial["grade"]["status"],trial["grade"]["status"]) if trial["grade"] else "缺少评分"
        lines.append(f"| {row['id']} | {state(row['gold'])} | {state(row['empty'])} | {state(row['agent'])} | {row['model_calls'] if row['model_calls'] is not None else '—'} | {row['tokens'] if row['tokens'] is not None else '—'} |")
    lines += ["", "逐题原始评分、失败原因、测试输出、候选 patch 和 CLI 轨迹见 report.json 中的 trial_dir。", "",
              "历史环境启动错误也保留在 harbor/ 下，不作为模型能力失败，也不删除。"]
    if not result["usage_complete_for_attempted_tasks"]:
        lines += ["", "部分模型运行缺少完整用量，以上仅是已记录用量，不能当作完整账单。"]
    for row in rows:
        if not row["calibrated"] and row["environment_audit"]:
            issue=row["environment_audit"]
            lines += ["", f"{row['id']} 未进入模型能力评测：测试证书有效期截止 {issue['not_after_utc']}；本机缺少 {issue['missing_host_file']}。参考补丁未能通过完整验收，未调用模型，也未更换题目。"]
    if "post_run_diagnostic" in result:
        diagnostic=result["post_run_diagnostic"]
        lines += ["", "## 评分规则修正后的统一复核", "",
            "原规则禁止改动已有测试文件，误伤了 Flask 在原文件里新增回归用例的做法。修正后，候选测试修改留作审查材料，验收端只应用生产代码修改，并恢复、运行官方受保护测试。", "",
            f"全部 {diagnostic['calibrated_tasks']} 道合格题都重新跑了参考补丁、空补丁和同一份候选补丁；新增模型请求 0 次，修复通过 {diagnostic['resolved_tasks']} 道。参考补丁全部通过，空补丁全部失败。", "",
            "这是发现规则问题后的统一诊断，不替换上面的冻结首轮成绩，也不当作预先约定的排行榜分数。", "",
            "| 题目 | 恢复官方测试后的候选补丁 |", "|---|---|"]
        for key,grade in diagnostic["grades"].items():
            lines.append(f"| {key} | {'通过' if grade['resolved'] else '未通过'} |")
    (OUTPUT/"report.md").write_text("\n".join(lines)+"\n",encoding="utf-8")
    print(json.dumps({key:result[key] for key in ["total_tasks","calibrated_tasks","resolved_tasks","total_model_calls","total_tokens"]}))
    return result


def run_job(name: str, task_path: Path, agent: str, kwargs: dict, concurrent: int) -> None:
    directory=OUTPUT/"harbor"/name
    if directory.exists():
        raise RuntimeError("job already exists; inspect it instead of silently rerunning: "+name)
    env=dict(os.environ,PYTHONPATH=str(ROOT),DOCKER_CONFIG=str(ROOT/".cache/docker-config"))
    args=[str(Path(sys.executable).parent/"harbor"),"run","-p",str(task_path),"-a",agent,"-n",str(concurrent),
          "--max-retries","0","-o",str(OUTPUT/"harbor"),"--job-name",name]
    for key,value in kwargs.items():
        args += ["--ak",f"{key}={str(value).lower() if isinstance(value,bool) else value}"]
    log=OUTPUT/(name+".console.log")
    print("starting "+name,flush=True)
    with log.open("w",encoding="utf-8") as stream:
        result=subprocess.run(args,cwd=ROOT,env=env,stdout=stream,stderr=subprocess.STDOUT)
    print(f"finished {name}: process_exit={result.returncode}; log={log}",flush=True)
    if result.returncode:
        raise RuntimeError("Harbor runner exited unsuccessfully; inspect "+str(log))


def main() -> None:
    global OUTPUT, MANIFEST
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument("phase",choices=["empty","agent","report"])
    parser.add_argument("--gold-job",default="gold-v5")
    parser.add_argument("--empty-job",default="empty-v4")
    parser.add_argument("--agent-job",default="xcode-v1")
    parser.add_argument("--credentials-file",type=Path)
    parser.add_argument("--run-root",type=Path,default=OUTPUT)
    parser.add_argument("--manifest",type=Path,default=MANIFEST)
    args=parser.parse_args()
    OUTPUT=args.run_root.resolve()
    MANIFEST=args.manifest.resolve()
    if args.phase=="empty":
        run_job(args.empty_job,OUTPUT/"tasks","evals.harbor_agent:EmptyPatchAgent",{},2)
    elif args.phase=="agent":
        if not args.credentials_file or not args.credentials_file.is_file():
            raise ValueError("provide the existing external credential document")
        result=report(args.gold_job,args.empty_job,args.agent_job)
        if any(not task["gold"] or not task["empty"] for task in result["tasks"]):
            raise RuntimeError("calibration jobs have unfinished or missing trials; wait for every frozen task")
        eligible=OUTPUT/"eligible-tasks"
        if eligible.exists():raise RuntimeError("eligible-tasks already exists; do not mutate a frozen run")
        eligible.mkdir()
        for task in result["tasks"]:
            if task["calibrated"]:shutil.copytree(OUTPUT/"tasks"/task["id"],eligible/task["id"])
        if not any(eligible.iterdir()):raise RuntimeError("no tasks passed calibration; no model calls made")
        manifest=json.loads(MANIFEST.read_text(encoding="utf-8"))
        budget=manifest["agent"]
        binary=OUTPUT/"build/xcode-linux-amd64"
        run_job(args.agent_job,eligible,"evals.harbor_agent:XCodeAgent",{
            "binary_path":str(binary),"provider":"deepseek","credentials_file":str(args.credentials_file),
            "workspace":"auto","conda_env":"auto","no_delegate":True,
            "context_strategy":budget.get("context_strategy","none"),"context_budget":budget.get("context_budget",12000),
            "max_model_calls":budget["max_model_calls"],"max_tokens":budget["max_tokens"],
            "max_steps":budget["max_steps"],"timeout_sec":budget["timeout_sec"]},2)
    report(args.gold_job,args.empty_job,args.agent_job)


if __name__=="__main__":
    main()
