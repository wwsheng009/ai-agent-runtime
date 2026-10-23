param(
  [string]$BaseUrl = 'http://172.18.3.238:18765/mcp',
  [string]$EnvFile = '',
  [string]$TokenVariable = 'AICLI_REMOTE_TOKEN',
  [string]$Scenario = 'smoke',
  [int]$Count = 100,
  [switch]$ListTools
)

$ErrorActionPreference = 'Stop'

function Get-CanaryToken {
  $value = [Environment]::GetEnvironmentVariable($TokenVariable)
  if ($value) { return $value.Trim() }
  if (-not $EnvFile -or -not (Test-Path -LiteralPath $EnvFile)) {
    throw "missing token: set `$env:$TokenVariable or pass -EnvFile"
  }
  $match = Select-String -LiteralPath $EnvFile -Pattern "^$([regex]::Escape($TokenVariable))=(.+)$" |
    Select-Object -First 1
  if (-not $match) { throw "missing $TokenVariable in $EnvFile" }
  return $match.Matches[0].Groups[1].Value.Trim()
}

function ConvertFrom-SseResponse {
  param([string]$Content)
  $data = $Content -split "`r?`n" |
    Where-Object { $_ -match '^data:\s*' } |
    Select-Object -First 1
  if ($data) {
    $payload = $data -replace '^data:\s*', ''
  } else {
    $payload = $Content.Trim()
  }
  if (-not $payload) { return $null }
  return $payload | ConvertFrom-Json
}

$token = Get-CanaryToken
$headers = @{
  Authorization = "Bearer $token"
  Accept = 'application/json, text/event-stream'
  'Content-Type' = 'application/json'
}
$script:SessionId = ''

