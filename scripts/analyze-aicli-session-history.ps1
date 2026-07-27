[CmdletBinding()]
param(
    [string]$Root = (Join-Path $HOME ".aicli\chat-logs"),
    [ValidateRange(1, 1000)]
    [int]$Sessions = 30,
    [ValidateRange(0, 3650)]
    [int]$Days = 0,
    [string]$JsonOut = ""
)

Set-StrictMode -Version 2.0
$ErrorActionPreference = "Stop"

function Get-PropertyValue {
    param($InputObject, [string]$Name, $Default = $null)
    if ($null -eq $InputObject) { return $Default }
    $property = $InputObject.PSObject.Properties[$Name]
    if ($null -eq $property) { return $Default }
    return $property.Value
}

function Get-ResultMetadata {
    param($Result)
    $metadata = @{}
    foreach ($name in @("tool_metadata", "metadata")) {
        $value = Get-PropertyValue $Result $name
        if ($null -ne $value) {
            foreach ($property in $value.PSObject.Properties) {
                $metadata[$property.Name] = $property.Value
            }
        }
    }
    $protocol = Get-PropertyValue $Result "protocol_result"
    $protocolMetadata = Get-PropertyValue $protocol "metadata"
    if ($null -ne $protocolMetadata) {
        foreach ($property in $protocolMetadata.PSObject.Properties) {
            if (-not $metadata.ContainsKey($property.Name)) {
                $metadata[$property.Name] = $property.Value
            }
        }
    }
    return $metadata
}

function Get-ResultField {
    param($Result, [hashtable]$Metadata, [string]$Name, $Default = $null)
    $value = Get-PropertyValue $Result $Name $null
    if ($null -ne $value -and [string]$value -ne "") { return $value }
    if ($Metadata.ContainsKey($Name)) { return $Metadata[$Name] }
    return $Default
}

function Get-FailureCategory {
    param([string]$Code, [string]$Text)
    if ($Text -match "old_string|stale.context|hunk.*not found|failed to find context|无法定位 hunk") {
        return "stale_context"
    }
    switch ($Code.ToUpperInvariant()) {
        { $_ -in @("STALE_CONTEXT", "TOOL_STALE_CONTEXT") } { return "stale_context" }
        { $_ -in @("TOOL_TIMEOUT", "TURN_DEADLINE_EXCEEDED") } { return "timeout" }
        { $_ -in @("TOOL_PATH_NOT_FOUND", "PATH_NOT_FOUND") } { return "path_missing" }
        { $_ -in @("TOOL_INVALID_ARGS", "INVALID_ARGUMENT") } { return "invalid_args" }
        { $_ -in @("TOOL_SHELL_COMPAT", "SHELL_COMPAT") } { return "shell_compat" }
        { $_ -in @("SPAWN_DEPTH_LIMIT", "AGENT_SPAWN_DEPTH_LIMIT") } { return "spawn_depth" }
    }
    switch -Regex ($Text) {
        "old_string|stale.context|hunk.*not found|failed to find context" { return "stale_context" }
        "timeout|deadline exceeded|timed out" { return "timeout" }
        "no such file|cannot find path|path.*not found|路径不存在|pattern_file.*失败" { return "path_missing" }
        "ParserError|heredoc|unexpected token|not recognized|regex parse" { return "shell_compat" }
        "参数.*无效|required argument|invalid arg|schema" { return "invalid_args" }
        "exit status|process exited|command.*failed|FAIL\s" { return "execution" }
        default { return "other" }
    }
}

