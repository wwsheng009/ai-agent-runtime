"""M5 A/B 分析器（knowledge off vs on，20 任务/臂）。

口径要点（均为本轮实测踩坑后固化的）：
  * status=completed 不等于成功。provider 配额耗尽时 60/60 全是
    status=completed + llm_observed=true，实际 60 次
    UPSTREAM_QUOTA_EXHAUSTED。故每臂必须过 integrity 闸门：
    llm_requests_ok > 0、turns_joined == tasks、turns_success > 0，
    否则标 INVALID。
  * token 主口径是 usage_turns.prompt_tokens（按 turn_id 与 driver 落盘
    的 turn 精确 join）。**不要**用 invoke 响应里的 usage：那是从
    session.InputTokenCount 等累计计数器取的，单任务必须做相邻差值，
    且上游失败时整列为 0。
  * usage_requests.turn_id 存的是 trace_id 而非 turn_id，直接按
    driver 的 turn_id join 会 0 命中——只能用来看错误类别。
  * "任务级成功"= 该 turn 的 usage_tool_calls 命中 expect_any。

收口轮八变更（与 knowledge_ab_driver.ps1 同步）：
  * token 主口径改为 driver 落盘的 invoke 响应 usage（每任务干净会话，
    可逐任务比较）；usage_turns 仍按 turn_id join 用于交叉校验。
  * 脚本原为 .tmp\ab_analyze.py（不入库），收口轮八迁入 backend/scripts\；
    输出目录默认 <repo>/.tmp，可用环境变量 AB_OUT_DIR / AB_WS 覆盖。
"""
from __future__ import annotations

import glob
import json
import os
import sqlite3
import statistics
import sys
from collections import defaultdict

HOME = os.path.expanduser("~")
USAGE_DB = os.path.join(HOME, ".aicli", "sessions", "runtime", "usage_analytics.sqlite")
HIST_DB = os.path.join(HOME, ".aicli", "sessions", "session_history.sqlite")
# backend/scripts/knowledge_ab_analyze.py -> <repo>
REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
TMP = os.environ.get("AB_OUT_DIR") or os.path.join(REPO, ".tmp")
AB_WS = os.environ.get("AB_WS") or os.path.join(os.environ.get("TEMP", ""), "p6_ab", "ws")

OK_STATUS = {"completed", "settled", "ok"}
ARMS = ["warm", "off", "on"]


def load_arm(arm: str) -> list[dict]:
    path = os.path.join(TMP, f"ab_run_{arm}.jsonl")
    recs = []
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if line:
                recs.append(json.loads(line))
    recs.sort(key=lambda r: r.get("idx", 0))
    return recs


def pct(numer: int, denom: int) -> float:
    return 100.0 * numer / denom if denom else 0.0


def stats(values: list[float]) -> dict:
    if not values:
        return {"n": 0}
    ordered = sorted(values)
    idx = max(0, min(len(ordered) - 1, int(round(0.95 * (len(ordered) - 1)))))
    return {
        "n": len(ordered),
        "mean_ms": round(statistics.mean(ordered), 1),
        "median_ms": round(statistics.median(ordered), 1),
        "p95_ms": round(ordered[idx], 1),
        "min_ms": round(ordered[0], 1),
        "max_ms": round(ordered[-1], 1),
    }


