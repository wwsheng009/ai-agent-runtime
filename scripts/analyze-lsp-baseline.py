#!/usr/bin/env python3
"""LSP 基线分析：读取 aicli 会话 runtime-events.jsonl，产出 §4.3 基线登记表。

口径（与 docs/plan/lsp-observability-and-analysis-plan-20260929.md §3/§4.2/§4.3 对齐）：

- 数据源：``<root>/**/runtime-events.jsonl``（新布局
  ``chat-logs/YYYY/MM/DD/<session-id>/events/``；rglob 同时覆盖旧布局），
  每行是一个 runtime 事件对象。
- ``lsp.request.finished``：一行 = 一次后写请求（每个被编辑文件一行），载荷为
  trigger/outcome/duration_ms/diag_count/appended_bytes/omitted_items/omitted_by_chars
  （标量；无路径、无正文）。
- 覆盖率分母：``tool.completed`` 且 ``payload.logical_tool`` ∈ EDIT_TOOLS 的调用数。
  一次编辑工具调用可能覆盖多个文件（多行 request 事件）——口径近似，报告中明示。
- 反模式纪律（§4.3 注）：未采集一律输出 ``n/a`` 与原因，**绝不渲染成 0**；
  **不做阈值告警**（阈值只在基线产出后固化，ADR-0003 D4）。

用法：
    python scripts/analyze-lsp-baseline.py                     # 全窗口
    python scripts/analyze-lsp-baseline.py --days 14           # 最近 14 天
    python scripts/analyze-lsp-baseline.py --since 2026-09-15 --json out.json
    python scripts/analyze-lsp-baseline.py --selftest          # 内置样例自验
"""

from __future__ import annotations

import argparse
import json
import math
import sys
import tempfile
from collections import Counter, defaultdict
from datetime import datetime, timedelta, timezone
from pathlib import Path

LSP_REQUEST_TYPE = "lsp.request.finished"
TOOL_COMPLETED_TYPE = "tool.completed"
# 首个发布时补发的状态事件带 first_publish_ms（冷启动观测，plan §5.8 第三轮）。
LSP_SERVER_STATE_TYPE = "lsp.server.state"

# 编辑类工具（与 §3.1 的 mutated_paths 覆盖面同口径；工具名以 runtime 的
# logical_tool 为准）。
EDIT_TOOLS = {
    "apply_patch",
    "write",
    "edit",
    "notebook_edit",
    "str_replace_editor",
    "patch",
}

DEGRADED_PREFIX = "degraded"


def parse_timestamp(value):
    if not isinstance(value, str) or not value.strip():
        return None
    text = value.strip()
    if text.endswith("Z"):
        text = text[:-1] + "+00:00"
    try:
        parsed = datetime.fromisoformat(text)
    except ValueError:
        return None
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=timezone.utc)
    return parsed


def percentile(sorted_values, fraction):
    """与 Go 侧 Metrics 的口径一致：ceil(p*n) 的 1-based 序号（n>=1）。"""
    if not sorted_values:
        return None
    index = int(math.ceil(fraction * len(sorted_values))) - 1
    index = max(0, min(index, len(sorted_values) - 1))
    return sorted_values[index]


def to_int(value):
    if isinstance(value, bool):
        return None
    if isinstance(value, (int, float)):
        return int(value)
    return None


def load_events(roots, since):
    stats = {
        "files": 0,
        "lines": 0,
        "malformed": 0,
        "skipped_old": 0,
        "skipped_files": 0,
    }
    events = []
    seen_paths = set()
    for root in roots:
        root_path = Path(root)
        if not root_path.exists():
            continue
        for path in sorted(root_path.rglob("runtime-events.jsonl")):
            key = str(path.resolve()).lower()
            if key in seen_paths:
                continue
            seen_paths.add(key)
            if since is not None:
                try:
                    if datetime.fromtimestamp(path.stat().st_mtime, tz=timezone.utc) < since:
                        stats["skipped_files"] += 1
                        continue
                except OSError:
                    pass
            stats["files"] += 1
            try:
                handle = path.open("r", encoding="utf-8", errors="replace")
            except OSError as exc:
                print(f"warn: 无法读取 {path}: {exc}", file=sys.stderr)
                continue
            with handle:
                for line in handle:
                    line = line.strip()
                    if not line:
                        continue
                    stats["lines"] += 1
                    # 便宜预筛：事件写入端用 json.Marshal（紧凑无空格），但这里只
                    # 按「类型值是否出现在行内」判断，兼容旧/美化格式；不含目标
                    # 类型的行直接跳过，避免对整库逐行 json.loads。
                    if (LSP_REQUEST_TYPE not in line and TOOL_COMPLETED_TYPE not in line
                            and LSP_SERVER_STATE_TYPE not in line):
                        continue
                    try:
                        event = json.loads(line)
                    except json.JSONDecodeError:
                        stats["malformed"] += 1
                        continue
                    if not isinstance(event, dict):
                        stats["malformed"] += 1
                        continue
                    event_type = str(event.get("type") or "").strip()
                    if event_type not in (LSP_REQUEST_TYPE, TOOL_COMPLETED_TYPE, LSP_SERVER_STATE_TYPE):
                        continue
                    timestamp = parse_timestamp(event.get("timestamp"))
                    if since is not None and (timestamp is None or timestamp < since):
                        stats["skipped_old"] += 1
                        continue
                    events.append((event_type, timestamp, event))
    return events, stats