function Get-LLMFailureCategory {
    param([string]$Code, [string]$Text)
    $combined = "$Code $Text"
    switch -Regex ($combined) {
        "USER_CANCELLED|context canceled|operation canceled" { return "user_cancelled" }
        "tool_use.*without.*tool_result|corresponding.*tool_result" { return "invalid_tool_replay" }
        "max_tokens.*>.*maximum|maximum allowed number of output tokens" { return "max_tokens_limit" }
        "model_not_found|No available channel for model|model.*not found" { return "model_unavailable" }
        "预扣费|余额|quota|insufficient|PERMISSION_DENIED|HTTP 402|HTTP 403" { return "quota_or_permission" }
        "already owned by aicli-actor|session.*owned" { return "session_lease" }
        "stream_interrupted|empty response body|disconnected before completion|unknown codex stream" { return "stream_interrupted" }
        "failed to send request:.*EOF|\bEOF\b" { return "stream_interrupted" }
        "HTTP 503|HTTP 502|temporarily unavailable|CPU overload|E4004" { return "provider_unavailable" }
        "thinking\.adaptive\.effort|Extra inputs are not permitted" { return "invalid_request_shape" }
        "TLS|certificate|x509" { return "tls" }
        "HTTP 401|invalid api key|incorrect api key|AUTH" { return "authentication" }
        default { return "other" }
    }
}

function Convert-ToolResult {
    param($Message, [string]$SessionID)
    $result = Get-PropertyValue (Get-PropertyValue $Message "content") "result"
    $metadata = Get-ResultMetadata $result
    $ok = [bool](Get-ResultField $result $metadata "ok" $false)
    $outcome = [string](Get-ResultField $result $metadata "outcome" "")
    $empty = [bool](Get-ResultField $result $metadata "empty_result" $false)
    $empty = $empty -or [bool](Get-ResultField $result $metadata "search_no_match" $false)
    $redirected = [bool](Get-ResultField $result $metadata "shell_search_redirected" $false)
    $nonZero = [bool](Get-ResultField $result $metadata "non_zero_exit" $false)
    $class = if ($redirected) { "redirected" }
        elseif ($empty -or $outcome -eq "empty") { "empty" }
        elseif ($outcome -eq "partial") { "partial" }
        elseif (-not $ok -or $outcome -eq "failed") { "hard" }
        elseif ($nonZero) { "content_nonzero" }
        else { "success" }
    $error = [string](Get-ResultField $result $metadata "error" "")
    $summary = [string](Get-ResultField $result $metadata "summary" "")
    $protocolSummary = [string](Get-PropertyValue (Get-PropertyValue $result "protocol_result") "summary" "")
    $detail = if ($protocolSummary -and $protocolSummary -ne $summary) { "$summary`n$protocolSummary" } else { $summary }
    $code = [string](Get-ResultField $result $metadata "error_code" "")
    if (-not $code) {
        $protocolError = Get-PropertyValue (Get-PropertyValue $result "protocol_result") "error"
        $code = [string](Get-PropertyValue $protocolError "code" "")
    }
    [pscustomobject]@{
        session = $SessionID
        timestamp = [string](Get-PropertyValue $Message "timestamp" "")
        call_id = [string](Get-PropertyValue $Message "tool_call_id" "")
        tool = [string](Get-PropertyValue (Get-PropertyValue $Message "content") "function" "unknown")
        class = $class
        ok = $ok
        outcome = $outcome
        error_code = $code.ToUpperInvariant()
        category = Get-FailureCategory $code ("$error $detail")
        arg_preview = [string](Get-ResultField $result $metadata "arg_preview" "")
        error = $error
        summary = $summary
        detail = $detail
    }
}

if (-not (Test-Path -LiteralPath $Root)) {
    throw "aicli chat log directory does not exist: $Root"
}

$cutoff = if ($Days -gt 0) { (Get-Date).AddDays(-$Days) } else { [datetime]::MinValue }
$candidateDirs = Get-ChildItem -LiteralPath $Root -Directory -Recurse -ErrorAction SilentlyContinue |
    Where-Object {
        $_.LastWriteTime -ge $cutoff -and
        $null -ne (Get-ChildItem -LiteralPath $_.FullName -File -Filter "chat_*.json" -ErrorAction SilentlyContinue | Select-Object -First 1)
    } |
    Sort-Object LastWriteTime -Descending

