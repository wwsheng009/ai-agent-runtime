<#
.SYNOPSIS
  aicli compat-mode E2E（E2E-COMPAT-01）：--compat-mode 基本可用性（退役方案 §6 第 4 条）。

.DESCRIPTION
  验收目标（全部通过才退出码 0；断言名固定，便于机器判读）：

    1. compat/no-tui-start：--debug 诊断证明走 compat 分支（plain interactive mode +
       控制台行输入），且未初始化 TUI surface（stderr 无 "surface.Enable()" 诊断行）。
    2. compat/mock-roundtrip：本地 mock provider 收到至少一轮 chat 请求（含用户消息）。
    3. compat/roundtrip-reply：助手输出落 stdout（标记 MOCK-COMPAT-REPLY-OK）。
    4. compat/exit-graceful：管道注入 /exit → 进程正常退出（退出码 0），stdout 出现告别语。
    5. compat/no-unified-render-bytes：stdout 不出现 unified 渲染窗口字节
       （备用屏 ?1049 / DEC2026 同步帧 ?2026 / DECSTBM 滚动区 \e[<n>;<m>r）。

  载体说明（退役方案 §6）：
    - compat 场景无 TUI，会话级行为用管道 harness 覆盖（in-process pipe 等价物：
      被测二进制 + 独立进程 stdin/stdout 管道）；真机项（wt 交互渲染）以
      test-aicli-windows-terminal-e2e.ps1 为准。
    - 无 ANSI 降级提示（surface.Enable() 失败路径）依赖非 VT 终端（Win7 conhost /
      非 VT），本机 VT 终端不可复现，留人工真机；代码锚点 commands/chat_setup.go:89-122。
    - mock provider 为本地 python http.server（/v1/chat/completions，非流式 JSON + SSE
      两形态），请求/响应落盘 artifacts 供审计；不读取、不触碰用户凭据与真实 provider。

.PARAMETER ExePath
  被测 aicli 可执行文件；缺省 <repo>/backend/.tmp/aicli-compat-e2e.exe。
.PARAMETER SkipBuild
  跳过 go build，直接使用 ExePath（要求文件已存在，便于复跑）。
.PARAMETER ArtifactDir
  证据目录；缺省 artifacts/aicli-compat-e2e/<yyyyMMdd-HHmmss>。
.PARAMETER TimeoutSeconds
  等待被测进程退出的超时（秒），缺省 120。

.EXAMPLE
  pwsh -NoProfile -File scripts/test-aicli-compat-mode-e2e.ps1

.EXAMPLE
  pwsh -NoProfile -File scripts/test-aicli-compat-mode-e2e.ps1 -SkipBuild
#>
[CmdletBinding()]
param(
    [string]$ExePath,
    [switch]$SkipBuild,
    [string]$ArtifactDir,
    [ValidateRange(10, 900)][int]$TimeoutSeconds = 120
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 3.0

$scenarioId = 'E2E-COMPAT-01'
$repoRoot = Split-Path -Parent $PSScriptRoot
$backend = Join-Path $repoRoot 'backend'
if (-not $ExePath) { $ExePath = Join-Path $backend '.tmp\aicli-compat-e2e.exe' }
if (-not $ArtifactDir) {
    $stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
    $ArtifactDir = Join-Path $repoRoot "artifacts\aicli-compat-e2e\$stamp"
}
New-Item -ItemType Directory -Path $ArtifactDir -Force | Out-Null
$runLog = Join-Path $ArtifactDir 'run.log'
$results = [System.Collections.Generic.List[object]]::new()

function Add-Result {
    param([Parameter(Mandatory)][string]$Name, [Parameter(Mandatory)][bool]$Ok, [string]$Detail = '')
    $status = if ($Ok) { 'pass' } else { 'fail' }
    $results.Add([pscustomobject]@{ name = $Name; status = $status; detail = $Detail })
    $line = "[$status] $Name - $Detail"
    Write-Host $line
    Add-Content -LiteralPath $runLog -Value $line -Encoding utf8
}

function Get-FreePort {
    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    $listener.Start()
    try { return ([System.Net.IPEndPoint]$listener.LocalEndpoint).Port } finally { $listener.Stop() }
}

function Wait-TcpReady {
    param([int]$Port, [int]$TimeoutMs = 10000)
    $deadline = [DateTime]::UtcNow.AddMilliseconds($TimeoutMs)
    while ([DateTime]::UtcNow -lt $deadline) {
        try {
            $client = [System.Net.Sockets.TcpClient]::new()
            $client.Connect('127.0.0.1', $Port)
            $client.Close()
            return $true
        } catch {
            Start-Sleep -Milliseconds 200
        }
    }
    return $false
}

Write-Host "==== $scenarioId · $ArtifactDir ===="

# 1) 构建被测二进制（缺省）。
if (-not $SkipBuild) {
    Push-Location $backend
    try {
        & go build -trimpath -o $ExePath ./cmd/aicli 2>&1 | Tee-Object -FilePath (Join-Path $ArtifactDir 'go-build.log')
        if ($LASTEXITCODE -ne 0) { throw "go build failed (rc=$LASTEXITCODE)" }
    } finally { Pop-Location }
}
if (-not (Test-Path -LiteralPath $ExePath -PathType Leaf)) {
    throw "被测二进制不存在: $ExePath（去掉 -SkipBuild 或先构建）"
}

# 2) 本地 mock provider（python http.server；非流式 JSON + SSE 两形态）。
$mockPy = Join-Path $ArtifactDir 'mock_llm.py'
@'
import json
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(sys.argv[1])
LOG_DIR = sys.argv[2]
MARKER = "MOCK-COMPAT-REPLY-OK"


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def _read_body(self):
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length) if length else b""
        try:
            return json.loads(raw or b"{}")
        except Exception:
            return {}

    def _log(self, path, body):
        entry = {"path": path, "body": body}
        with open(LOG_DIR + "/mock-request.jsonl", "a", encoding="utf-8") as f:
            f.write(json.dumps(entry, ensure_ascii=False) + "\n")

    def do_POST(self):
        body = self._read_body()
        self._log(self.path, body)
        model = body.get("model") or "mock-model"
        if body.get("stream"):
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Cache-Control", "no-cache")
            self.end_headers()
            chunks = [
                {"id": "mock-1", "object": "chat.completion.chunk", "created": 0, "model": model,
                 "choices": [{"index": 0, "delta": {"role": "assistant", "content": MARKER}, "finish_reason": None}]},
                {"id": "mock-1", "object": "chat.completion.chunk", "created": 0, "model": model,
                 "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}]},
            ]
            for chunk in chunks:
                self.wfile.write(("data: " + json.dumps(chunk, ensure_ascii=False) + "\n\n").encode("utf-8"))
            self.wfile.write(b"data: [DONE]\n\n")
            return
        payload = {
            "id": "mock-1", "object": "chat.completion", "created": 0, "model": model,
            "choices": [{"index": 0, "message": {"role": "assistant", "content": MARKER}, "finish_reason": "stop"}],
            "usage": {"prompt_tokens": 5, "completion_tokens": 5, "total_tokens": 10},
        }
        data = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        data = json.dumps({"object": "list", "data": [{"id": "mock-model", "object": "model"}]}).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


ThreadingHTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
'@ | Set-Content -LiteralPath $mockPy -Encoding utf8

$port = Get-FreePort
$mockProc = Start-Process -FilePath 'python' -ArgumentList @($mockPy, "$port", $ArtifactDir) -PassThru -WindowStyle Hidden
$mockReady = $false
try {
    $mockReady = Wait-TcpReady -Port $port -TimeoutMs 10000
} finally {
    if (-not $mockReady -and $mockProc -and -not $mockProc.HasExited) {
        Stop-Process -Id $mockProc.Id -Force -ErrorAction SilentlyContinue
    }
}
Add-Result -Name 'compat/mock-ready' -Ok $mockReady -Detail "port=$port"
if (-not $mockReady) {
    throw "本地 mock provider 未就绪（python 不可用或端口被占）"
}

try {
# 3) 临时配置 + 隔离目录（不触碰用户 ~/.aicli 与真实凭据）。
$configPath = Join-Path $ArtifactDir 'config.yaml'
@"
providers:
  default_provider: mockcompat
  items:
    mockcompat:
      api_key: compat-e2e-local-key
      base_url: http://127.0.0.1:$port
      api_path: ""
      forward_url: /v1/chat/completions
      protocol: openai
      default_model: mock-model
      enabled: true
      supported_models:
        - mock-model
aicli:
  chat:
    default_provider: mockcompat
    default_model: mock-model
    reasoning_effort: low
"@ | Set-Content -LiteralPath $configPath -Encoding utf8

$sessionDir = Join-Path $ArtifactDir 'sessions'
$logDir = Join-Path $ArtifactDir 'chat-logs'
$workDir = Join-Path $ArtifactDir 'work'
New-Item -ItemType Directory -Path $sessionDir, $logDir, $workDir -Force | Out-Null

# 4) 运行被测进程：compat-mode + headless + 管道输入（hi → 等待 → /exit）。
$psi = [System.Diagnostics.ProcessStartInfo]::new()
$psi.FileName = $ExePath
$psi.Arguments = @(
    'chat', '--compat-mode', '--headless', '--debug', '--stream', '--disable-tools',
    '--provider', 'mockcompat', '--model', 'mock-model', '--reasoning-effort', 'low',
    '--config', "`"$configPath`"",
    '--session-dir', "`"$sessionDir`"", '--user', 'compat-e2e',
    '--log-dir', "`"$logDir`""
) -join ' '
$psi.WorkingDirectory = $workDir
$psi.UseShellExecute = $false
$psi.RedirectStandardInput = $true
$psi.RedirectStandardOutput = $true
$psi.RedirectStandardError = $true
$psi.StandardOutputEncoding = [System.Text.Encoding]::UTF8
$psi.StandardErrorEncoding = [System.Text.Encoding]::UTF8

$proc = [System.Diagnostics.Process]::new()
$proc.StartInfo = $psi
[void]$proc.Start()
$stdoutTask = $proc.StandardOutput.ReadToEndAsync()
$stderrTask = $proc.StandardError.ReadToEndAsync()
try {
    $proc.StandardInput.Write("hi`n")
    $proc.StandardInput.Flush()
    Start-Sleep -Milliseconds 1500
    $proc.StandardInput.Write("/exit`n")
    $proc.StandardInput.Flush()
    $proc.StandardInput.Close()
} catch {
    Add-Result -Name 'compat/stdin-inject' -Ok $false -Detail $_.Exception.Message
}
$exited = $proc.WaitForExit($TimeoutSeconds * 1000)
if (-not $exited) {
    try { $proc.Kill($true) } catch { }
}
$stdout = if ($stdoutTask.Wait(5000)) { $stdoutTask.Result } else { '' }
$stderr = if ($stderrTask.Wait(5000)) { $stderrTask.Result } else { '' }
$exitCode = if ($exited) { $proc.ExitCode } else { -1 }
$stdout | Set-Content -LiteralPath (Join-Path $ArtifactDir 'aicli.stdout.log') -Encoding utf8
$stderr | Set-Content -LiteralPath (Join-Path $ArtifactDir 'aicli.stderr.log') -Encoding utf8

# 5) 断言。
$esc = [char]27
$hasCompatDiag = $stderr.Contains('[aicli-diag] compat mode: plain interactive mode')
$hasSurfaceEnableDiag = $stderr.Contains('[aicli-diag] surface.Enable()')
Add-Result -Name 'compat/no-tui-start' -Ok ($hasCompatDiag -and -not $hasSurfaceEnableDiag) `
    -Detail "compat-diag=$hasCompatDiag surface-enable-diag=$hasSurfaceEnableDiag"

$mockLog = Join-Path $ArtifactDir 'mock-request.jsonl'
$mockRequests = @()
if (Test-Path -LiteralPath $mockLog) { $mockRequests = @(Get-Content -LiteralPath $mockLog -Encoding utf8) }
$roundtripLogged = ($mockRequests.Count -ge 1) -and (($mockRequests -join "`n").Contains('"hi"'))
Add-Result -Name 'compat/mock-roundtrip' -Ok $roundtripLogged -Detail "mock_requests=$($mockRequests.Count)"

Add-Result -Name 'compat/roundtrip-reply' -Ok ($stdout.Contains('MOCK-COMPAT-REPLY-OK')) `
    -Detail "stdout_bytes=$($stdout.Length)"

$exitOk = ($exited -and $exitCode -eq 0) -and $stdout.Contains('再见')
Add-Result -Name 'compat/exit-graceful' -Ok $exitOk -Detail "exit_code=$exitCode farewell=$($stdout.Contains('再见'))"

$hasAltScreen = $stdout.Contains("$esc[?1049")
$hasSyncFrames = $stdout.Contains("$esc[?2026")
$hasScrollRegion = [regex]::IsMatch($stdout, [regex]::Escape($esc) + '\[\d+;\d+r')
$noRenderBytes = -not ($hasAltScreen -or $hasSyncFrames -or $hasScrollRegion)
Add-Result -Name 'compat/no-unified-render-bytes' -Ok $noRenderBytes `
    -Detail "alt_screen=$hasAltScreen sync_frames=$hasSyncFrames scroll_region=$hasScrollRegion"

} finally {
    # 收尾：无论成败都停 mock。
    if ($mockProc -and -not $mockProc.HasExited) {
        Stop-Process -Id $mockProc.Id -Force -ErrorAction SilentlyContinue
    }
}

# 6) 写 summary。
$failed = @($results | Where-Object { $_.status -eq 'fail' })
$summary = [pscustomobject]@{
    scenario = $scenarioId
    stamp    = (Split-Path -Leaf $ArtifactDir)
    pass     = @($results | Where-Object { $_.status -eq 'pass' }).Count
    fail     = $failed.Count
    skip     = 0
    results  = $results
    artifacts = @{
        stdout  = 'aicli.stdout.log'
        stderr  = 'aicli.stderr.log'
        mock    = 'mock-request.jsonl'
        config  = 'config.yaml'
    }
    notes = @(
        '无 ANSI 降级提示（surface.Enable() 失败路径）依赖非 VT 终端（Win7 conhost），本机不可复现，留人工真机',
        '真机交互渲染项以 test-aicli-windows-terminal-e2e.ps1 为准'
    )
}
$summary | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath (Join-Path $ArtifactDir 'summary.json') -Encoding utf8

Write-Host "==== $scenarioId 结果：pass=$($summary.pass) fail=$($summary.fail) ===="
if ($failed.Count -gt 0) {
    $failed | ForEach-Object { Write-Host "  FAIL $($_.name): $($_.detail)" }
    exit 1
}
exit 0