def aggregate(events, stats):
    requests = []
    edit_calls = 0
    edit_output_bytes = 0
    edit_output_events = 0
    edit_calls_by_session = Counter()
    edit_output_bytes_by_session = Counter()
    # 冷启动延迟：同一 (session, server) 只取最早一条带 first_publish_ms 的状态
    # 事件（首个发布即该连接的冷启动延迟；同会话内重启会再发一条，取首次）。
    cold_first = {}

    for event_type, timestamp, event in events:
        payload = event.get("payload")
        if not isinstance(payload, dict):
            payload = {}
        if event_type == LSP_REQUEST_TYPE:
            requests.append(
                {
                    "session_id": str(event.get("session_id") or "").strip(),
                    "timestamp": timestamp,
                    "trigger": str(payload.get("trigger") or "").strip(),
                    "outcome": str(payload.get("outcome") or "").strip(),
                    "duration_ms": to_int(payload.get("duration_ms")) or 0,
                    "diag_count": to_int(payload.get("diag_count")) or 0,
                    "appended_bytes": to_int(payload.get("appended_bytes")) or 0,
                    "appended_diag_bytes": to_int(payload.get("appended_diag_bytes")) or 0,
                    "appended_note_bytes": to_int(payload.get("appended_note_bytes")) or 0,
                    "appended_empty_bytes": to_int(payload.get("appended_empty_bytes")) or 0,
                    "total_diag_count": to_int(payload.get("total_diag_count")) or 0,
                    "new_diag_count": to_int(payload.get("new_diag_count")) or 0,
                    "omitted_items": to_int(payload.get("omitted_items")) or 0,
                    "omitted_by_chars": to_int(payload.get("omitted_by_chars")) or 0,
                    "server": str(payload.get("server") or "").strip(),
                    "path_fingerprint": str(payload.get("path_fingerprint") or "").strip(),
                    "diag_fingerprint": str(payload.get("diag_fingerprint") or "").strip(),
                    "reason_category": str(payload.get("reason_category") or "").strip(),
                    "cold_fast_fail": payload.get("cold_fast_fail"),
                    "attempted_members": to_int(payload.get("attempted_members")) or 0,
                }
            )
            continue
        if event_type == LSP_SERVER_STATE_TYPE:
            first_publish_ms = to_int(payload.get("first_publish_ms"))
            if first_publish_ms is None or first_publish_ms <= 0:
                continue
            key = (str(event.get("session_id") or "").strip(),
                   str(payload.get("server") or "").strip())
            prev = cold_first.get(key)
            if prev is None or (timestamp is not None and (prev[0] is None or timestamp < prev[0])):
                cold_first[key] = (timestamp, first_publish_ms)
            continue
        tool_name = str(payload.get("logical_tool") or "").strip()
        if tool_name in EDIT_TOOLS:
            edit_calls += 1
            session_id = str(event.get("session_id") or "").strip()
            edit_calls_by_session[session_id] += 1
            output_bytes = to_int(payload.get("output_model_visible_bytes"))
            if output_bytes is None:
                output_bytes = to_int(payload.get("output_original_bytes"))
            if output_bytes is not None and output_bytes > 0:
                edit_output_bytes += output_bytes
                edit_output_events += 1
                edit_output_bytes_by_session[session_id] += output_bytes

    triggers = Counter(item["trigger"] for item in requests)
    outcomes = Counter(item["outcome"] for item in requests)
    servers = Counter(item["server"] for item in requests if item["server"])

    injected = outcomes.get("injected", 0)
    diag_hit = sum(1 for item in requests if item["outcome"] == "injected" and item["diag_count"] > 0)
    degraded = sum(count for outcome, count in outcomes.items() if outcome.startswith(DEGRADED_PREFIX))
    no_server = outcomes.get("no_server", 0)
    clean = outcomes.get("clean", 0)
    cold_starts = sorted(value for _, value in cold_first.values())
    degrade_reasons = Counter(
        item["reason_category"] for item in requests if item["reason_category"]
    )
    # O2/O4/O5 新字段（2026-10-01 起落盘）：旧构建无字段时保持 0，报告渲染为
    # "未采集"而不是 0。
    appended_diag_bytes = sum(item["appended_diag_bytes"] for item in requests)
    appended_note_bytes = sum(item["appended_note_bytes"] for item in requests)
    appended_empty_bytes = sum(item["appended_empty_bytes"] for item in requests)
    total_diag_count = sum(item["total_diag_count"] for item in requests)
    new_diag_count = sum(item["new_diag_count"] for item in requests)
    cold_first_probe = sum(
        1 for item in requests
        if item["outcome"] == "degraded_no_fresh" and item["cold_fast_fail"] is False
    )
    cold_repeat = sum(
        1 for item in requests
        if item["outcome"] == "degraded_no_fresh" and item["cold_fast_fail"] is True
    )
    multi_member_requests = sum(1 for item in requests if item["attempted_members"] > 1)
    attempted_members_max = max((item["attempted_members"] for item in requests), default=0)

    durations = sorted(item["duration_ms"] for item in requests)
    truncated = [item for item in requests if item["omitted_items"] > 0 or item["omitted_by_chars"] > 0]

    by_day = defaultdict(lambda: {"requests": 0, "injected": 0, "degraded": 0, "durations": []})
    for item in requests:
        timestamp = item["timestamp"]
        day = timestamp.date().isoformat() if timestamp is not None else "unknown"
        bucket = by_day[day]
        bucket["requests"] += 1
        if item["outcome"] == "injected":
            bucket["injected"] += 1
        if item["outcome"].startswith(DEGRADED_PREFIX):
            bucket["degraded"] += 1
        bucket["durations"].append(item["duration_ms"])

    sessions = {item["session_id"] for item in requests if item["session_id"]}
    timestamps = [item["timestamp"] for item in requests if item["timestamp"] is not None]
    # §4.3 反模式纪律：覆盖率分母只算「LSP 活跃会话」（窗口内出现过请求事件的
    # 会话）内的编辑调用——把未启用/未触达 LSP 的会话算进分母会把"未采集"
    # 伪装成"覆盖率为 0"。
    active_edit_calls = sum(
        count for session_id, count in edit_calls_by_session.items() if session_id in sessions
    )
    active_edit_output_bytes = sum(
        total for session_id, total in edit_output_bytes_by_session.items() if session_id in sessions
    )

    # lsp_closure_ratio（§3.3）：同一会话内，被注入的诊断集合是否在下一次同文件
    # 编辑后消失（下一次请求 outcome=clean）。只统计带 fingerprint 的 injected
    # 请求；无 eligible 样本输出 n/a（不把未采集渲染成 0）。
    closure_eligible = 0
    closure_closed = 0
    by_session_path = defaultdict(list)
    for item in requests:
        session_id = item["session_id"]
        path_fingerprint = item["path_fingerprint"]
        if not session_id or not path_fingerprint:
            continue
        by_session_path[(session_id, path_fingerprint)].append(item)
    for facts in by_session_path.values():
        facts.sort(key=lambda x: x["timestamp"] or datetime.max.replace(tzinfo=timezone.utc))
        for index, item in enumerate(facts):
            if item["outcome"] != "injected" or not item["diag_fingerprint"]:
                continue
            closure_eligible += 1
            if index + 1 < len(facts) and facts[index + 1]["outcome"] == "clean":
                closure_closed += 1

    return {
        "requests": len(requests),
        "triggers": dict(triggers),
        "outcomes": dict(outcomes),
        "servers": dict(servers),
        "injected": injected,
        "diag_hit": diag_hit,
        "clean": clean,
        "no_server": no_server,
        "degraded": degraded,
        "degrade_reasons": dict(degrade_reasons),
        "durations": durations,
        "latency_p50_ms": percentile(durations, 0.50),
        "latency_p95_ms": percentile(durations, 0.95),
        "cold_first_publish_p50_ms": percentile(cold_starts, 0.50),
        "cold_first_publish_p95_ms": percentile(cold_starts, 0.95),
        "cold_first_publish_samples": cold_starts,
        "appended_bytes": sum(item["appended_bytes"] for item in requests),
        "diag_count": sum(item["diag_count"] for item in requests),
        "truncated_requests": len(truncated),
        "omitted_items": sum(item["omitted_items"] for item in requests),
        "omitted_by_chars": sum(item["omitted_by_chars"] for item in requests),
        "appended_diag_bytes": appended_diag_bytes,
        "appended_note_bytes": appended_note_bytes,
        "appended_empty_bytes": appended_empty_bytes,
        "total_diag_count": total_diag_count,
        "new_diag_count": new_diag_count,
        "cold_first_probe": cold_first_probe,
        "cold_repeat": cold_repeat,
        "multi_member_requests": multi_member_requests,
        "attempted_members_max": attempted_members_max,
        "sessions": len(sessions),
        "first_at": min(timestamps).isoformat() if timestamps else None,
        "last_at": max(timestamps).isoformat() if timestamps else None,
        "edit_calls": edit_calls,
        "edit_output_bytes": edit_output_bytes,
        "edit_output_events": edit_output_events,
        "active_edit_calls": active_edit_calls,
        "active_edit_output_bytes": active_edit_output_bytes,
        "closure_eligible": closure_eligible,
        "closure_closed": closure_closed,
        "by_day": by_day,
        "scan": stats,
    }


