"""One-off r3 analysis: when do orchestrators delegate, and do sub-agents get gated."""
import json
import glob
import os

for trial in sorted(glob.glob(".eval/unseen-r3/harbor/xcode-v1/*__*/agent/events.jsonl")):
    tid = os.path.basename(os.path.dirname(os.path.dirname(trial))).rsplit("__", 1)[0]
    spawn_steps = []
    sub_notices = 0
    gated_sub_notices = 0
    sub_ids = set()
    for line in open(trial, errors="replace"):
        try:
            ev = json.loads(line)
        except Exception:
            continue
        if ev.get("kind") == "agent.spawned":
            spawn_steps.append(ev.get("step"))
        if ev.get("agent_id"):
            sub_ids.add(ev.get("agent_id"))
            if ev.get("kind") == "budget.status":
                sub_notices += 1
                if "EXPLORATION" in (ev.get("data") or ""):
                    gated_sub_notices += 1
    if spawn_steps:
        print(f"{tid:26s} spawns@{spawn_steps} sub_agents={len(sub_ids)} sub_budget_notices={sub_notices} gated={gated_sub_notices}")
