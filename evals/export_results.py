"""Export the evaluation corpus for publication.

Produces, under the repo:

  evals/EVAL_RESULTS.md    human-readable results (committed)
  evals/eval-results.json  the same numbers, machine-readable (committed)
  output/eval-export/eval-bundle.zip   deduplicated corpus, paths sanitized

Source of truth is each harbor trial's top-level ``result.json``:
``verifier_result.rewards.reward`` is the grader's verdict (1.0 resolved,
0.0 not resolved) and ``exception_info`` marks infrastructure failures. The
per-task ``verifier/grade.json`` exists only for a subset of trials, so it is
used as a cross-check, never as the primary record.

The corpus under .eval/ is ~2.1 GB, nearly all of it repetition: every task
directory carries a byte-identical 5 MB native.zip, and every cell its own
14 MB copy of the built binary. Dropping binaries and collapsing identical
files by content hash cuts the bundle to tens of MB with no distinct byte lost.

Usage:
    python evals/export_results.py                 # summary + bundle
    python evals/export_results.py --summary-only  # skip the zip
    python evals/export_results.py --check ZIP     # verify an existing bundle
"""
from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import os
import re
import zipfile
from collections import defaultdict
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
EVAL_DIR = ROOT / ".eval"
OUT_DIR = ROOT / "output" / "eval-export"

# Built binaries are reproducible from the recorded git revision; bundling
# dozens of copies of the same executable buys nothing.
SKIP_NAMES = {"xcode.exe", "xcode-linux-amd64", "xcode", "xcode.exe.tmp"}
SKIP_DIRS = {"__pycache__", ".git"}
SKIP_EXTS = {".pyc", ".pyo"}

# Content dedup below this size costs more than it saves and loses file names
# (all empty files hash alike), so it applies only to genuinely bulky files.
MIN_DEDUPE_BYTES = 1024 * 1024

# Only the local account segment is rewritten; the surrounding path stays
# readable so logs remain diagnosable. The repo root is collapsed to <repo>
# so the archive reads the same on any machine.
SANITIZE_RULES = [
    (re.compile(r"(Users)([\\/]+)18306\b"), r"\1\2user"),
    (re.compile(r"[A-Za-z]:[\\/]{1,4}Projects[\\/]{1,4}XCode"), "<repo>"),
    (re.compile(r"/mnt/[a-z]/Projects/XCode"), "<repo>"),
]
# Residue that must not survive packaging. Timestamps and hashes contain the
# digit run by coincidence, so only path-shaped matches count.
RESIDUAL_PATTERNS = [
    (re.compile(r"Users[\\/]+18306"), "local account name"),
]

CALIBRATION_PREFIXES = ("empty-", "gold-")

# Scores already stated in the round reports under evals/. Recomputed numbers
# are shown beside them, with the reason for any difference, so a reader can
# reconcile the two instead of finding an unexplained contradiction.
PUBLISHED = [
    ("Round 2 (`evals/UNSEEN_R2_REPORT.md`)", "5/6", "unseen-r2",
     "Same passes, different denominator. The report scores the 6 tasks that "
     "ran to completion and sets the 3 cli tasks aside as provider cut-offs. "
     "Here the denominator is every task the grader actually scored, so those "
     "3 are counted (as failures) and flagged as cut-short. One further task "
     "never reached the grader and is listed as no-verdict."),
    ("Round 3 (`evals/UNSEEN_R3_REPORT.md`)", "3/8", "unseen-r3",
     "Matches. Both sides exclude the 2 tasks that lost their patch and count "
     "the killed sympy-13031 run as a failure."),
    ("Pilot (`evals/PILOT_REPORT.md`)", "1/5 first pass", "pilot-five",
     "The frozen first pass ran 4 of 5 tasks (grpc-go never called the model) "
     "and resolved 1, which is the 1/4 shown above. A later regrade, after the "
     "rule that forbade touching existing test files was corrected, resolved 2 "
     "more from the same patches with no new model calls."),
]