def ratio_text(numerator, denominator, digits=4):
    if not denominator:
        return "n/a（分母为 0）"
    return f"{numerator / denominator:.{digits}f}"


def cell(value):
    return "n/a" if value is None else str(value)


def render_markdown(rows, stats):
    lines = []
    lines.append("## LSP 基线报告（自动生成，阈值待人工固化）")
    lines.append("")
    lines.append(f"- 扫描文件：{stats['scan']['files']} 个 runtime-events.jsonl，"
                 f"{stats['scan']['lines']} 行（损坏 {stats['scan']['malformed']} 行）")
    lines.append(f"- 窗口：{cell(stats['first_at'])} → {cell(stats['last_at'])}；"
                 f"会话 {stats['sessions']} 个；请求 {stats['requests']} 次")
    lines.append(f"- 读数由 `lsp.request.finished`（A 通道落盘）复算；"
                 f"未采集项输出 n/a，不渲染为 0；本报告不做阈值告警（ADR-0003 D4）")
    lines.append("")

    lines.append("### §4.3 基线登记表（待人工回填 `结论/阈值`）")
    lines.append("")
    lines.append("| 指标 | 基线值 | 采样窗口 | 样本量 | 结论/阈值 | 日期 |")
    lines.append("| --- | --- | --- | --- | --- | --- |")
    for row in rows:
        lines.append(
            f"| `{row['metric']}` | {row['value']} | {row['window']} | {row['samples']} "
            f"| {row['conclusion']} | {row['date']} |"
        )
    lines.append("")

    lines.append("### 事实明细（不构成阈值判断）")
    lines.append("")
    lines.append(f"- 触发：{json.dumps(stats['triggers'], ensure_ascii=False)}")
    lines.append(f"- 结果：{json.dumps(stats['outcomes'], ensure_ascii=False)}")
    unclassified = int(stats["outcomes"].get("degraded", 0) or 0)
    if unclassified:
        lines.append(
            f"- 未分类降级：{unclassified} 条（旧构建无 `reason` 字段或其他未知原因；"
            f"计入 fallback 分子，但无法按原因细分）"
        )
    if stats.get("degrade_reasons"):
        lines.append(
            f"- 降级原因分布（新构建，低敏枚举）："
            f"{json.dumps(stats['degrade_reasons'], ensure_ascii=False)}"
        )
    lines.append(f"- 等待：P50 = {cell(stats['latency_p50_ms'])} ms，"
                 f"P95 = {cell(stats['latency_p95_ms'])} ms（n={len(stats['durations'])}）")
    lines.append(f"- 诊断：命中 {stats['diag_hit']} 次（injected {stats['injected']} 次），"
                 f"累计条数 {stats['diag_count']}")
    if stats["total_diag_count"] > 0:
        lines.append(f"- 诊断新旧构成（O9/A6）：新增 {stats['new_diag_count']} / "
                     f"全量 {stats['total_diag_count']}（scope=all 时全量即注入量）")
    lines.append(f"- 追加：累计 {stats['appended_bytes']} 字节；"
                 f"截断请求 {stats['truncated_requests']} 次（省略 {stats['omitted_items']} 条 / "
                 f"{stats['omitted_by_chars']} 字符）")
    if (stats["appended_diag_bytes"] + stats["appended_note_bytes"]
            + stats["appended_empty_bytes"]) > 0:
        lines.append(f"- 追加拆分（O2，新构建样本）：诊断 {stats['appended_diag_bytes']} / "
                     f"提示 {stats['appended_note_bytes']} / 空块 {stats['appended_empty_bytes']} 字节")
    if stats["cold_first_probe"] + stats["cold_repeat"] > 0:
        lines.append(f"- 冷路径探针（O4）：首探针 {stats['cold_first_probe']} / "
                     f"重复 {stats['cold_repeat']}（no_fresh 请求）")
    if stats["multi_member_requests"] > 0:
        lines.append(f"- 多成员请求（O5）：{stats['multi_member_requests']} 次"
                     f"（最多尝试 {stats['attempted_members_max']} 个成员）")
    lines.append(f"- 服务分布：{json.dumps(stats['servers'], ensure_ascii=False)}")
    lines.append(f"- 编辑调用（tool.completed 中的编辑类工具）：{stats['edit_calls']} 次；"
                 f"可读回执字节 {stats['edit_output_bytes']}（{stats['edit_output_events']} 次回执含字节）")
    lines.append("")

    lines.append("### 按日明细")
    lines.append("")
    lines.append("| 日期 | 请求 | 注入 | 降级 | P95(ms) |")
    lines.append("| --- | --- | --- | --- | --- |")
    for day in sorted(stats["by_day"].keys()):
        bucket = stats["by_day"][day]
        p95 = percentile(sorted(bucket["durations"]), 0.95)
        lines.append(f"| {day} | {bucket['requests']} | {bucket['injected']} | "
                     f"{bucket['degraded']} | {cell(p95)} |")
    lines.append("")

    lines.append("### 口径与限制")
    lines.append("")
    lines.append("- 覆盖率分母是「编辑类工具调用次数」，一次调用可能覆盖多文件（多行 request 事件），"
                 "因此该比值是近似口径，需与 `mutated_paths` 覆盖缺口一起判读。")
    lines.append("- `lsp_closure_ratio` 需要诊断身份（fingerprint）才能计算续轮消失率："
                 "基于 path/diag fingerprint 计算，被注入的诊断集合在下一次同文件编辑后"
                 "消失（下一次请求为 clean）才算闭环；无 fingerprint 样本标记 n/a。")
    lines.append("- `lsp_append_bytes_ratio` 依赖编辑回执的 `output_model_visible_bytes`；"
                 "回执未携带时标记 n/a（不猜分母）。")
    lines.append("- `lsp_cold_first_publish_p95` 依赖首个发布时补发的 `first_publish_ms`"
                 "（新构建落盘后开始采集；旧事件标记 n/a，不把未采集渲染成 0）。")
    lines.append("- `lsp_cold_first_probe_ratio` 拆分 no_fresh：首探针（路径未标记已知冷、"
                 "按完整预算等待）与重复探针（`cold_fast_fail=true`，已被路径级快速失败覆盖）；"
                 "无带该字段的样本时输出 n/a。")
    lines.append("- `lsp_diag_new_ratio` 是 A6（scope 默认值）的判据：全量为 scope 过滤前的"
                 "条数，新增为编辑前不存在的条数；新增占比低说明 scope=all 在反复重发既有"
                 "问题，切 scope=changed 的收益大；无带该字段的样本时输出 n/a。")
    lines.append("- 本报告只汇总 aicli/runtime-server 会话事件；runtime-server 的事件目录"
                 "可用 `--root` 指向其 chat-logs/事件根。")
    return "\n".join(lines) + "\n"


