# usage-analytics-baseline.ps1
#
# 用途（方案 §4 批次 0.4 / 附录 A.1）：
#   输出与实施计划 §0.4 同构的可复现基线：分析库各表计数（含已归档的
#   analytics_legacy_*）、会话库 session_events 总数与类型分布、子代理完成
#   事件的 success/status 字段分布、tool.* 计数、session_tool_receipts 行数。
#   用于每次改动后对比"数据可信性"是否回归。
#
# 前置条件：
#   - Windows PowerShell 5.1+ / PowerShell 7+
#   - Python 3 可执行文件在 PATH 中（仅标准库 sqlite3/json）
#
# 用法：
#   pwsh -File usage-analytics-baseline.ps1
#   pwsh -File usage-analytics-baseline.ps1 -Json
#   pwsh -File usage-analytics-baseline.ps1 -AnalyticsDb D:\tmp\usage_analytics.sqlite -SessionDb D:\tmp\session_runtime.sqlite -Top 50
#
# 说明：任何库/表缺失都按 0 或空数组降级输出，不报错退出（退出码始终 0，
#       除非 Python 不可用）。

[CmdletBinding()]
param(
    [string]$AnalyticsDb = (Join-Path $HOME ".aicli\sessions\runtime\usage_analytics.sqlite"),
    [string]$SessionDb = (Join-Path $HOME ".aicli\sessions\runtime\session_runtime.sqlite"),
    [int]$Top = 30,
    [switch]$Json
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

function Get-PythonCommand {
    foreach ($candidate in @("python", "python3", "py")) {
        $command = Get-Command $candidate -ErrorAction SilentlyContinue
        if ($null -ne $command) { return $command.Source }
    }
    throw "未找到 Python 3。请安装 Python 并确保其在 PATH 中（脚本仅用标准库）。"
}

$python = Get-PythonCommand
$analyticsPath = [System.IO.Path]::GetFullPath($AnalyticsDb)
$sessionPath = [System.IO.Path]::GetFullPath($SessionDb)

$pythonScript = @'
import json
import os
import sqlite3
import sys

analytics_path = sys.argv[1]
session_path = sys.argv[2]
top = int(sys.argv[3])
as_json = sys.argv[4] == "1"

ANALYTICS_TABLES = [
    "usage_requests",
    "usage_sessions",
    "usage_tool_calls",
    "usage_subagents",
    "usage_turns",
    "analytics_sessions",
    "analytics_turns",
    "analytics_llm_requests",
    "analytics_legacy_sessions",
    "analytics_legacy_turns",
    "analytics_legacy_llm_requests",
]

SESSION_TABLES = ["session_events", "session_tool_receipts"]


def connect(path):
    if not os.path.exists(path):
        return None
    try:
        conn = sqlite3.connect(path, timeout=5.0)
        conn.execute("PRAGMA busy_timeout=5000")
        return conn
    except sqlite3.Error:
        return None


def table_exists(conn, name):
    try:
        return conn.execute(
            "SELECT name FROM sqlite_master WHERE type='table' AND name=?", (name,)
        ).fetchone() is not None
    except sqlite3.Error:
        return False


def count(conn, name):
    if conn is None or not table_exists(conn, name):
        return 0
    try:
        return conn.execute('SELECT COUNT(*) FROM "%s"' % name).fetchone()[0]
    except sqlite3.Error:
        return 0


def count_events_by_type(conn, event_type):
    if conn is None or not table_exists(conn, "session_events"):
        return 0
    try:
        return conn.execute(
            "SELECT COUNT(*) FROM session_events WHERE type = ?", (event_type,)
        ).fetchone()[0]
    except sqlite3.Error:
        return 0


def query(conn, sql, params=()):
    if conn is None:
        return []
    try:
        return conn.execute(sql, params).fetchall()
    except sqlite3.Error:
        return []


report = {
    "analytics_db": {"path": analytics_path, "exists": os.path.exists(analytics_path), "tables": {}},
    "session_db": {"path": session_path, "exists": os.path.exists(session_path), "tables": {}},
    "events": {"total": 0, "by_type": [], "tool_events": {}, "receipts": 0},
    "subagent_completed": {"total": 0, "success": {}, "status": {}, "missing_fields": 0},
}

analytics = connect(analytics_path)
for name in ANALYTICS_TABLES:
    report["analytics_db"]["tables"][name] = count(analytics, name)
if analytics is not None:
    analytics.close()

session = connect(session_path)
for name in SESSION_TABLES:
    report["session_db"]["tables"][name] = count(session, name)

if session is not None and table_exists(session, "session_events"):
    report["events"]["total"] = count(session, "session_events")
    rows = query(
        session,
        "SELECT type, COUNT(*) FROM session_events GROUP BY type ORDER BY COUNT(*) DESC, type ASC LIMIT ?",
        (top,),
    )
    report["events"]["by_type"] = [{"type": row[0], "count": row[1]} for row in rows]
    for event_type in ("tool.requested", "tool.completed", "tool_started", "tool_finished"):
        report["events"]["tool_events"][event_type] = count_events_by_type(session, event_type)
    # 子代理字段分布：payload 为 JSON 文本（不同版本列名可能不同，逐一尝试）。
    payload_column = None
    columns = query(session, "PRAGMA table_info(session_events)")
    for column in columns:
        if column[1] in ("payload_json", "payload", "data_json"):
            payload_column = column[1]
            break
    if payload_column is not None:
        rows = query(
            session,
            'SELECT "%s" FROM session_events WHERE type = ? LIMIT 20000' % payload_column,
            ("subagent.completed",),
        )
        success_counter = {}
        status_counter = {}
        missing = 0
        for (raw,) in rows:
            report["subagent_completed"]["total"] += 1
            try:
                payload = json.loads(raw) if raw else {}
            except (TypeError, ValueError):
                payload = {}
            if not isinstance(payload, dict):
                payload = {}
            if "success" in payload:
                key = str(payload.get("success")).lower()
                success_counter[key] = success_counter.get(key, 0) + 1
            if "status" in payload:
                key = str(payload.get("status"))
                status_counter[key] = status_counter.get(key, 0) + 1
            if "success" not in payload and "status" not in payload:
                missing += 1
        report["subagent_completed"]["success"] = success_counter
        report["subagent_completed"]["status"] = status_counter
        report["subagent_completed"]["missing_fields"] = missing

report["events"]["receipts"] = report["session_db"]["tables"].get("session_tool_receipts", 0)


if session is not None:
    session.close()

if as_json:
    print(json.dumps(report, ensure_ascii=False, indent=2))

    sys.exit(0)

print("== analytics db ==")
print("path: {}".format(report["analytics_db"]["path"]))
print("exists: {}".format(report["analytics_db"]["exists"]))
for name, value in report["analytics_db"]["tables"].items():
    print("  {:<32} {}".format(name, value))
print("== session db ==")
print("path: {}".format(report["session_db"]["path"]))
print("exists: {}".format(report["session_db"]["exists"]))
for name, value in report["session_db"]["tables"].items():
    print("  {:<32} {}".format(name, value))
print("== events ==")
print("total: {}".format(report["events"]["total"]))
for row in report["events"]["by_type"]:
    print("  {:<32} {}".format(row["type"], row["count"]))
print("tool events: " + json.dumps(report["events"]["tool_events"], ensure_ascii=False))
print("receipts: {}".format(report["events"]["receipts"]))
print("== subagent.completed ==")
print("total: {}".format(report["subagent_completed"]["total"]))
print("success: " + json.dumps(report["subagent_completed"]["success"], ensure_ascii=False))
print("status: " + json.dumps(report["subagent_completed"]["status"], ensure_ascii=False))
print("missing_fields: {}".format(report["subagent_completed"]["missing_fields"]))
'@

$tempScript = Join-Path $env:TEMP ("usage-analytics-baseline-" + [System.Guid]::NewGuid().ToString("N") + ".py")
try {
    Set-Content -LiteralPath $tempScript -Value $pythonScript -Encoding UTF8
    $jsonFlag = if ($Json) { "1" } else { "0" }
    & $python $tempScript $analyticsPath $sessionPath $Top $jsonFlag
    if ($LASTEXITCODE -ne 0) {
        Write-Error "基线脚本失败（python 退出码 $LASTEXITCODE）"
        exit 2
    }
}
finally {
    Remove-Item -LiteralPath $tempScript -Force -ErrorAction SilentlyContinue
}
exit 0