# Human labels. Keys are cells that hold actual agent runs worth naming;
# anything else falls back to its path.
CELL_LABELS = {
    "ablation/staged-a-aligned/a-single":
        "Staged ablation A - single agent",
    "ablation/staged/b-single-review":
        "Staged ablation B - single agent + cross-model review",
    "ablation/staged/c-pipeline":
        "Staged ablation C - planner / developer / reviewer pipeline",
    "ablation/strict-bc/b-single-review":
        "Strict-review B - single agent + cross-model review",
    "ablation/strict-bc/c-pipeline":
        "Strict-review C - pipeline",
    "ablation/a-single":
        "Early A - single agent (partial task set)",
    "ablation/b-single-review":
        "Early B - single agent + review (partial task set)",
    "ablation/a-missing-scores-retry/a-single":
        "Retry for single-agent trials whose verdict was missing",
    "ablation/staged-glm429-retry/c-pipeline":
        "Pipeline retry after the reviewer quota reset",
    "ablation/paired-first-submission/a-single":
        "Paired study - plain submission (no review, no self-check)",
    "ablation/paired-first-review/study/self-check":
        "Paired study - unguided self-check",
    "ablation/paired-first-review/study/review-feedback":
        "Paired study - independent review feedback",
    "ablation/paired-first-review/relaxed-two/self-check":
        "Paired study - unguided self-check (relaxed gate)",
    "ablation/paired-first-review/relaxed-two/review-feedback":
        "Paired study - review feedback (relaxed gate)",
    "ablation/paired-first-review/sympy-observed-pair/self-check":
        "Observed pair on sympy-13031 - unguided self-check",
    "ablation/paired-first-review/sympy-observed-pair/review-feedback":
        "Observed pair on sympy-13031 - review feedback",
    "ablation/paired-review/smoke/self-check":
        "Smoke - unguided self-check",
    "ablation/paired-review/smoke/review-feedback":
        "Smoke - review feedback",
    "pilot-five": "Pilot round - five repositories",
    "runtime-fix": "Runtime regression check (before fix)",
    "runtime-fix-v2": "Runtime regression check (after fix)",
    "unseen-r2": "Round 2 - unseen tasks",
    "unseen-r3": "Round 3 - unseen tasks",
    "unseen-ten": "Ten-task round",
    "stage-budget-frozen-602-20261111T090149Z":
        "Budget-floor probe on dayjs-602",
    "workflow-repair-602-v1": "Workflow repair probe on dayjs-602",
    "workflow-repair-cli405-v1": "Workflow repair probe on cli-405",
    "ablation/staged-pressure/sympy__sympy-13091":
        "Repeated runs on sympy-13091 under pressure",
}


# --------------------------------------------------------------------------
# discovery
# --------------------------------------------------------------------------

def iter_trial_records():
    """Yield one record per harbor trial.

    Only the harbor-level result.json counts; ``agent/result.json`` describes
    the agent process, and is read alongside to tell a genuine attempt from a
    run that never produced anything.
    """
    for res in sorted(EVAL_DIR.rglob("harbor/**/result.json")):
        parts = res.relative_to(EVAL_DIR).parts
        if "harbor" not in parts:
            continue
        i = parts.index("harbor")
        rest = parts[i + 2:]
        if not rest or "__" not in rest[0]:
            continue  # stray result.json directly under the variant
        if len(rest) > 1 and rest[1] == "agent":
            continue
        try:
            with open(res, encoding="utf-8", errors="replace") as fh:
                rec = json.load(fh)
        except Exception:
            continue
        vr = rec.get("verifier_result")
        rewards = vr.get("rewards") if isinstance(vr, dict) else None
        reward = rewards.get("reward") if isinstance(rewards, dict) else None

        trial_dir = res.parent
        agent = {}
        apath = trial_dir / "agent" / "result.json"
        if apath.is_file():
            try:
                with open(apath, encoding="utf-8", errors="replace") as fh:
                    agent = json.load(fh)
            except Exception:
                agent = {}
        yield {
            "cell": "/".join(parts[:i]),
            "variant": parts[i + 1],
            "task": "__".join(rest[0].split("__")[:2]),
            "task_dir": rest[0],
            "reward": reward,
            "exception": bool(rec.get("exception_info")),
            "agent_status": agent.get("status"),
            "agent_stop": agent.get("stop_reason"),
            "patch_chars": patch_size(trial_dir),
            "trial": str(res),
        }


