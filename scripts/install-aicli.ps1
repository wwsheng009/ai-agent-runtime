<#
.SYNOPSIS
  从 GitHub Release 下载并安装 aicli 到用户可执行目录（Windows）。

.DESCRIPTION
  默认安装到 %LOCALAPPDATA%\Programs\aicli，并自动追加到当前用户 PATH。
  自动识别 amd64 / arm64。

.EXAMPLE
  iwr -useb https://raw.githubusercontent.com/wwsheng009/ai-agent-runtime/main/scripts/install-aicli.ps1 | iex

.EXAMPLE
  $env:AICLI_VERSION = 'v0.1.0'; ./install-aicli.ps1

.PARAMETER Version
  版本 tag（默认 latest 或 $env:AICLI_VERSION）。

.PARAMETER InstallDir
  安装目录（默认 %LOCALAPPDATA%\Programs\aicli 或 $env:AICLI_INSTALL_DIR）。
#>
[CmdletBinding()]
param(
  [string]$Version = $env:AICLI_VERSION,
  [string]$InstallDir = $env:AICLI_INSTALL_DIR,
  [string]$Repo = $(if ($env:AICLI_REPO) { $env:AICLI_REPO } else { 'wwsheng009/ai-agent-runtime' })
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$BinName = 'aicli'

if ([string]::IsNullOrWhiteSpace($Version))     { $Version = 'latest' }
if ([string]::IsNullOrWhiteSpace($InstallDir))  { $InstallDir = Join-Path $env:LOCALAPPDATA 'Programs\aicli' }

function Info($msg) { Write-Host "[INFO] $msg" -ForegroundColor Green }
function Warn($msg) { Write-Host "[WARN] $msg" -ForegroundColor Yellow }
function Die ($msg) { Write-Host "[ERR ] $msg" -ForegroundColor Red; exit 1 }

# ---- 架构识别 ----
$arch = switch -Regex ($env:PROCESSOR_ARCHITECTURE) {
  '^ARM64$'        { 'arm64'; break }
  '^AMD64$|^x86$'  { 'amd64'; break }
  default          { 'amd64' }
}

# ---- 解析版本 ----
if ($Version -eq 'latest') {
  Info '查询最新版本...'
  try {
    $rel = Invoke-RestMethod -UseBasicParsing -Uri "https://api.github.com/repos/$Repo/releases/latest" `
      -Headers @{ 'User-Agent' = 'aicli-installer' }
    $Version = $rel.tag_name
  } catch {
    Die "无法获取最新版本: $($_.Exception.Message)"
  }
}
Info "目标版本: $Version (windows/$arch)"

$archive = "$BinName-$Version-windows-$arch.zip"
$url = "https://github.com/$Repo/releases/download/$Version/$archive"

$tmp = Join-Path $env:TEMP ([Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp -Force | Out-Null

try {
  $zipPath = Join-Path $tmp $archive
  Info "下载 $url"
  try {
    Invoke-WebRequest -Uri $url -OutFile $zipPath -UseBasicParsing
  } catch {
    Die "下载失败: $url`n$($_.Exception.Message)"
  }

  # ---- sha256 校验 ----
  $sumPath = "$zipPath.sha256"
  try {
    Invoke-WebRequest -Uri "$url.sha256" -OutFile $sumPath -UseBasicParsing
    $expected = ((Get-Content -Raw $sumPath).Trim() -split '\s+')[0]
    $actual = (Get-FileHash -Algorithm SHA256 $zipPath).Hash.ToLower()
    if ($expected.ToLower() -ne $actual) {
      Die "sha256 校验失败: expect=$expected got=$actual"
    }
    Info 'sha256 校验通过'
  } catch {
    Warn "跳过 sha256 校验: $($_.Exception.Message)"
  }

  # ---- 解压 & 安装 ----
  Expand-Archive -Path $zipPath -DestinationPath $tmp -Force
  $srcExe = Join-Path $tmp "$BinName.exe"
  if (-not (Test-Path $srcExe)) { Die "归档中未找到 $BinName.exe" }

  if (-not (Test-Path $InstallDir)) {
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
  }
  $dstExe = Join-Path $InstallDir "$BinName.exe"

  # 如果目标正在运行，Copy-Item 会失败，给个清晰提示
  try {
    Copy-Item -Path $srcExe -Destination $dstExe -Force
  } catch {
    Die "拷贝二进制失败（可能 $BinName.exe 正在运行）: $($_.Exception.Message)"
  }
  Info "已安装 $BinName.exe -> $dstExe"

  # ---- 追加到用户 PATH ----
  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  $parts = @()
  if ($userPath) { $parts = $userPath -split ';' | Where-Object { $_ -ne '' } }
  $alreadyOnPath = $parts | Where-Object { $_.TrimEnd('\') -ieq $InstallDir.TrimEnd('\') }
  if (-not $alreadyOnPath) {
    $newPath = if ($userPath) { "$userPath;$InstallDir" } else { $InstallDir }
    [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
    Info "已将 $InstallDir 加入用户 PATH，新开终端生效"
  } else {
    Info "$InstallDir 已在用户 PATH"
  }

  # 当前会话内立即可用
  if (-not (($env:Path -split ';') | Where-Object { $_.TrimEnd('\') -ieq $InstallDir.TrimEnd('\') })) {
    $env:Path = "$env:Path;$InstallDir"
  }

  & $dstExe version 2>$null
} finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