$loadedSessions = [System.Collections.Generic.List[object]]::new()
$emptySessionCount = 0
foreach ($directory in $candidateDirs) {
    $messages = [System.Collections.Generic.List[object]]::new()
    $statuses = [System.Collections.Generic.List[string]]::new()
    $providers = [System.Collections.Generic.List[string]]::new()
    $parseErrors = 0
    foreach ($file in (Get-ChildItem -LiteralPath $directory.FullName -File -Filter "chat_*.json")) {
        try {
            $chat = Get-Content -LiteralPath $file.FullName -Raw -Encoding UTF8 | ConvertFrom-Json
        } catch {
            $parseErrors++
            continue
        }
        $status = [string](Get-PropertyValue $chat "status" "")
        if ($status) { $statuses.Add($status) }
        $provider = [string](Get-PropertyValue $chat "provider" "")
        $model = [string](Get-PropertyValue $chat "model" "")
        if ($provider -or $model) { $providers.Add("$provider/$model") }
        foreach ($message in @(Get-PropertyValue $chat "messages" @())) {
            $messages.Add($message)
        }
    }
    $meaningful = @($messages | Where-Object {
        [string](Get-PropertyValue $_ "message_type" "") -in @("request", "response", "tool_call", "tool_result")
    }).Count -gt 0
    $loaded = [pscustomobject]@{
        id = $directory.Name
        path = $directory.FullName
        last_write = $directory.LastWriteTime
        meaningful = $meaningful
        parse_errors = $parseErrors
        statuses = @($statuses | Select-Object -Unique)
        providers = @($providers | Select-Object -Unique)
        messages = @($messages)
    }
    if ($meaningful) {
        $loadedSessions.Add($loaded)
        if ($loadedSessions.Count -ge $Sessions) { break }
    } else {
        $emptySessionCount++
    }
}

$selected = @($loadedSessions)
if ($selected.Count -eq 0) {
    throw "No meaningful aicli sessions were found under $Root"
}

$toolResults = [System.Collections.Generic.List[object]]::new()
$sessionRows = [System.Collections.Generic.List[object]]::new()
$llmFailures = [System.Collections.Generic.List[object]]::new()
$goalNoops = [System.Collections.Generic.List[object]]::new()