def patch_size(trial_dir):
    """Non-blank length of the diff the agent produced, or None if absent."""
    for rel in ("artifacts/model.patch", "agent/model.patch"):
        p = trial_dir / rel
        if p.is_file():
            try:
                return len(p.read_text(encoding="utf-8", errors="replace").strip())
            except OSError:
                return None
    return None


def is_calibration(variant):
    return variant.startswith(CALIBRATION_PREFIXES)


def collect():
    """(cell, calibration|agent) -> task -> trials.

    Calibration trials must never share a bucket with agent trials: a gold
    patch resolving a task says the harness works, not that the agent did.
    """
    cells = defaultdict(lambda: defaultdict(list))
    for rec in iter_trial_records():
        key = (rec["cell"], rec["variant"]) if is_calibration(rec["variant"]) else (rec["cell"], None)
        cells[key][rec["task"]].append(rec)
    return cells


def task_outcome(trials):
    """Resolve one task from its (possibly retried) trials.

    Two states, and one diagnostic flag that keeps a confound visible:

    graded       the grader returned a verdict. This is reported as-is: a 0.0
                 means the patch, whatever it was, did not pass the hidden
                 tests. The verdict is the grader's, not ours to reinterpret.
    no-verdict   no trial ever reached the grader (the harness raised). Those
                 leave the fraction and are counted separately, because an
                 exception says nothing about the model.

    ``empty_patch`` marks a graded failure whose diff was blank — the run
    ended before the agent edited anything. It stays in the fraction (the
    grader did judge it) but is counted separately so a reader can see how
    much of a deficit is a budget ceiling rather than a wrong fix.
    """
    graded = [t for t in trials if t["reward"] is not None]
    if graded:
        best = max(graded, key=lambda t: t["reward"])
        verdict = "pass" if best["reward"] >= 1.0 else "fail"
        empty = verdict == "fail" and all((t["patch_chars"] or 0) == 0 for t in graded)
        return {"verdict": verdict,
                "attempts": len(trials),
                "recovered": any(t["exception"] or t["reward"] is None for t in trials),
                "cut_short": any(t["agent_status"] in ("error", "timeout", "stopped")
                                 for t in graded),
                "empty_patch": empty}
    return {"verdict": "no-verdict",
            "attempts": len(trials),
            "recovered": False,
            "cut_short": True,
            "empty_patch": False}


def cell_meta(cell):
    rev = binary = None
    meta = EVAL_DIR / cell / "meta.txt"
    if meta.is_file():
        lines = [ln.strip() for ln in meta.read_text(encoding="utf-8", errors="replace").splitlines() if ln.strip()]
        rev = lines[0] if lines else None
        binary = lines[1] if len(lines) > 1 else None
    if binary is None:
        for rj in sorted((EVAL_DIR / cell).rglob("result.json")):
            if "agent" not in rj.parts:
                continue
            try:
                d = json.load(open(rj, encoding="utf-8", errors="replace"))
            except Exception:
                continue
            m = re.search(r"sha256:([0-9a-f]{64})", d.get("binary_version") or "")
            if m:
                binary = m.group(1)
                break
    return rev, binary


def build_runs(cells):
    runs = []
    for (cell, cal_variant) in sorted(cells, key=lambda k: (k[0], k[1] or "")):
        rev, binary = cell_meta(cell)
        tasks = {}
        attempts = no_verdict = recovered = resolved = cut_short = empty = 0
        for tid, trials in sorted(cells[(cell, cal_variant)].items()):
            out = task_outcome(trials)
            tasks[tid] = out
            attempts += out["attempts"]
            if out["verdict"] == "no-verdict":
                no_verdict += 1
            else:
                if out["verdict"] == "pass":
                    resolved += 1
                if out["recovered"]:
                    recovered += 1
                if out["cut_short"]:
                    cut_short += 1
                if out["empty_patch"]:
                    empty += 1
        scored = sum(1 for t in tasks.values() if t["verdict"] != "no-verdict")
        if cal_variant:
            variants = [cal_variant]
        else:
            variants = sorted({t["variant"] for ts in cells[(cell, cal_variant)].values() for t in ts})
        label = CELL_LABELS.get(cell, cell)
        if cal_variant:
            label = f"Verifier calibration on {cell}"
        runs.append({
            "cell": cell,
            "label": label,
            "variants": variants,
            "calibration": bool(cal_variant),
            "tasks": tasks,
            "resolved": resolved,
            "scored": scored,
            "no_verdict": no_verdict,
            "cut_short_scored": cut_short,
            "empty_patch_failures": empty,
            "attempts": attempts,
            "recovered_after_retry": recovered,
            "git_revision": rev,
            "binary_sha256": binary,
        })
    return runs