def build_rows(stats):
    date = datetime.now(timezone.utc).date().isoformat()
    window = f"{cell(stats['first_at'])} → {cell(stats['last_at'])}"

    inline = stats["triggers"].get("inline", 0)
    if stats["sessions"] == 0:
        coverage = "n/a（窗口内无 LSP 活跃会话）"
    elif stats["active_edit_calls"] > 0:
        coverage = ratio_text(inline, stats["active_edit_calls"])
    else:
        coverage = "n/a（LSP 活跃会话内无编辑类工具回执）"
    diag_hit_ratio = (
        ratio_text(stats["diag_hit"], stats["injected"])
        if stats["injected"] > 0
        else "n/a（窗口内无 injected 请求）"
    )
    # 与运行时读数（internal/lsp/metrics.go）同口径：分母排除 no_server，
    # 否则"没有任何成员认领"会稀释 fallback（plan §3.3/§5.8）。
    attempted = stats["requests"] - stats["no_server"]
    fallback_ratio = (
        ratio_text(stats["degraded"], attempted)
        if attempted > 0
        else "n/a（窗口内无 LSP 请求）" if stats["requests"] == 0
        else "n/a（attempted 为 0：全部请求均无 server 认领）"
    )
    fallback_samples = (
        f"degraded {stats['degraded']} / attempted {attempted}"
        f"（no_server {stats['no_server']}、clean {stats['clean']}）"
        if attempted > 0
        else f"no attempted requests（no_server {stats['no_server']}）"
    )
    if stats["sessions"] == 0:
        append_ratio = "n/a（窗口内无 LSP 活跃会话）"
    elif stats["active_edit_output_bytes"] > 0:
        append_ratio = ratio_text(stats["appended_bytes"], stats["active_edit_output_bytes"])
    else:
        append_ratio = "n/a（LSP 活跃会话内编辑回执缺少 output_model_visible_bytes）"
    append_samples = (f"追加 {stats['appended_bytes']} B / LSP 活跃会话内回执可见 "
                      f"{stats['active_edit_output_bytes']} B（全部 {stats['edit_output_bytes']} B）")
    if (stats["appended_diag_bytes"] + stats["appended_note_bytes"]
            + stats["appended_empty_bytes"]) > 0:
        append_samples += (f"（新构建拆分：诊断 {stats['appended_diag_bytes']} / "
                           f"提示 {stats['appended_note_bytes']} / 空块 {stats['appended_empty_bytes']}）")
    p95 = None if stats["latency_p95_ms"] is None else f"{stats['latency_p95_ms']} ms"
    if stats["closure_eligible"] > 0:
        closure_value = ratio_text(stats["closure_closed"], stats["closure_eligible"])
        closure_samples = (f"closed {stats['closure_closed']} / eligible {stats['closure_eligible']}"
                           "（下一次同文件编辑为 clean）")
        closure_conclusion = "待标定（需人工判读）"
    else:
        closure_value = "n/a（窗口内无带 fingerprint 的 injected 请求）"
        closure_samples = "eligible 0（未采集，非缺失数据）"
        closure_conclusion = "待采集（需要 path/diag fingerprint 事件）"

    return [
        {
            "metric": "lsp_edit_coverage_ratio",
            "value": coverage,
            "window": window,
            "samples": f"inline {inline} / LSP 活跃会话内 edit {stats['active_edit_calls']}"
                       f"（全部 edit {stats['edit_calls']}）",
            "conclusion": "待标定（需人工判读）",
            "date": date,
        },
        {
            "metric": "lsp_diag_hit_ratio",
            "value": diag_hit_ratio,
            "window": window,
            "samples": f"hit {stats['diag_hit']} / injected {stats['injected']}",
            "conclusion": "待标定（需人工判读）",
            "date": date,
        },
        {
            "metric": "lsp_diag_new_ratio",
            "value": diag_new_row_value(stats),
            "window": window,
            "samples": diag_new_row_samples(stats),
            "conclusion": diag_new_row_conclusion(stats),
            "date": date,
        },
        {
            "metric": "lsp_fallback_ratio",
            "value": fallback_ratio,
            "window": window,
            "samples": fallback_samples,
            "conclusion": "待标定（需人工判读）",
            "date": date,
        },
        {
            "metric": "lsp_wait_latency_p95",
            "value": cell(p95),
            "window": window,
            "samples": f"n={len(stats['durations'])}（P50 {cell(stats['latency_p50_ms'])} ms）",
            "conclusion": "待标定（需人工判读）",
            "date": date,
        },
        {
            "metric": "lsp_append_bytes_ratio",
            "value": append_ratio,
            "window": window,
            "samples": append_samples,
            "conclusion": "待标定（需人工判读）",
            "date": date,
        },
        {
            "metric": "lsp_closure_ratio",
            "value": closure_value,
            "window": window,
            "samples": closure_samples,
            "conclusion": closure_conclusion,
            "date": date,
        },
        {
            "metric": "lsp_cold_first_publish_p95",
            "value": cold_row_value(stats),
            "window": window,
            "samples": cold_row_samples(stats),
            "conclusion": "待标定（需人工判读）",
            "date": date,
        },
        {
            "metric": "lsp_cold_first_probe_ratio",
            "value": cold_probe_row_value(stats),
            "window": window,
            "samples": cold_probe_row_samples(stats),
            "conclusion": cold_probe_row_conclusion(stats),
            "date": date,
        },
    ]