def usage_by_turn(session_ids: list[str]) -> dict:
    """turn_id -> 该 turn 的 token/工具/工具名集合。"""
    if not os.path.exists(USAGE_DB) or not session_ids:
        return {}
    con = sqlite3.connect(f"file:{USAGE_DB}?mode=ro", uri=True)
    con.row_factory = sqlite3.Row
    out = defaultdict(lambda: {
        "requests": 0, "ok_requests": 0, "err_requests": 0,
        "error_categories": set(), "prompt_tokens": 0,
        "completion_tokens": 0, "total_tokens": 0, "cache_read_tokens": 0,
        "turn_row": False, "steps": 0, "turn_success": 0,
        "tool_calls": 0, "tool_names": set(), "tool_errors": 0,
        "usage_available": 0,
    })
    marks = ",".join("?" * len(session_ids))
    try:
        for r in con.execute(
            f"select turn_id, success, steps, prompt_tokens, completion_tokens,"
            f" total_tokens, cache_read_tokens, tool_error_count"
            f" from usage_turns where session_id in ({marks})", session_ids
        ):
            k = r["turn_id"] or ""
            if not k:
                continue
            e = out[k]
            e["turn_row"] = True
            e["turn_success"] = 1 if r["success"] else 0
            e["steps"] = r["steps"] or 0
            e["prompt_tokens"] = r["prompt_tokens"] or 0
            e["completion_tokens"] = r["completion_tokens"] or 0
            e["total_tokens"] = r["total_tokens"] or 0
            e["cache_read_tokens"] = r["cache_read_tokens"] or 0
        for r in con.execute(
            f"select turn_id, status, success, error_category, prompt_tokens,"
            f" completion_tokens, total_tokens, usage_available, duration_ms"
            f" from usage_requests where session_id in ({marks})", session_ids
        ):
            k = r["turn_id"] or ""
            if not k:
                continue
            e = out[k]
            e["requests"] += 1
            if r["success"]:
                e["ok_requests"] += 1
            else:
                e["err_requests"] += 1
                if r["error_category"]:
                    e["error_categories"].add(r["error_category"])
            # token 归 usage_turns（按 driver 的 turn_id 可精确 join）；
            # usage_requests 只用来数请求与错误类别，避免重复累加。
            e["usage_available"] += 1 if r["usage_available"] else 0
        for r in con.execute(
            f"select turn_id, tool_name, ok, error_code from usage_tool_calls"
            f" where session_id in ({marks})", session_ids
        ):
            k = r["turn_id"] or ""
            if not k:
                continue
            e = out[k]
            e["tool_calls"] += 1
            if r["tool_name"]:
                e["tool_names"].add(r["tool_name"])
            if r["ok"] == 0 or r["error_code"]:
                e["tool_errors"] += 1
    finally:
        con.close()
    return out


def usage_requests_by_session(session_ids: list[str]) -> dict:
    """按 session 汇总 usage_requests 的成功/失败与错误类别。

    usage_requests.turn_id 存的是 trace_id，不能按 driver 的 turn_id join，
    所以请求级成功率只能在 session 维度统计（作为每臂的完整性闸门）。
    """
    out = {"requests": 0, "ok": 0, "failed": 0, "errors": {}}
    if not os.path.exists(USAGE_DB) or not session_ids:
        return out
    con = sqlite3.connect(f"file:{USAGE_DB}?mode=ro", uri=True)
    con.row_factory = sqlite3.Row
    marks = ",".join("?" * len(session_ids))
    try:
        for r in con.execute(
            f"select success, error_category from usage_requests"
            f" where session_id in ({marks})", session_ids
        ):
            out["requests"] += 1
            if r["success"]:
                out["ok"] += 1
            else:
                out["failed"] += 1
                cat = r["error_category"] or "UNSPECIFIED"
                out["errors"][cat] = out["errors"].get(cat, 0) + 1
    finally:
        con.close()
    return out