# --------------------------------------------------------------------------
# markdown
# --------------------------------------------------------------------------

ARMS = [
    ("A", "ablation/staged-a-aligned/a-single", "single agent, no review"),
    ("B", "ablation/staged/b-single-review", "single agent + cross-model review"),
    ("C", "ablation/staged/c-pipeline", "planner / developer / reviewer pipeline"),
]


def fmt_verdict(out):
    if out is None:
        return "—"
    v = out["verdict"]
    if v == "no-verdict":
        return "no verdict"
    return "pass" if v == "pass" else "fail"


def render_markdown(runs, generated, published=None):
    by_cell = {r["cell"]: r for r in runs}
    out = []
    add = out.append
    add("# Evaluation results")
    add("")
    add("Every verdict below is the grader's own, taken from each harbor")
    add("trial's `result.json` (`verifier_result.rewards.reward`). A task counts")
    add("as resolved only when a hidden test that failed before the patch passes")
    add("after it; the agent's claim of completion carries no weight.")
    add("")
    add("`no verdict` means no trial produced a graded result — the harness")
    add("raised, or the run died before any patch existed. Those tasks are")
    add("excluded from the fraction and counted separately rather than scored as")
    add("wrong answers, because a provider error says nothing about the model.")
    add("")
    add(f"Generated {generated} by `python evals/export_results.py`.")
    add("")

    # -- controlled comparison
    add("## The controlled three-arm comparison")
    add("")
    add("These three arms share one task set, one budget and one binary")
    add("(`f8033d16…`); only the collaboration topology differs. Nothing outside")
    add("this table is comparable to it — other rounds used different binaries,")
    add("different task sets, or no completion gate at all.")
    add("")
    arms_present = [a for a in ARMS if a[1] in by_cell]
    if len(arms_present) == len(ARMS):
        task_ids = sorted({t for _, c, _ in ARMS for t in by_cell[c]["tasks"]})
        add("| Task | " + " | ".join(f"{k} — {d}" for k, _, d in ARMS) + " |")
        add("|---|" + "---|" * len(ARMS))
        for tid in task_ids:
            row = [fmt_verdict(by_cell[c]["tasks"].get(tid)) for _, c, _ in ARMS]
            add(f"| `{tid}` | " + " | ".join(row) + " |")
        add("")
        add("| **Resolved** | " + " | ".join(
            f"**{by_cell[c]['resolved']}/{by_cell[c]['scored']}**" for _, c, _ in ARMS) + " |")
        add("| No verdict | " + " | ".join(
            str(by_cell[c]["no_verdict"]) for _, c, _ in ARMS) + " |")
        add("")

        # what identical totals hide
        add("The three arms land on the same total, but not on the same tasks:")
        add("")
        cellmap = {k: c for k, c, _ in ARMS}
        separating = 0
        for tid in task_ids:
            who = [k for k, _, _ in ARMS
                   if by_cell[cellmap[k]]["tasks"].get(tid, {}).get("verdict") == "pass"]
            if 0 < len(who) < len(ARMS):
                add(f"- `{tid}` is resolved only by arm **{', '.join(who)}**.")
                separating += 1
        if separating == 0:
            add("- No task separates the arms.")
        add("")
        add("Read that honestly: on this task set the topology made no difference")
        add("to the total. Each task is a single trial, so the per-task differences")
        add("above are not a measured effect; claiming one topology beats another")
        add("would need several trials per cell.")
        add("")

        cut = [(k, by_cell[c]) for k, c, _ in ARMS
               if by_cell[c]["cut_short_scored"] or by_cell[c]["empty_patch_failures"]]
        if cut:
            add("Within these failures, some runs never got as far as an edit:")
            add("")
            for k, r in cut:
                wrong = [t for t, o in r["tasks"].items()
                         if o["verdict"] == "fail" and not o["empty_patch"]
                         and (o["cut_short"] or o["recovered"])]
                blank = [t for t, o in r["tasks"].items() if o["empty_patch"]]
                parts = []
                if wrong:
                    parts.append("a patch was written and graded: "
                                 + ", ".join("`%s`" % n for n in wrong))
                if blank:
                    parts.append("no patch was written at all: "
                                 + ", ".join("`%s`" % n for n in blank))
                add(f"- Arm **{k}** — " + "; ".join(parts) + ".")
            add("")
            add("The verdicts stand as recorded. But a blank patch means the run")
            add("died before editing, so those tasks measure the budget ceiling or")
            add("an outage, not a wrong fix — the same tasks would need rerunning")
            add("on a larger budget before anyone attributes them to the model.")
            add("")

    # -- every run
    add("## All runs")
    add("")
    add("| Run | What it is | Resolved | No verdict | Cut short | Empty patch | Trials | Binary |")
    add("|---|---|---:|---:|---:|---:|---:|---|")
    for r in runs:
        if r["calibration"]:
            continue
        b = (r["binary_sha256"] or "?")[:12]
        add(f"| `{r['cell']}` | {r['label']} | {r['resolved']}/{r['scored']} "
            f"| {r['no_verdict']} | {r['cut_short_scored']} | {r['empty_patch_failures']} "
            f"| {r['attempts']} | `{b}` |")
    add("")
    add("`No verdict` tasks never reached the grader and leave the fraction.")
    add("`Cut short` tasks ended early (budget exhausted, agent error) but were")
    add("still graded. `Empty patch` tasks were graded as failures without the")
    add("agent having edited anything at all, so they measure the budget ceiling")
    add("or an outage rather than a wrong fix. Both of the latter two stay in the")
    add("fraction, because a verdict from the grader is the honest record of what")
    add("happened — but they are shown so nobody reads them as model capability.")
    add("")

    if published:
        add("## Against the published round reports")
        add("")
        add("The numbers above are recomputed from raw trial records, so they can")
        add("differ from the reports written when each round finished. Where they")
        add("differ, the cause is a difference in attribution, not in data:")
        add("")
        add("| Round | Published | Recomputed | Why they differ |")
        add("|---|---|---|---|")
        for row in published:
            add(f"| {row['round']} | {row['published']} | {row['recomputed']} | {row['why']} |")
        add("")

    note = [r for r in runs if r["recovered_after_retry"] and not r["calibration"]]
    if note:
        add("Rows whose count includes a retry:")
        add("")
        for r in note:
            names = [t for t, o in r["tasks"].items() if o["recovered"]]
            add(f"- `{r['cell']}`: {', '.join('`%s`' % n for n in names)} "
                f"produced no verdict on the first attempt and were rerun.")
        add("")

    # -- calibration
    cal = [r for r in runs if r["calibration"]]
    if cal:
        add("## Verifier calibration")
        add("")
        add("Each row applies a known patch and checks that the grader reacts")
        add("correctly: `empty-*` supplies no fix and must fail, `gold-*` supplies")
        add("the official fix and must pass. These validate the harness, not the")
        add("agent.")
        add("")
        add("| Run | Variant | Resolved | Expected |")
        add("|---|---|---:|---|")
        for r in cal:
            expect = "all fail" if r["variants"][0].startswith("empty-") else "all pass"
            add(f"| `{r['cell']}` | `{r['variants'][0]}` | {r['resolved']}/{r['scored']} | {expect} |")
        add("")

        bad_empty = [r for r in cal
                     if r["variants"][0].startswith("empty-") and r["resolved"] > 0]
        if bad_empty:
            add("**A negative control passed, which it must not.** An empty patch")
            add("resolving a task means that task's grader accepts no fix at all:")
            add("")
            for r in bad_empty:
                names = [t for t, o in r["tasks"].items() if o["verdict"] == "pass"]
                add(f"- `{r['cell']}` / `{r['variants'][0]}`: "
                    f"{', '.join('`%s`' % n for n in names)}")
            add("")
            add("Any agent score on such a task is meaningless, and the task needs")
            add("fixing before it is used again.")
            add("")

        bad_gold = [r for r in cal
                    if r["variants"][0].startswith("gold-") and r["resolved"] != r["scored"]]
        if bad_gold:
            add("Gold-patch rows that did not fully pass. These are recorded as")
            add("harness or environment problems, exactly what the control exists")
            add("to expose, and the affected tasks carry no usable agent score:")
            add("")
            for r in bad_gold:
                names = [t for t, o in r["tasks"].items() if o["verdict"] != "pass"]
                add(f"- `{r['cell']}` / `{r['variants'][0]}`: "
                    f"{r['resolved']}/{r['scored']} passed — "
                    f"{', '.join('`%s`' % n for n in names)}")
            add("")
    return "\n".join(out) + "\n"