foreach ($session in $selected) {
    $callIDs = @{}
    $resultIDs = @{}
    $requestIDs = @{}
    $responseIDs = @{}
    $sessionResults = [System.Collections.Generic.List[object]]::new()
    $llmRequests = 0
    $llmResponses = 0

    $messageIndex = 0
    foreach ($message in $session.messages) {
        $messageIndex++
        $messageType = [string](Get-PropertyValue $message "message_type" "")
        $callID = [string](Get-PropertyValue $message "tool_call_id" "")
        $requestID = [string](Get-PropertyValue $message "request_id" "")
        switch ($messageType) {
            "request" {
                $dedupeKey = if ($requestID) { $requestID } else { "request:$messageIndex" }
                if (-not $requestIDs.ContainsKey($dedupeKey)) {
                    $requestIDs[$dedupeKey] = $true
                    $llmRequests++
                }
            }
            "response" {
                $dedupeKey = if ($requestID) { $requestID } else { "response:$messageIndex" }
                if ($responseIDs.ContainsKey($dedupeKey)) { continue }
                $responseIDs[$dedupeKey] = $true
                $llmResponses++
                $content = Get-PropertyValue $message "content"
                $successValue = Get-PropertyValue $content "success" $null
                $errorText = [string](Get-PropertyValue $content "error" (Get-PropertyValue $message "error" ""))
                if (($null -ne $successValue -and -not [bool]$successValue) -or $errorText) {
                    $llmFailures.Add([pscustomobject]@{
                        session = $session.id
                        timestamp = [string](Get-PropertyValue $message "timestamp" "")
                        error_code = [string](Get-PropertyValue $content "error_code" "")
                        error = $errorText
                        category = Get-LLMFailureCategory ([string](Get-PropertyValue $content "error_code" "")) $errorText
                    })
                }
            }
            "tool_call" {
                if ($callID) { $callIDs[$callID] = $true }
            }
            "tool_result" {
                $dedupeKey = if ($callID) { $callID } else { "no-id:$($sessionResults.Count)" }
                if ($resultIDs.ContainsKey($dedupeKey)) { continue }
                $resultIDs[$dedupeKey] = $true
                $row = Convert-ToolResult $message $session.id
                $sessionResults.Add($row)
                $toolResults.Add($row)
                if ($row.tool -eq "update_goal" -and $row.class -eq "success" -and
                    ("$($row.detail) $($row.error)" -match '"updated"\s*:\s*false|goal_missing')) {
                    $goalNoops.Add($row)
                }
            }
        }
    }

    $hardCount = @($sessionResults | Where-Object class -eq "hard").Count
    $sessionRows.Add([pscustomobject]@{
        session = $session.id
        last_write = $session.last_write
        statuses = ($session.statuses -join ",")
        providers = ($session.providers -join ";")
        llm_requests = $llmRequests
        llm_responses = $llmResponses
        tool_calls = $callIDs.Count
        tool_results = $sessionResults.Count
        unmatched_calls = @($callIDs.Keys | Where-Object { -not $resultIDs.ContainsKey($_) }).Count
        hard_errors = $hardCount
        hard_error_rate = if ($sessionResults.Count) { $hardCount / $sessionResults.Count } else { 0 }
        parse_errors = $session.parse_errors
    })
}

$total = $toolResults.Count
$classes = @{}
foreach ($name in @("success", "hard", "empty", "partial", "content_nonzero", "redirected")) {
    $classes[$name] = @($toolResults | Where-Object class -eq $name).Count
}
$hardRate = if ($total) { $classes.hard / $total } else { 0 }
$nonFailRate = if ($total) { ($total - $classes.hard) / $total } else { 0 }
$llmResponseTotal = ($sessionRows | Measure-Object llm_responses -Sum).Sum
$llmFailureRate = if ($llmResponseTotal) { $llmFailures.Count / $llmResponseTotal } else { 0 }

$byTool = @($toolResults | Group-Object tool | ForEach-Object {
    $hard = @($_.Group | Where-Object class -eq "hard").Count
    [pscustomobject]@{
        tool = $_.Name
        calls = $_.Count
        hard = $hard
        hard_rate = if ($_.Count) { $hard / $_.Count } else { 0 }
        empty = @($_.Group | Where-Object class -eq "empty").Count
        partial = @($_.Group | Where-Object class -eq "partial").Count
        content_nonzero = @($_.Group | Where-Object class -eq "content_nonzero").Count
        redirected = @($_.Group | Where-Object class -eq "redirected").Count
    }
} | Sort-Object @{Expression = "hard"; Descending = $true}, @{Expression = "calls"; Descending = $true})

$byCategory = @($toolResults | Where-Object class -eq "hard" | Group-Object category | ForEach-Object {
    [pscustomobject]@{ category = $_.Name; count = $_.Count }
} | Sort-Object count -Descending)

$byErrorCode = @($toolResults | Where-Object class -eq "hard" | Group-Object {
    if ($_.error_code) { $_.error_code } else { "UNSTRUCTURED" }
} | ForEach-Object {
    [pscustomobject]@{ error_code = $_.Name; count = $_.Count }
} | Sort-Object count -Descending)

$failureExamples = @($toolResults | Where-Object class -eq "hard" | Select-Object -First 100 |
    Select-Object session, timestamp, tool, error_code, category, arg_preview, error, summary)