def knowledge_snapshot_stats(session_ids: list[str]) -> dict:
    """从 A/B 工作区的 knowledge.db 读 context_snapshots 的注入/过滤数。"""
    cands = []
    for pat in (os.path.join(AB_WS, ".aicli", "**", "*.db"),
                os.path.join(AB_WS, "**", "knowledge.db")):
        cands.extend(glob.glob(pat, recursive=True))
    cands = [c for c in cands if "knowledge" in os.path.basename(c)]
    if not cands:
        return {"db": None}
    db = sorted(cands, key=os.path.getmtime)[-1]
    con = sqlite3.connect(f"file:{db}?mode=ro", uri=True)
    con.row_factory = sqlite3.Row
    info = {"db": db, "rows": 0, "injected": 0, "filtered": 0, "by_tool": {}}
    try:
        cols = {r[1] for r in con.execute("pragma table_info(context_snapshots)")}
        if "session_id" not in cols or "budget_json" not in cols:
            info["error"] = f"unexpected columns: {sorted(cols)}"
            return info
        marks = ",".join("?" * len(session_ids))
        for r in con.execute(
            f"select session_id, budget_json from context_snapshots"
            f" where session_id in ({marks})", session_ids
        ):
            info["rows"] += 1
            try:
                budget = json.loads(r["budget_json"] or "{}")
            except (TypeError, ValueError):
                budget = {}
            inj = budget.get("injected")
            if isinstance(inj, list):
                info["by_tool"][r["session_id"]] = len(inj)
            info["injected"] += len(inj) if isinstance(inj, list) else 0
            info["filtered"] += budget.get("filtered") or budget.get("filtered_count") or 0
    finally:
        con.close()
    return info


def _rel_stats(pairs: list[tuple[float, float]]) -> dict:
    """pairs = [(baseline, treatment), ...] -> 相对降幅统计（正值=更省）。"""
    rels = []
    for base, treat in pairs:
        if base and base > 0:
            rels.append(100.0 * (base - treat) / base)
    out = {
        "n_pairs": len(pairs),
        "mean_baseline": round(statistics.mean([p[0] for p in pairs]), 1) if pairs else None,
        "mean_treatment": round(statistics.mean([p[1] for p in pairs]), 1) if pairs else None,
        "n_rel": len(rels),
    }
    if not rels:
        return out
    mean = statistics.mean(rels)
    sd = statistics.stdev(rels) if len(rels) > 1 else 0.0
    out.update({
        "mean_rel_drop_pct": round(mean, 2),
        "median_rel_drop_pct": round(statistics.median(rels), 2),
        "ci95_rel_drop_pct": [
            round(mean - 1.96 * sd / (len(rels) ** 0.5), 2),
            round(mean + 1.96 * sd / (len(rels) ** 0.5), 2),
        ],
        "n_treatment_lower": sum(1 for r in rels if r > 0),
        "n_treatment_higher": sum(1 for r in rels if r < 0),
        "n_equal": sum(1 for r in rels if r == 0),
        "n_baseline_zero": len(pairs) - len(rels),
    })
    return out


def paired_analysis(report: dict, base_arm: str = "off", treat_arm: str = "on") -> dict:
    """同任务配对比较（比两臂均值更强：消除任务集难度差异）。

    只用两臂都 join 到 usage 的任务；缺任一侧的任务单独列出。
    """
    def index(arm: str) -> dict:
        rows = (report.get("arms", {}).get(arm) or {}).get("per_task") or []
        return {r["id"]: r for r in rows if r.get("id")}

    bi, ti = index(base_arm), index(treat_arm)
    both, unpaired = [], []
    for tid, b in sorted(bi.items(), key=lambda kv: kv[1].get("idx") or 0):
        t = ti.get(tid)
        if t is not None and b["joined"] and t["joined"]:
            both.append((tid, b, t))
        else:
            unpaired.append({
                "task_id": tid,
                "base_status": b.get("status"),
                "base_joined": b["joined"],
                "treat_status": (t or {}).get("status"),
                "treat_joined": int(bool(t and t["joined"])),
            })
    only_treat = [
        {"task_id": tid, "treat_status": ti[tid].get("status"),
         "treat_joined": ti[tid]["joined"]}
        for tid in ti if tid not in bi
    ]

    pairs = [(b["prompt_tokens"], t["prompt_tokens"]) for _, b, t in both]
    out = {
        "base_arm": base_arm,
        "treat_arm": treat_arm,
        "n_pairs": len(both),
        "paired_task_ids": [tid for tid, _, _ in both],
        "unpaired": unpaired,
        "treat_only": only_treat,
        "prompt_tokens": _rel_stats(pairs),
        "total_tokens": _rel_stats(
            [(b["total_tokens"], t["total_tokens"]) for _, b, t in both]
        ),
        "completion_tokens": _rel_stats(
            [(b["completion_tokens"], t["completion_tokens"]) for _, b, t in both]
        ),
        "cache_read_tokens": _rel_stats(
            [(b["cache_read_tokens"], t["cache_read_tokens"]) for _, b, t in both]
        ),
        "wall_ms": _rel_stats([(b["wall_ms"], t["wall_ms"]) for _, b, t in both]),
        "tool_calls": _rel_stats([(b["tool_calls"], t["tool_calls"]) for _, b, t in both]),
        "success": {
            "base_task_hit": sum(b["task_hit"] for _, b, t in both),
            "treat_task_hit": sum(t["task_hit"] for _, b, t in both),
            "base_turn_success": sum(b["turn_success"] for _, b, t in both),
            "treat_turn_success": sum(t["turn_success"] for _, b, t in both),
            "n": len(both),
        },
    }
    return out


