# usage-analytics-archive-legacy.ps1
#
# 用途（方案 §4 批次 0.4）：
#   把 usage_analytics.sqlite 中已无代码引用的三张旧表
#   （analytics_sessions / analytics_turns / analytics_llm_requests）
#   导出为 JSON 备份，并重命名为 analytics_legacy_*，消除"分析 API 读新表、
#   运维查旧表"的双口径。默认**绝不 DROP**。
#
# 前置条件：
#   - Windows PowerShell 5.1+ / PowerShell 7+
#   - Python 3 可执行文件在 PATH 中（脚本仅用标准库 sqlite3/json）
#   - 建议先关闭正在运行的 aicli / runtime-server（避免写锁竞争；脚本自身
#     使用 busy_timeout，仍可在并发下安全运行）
#
# 用法：
#   pwsh -File usage-analytics-archive-legacy.ps1
#   pwsh -File usage-analytics-archive-legacy.ps1 -AnalyticsDb D:\tmp\usage_analytics.sqlite
#   pwsh -File usage-analytics-archive-legacy.ps1 -Restore        # 回滚：legacy -> 原名
#
# 退出码：0 成功（含幂等空操作）；2 确定性失败（库不可读、重命名失败）。

[CmdletBinding()]
param(
    [string]$AnalyticsDb = (Join-Path $HOME ".aicli\sessions\runtime\usage_analytics.sqlite"),
    [string]$BackupRoot = "",
    [switch]$Restore
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
$dbPath = [System.IO.Path]::GetFullPath($AnalyticsDb)
if ([string]::IsNullOrWhiteSpace($BackupRoot)) {
    $BackupRoot = Join-Path ([System.IO.Path]::GetDirectoryName($dbPath)) "backup"
}

if (-not (Test-Path -LiteralPath $dbPath)) {
    Write-Host "分析库不存在：$dbPath（无旧表可归档，按幂等空操作处理）"
    exit 0
}

$pythonScript = @'
import datetime
import json
import os
import sqlite3
import sys

db_path = sys.argv[1]
backup_root = sys.argv[2]
restore = sys.argv[3] == "1"

ORIGINALS = ["analytics_sessions", "analytics_turns", "analytics_llm_requests"]
LEGACY = {name: "analytics_legacy_" + name[len("analytics_"):] for name in ORIGINALS}


def table_exists(conn, name):
    row = conn.execute(
        "SELECT name FROM sqlite_master WHERE type='table' AND name=?", (name,)
    ).fetchone()
    return row is not None


def export_table(conn, name, stamp):
    folder = os.path.join(backup_root, stamp)
    os.makedirs(folder, exist_ok=True)
    target = os.path.join(folder, name + ".json")
    cursor = conn.execute('SELECT * FROM "%s"' % name)
    columns = [description[0] for description in cursor.description]
    rows = []
    for raw in cursor.fetchall():
        row = {}
        for index, column in enumerate(columns):
            value = raw[index]
            if isinstance(value, (bytes, bytearray)):
                value = "<{} bytes>".format(len(value))
            row[column] = value
        rows.append(row)
    with open(target, "w", encoding="utf-8") as handle:
        json.dump({"table": name, "exported_at": datetime.datetime.now(datetime.timezone.utc).isoformat(), "row_count": len(rows), "rows": rows}, handle, ensure_ascii=False, indent=2)
    return target, len(rows)


conn = sqlite3.connect(db_path, timeout=5.0)
conn.execute("PRAGMA busy_timeout=5000")

if restore:
    restored = []
    for original, legacy in LEGACY.items():
        if table_exists(conn, legacy) and not table_exists(conn, original):
            conn.execute('ALTER TABLE "%s" RENAME TO "%s"' % (legacy, original))
            restored.append(legacy + " -> " + original)
    conn.commit()
    if not restored:
        print("already restored (no analytics_legacy_* tables to restore)")
    else:
        print("restored: " + ", ".join(restored))
    conn.close()
    sys.exit(0)

pending = [name for name in ORIGINALS if table_exists(conn, name)]
already = [LEGACY[name] for name in ORIGINALS if table_exists(conn, LEGACY[name])]

if not pending:
    if already:
        print("already archived: " + ", ".join(already))
    else:
        print("no legacy tables found: nothing to archive")
    conn.close()
    sys.exit(0)

stamp = datetime.datetime.now().strftime("%Y%m%d-%H%M%S")
exported = []
for name in pending:
    target, count = export_table(conn, name, stamp)
    exported.append((name, target, count))

for name in pending:
    conn.execute('ALTER TABLE "%s" RENAME TO "%s"' % (name, LEGACY[name]))
conn.commit()
conn.close()

for name, target, count in exported:
    print("exported {} ({} rows) -> {}".format(name, count, target))
print("archived: " + ", ".join(name + " -> " + LEGACY[name] for name in pending))
print("backup dir: " + os.path.join(backup_root, stamp))
'@

$tempScript = Join-Path $env:TEMP ("usage-analytics-archive-" + [System.Guid]::NewGuid().ToString("N") + ".py")
try {
    Set-Content -LiteralPath $tempScript -Value $pythonScript -Encoding UTF8
    $restoreFlag = if ($Restore) { "1" } else { "0" }
    & $python $tempScript $dbPath $BackupRoot $restoreFlag
    if ($LASTEXITCODE -ne 0) {
        Write-Error "归档脚本失败（python 退出码 $LASTEXITCODE）"
        exit 2
    }
}
finally {
    Remove-Item -LiteralPath $tempScript -Force -ErrorAction SilentlyContinue
}
exit 0