function Invoke-Mcp {
  param(
    [Parameter(Mandatory = $true)][string]$Method,
    [hashtable]$Params = @{},
    [int]$Id = 1,
    [switch]$Notification
  )
  $payload = @{ jsonrpc = '2.0'; method = $Method }
  if (-not $Notification) {
    $payload.id = $Id
    $payload.params = $Params
  } else {
    $payload.params = $Params
  }
  $requestHeaders = @{} + $headers
  if ($script:SessionId) { $requestHeaders['Mcp-Session-Id'] = $script:SessionId }
  $body = $payload | ConvertTo-Json -Depth 20 -Compress
  try {
    $response = Invoke-WebRequest -Uri $BaseUrl -Method Post -Headers $requestHeaders `
      -Body $body -TimeoutSec 120
  } catch {
    if ($Notification) { return $null }
    throw
  }
  $session = $response.Headers['Mcp-Session-Id'] | Select-Object -First 1
  if ($session) { $script:SessionId = $session }
  return ConvertFrom-SseResponse -Content ([string]$response.Content)
}

$init = Invoke-Mcp -Method 'initialize' -Id 1 -Params @{
  protocolVersion = '2025-03-26'
  capabilities = @{}
  clientInfo = @{ name = 'toolcore-238-canary'; version = '1' }
}
if (-not $init -or $init.error) { throw "initialize failed: $($init.error.message)" }
Invoke-Mcp -Method 'notifications/initialized' -Notification | Out-Null

$listed = Invoke-Mcp -Method 'tools/list' -Id 2
if (-not $listed -or $listed.error) { throw "tools/list failed: $($listed.error.message)" }
$tools = @($listed.result.tools)
$toolNames = @($tools | ForEach-Object { $_.name })

if ($ListTools) {
  $tools | ConvertTo-Json -Depth 30
  exit 0
}

$missing = @('remote_238__view', 'remote_238__ls', 'remote_238__glob',
  'remote_238__grep', 'remote_238__shell', 'remote_238__artifact_read') |
  Where-Object { $_ -notin $toolNames }
if ($missing.Count -gt 0) {
  throw "missing canary tools: $($missing -join ', ')"
}

function Get-ArtifactId {
  param([object]$Result)
  $json = $Result | ConvertTo-Json -Depth 20 -Compress
  $match = [regex]::Match($json, 'art_[0-9a-f]{16,}')
  if ($match.Success) { return $match.Value }
  return ''
}

function Invoke-CanaryTool {
  param(
    [string]$Name,
    [hashtable]$Arguments,
    [int]$Id
  )
  $watch = [Diagnostics.Stopwatch]::StartNew()
  $response = Invoke-Mcp -Method 'tools/call' -Id $Id -Params @{
    name = $Name
    arguments = $Arguments
  }
  $watch.Stop()
  if (-not $response) { throw "$Name returned no response" }
  if ($response.error) { throw "$Name RPC error: $($response.error.message)" }
  $result = $response.result
  $text = @($result.content | ForEach-Object { $_.text }) -join "`n"
  $artifact = Get-ArtifactId -Result $result
  [pscustomobject]@{
    tool = $Name
    ok = -not [bool]$result.isError
    bytes = [Text.Encoding]::UTF8.GetByteCount($text)
    artifact_id = $artifact
    duration_ms = $watch.ElapsedMilliseconds
    preview = if ($text.Length -gt 180) { $text.Substring(0, 180) } else { $text }
  }
}

$results = @()
switch ($Scenario) {
  'smoke' {
    $results += Invoke-CanaryTool 'remote_238__view' @{
      file_path = 'README.md'
      limit = 20
    } 10
    $results += Invoke-CanaryTool 'remote_238__ls' @{
      path = '.'
    } 11
    $results += Invoke-CanaryTool 'remote_238__glob' @{
      pattern = '**/*.md'
      path = '.'
    } 12
    $results += Invoke-CanaryTool 'remote_238__grep' @{
      pattern = 'README'
      path = '.'
      glob = '*.md'
    } 13
    $results += Invoke-CanaryTool 'remote_238__shell' @{
      command = 'git status'
    } 14
    $artifactSource = Invoke-CanaryTool 'remote_238__shell' @{
      command = 'git log'
    } 15
    $results += $artifactSource
    $artifact = $artifactSource.artifact_id
    if (-not $artifact) {
      throw 'git log did not produce an artifact_id; cannot exercise artifact_read'
    }
    $results += Invoke-CanaryTool 'remote_238__artifact_read' @{
      artifact_id = $artifact
      offset = 0
      limit = 4096
    } 16
    $scopeResponse = Invoke-Mcp -Method 'tools/call' -Id 17 -Params @{
      name = 'remote_238__artifact_read'
      arguments = @{ artifact_ref = "remote:999:$artifact"; offset = 0; limit = 64 }
    }
    $scopeText = @($scopeResponse.result.content | ForEach-Object { $_.text }) -join "`n"
    $scopeDetail = if ($scopeResponse.error) {
      $scopeResponse.error | ConvertTo-Json -Depth 8 -Compress
    } else {
      $scopeResponse.result | ConvertTo-Json -Depth 8 -Compress
    }
    $results += [pscustomobject]@{
      tool = 'remote_238__artifact_read_scope'
      ok = [bool]$scopeResponse.result.isError -and
        $scopeText -match 'ARTIFACT_SCOPE_DENIED|artifact scope denied'
      bytes = [Text.Encoding]::UTF8.GetByteCount($scopeText)
      artifact_id = ''
      duration_ms = 0
      preview = if ($scopeText.Length -gt 180) { $scopeText.Substring(0, 180) } else { $scopeText }
      detail = $scopeDetail
    }
  }
  'soak' {
    $artifactSource = Invoke-CanaryTool 'remote_238__shell' @{
      command = 'git log'
    } 15
    $artifact = $artifactSource.artifact_id
    if (-not $artifact) { throw 'artifact_id missing for soak' }
    $calls = @(
      @{ name = 'remote_238__view'; args = @{ file_path = 'README.md'; limit = 20 } },
      @{ name = 'remote_238__ls'; args = @{ path = '.' } },
      @{ name = 'remote_238__glob'; args = @{ pattern = '**/*.md'; path = '.' } },
      @{ name = 'remote_238__grep'; args = @{ pattern = 'README'; path = '.'; glob = '*.md' } },
      @{ name = 'remote_238__shell'; args = @{ command = 'git status' } },
      @{ name = 'remote_238__artifact_read'; args = @{ artifact_id = $artifact; offset = 0; limit = 4096 } }
    )
    for ($i = 0; $i -lt $Count; $i++) {
      $call = $calls[$i % $calls.Count]
      $results += Invoke-CanaryTool $call.name $call.args (100 + $i)
    }
    $failed = @($results | Where-Object { -not $_.ok })
    $durations = @($results | ForEach-Object { $_.duration_ms } | Sort-Object)
    $p50 = if ($durations.Count) { $durations[[Math]::Floor(($durations.Count - 1) * 0.50)] } else { 0 }
    $p95 = if ($durations.Count) { $durations[[Math]::Floor(($durations.Count - 1) * 0.95)] } else { 0 }
    [pscustomobject]@{
      scenario = 'soak'
      count = $results.Count
      ok = $results.Count - $failed.Count
      failed = $failed.Count
      error_rate = if ($results.Count) { [Math]::Round($failed.Count / $results.Count, 4) } else { 0 }
      p50_ms = $p50
      p95_ms = $p95
      artifact_id = $artifact
      tools = @($results | Group-Object tool | ForEach-Object {
        [pscustomobject]@{
          tool = $_.Name
          count = $_.Count
          ok = @($_.Group | Where-Object { $_.ok }).Count
          failed = @($_.Group | Where-Object { -not $_.ok }).Count
        }
      })
    } | ConvertTo-Json -Depth 8
    if ($failed.Count -gt 0) {
      $failed | ConvertTo-Json -Depth 8
      exit 1
    }
    exit 0
  }
  default { throw "unknown scenario: $Scenario" }
}

$results | ConvertTo-Json -Depth 8
if (@($results | Where-Object { -not $_.ok }).Count -gt 0) { exit 1 }