def main() -> int:
    report = {"arms": {}, "generated_from": TMP}
    print(f"A/B 工作区: {AB_WS}")
    for arm in ARMS:
        try:
            recs = load_arm(arm)
        except FileNotFoundError:
            report["arms"][arm] = {"error": "missing jsonl"}
            continue
        sids = sorted({r["session"] for r in recs if r.get("session")})
        by_turn = usage_by_turn(sids)
        req_agg = usage_requests_by_session(sids)

        ok_status = 0
        invoke_err = 0
        llm_observed = 0
        task_hit = 0
        turns_joined = 0
        turns_success = 0
        wall_all, wall_ok = [], []
        tok_in = tok_out = tok_total = 0
        cache_read = 0
        usage_avail_turns = 0
        tools_total = 0
        errors = defaultdict(int)
        no_usage_reasons = []
        per_task = []
        missing_turns = []

        for rec in recs:
            status = rec.get("status", "")
            if status in OK_STATUS:
                ok_status += 1
                wall_ok.append(rec.get("wall_ms") or 0)
            if status == "invoke_error":
                invoke_err += 1
            if rec.get("llm_observed"):
                llm_observed += 1
            wall_all.append(rec.get("wall_ms") or 0)

            u = by_turn.get(rec.get("turn") or "")
            row = {
                "id": rec.get("id"),
                "idx": rec.get("idx"),
                "status": status,
                "session": rec.get("session"),
                "turn": rec.get("turn"),
                "wall_ms": rec.get("wall_ms") or 0,
                "joined": 1 if u else 0,
                "prompt_tokens": None,
                "total_tokens": None,
                "completion_tokens": None,
                "cache_read_tokens": None,
                "tool_calls": 0,
                "turn_success": 0,
                "task_hit": 0,
                "tool_names": [],
                "error_categories": [],
            }
            if u:
                row["prompt_tokens"] = u["prompt_tokens"]
                row["total_tokens"] = u["total_tokens"]
                row["completion_tokens"] = u["completion_tokens"]
                row["cache_read_tokens"] = u["cache_read_tokens"]
                row["tool_calls"] = u["tool_calls"]
                row["turn_success"] = u["turn_success"]
                row["tool_names"] = sorted(u["tool_names"])
                row["error_categories"] = sorted(u["error_categories"])
            else:
                missing_turns.append(rec.get("id"))
            per_task.append(row)

            if u:
                turns_joined += 1
                turns_success += u["turn_success"]
                tok_in += u["prompt_tokens"]
                tok_out += u["completion_tokens"]
                tok_total += u["total_tokens"]
                cache_read += u["cache_read_tokens"]
                if u["usage_available"]:
                    usage_avail_turns += 1
                else:
                    no_usage_reasons.extend(sorted(u["error_categories"]) or ["NO_USAGE"])
                tools_total += u["tool_calls"]
                for cat in u["error_categories"]:
                    errors[cat] += 1
                expect = set(rec.get("expect_any") or [])
                if expect and (expect & u["tool_names"]):
                    task_hit += 1
                    per_task[-1]["task_hit"] = 1

        total = len(recs)
        arm_report = {
            "tasks": total,
            "ok_status": ok_status,
            "ok_status_pct": round(pct(ok_status, total), 1),
            "invoke_error": invoke_err,
            "llm_observed": llm_observed,
            "task_hit": task_hit,
            "task_hit_pct": round(pct(task_hit, total), 1),
            "wall_all": stats(wall_all),
            "wall_ok": stats(wall_ok),
            "sessions": sids,
            "distinct_sessions": len(sids),
            "turns_joined": turns_joined,
            "turns_joined_pct": round(pct(turns_joined, total), 1),
            "turns_success": turns_success,
            "llm_requests": req_agg["requests"],
            "llm_requests_ok": req_agg["ok"],
            "llm_requests_failed": req_agg["failed"],
            "turns_with_usage": usage_avail_turns,
            "tokens_input": tok_in,
            "tokens_output": tok_out,
            "tokens_total": tok_total,
            "tokens_cache_read": cache_read,
            "tool_calls": tools_total,
            "request_errors": req_agg["errors"],
            "turn_errors": dict(errors),
            "no_usage_samples": sorted(set(no_usage_reasons))[:6],
            "missing_turn_ids": missing_turns,
            "per_task": per_task,
        }
        # 完整性闸门：配额耗尽时 invoke 仍会返回 status=completed，
        # 不看 usage 表就会把"上游全程失败"误读成"知识层有效"。
        # 三档：INVALID=没有任何可用 usage；PARTIAL=部分任务缺 usage 行
        # （单任务超时/中断），数字只在 join 到的子集上成立。
        if req_agg["ok"] > 0 and turns_joined > 0 and turns_success > 0:
            arm_report["integrity"] = "OK" if turns_joined == total else "PARTIAL"
        else:
            arm_report["integrity"] = "INVALID"
        report["arms"][arm] = arm_report
        report.setdefault("knowledge", knowledge_snapshot_stats(sids))

    report["paired"] = paired_analysis(report)

    print("arm    tasks joined ok%  integrity  prompt_tok/task  tool_calls/task  wall_ok_p95")
    for arm in ARMS:
        a = report["arms"].get(arm) or {}
        n = a.get("tasks") or 0
        j = a.get("turns_joined") or 0
        print(
            f"{arm:<6} {n:>5} {j:>6} {a.get('ok_status_pct', 0):>5} "
            f"{a.get('integrity', '?'):>9} "
            f"{(a.get('tokens_input', 0) / j if j else 0):>16.0f} "
            f"{(a.get('tool_calls', 0) / j if j else 0):>17.2f} "
            f"{a.get('wall_ok', {}).get('p95_ms', 0):>12.0f}"
        )
    p = report.get("paired") or {}
    pt = p.get("prompt_tokens") or {}
    print(
        f"\npaired n={p.get('n_pairs')} prompt_tokens drop mean={pt.get('mean_rel_drop_pct')}% "
        f"median={pt.get('median_rel_drop_pct')}% ci95={pt.get('ci95_rel_drop_pct')} "
        f"lower/higher={pt.get('n_treatment_lower')}/{pt.get('n_treatment_higher')}"
    )
    print(f"paired success: {p.get('success')}")
    if p.get("unpaired"):
        print(f"unpaired: {p['unpaired']}")
    if p.get("treat_only"):
        print(f"treat_only: {p['treat_only']}")
    out_path = os.path.join(TMP, "ab_report.json")
    with open(out_path, "w", encoding="utf-8") as fh:
        json.dump(report, fh, ensure_ascii=False, indent=2)
    print(f"\nwritten: {out_path}")
    return 0


if __name__ == "__main__":
    sys.exit(main())