# --------------------------------------------------------------------------
# bundle
# --------------------------------------------------------------------------

def is_text(data):
    if b"\x00" in data[:4096]:
        return False
    try:
        data.decode("utf-8")
        return True
    except UnicodeDecodeError:
        return False


def sanitize(data):
    text = data.decode("utf-8")
    for rx, repl in SANITIZE_RULES:
        text = rx.sub(repl, text)
    return text.encode("utf-8")


def collect_files():
    """Read every file, keeping a dedup map for large ones only.

    Dedup exists to kill the 1.39 GB of identical 5 MB ``native.zip`` copies.
    Applying it to small files destroys the tree: every empty file shares one
    hash, so hundreds of blank ``model.patch`` files would collapse onto
    whichever empty file was seen first and lose their names. Anything under
    the threshold is therefore kept in place, and the whole tree stays
    browsable.
    """
    seen = {}
    kept, dups, skipped = [], [], []
    for dirpath, dirnames, filenames in os.walk(EVAL_DIR):
        dirnames[:] = sorted(d for d in dirnames if d not in SKIP_DIRS)
        for name in sorted(filenames):
            p = Path(dirpath) / name
            if name in SKIP_NAMES or p.suffix.lower() in SKIP_EXTS:
                skipped.append(p)
                continue
            try:
                raw = p.read_bytes()
            except OSError:
                skipped.append(p)
                continue
            if len(raw) < MIN_DEDUPE_BYTES:
                kept.append((p, raw))
                continue
            digest = hashlib.sha256(raw).hexdigest()
            if digest in seen:
                dups.append((p, seen[digest]))
                continue
            seen[digest] = p
            kept.append((p, raw))
    return kept, dups, skipped