def cold_probe_row_value(stats):
    """no_fresh 中首探针占比（O4）：未标记已知冷、按完整预算等待的那部分。"""
    total = stats["cold_first_probe"] + stats["cold_repeat"]
    if total <= 0:
        return "n/a（窗口内无带 cold_fast_fail 的 no_fresh 请求；新构建落盘后开始采集）"
    return ratio_text(stats["cold_first_probe"], total)


def diag_new_row_value(stats):
    """注入诊断中新增（编辑前不存在）的占比：A6（scope 默认值）判据。"""
    if stats["total_diag_count"] <= 0:
        return "n/a（窗口内无带 total_diag_count 的诊断样本；新构建落盘后开始采集）"
    return ratio_text(stats["new_diag_count"], stats["total_diag_count"])


def diag_new_row_samples(stats):
    if stats["total_diag_count"] <= 0:
        return "n=0（未采集，非缺失数据）"
    return (f"new {stats['new_diag_count']} / all {stats['total_diag_count']}"
            "（全量为 scope 过滤前条数）")


def diag_new_row_conclusion(stats):
    if stats["total_diag_count"] <= 0:
        return "待采集（需要 total_diag_count 事件字段）"
    return "待标定（A6：新增占比低 → 考虑 scope=changed 默认）"


def cold_probe_row_samples(stats):
    if stats["cold_first_probe"] + stats["cold_repeat"] <= 0:
        return "n=0（未采集，非缺失数据）"
    return (f"first {stats['cold_first_probe']} / repeat {stats['cold_repeat']}"
            "（repeat 已由路径级快速失败覆盖）")