$llmFailureExamples = @($llmFailures | Select-Object -First 100)
$llmFailureCategories = @($llmFailures | Group-Object category | ForEach-Object {
    [pscustomobject]@{ category = $_.Name; count = $_.Count }
} | Sort-Object count -Descending)

$report = [ordered]@{
    generated_at = (Get-Date).ToString("o")
    root = $Root
    requested_sessions = $Sessions
    analyzed_sessions = $selected.Count
    ignored_empty_sessions = $emptySessionCount
    window = [ordered]@{
        newest = ($selected | Select-Object -First 1).last_write.ToString("o")
        oldest = ($selected | Select-Object -Last 1).last_write.ToString("o")
    }
    totals = [ordered]@{
        tool_results = $total
        hard_errors = $classes.hard
        hard_error_rate = $hardRate
        non_fail_rate = $nonFailRate
        classes = $classes
        llm_responses = $llmResponseTotal
        llm_failures = $llmFailures.Count
        llm_failure_rate = $llmFailureRate
        unmatched_tool_calls = ($sessionRows | Measure-Object unmatched_calls -Sum).Sum
        goal_update_noops = $goalNoops.Count
    }
    by_tool = $byTool
    hard_error_categories = $byCategory
    hard_error_codes = $byErrorCode
    llm_failure_categories = $llmFailureCategories
    sessions = @($sessionRows | Sort-Object last_write -Descending)
    hard_error_examples = $failureExamples
    llm_failure_examples = $llmFailureExamples
}

Write-Output "aicli recent session analysis"
Write-Output "window=$($report.window.oldest) .. $($report.window.newest)"
Write-Output "sessions=$($report.analyzed_sessions) ignored_empty_sessions=$($report.ignored_empty_sessions)"
Write-Output ("tool_results={0} hard={1} hard_error_rate={2:P2} non_fail_rate={3:P2}" -f $total, $classes.hard, $hardRate, $nonFailRate)
Write-Output ("empty={0} partial={1} content_nonzero={2} redirected={3}" -f $classes.empty, $classes.partial, $classes.content_nonzero, $classes.redirected)
Write-Output ("llm_responses={0} llm_failures={1} llm_failure_rate={2:P2} unmatched_tool_calls={3} goal_update_noops={4}" -f $llmResponseTotal, $llmFailures.Count, $llmFailureRate, $report.totals.unmatched_tool_calls, $goalNoops.Count)
Write-Output "`n=== BY TOOL ==="
$byTool | Format-Table tool, calls, hard, @{Name = "hard_rate"; Expression = { "{0:P2}" -f $_.hard_rate }}, empty, partial, content_nonzero, redirected -AutoSize |
    Out-String -Width 180 | Write-Output
Write-Output "=== HARD ERROR CATEGORIES ==="
$byCategory | Format-Table -AutoSize | Out-String -Width 120 | Write-Output
Write-Output "=== HARD ERROR CODES ==="
$byErrorCode | Format-Table -AutoSize | Out-String -Width 120 | Write-Output
Write-Output "=== LLM FAILURE CATEGORIES ==="
$llmFailureCategories | Format-Table -AutoSize | Out-String -Width 120 | Write-Output
Write-Output "=== BY SESSION ==="
$sessionRows | Sort-Object last_write -Descending |
    Format-Table session, llm_requests, llm_responses, tool_results, hard_errors, @{Name = "hard_rate"; Expression = { "{0:P2}" -f $_.hard_error_rate }}, unmatched_calls, statuses -AutoSize |
    Out-String -Width 180 | Write-Output

if ($JsonOut) {
    $resolvedOut = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($JsonOut)
    $parent = Split-Path -Parent $resolvedOut
    if ($parent) { New-Item -ItemType Directory -Force -Path $parent | Out-Null }
    $report | ConvertTo-Json -Depth 12 | Set-Content -LiteralPath $resolvedOut -Encoding UTF8
    Write-Output "json_report=$resolvedOut"
}