def write_bundle(runs, out_zip):
    kept, dups, skipped = collect_files()
    out_zip.parent.mkdir(parents=True, exist_ok=True)
    manifest = {
        "schema": 1,
        "files_kept": len(kept),
        "files_deduplicated": len(dups),
        "files_skipped": len(skipped),
        "binaries_excluded": sorted({p.name for p in skipped}),
        # Rule patterns are deliberately not echoed: one of them contains the
        # local account name, which is the very thing being removed.
        "sanitized": ["local account name -> user", "repo root -> <repo>"],
        "duplicates": [
            {"path": str(p.relative_to(EVAL_DIR)).replace(os.sep, "/"),
             "identical_to": str(q.relative_to(EVAL_DIR)).replace(os.sep, "/")}
            for p, q in dups
        ],
    }
    raw_total = 0
    with zipfile.ZipFile(out_zip, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as zf:
        for p, raw in kept:
            raw_total += len(raw)
            arc = str(p.relative_to(EVAL_DIR)).replace(os.sep, "/")
            zf.writestr(arc, sanitize(raw) if is_text(raw) else raw)
        zf.writestr("_export/manifest.json", json.dumps(manifest, indent=2))
        zf.writestr("_export/summary.json", json.dumps({"schema": 1, "runs": runs}, indent=2))
        zf.writestr("_export/README.md", (
            "# Evaluation corpus\n\n"
            "Raw material behind `evals/EVAL_RESULTS.md`.\n\n"
            f"- {len(kept)} distinct files kept\n"
            f"- {len(dups)} byte-identical duplicates collapsed; `manifest.json`\n"
            "  maps each removed path to the copy that was kept\n"
            f"- Built binaries excluded ({', '.join(manifest['binaries_excluded'])});\n"
            "  rebuild them from the git revision recorded in each cell's `meta.txt`\n\n"
            "Local account names in paths were rewritten to `user` so the archive\n"
            "carries no personal home directory. No API key is present: credentials\n"
            "live in `.xcode/config`, which is never written into run output.\n\n"
            "Per-trial verdicts are the `verifier_result.rewards.reward` field of\n"
            "each harbor `result.json`.\n"
        ))
    return manifest, raw_total


def check_bundle(path):
    problems = []
    with zipfile.ZipFile(path) as zf:
        for info in zf.infolist():
            if info.is_dir():
                continue
            data = zf.read(info.filename)
            if not is_text(data):
                continue
            text = data.decode("utf-8")
            for rx, why in RESIDUAL_PATTERNS:
                for m in rx.finditer(text):
                    problems.append((info.filename, why, m.group(0)[:40]))
            for m in re.finditer(r"sk-[A-Za-z0-9]{20,}", text):
                problems.append((info.filename, "long sk- token", m.group(0)[:5] + "..."))
            for m in re.finditer(r"[A-Za-z]:[\\/]{1,4}Projects[\\/]{1,4}XCode"
                                 r"|/mnt/[a-z]/Projects/XCode", text):
                problems.append((info.filename, "unsanitized repo root", m.group(0)[:40]))
    return problems


# --------------------------------------------------------------------------

def published_rows(runs):
    # Only agent runs: a calibration row in the same cell must never stand in
    # for the agent's score.
    by_cell = {r["cell"]: r for r in runs if not r["calibration"]}
    rows = []
    for label, stated, cell, why in PUBLISHED:
        r = by_cell.get(cell)
        if not r:
            continue
        recomputed = f"{r['resolved']}/{r['scored']}"
        if r["no_verdict"]:
            recomputed += f" ({r['no_verdict']} no verdict)"
        rows.append({"round": label, "published": stated,
                     "recomputed": recomputed, "why": why})
    return rows


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--summary-only", action="store_true")
    ap.add_argument("--check", metavar="ZIP", help="verify an existing bundle")
    ap.add_argument("--out", default=None)
    args = ap.parse_args()

    if args.check:
        problems = check_bundle(Path(args.check))
        if problems:
            print(f"{len(problems)} problem(s):")
            for f, why, sample in problems[:40]:
                print(f"  {f}: {why}: {sample}")
            raise SystemExit(1)
        print("bundle clean: no local account names, no long sk- tokens")
        return

    runs = build_runs(collect())
    if not runs:
        raise SystemExit("no harbor trials found under .eval/")
    generated = dt.date.today().isoformat()

    md_path = ROOT / "evals" / "EVAL_RESULTS.md"
    json_path = ROOT / "evals" / "eval-results.json"
    md_path.write_text(render_markdown(runs, generated, published_rows(runs)), encoding="utf-8")
    json_path.write_text(json.dumps(
        {"schema": 1, "generated": generated, "runs": runs,
         "published": published_rows(runs)}, indent=2) + "\n",
        encoding="utf-8")
    print(f"wrote {md_path.relative_to(ROOT)}")
    print(f"wrote {json_path.relative_to(ROOT)}")

    for r in runs:
        if not r["calibration"]:
            print("  %-56s %d/%d  no-verdict=%d" %
                  (r["cell"], r["resolved"], r["scored"], r["no_verdict"]))

    if args.summary_only:
        return

    out = Path(args.out) if args.out else OUT_DIR / "eval-bundle.zip"
    manifest, raw_total = write_bundle(runs, out)
    print()
    print("kept %d distinct files, collapsed %d duplicates, skipped %d binaries"
          % (manifest["files_kept"], manifest["files_deduplicated"], manifest["files_skipped"]))
    print("%.1f MB raw -> %.1f MB zipped: %s"
          % (raw_total / 1048576, out.stat().st_size / 1048576, out))


if __name__ == "__main__":
    main()