def cold_probe_row_conclusion(stats):
    if stats["cold_first_probe"] + stats["cold_repeat"] <= 0:
        return "待采集（需要 cold_fast_fail 事件字段）"
    return "待标定（需人工判读）"


def cold_row_value(stats):
    """冷启动延迟 P95（启动→首个发布）；未采集输出 n/a + 原因。"""
    p95 = stats.get("cold_first_publish_p95_ms")
    if p95 is None:
        return "n/a（窗口内无带 first_publish_ms 的 server 状态事件；新构建落盘后开始采集）"
    return f"{p95} ms"


def cold_row_samples(stats):
    p50 = stats.get("cold_first_publish_p50_ms")
    count = len(stats.get("cold_first_publish_samples") or [])
    if count == 0:
        return "n=0（未采集，非缺失数据）"
    return f"n={count}（P50 {cell(p50)} ms；按 (session, server) 取首个发布）"


def selftest():
    with tempfile.TemporaryDirectory() as tmp:
        events_dir = Path(tmp) / "2026" / "09" / "29" / "sess_a" / "events"
        events_dir.mkdir(parents=True)
        lines = [
            {"type": "lsp.request.finished", "session_id": "s1", "timestamp": "2026-09-29T10:00:00Z",
             "payload": {"trigger": "inline", "outcome": "injected", "duration_ms": 10,
                         "diag_count": 2, "appended_bytes": 100,
                         "appended_diag_bytes": 80, "appended_note_bytes": 10,
                         "appended_empty_bytes": 10, "attempted_members": 2,
                         "total_diag_count": 3, "new_diag_count": 2, "server": "gopls",
                         "path_fingerprint": "p1", "diag_fingerprint": "d1"}},
            {"type": "lsp.request.finished", "session_id": "s1", "timestamp": "2026-09-29T10:02:00Z",
             "payload": {"trigger": "tool", "outcome": "clean", "duration_ms": 7,
                         "path_fingerprint": "p1"}},
            {"type": "lsp.request.finished", "session_id": "s1", "timestamp": "2026-09-29T10:01:00Z",
             "payload": {"trigger": "inline", "outcome": "degraded_no_fresh", "duration_ms": 30,
                         "reason_category": "wait_timeout", "cold_fast_fail": False}},
            {"type": "lsp.request.finished", "session_id": "s2", "timestamp": "2026-09-29T10:02:00Z",
             "payload": {"trigger": "tool", "outcome": "no_server", "duration_ms": 5}},
            {"type": "lsp.server.state", "session_id": "s1", "timestamp": "2026-09-29T10:00:00Z",
             "payload": {"server": "gopls", "state": "ready", "pid": 42, "first_publish_ms": 1234}},
            {"type": "tool.completed", "session_id": "s1", "timestamp": "2026-09-29T10:00:00Z",
             "payload": {"logical_tool": "apply_patch", "output_model_visible_bytes": 1000}},
            {"type": "tool.completed", "session_id": "s1", "timestamp": "2026-09-29T10:01:00Z",
             "payload": {"logical_tool": "write", "output_model_visible_bytes": 100}},
            {"type": "tool.completed", "session_id": "s1", "timestamp": "2026-09-29T10:01:30Z",
             "payload": {"logical_tool": "grep"}},
        ]
        path = events_dir / "runtime-events.jsonl"
        path.write_text("\n".join(json.dumps(line) for line in lines) + "\n", encoding="utf-8")

        events, stats = load_events([tmp], None)
        result = aggregate(events, stats)
        checks = [
            ("requests", result["requests"], 4),
            ("inline", result["triggers"].get("inline"), 2),
            ("tool", result["triggers"].get("tool"), 2),
            ("injected", result["injected"], 1),
            ("diag_hit", result["diag_hit"], 1),
            ("degraded", result["degraded"], 1),
            ("degrade_reasons.wait_timeout", result["degrade_reasons"].get("wait_timeout"), 1),
            ("no_server", result["no_server"], 1),
            ("edit_calls", result["edit_calls"], 2),
            ("p50", result["latency_p50_ms"], 7),
            ("p95", result["latency_p95_ms"], 30),
            ("edit_output_bytes", result["edit_output_bytes"], 1100),
            ("cold_first_publish_p50_ms", result["cold_first_publish_p50_ms"], 1234),
            ("cold_first_publish_p95_ms", result["cold_first_publish_p95_ms"], 1234),
            ("appended_diag_bytes", result["appended_diag_bytes"], 80),
            ("appended_note_bytes", result["appended_note_bytes"], 10),
            ("appended_empty_bytes", result["appended_empty_bytes"], 10),
            ("cold_first_probe", result["cold_first_probe"], 1),
            ("cold_repeat", result["cold_repeat"], 0),
            ("multi_member_requests", result["multi_member_requests"], 1),
            ("attempted_members_max", result["attempted_members_max"], 2),
            ("total_diag_count", result["total_diag_count"], 3),
            ("new_diag_count", result["new_diag_count"], 2),
            ("sessions", result["sessions"], 2),
            ("closure_eligible", result["closure_eligible"], 1),
            ("closure_closed", result["closure_closed"], 1),
        ]
        failures = [f"{name}: got {actual}, want {want}" for name, actual, want in checks if actual != want]
        rows = {row["metric"]: row["value"] for row in build_rows(result)}
        if rows["lsp_edit_coverage_ratio"] != ratio_text(2, 2):
            failures.append(f"coverage: got {rows['lsp_edit_coverage_ratio']}")
        if rows["lsp_append_bytes_ratio"] != ratio_text(100, 1100):
            failures.append(f"append ratio: got {rows['lsp_append_bytes_ratio']}")
        if rows["lsp_fallback_ratio"] != ratio_text(1, 3):
            failures.append(f"fallback: got {rows['lsp_fallback_ratio']}")
        if rows["lsp_closure_ratio"] != ratio_text(1, 1):
            failures.append(f"closure: got {rows['lsp_closure_ratio']}")
        if rows["lsp_cold_first_publish_p95"] != "1234 ms":
            failures.append(f"cold first publish: got {rows['lsp_cold_first_publish_p95']}")
        if rows["lsp_cold_first_probe_ratio"] != ratio_text(1, 1):
            failures.append(f"cold probe ratio: got {rows['lsp_cold_first_probe_ratio']}")
        if rows["lsp_diag_new_ratio"] != ratio_text(2, 3):
            failures.append(f"diag new ratio: got {rows['lsp_diag_new_ratio']}")
        if failures:
            print("selftest FAILED:")
            for failure in failures:
                print("  -", failure)
            return 1
        print("selftest OK（4 请求 / 覆盖率 1.0 / P50 7ms / 追加比 100/1100 / fallback 1/3 / "
              "closure 1.0 / cold probe 1.0 / diag new 2/3）")
        return 0


def main(argv=None):
    # Windows 控制台默认 GBK：显式用 UTF-8 输出，避免中文报告花屏。
    for stream in (sys.stdout, sys.stderr):
        try:
            stream.reconfigure(encoding="utf-8")
        except (AttributeError, ValueError):
            pass
    parser = argparse.ArgumentParser(description="LSP 基线分析（§4.3 登记表）")
    parser.add_argument("--root", action="append", default=None,
                        help="chat-logs 根目录，可重复；默认 ~/.aicli/chat-logs")
    parser.add_argument("--days", type=int, default=0, help="只看最近 N 天（0 = 全窗口）")
    parser.add_argument("--since", default="", help="起始日期 YYYY-MM-DD（与 --days 二选一）")
    parser.add_argument("--json", default="", help="把聚合结果写入 JSON 文件")
    parser.add_argument("--out", default="", help="把 Markdown 报告写入文件（默认 stdout）")
    parser.add_argument("--selftest", action="store_true", help="用内置样例自验后退出")
    args = parser.parse_args(argv)

    if args.selftest:
        return selftest()

    roots = args.root or [str(Path.home() / ".aicli" / "chat-logs")]
    since = None
    now = datetime.now(timezone.utc)
    if args.since:
        try:
            since = datetime.strptime(args.since, "%Y-%m-%d").replace(tzinfo=timezone.utc)
        except ValueError:
            print(f"error: --since 需要 YYYY-MM-DD，收到 {args.since!r}", file=sys.stderr)
            return 2
    elif args.days > 0:
        since = now - timedelta(days=args.days)

    events, stats = load_events(roots, since)
    result = aggregate(events, stats)
    report = render_markdown(build_rows(result), result)

    if args.json:
        Path(args.json).write_text(
            json.dumps(result, ensure_ascii=False, indent=2, default=str) + "\n",
            encoding="utf-8",
        )
    if args.out:
        Path(args.out).write_text(report, encoding="utf-8")
        print(f"报告已写入 {args.out}（请求 {result['requests']} 次，会话 {result['sessions']} 个）")
    else:
        sys.stdout.write(report)
    return 0


if __name__ == "__main__":
    sys.exit(main())
