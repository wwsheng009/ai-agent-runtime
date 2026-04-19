# Skills API Client

> 迁移说明（2026-03-30）：
> - `skills / agent` HTTP 服务、`pkg/skillsapi` 与 `cmd/skillsapi-demo` 的当前实现位于 `E:\projects\ai\ai-agent-runtime\backend`
> - `ai-gateway` 已不再挂载 `/api/agent`、`/api/skills`
> - 下面示例默认面向独立 `runtime-server`，推荐监听地址为 `http://127.0.0.1:8081`

## 目标

为 `skills / agent` HTTP 接口提供一层可复用的 Go client，而不是让调用方自己拼接 URL、query 和鉴权头。

当前客户端实现位于：

- `E:\projects\ai\ai-agent-runtime\backend\pkg\skillsapi\client.go`

可运行示例命令：

- `E:\projects\ai\ai-agent-runtime\backend\cmd\skillsapi-demo\main.go`

## 当前能力

已支持：

- `ListSkills`
- `GetSkill`
- `SearchSkills`
- `GetStats`
- `ExportSkills`
- `ExecuteSkill`
- `AgentChat`
- `AgentChatStream`
- `CreateSkill`
- `UpdateSkill`
- `DeleteSkill`
- `ImportSkills`
- `ReloadSkills`
- `StartHotReload`
- `StopHotReload`
- `ReloadHotReload`
- `GetHotReloadStats`
- `GetSearchStats`
- `ReindexSearchIndex`
- `GetGovernancePolicy`
- `GetCapabilities`
- `GetRuntimeStatus`
- `GetRuntimeHealth`
- `ReloadRuntimeMCPs`
- `ValidateRuntime`
- `GetUsageStats`
- `ResetUsageStats`
- `GetUsagePolicy`
- `GetMutationPolicy`
- `UpdateMutationPolicy`
- `GetAuthPolicy`
- `UpdateAuthPolicy`
- `DeleteAuthPolicyEntry`
- `UpdateUsagePolicy`
- `DeleteUsagePolicyEntry`
- `GetUsageLedger`
- `CreateSession`
- `ListSessions`
- `GetSession`
- `UpdateSession`
- `DeleteSession`
- `ReportTaskOutcome`
- `CompleteTask`
- `FailTask`
- `BlockTask`
- `GetTeamTask`
- `ListTeamTasks`
- `ListTaskDependencies`
- `ListTaskDependents`
- `CreateTeamTask`
- `UpdateTeamTask`
- `AddTaskDependency`
- `SendTeamMailboxMessage`
- `CreateTeam`
- `ListTeams`
- `ListTeammates`
- `PlanTeamTasks`
- `GetTaskGraph`
- `ClaimReadyTasks`
- `ReclaimExpiredTasks`
- `MarkReadyTasks`
- `ReplanTask`
- `ListTeamMailbox`
- `AckTeamMailboxMessage`
- `GetSessionHistory`
- `ClearSessionHistory`
- `GetSessionStats`
- `SearchSessions`
- `ArchiveSession`
- `ActivateSession`
- `CloseSession`
- `BatchDeleteSessions`
- `BatchArchiveSessions`
- `SpawnSessionAgent`
- `GetSessionAgentStatus`
- `SendSessionAgentInput`
- `WaitSessionAgents`
- `ListSessionAgentEvents`
- `CloseSessionAgent`
- `ResumeSessionAgent`

说明：

- 轻量 child-agent 控制面已经提供独立 HTTP API：`/api/runtime/sessions/{id}/agents*`
- 现在 `pkg/skillsapi` 也已提供 typed helper；HTTP 语义与 `curl` 示例见 `docs/skill_runtime/session_agent_api.md`

## 初始化

```go
client := skillsapi.NewClient(
  "http://127.0.0.1:8081",
  skillsapi.WithAdminToken("your-admin-token"),
)
```

可选项：

- `WithAdminToken(token)`
- `WithTenantID(tenantID)`
- `WithProjectID(projectID)`
- `WithHTTPClient(client)`
- `WithHeader(key, value)`

如果服务端启用了 scope resolver，也可以直接传认证头：

```go
client := skillsapi.NewClient(
  "http://127.0.0.1:8081",
  skillsapi.WithHeader("X-User-ID", "alice"),
  skillsapi.WithHeader("X-Tenant-ID", "team-a"),
  skillsapi.WithHeader("X-Project-ID", "ops"),
)
```

如果服务端启用了 JWT claims scope resolver，也可以直接透传 Bearer JWT：

```go
client := skillsapi.NewClient(
  "http://127.0.0.1:8081",
  skillsapi.WithHeader("Authorization", "Bearer <jwt>"),
)
```

如需查看当前生效的 auth / scope 解析策略：

```go
authPolicy, err := client.GetAuthPolicy(ctx)
```

## Demo Command

仓库里带了一个最小可运行 consumer，会直接使用 `AgentChat` / `AgentChatStream` 和 typed helper。

推荐先在 `E:\projects\ai\ai-agent-runtime\backend` 启动独立服务：

```bash
go run ./cmd/runtime-server --listen 127.0.0.1:8081
```

然后在同一仓库目录执行 demo：

```bash
go run ./cmd/skillsapi-demo -url http://127.0.0.1:8081 -message "plan this task"
go run ./cmd/skillsapi-demo -url http://127.0.0.1:8081 -message "stream this task" -stream -planning-mode planner_preferred
go run ./cmd/skillsapi-demo -mode session-agent -agent-action spawn -url http://127.0.0.1:8081 -agent-type explorer -fork-context
go run ./cmd/skillsapi-demo -mode session-agent -agent-action input -url http://127.0.0.1:8081 -parent-session-id <parent> -agent-id <child> -message "Summarize progress"
go run ./cmd/skillsapi-demo -mode session-agent -agent-action wait -url http://127.0.0.1:8081 -parent-session-id <parent> -agent-id <child> -agent-timeout-ms 10000
go run ./cmd/skillsapi-demo -mode session-agent -agent-action events -url http://127.0.0.1:8081 -parent-session-id <parent> -agent-id <child> -after-seq 0 -limit 20 -wait-ms 5000
```

更完整的命令说明见：

- `E:\projects\ai\ai-agent-runtime\backend\cmd\skillsapi-demo\README.md`

运行期更新 auth/scope resolver：

```go
updated, err := client.UpdateAuthPolicy(ctx, skillsapi.AuthPolicyUpdateRequest{
  AdminRoles: []string{"skills-admin", "platform-admin"},
  RoleClaims: []string{"role", "roles"},
})
```

说明：

- 如果服务端接了 `configManager`，更新会同步到当前配置快照
- 如果服务端的 `configManager` 绑定了配置文件，auth policy 字段会同步写回 YAML
- 文件写回只覆盖 `skills_runtime` 下的 auth/scope policy 字段

`UpdateUsagePolicy()` 也遵循相同原则：

- 如果服务端绑定了 `configManager` 和配置文件，usage/quota policy 会同步写回 YAML
- 仅覆盖 `skills_runtime` 下的 usage/quota 相关字段

`UpdateMutationPolicy()` 也遵循相同原则：

- 如果服务端绑定了 `configManager` 和配置文件，mutation policy 会同步写回 YAML
- 仅覆盖 `skills_runtime` 下的这些字段：
  - `read_only`
  - `disable_import`
  - `disable_persist`
  - `disable_reload_ops`
  - `disable_hot_reload_ops`

读取当前 mutation policy：

```go
mutationPolicy, err := client.GetMutationPolicy(ctx)
```

## Session Agent Client

轻量 child-agent 控制面对应的 Go client 方法：

- `SpawnSessionAgent`
- `GetSessionAgentStatus`
- `SendSessionAgentInput`
- `WaitSessionAgents`
- `ListSessionAgentEvents`
- `CloseSessionAgent`
- `ResumeSessionAgent`

示例：

```go
parent, err := client.CreateSession(ctx, skillsapi.CreateSessionRequest{
  UserID: "demo-user",
  Title:  "parent",
})

forkContext := true
spawned, err := client.SpawnSessionAgent(ctx, parent.Session.ID, skillsapi.SpawnSessionAgentRequest{
  AgentType:   "explorer",
  ForkContext: &forkContext,
})

_, err = client.SendSessionAgentInput(ctx, parent.Session.ID, spawned.Agent.SessionID, skillsapi.SendSessionAgentInputRequest{
  Message: "Summarize the current session.",
})

waited, err := client.WaitSessionAgents(ctx, parent.Session.ID, skillsapi.WaitSessionAgentsRequest{
  IDs:       []string{spawned.Agent.SessionID},
  TimeoutMs: 10000,
})

events, err := client.ListSessionAgentEvents(ctx, parent.Session.ID, spawned.Agent.SessionID, skillsapi.ListSessionAgentEventsParams{
  AfterSeq: 0,
  Limit:    20,
  WaitMs:   0,
})
```

如果只想看 HTTP 层契约、状态语义和 `curl` 用法，直接看：

- `docs/skill_runtime/session_agent_api.md`

## Team Task Outcome Client

Canonical Team task outcome API:

- `POST /api/runtime/teams/{id}/tasks/{task_id}/outcome`

Go client methods:

- `ReportTaskOutcome`
- `CompleteTask`
- `FailTask`
- `BlockTask`
- `GetTeamTask`
- `ListTeamTasks`
- `ListTaskDependencies`
- `ListTaskDependents`
- `CreateTeamTask`
- `UpdateTeamTask`
- `AddTaskDependency`
- `SendTeamMailboxMessage`
- `CreateTeam`
- `ListTeams`
- `ListTeammates`
- `ClaimReadyTasks`
- `ReplanTask`
- `ListTeamMailbox`
- `AckTeamMailboxMessage`

Example:

```go
resp, err := client.ReportTaskOutcome(ctx, "team-1", "task-1", skillsapi.ReportTaskOutcomeRequest{
  TaskStatus: "handoff",
  Summary:    "pass to reviewer",
  Blocker:    "need security review",
  HandoffTo:  "mate-2",
  TeammateID: "mate-1",
  AutoReplan: boolPtr(false),
})
```

Compatibility notes:

- `ReportTaskOutcome` uses the canonical `/outcome` endpoint.
- `CompleteTask`, `FailTask`, and `BlockTask` are compatibility wrappers around the legacy HTTP aliases.
- Legacy HTTP aliases still return compatibility headers on the server side.

Task and mailbox helpers:

```go
taskResp, err := client.GetTeamTask(ctx, "team-1", "task-1", skillsapi.GetTeamTaskOptions{
  IncludeDependencies: true,
  IncludeDependents:   true,
})

teamResp, err := client.CreateTeam(ctx, skillsapi.CreateTeamRequest{
  WorkspaceID:   "workspace-a",
  LeadSessionID: "lead-session",
  Status:        "active",
})

teamsResp, err := client.ListTeams(ctx, skillsapi.ListTeamsParams{
  Status:      "active",
  WorkspaceID: "workspace-a",
})

teammatesResp, err := client.ListTeammates(ctx, teamResp.Team.ID, skillsapi.ListTeammatesParams{
  State: "idle",
})

tasksResp, err := client.ListTeamTasks(ctx, "team-1", skillsapi.ListTeamTasksParams{
  Status:              []string{"running"},
  IncludeDependencies: true,
  IncludeDependents:   true,
})

depsResp, err := client.ListTaskDependencies(ctx, "team-1", "task-1")
dependentsResp, err := client.ListTaskDependents(ctx, "team-1", "task-1")

createTaskResp, err := client.CreateTeamTask(ctx, "team-1", skillsapi.CreateTeamTaskRequest{
  Title:    "new task",
  Goal:     "ship feature",
  Status:   "pending",
  Priority: 2,
})

newStatus := "ready"
updateTaskResp, err := client.UpdateTeamTask(ctx, "team-1", createTaskResp.Task.ID, skillsapi.UpdateTeamTaskRequest{
  Status: &newStatus,
})

_, err = client.AddTaskDependency(ctx, "team-1", createTaskResp.Task.ID, "task-dep-1")

sendMailResp, err := client.SendTeamMailboxMessage(ctx, "team-1", skillsapi.SendTeamMailboxMessageRequest{
  FromAgent: "lead",
  ToAgent:   "mate-1",
  Kind:      "question",
  Body:      "confirm the task boundary",
})

claimResp, err := client.ClaimReadyTasks(ctx, "team-1", skillsapi.ClaimReadyTasksRequest{
  Limit: 1,
})

replanResp, err := client.ReplanTask(ctx, "team-1", "task-1", skillsapi.ReplanTaskRequest{
  AutoPersist: true,
})

mailboxResp, err := client.ListTeamMailbox(ctx, "team-1", skillsapi.ListTeamMailboxParams{
  ToAgent:    "mate-1",
  UnreadOnly: true,
  Limit:      20,
})

_, err = client.AckTeamMailboxMessage(ctx, "team-1", "mail-1", "mate-1")
```

读取统一治理视图：

```go
governance, err := client.GetGovernancePolicy(ctx)
```

## 基础查询

```go
ctx := context.Background()

listResp, err := client.ListSkills(ctx, skillsapi.ListSkillsParams{
  SourceLayer: "external",
})

searchResp, err := client.SearchSkills(ctx, skillsapi.SearchSkillsParams{
  Query: "shell",
  Mode:  "hybrid",
  Limit: 10,
})

statsResp, err := client.GetStats(ctx, skillsapi.ListSkillsParams{})
```

`GetStats` 会直接返回：

- `skill_dirs`
- `source_summary`
- `mutation_policy`
- `runtime`

`GetStatsResponse` 还可以直接解码 `search` / `embedding`：

```go
search, err := statsResp.DecodeSearch()
if err != nil {
  return err
}

embedding, err := statsResp.DecodeEmbedding()
if err != nil {
  return err
}

if search != nil && search.HasSearchTraffic() {
  fmt.Println(search.TotalRequests, search.LastUsedEmbedding)
}
if embedding != nil && embedding.Indexed() {
  fmt.Println(embedding.Stats.IndexSize)
}
```

读取 provider / MCP 运行时状态：

```go
runtimeStatus, err := client.GetRuntimeStatus(ctx)
```

当前会返回：

- `default_model`
- `providers`
- `provider_count`
- `mcps`
- `mcp_count`

每个 `mcps` 项还包含：

- `trust_level`
- `execution_mode`

也可以直接用 helper 判断：

```go
 for _, mcp := range runtimeStatus.Runtime.MCPs {
  switch {
  case mcp.IsLocalMCP():
    fmt.Println(mcp.Name, "is local")
  case mcp.IsTrustedRemote():
    fmt.Println(mcp.Name, "is trusted remote")
  case mcp.IsRemoteMCP():
    fmt.Println(mcp.Name, "is remote")
  }
}
```

如果你只想看整体摘要：

```go
summary := runtimeStatus.Runtime.MCPSummary()
fmt.Println(summary.Names, summary.LocalCount, summary.RemoteCount)
```

也可以直接过滤：

```go
local := runtimeStatus.Runtime.LocalMCPs()
remote := runtimeStatus.Runtime.RemoteMCPs()
```

读取更适合运维的健康摘要：

```go
runtimeHealth, err := client.GetRuntimeHealth(ctx)
```

当前会返回：

- `health.healthy`
- `health.healthy_providers`
- `health.unhealthy_providers`
- `health.connected_mcps`
- `health.disconnected_mcps`
- `health.issues`

读取 runtime 配置健康校验结果：

```go
validationResp, err := client.ValidateRuntime(ctx)
```

当前会返回：

- `validation.healthy`
- `validation.issues`
- `validation.warnings`
- `validation.skill_count`
- `validation.skill_dirs`
- `validation.default_model`

触发 MCP runtime 重新加载与重连：

```go
reloadResp, err := client.ReloadRuntimeMCPs(ctx)
```

当前会返回：

- `reloaded`
- `runtime`
- `health`

## 执行 Skill

注意：`ExecuteSkill` 是 **admin/debug** 入口，业务侧推荐统一使用 `AgentChat`。

```go
resp, err := client.ExecuteSkill(ctx, "run_shell_command", skillsapi.ExecuteSkillRequest{
  Prompt: "echo hello",
  UserID: "demo-user",
})
```

如需更方便地读取结构化执行结果：

```go
decoded, err := resp.DecodeResult()
if err != nil {
  return err
}

if decoded != nil && decoded.ErrorCode != "" {
  fmt.Println(decoded.ErrorCode, decoded.ErrorContext)
}
```

`decoded.Observations[*].Metrics` 中也可能带：

- `mcp_name`
- `mcp_trust_level`
- `execution_mode`

也可以直接读取治理 helper：

```go
 for _, obs := range decoded.Observations {
  governance := obs.Governance()
  if governance.IsRemoteMCP() {
    fmt.Println(obs.Step, governance.MCPName, governance.MCPTrustLevel)
  }
}
```

如果你只想知道这次执行整体有没有触达远程 MCP，可以直接看摘要：

```go
summary := decoded.GovernanceSummary()
if summary.UsesRemoteMCP() {
  fmt.Println("remote MCPs:", summary.MCPNames)
}
if summary.UsesUntrustedRemoteMCP() {
  fmt.Println("contains untrusted remote MCP usage")
}
```

如果调用失败返回的是 `*skillsapi.APIError`，还可以直接按错误码分支：

```go
resp, err := client.ExecuteSkill(ctx, "run_shell_command", req)
if err != nil {
  var apiErr *skillsapi.APIError
  if errors.As(err, &apiErr) && apiErr.HasCode("AGENT_PERMISSION") {
    if policy, ok := apiErr.ContextValue("policy"); ok {
      fmt.Println("denied by", policy)
    }
    if apiErr.Governance().IsSandboxPolicy() {
      fmt.Println("sandbox policy denied execution")
    }
  }
  return err
}
```

如果是远程 MCP 治理失败，也可以直接读治理摘要：

```go
var apiErr *skillsapi.APIError
if errors.As(err, &apiErr) {
  governance := apiErr.Governance()
  if governance.IsMCPGovernance() && governance.IsRemoteMCP() {
    fmt.Println(governance.MCPName, governance.MCPTrustLevel)
  }
}
```

## Agent Chat

### 非流式

```go
resp, err := client.AgentChat(ctx, skillsapi.AgentChatRequest{
  Messages: []skillsapi.Message{
    {Role: "user", Content: "please help me"},
  },
  EnableRouting: true,
  PlanningMode:  "planner_preferred",
  WorkspacePath: "E:/projects/my-repo",
})
```

同样可以解码成更稳定的结果结构：

```go
decoded, err := resp.DecodeResult()
if err != nil {
  return err
}

if decoded != nil {
  fmt.Println(decoded.Kind, decoded.Source, decoded.Skill)
}
```

`decoded.Observations[*].Metrics` 里也可能带：

- `mcp_name`
- `mcp_trust_level`
- `execution_mode`

同样可以直接看治理摘要：

```go
summary := decoded.GovernanceSummary()
fmt.Println(summary.MCPNames, summary.RemoteMCPCount)
```

如果你想避免自己拆 `result.orchestration` / `result.planning`：

```go
orchestration, err := decoded.DecodeOrchestration()
if err != nil {
  return err
}

planning, err := decoded.DecodePlanning()
if err != nil {
  return err
}

if orchestration != nil {
  fmt.Println(orchestration.Skill, orchestration.RouteAttempted)
  if selected := orchestration.SelectedRoute(); selected != nil {
    fmt.Println(selected.Skill, selected.Score)
  }
  if orchestration.ObservationSummary != nil {
    fmt.Println(orchestration.ObservationSummary.TotalDurationMS)
  }
}
if planning != nil {
  fmt.Println(planning.Mode, planning.StepCount)
  if len(planning.Steps) > 0 {
    fmt.Println(planning.Steps[0].Tool)
  }
}

subagents, err := decoded.DecodeSubagentSummary()
if err != nil {
  return err
}
if subagents != nil {
  fmt.Println(subagents.Count, subagents.PatchCount)
}

toolCalls, err := decoded.DecodeToolCalls()
if err != nil {
  return err
}
if len(toolCalls) > 0 {
  fmt.Println(toolCalls[0].Name)
}

usage, err := decoded.DecodeUsage()
if err != nil {
  return err
}
duration, err := decoded.DecodeDuration()
if err != nil {
  return err
}
state, err := decoded.DecodeState()
if err != nil {
  return err
}
if usage != nil && duration != nil {
  fmt.Println(usage.TotalTokens, duration.Elapsed())
}
if state != nil && state.HasErrors() {
  fmt.Println(state.Errors)
}

fmt.Println(decoded.MetadataString("finish_reason"))
if cached, ok := decoded.MetadataBool("cached"); ok {
  fmt.Println(cached)
}
```

可选字段：

- `EnableRouting`
- `PlanningMode`
- `WorkspacePath`
- `SessionID`
- `UserID / TenantID / ProjectID`

### 流式

```go
stream, err := client.AgentChatStream(ctx, skillsapi.AgentChatRequest{
  Messages: []skillsapi.Message{
    {Role: "user", Content: "stream please"},
  },
  PlanningMode: "planner_preferred",
})
if err != nil {
  return err
}
defer stream.Close()

for {
  event, err := stream.Next()
  if err == io.EOF {
    break
  }
  if err != nil {
    return err
  }

  switch event.Event {
  case "chunk":
  case "planning":
  case "result":
  case "done":
  }
}
```

如果希望避免自己手动解 `map[string]interface{}`，可以直接使用 typed helper：

```go
for {
  event, err := stream.Next()
  if err == io.EOF {
    break
  }
  if err != nil {
    return err
  }

  switch event.Event {
  case "meta":
    meta, err := event.DecodeMetaPayload()
    if err != nil {
      return err
    }
    fmt.Println(meta.Source, meta.Kind)
    orchestration, err := meta.DecodeOrchestration()
    if err != nil {
      return err
    }
    planning, err := meta.DecodePlanning()
    if err != nil {
      return err
    }
    _, _ = orchestration, planning
  case "planning":
    planning, err := event.DecodePlanningPayload()
    if err != nil {
      return err
    }
    fmt.Println(planning.Mode, planning.Attempted)
    if len(planning.SubagentTasks) > 0 {
      fmt.Println(planning.SubagentTasks[0].Role)
    }
  case "reasoning", "tool_call", "tool_start", "tool_end", "chunk":
    chunk, err := event.DecodeChunkPayload()
    if err != nil {
      return err
    }
    switch event.Event {
    case "reasoning":
      reasoning, err := chunk.DecodeReasoning()
      if err != nil {
        return err
      }
      _, _ = reasoning, chunk
    case "tool_call", "tool_start", "tool_end":
      tool, err := chunk.DecodeTool()
      if err != nil {
        return err
      }
      toolCall, err := chunk.DecodeToolCall()
      if err != nil {
        return err
      }
      delta, err := chunk.DecodeDelta()
      if err != nil {
        return err
      }
      _, _, _ = tool, toolCall, delta
      fmt.Println(chunk.MetadataString("phase"))
    case "chunk":
      text, err := chunk.DecodeText()
      if err != nil {
        return err
      }
      _, _ = text, chunk
    }
  case "result":
    result, err := event.DecodeResultPayload()
    if err != nil {
      return err
    }
    fmt.Println(result.Kind, result.Source)
    _, _ = result.DecodeOrchestration()
    _, _ = result.DecodePlanning()
    _, _ = result.DecodeSubagentSummary()
  case "done":
    done, err := event.DecodeDonePayload()
    if err != nil {
      return err
    }
    finalResult, err := done.DecodeResult()
    if err != nil {
      return err
    }
    _ = finalResult
  case "error":
    errPayload, err := event.DecodeErrorPayload()
    if err != nil {
      return err
    }
    fmt.Println(errPayload.Source, errPayload.Message)
  }
}
```

如果你希望连 `switch + DecodeXxx()` 都省掉，可以直接用更高层的 `NextDecoded()`：

```go
for {
  decoded, err := stream.NextDecoded()
  if err == io.EOF {
    break
  }
  if err != nil {
    return err
  }

  switch {
  case decoded.Meta != nil:
    fmt.Println(decoded.Meta.Source, decoded.Meta.Kind)
  case decoded.Planning != nil:
    fmt.Println(decoded.Planning.Mode, decoded.Planning.SubagentTaskCount)
  case decoded.Orchestration != nil:
    fmt.Println(decoded.Orchestration.Source, decoded.Orchestration.RouteMatched)
  case decoded.Result != nil:
    fmt.Println(decoded.Result.Source, decoded.Result.Success)
  case decoded.Done != nil:
    finalResult, err := decoded.Done.DecodeResult()
    if err != nil {
      return err
    }
    _ = finalResult
  case decoded.Error != nil:
    return fmt.Errorf("stream failed: %s", decoded.Error.Message)
  }
}
```

如果你希望把循环本身也隐藏掉，可以直接用 callback 风格的 `Consume()`：

```go
err = stream.Consume(skillsapi.StreamHandlers{
  OnMeta: func(meta *skillsapi.StreamMetaPayload) error {
    fmt.Println(meta.Source, meta.Kind)
    return nil
  },
  OnPlanning: func(planning *skillsapi.StreamPlanningPayload) error {
    fmt.Println(planning.Mode, planning.SubagentTaskCount)
    return nil
  },
  OnOrchestration: func(orchestration *skillsapi.StreamOrchestrationPayload) error {
    fmt.Println(orchestration.Source, orchestration.RouteMatched)
    return nil
  },
  OnResult: func(result *skillsapi.StreamResultPayload) error {
    fmt.Println(result.Source, result.Success)
    return nil
  },
  OnDone: func(done *skillsapi.StreamDonePayload) error {
    finalResult, err := done.DecodeResult()
    if err != nil {
      return err
    }
    _ = finalResult
    return nil
  },
  OnError: func(errPayload *skillsapi.StreamErrorPayload) error {
    return fmt.Errorf("stream failed: %s", errPayload.Message)
  },
})
if err != nil {
  return err
}
```

如果你仍然想保留最底层的原始解码方式，也可以直接用：

```go
var payload map[string]interface{}
if err := event.Decode(&payload); err != nil {
  return err
}
```

当 `planning_mode=planner_preferred` 时：

- 非流式可通过 `decoded.DecodePlanning()` 直接读取 planning 摘要
- 流式会额外收到 `event: planning`
- `decoded.DecodeOrchestration()` 中会带：
  - `planning_attempted`
  - `planning_source`
  - `plan_step_count`
  - `planning_error`

## 管理接口

写操作会自动带上 `X-Skills-Admin-Token`（如果 client 配置了 `WithAdminToken`）。

```go
createResp, err := client.CreateSkill(ctx, skillsapi.Skill{
  Name:        "team_echo",
  Description: "team skill",
  Triggers: []skillsapi.Trigger{{
    Type:   "keyword",
    Values: []string{"team echo"},
    Weight: 1,
  }},
}, skillsapi.CreateSkillOptions{
  Persist: true,
})
```

更新与删除：

```go
persist := true
_, err = client.UpdateSkill(ctx, "team_echo", updatedSkill, skillsapi.UpdateSkillOptions{
  Persist: &persist,
})

_, err = client.DeleteSkill(ctx, "team_echo", skillsapi.DeleteSkillOptions{
  DeleteFile: true,
})
```

导入/导出与 reload：

```go
exportResp, err := client.ExportSkills(ctx, skillsapi.ListSkillsParams{})

importResp, err := client.ImportSkills(ctx, []skillsapi.Skill{
  {
    Name:        "team_echo",
    Description: "imported team skill",
    Triggers: []skillsapi.Trigger{{
      Type:   "keyword",
      Values: []string{"team echo"},
      Weight: 1,
    }},
  },
})

importPersistResp, err := client.ImportSkillsWithOptions(ctx, []skillsapi.Skill{
  {
    Name:         "team_prompt_skill",
    Description:  "imported and persisted skill",
    SystemPrompt: "You are a persisted imported skill.",
    UserPrompt:   "Return only the requested output.",
    Triggers: []skillsapi.Trigger{{
      Type:   "keyword",
      Values: []string{"team prompt"},
      Weight: 1,
    }},
  },
}, skillsapi.ImportSkillsOptions{
  Persist:   true,
  TargetDir: "./team-skills",
})

reloadResp, err := client.ReloadSkills(ctx, skillsapi.ReloadSkillsRequest{
  Dirs: []string{"./team-skills"},
})
```

hot reload：

```go
startResp, err := client.StartHotReload(ctx, skillsapi.HotReloadRequest{
  Dirs: []string{"./team-skills"},
})

statsResp, err := client.GetHotReloadStats(ctx)
reloadHotResp, err := client.ReloadHotReload(ctx)
stopResp, err := client.StopHotReload(ctx)
```

search admin：

```go
searchStats, err := client.GetSearchStats(ctx)
reindexResp, err := client.ReindexSearchIndex(ctx, true)
```

也可以直接解码 `search` / `embedding`：

```go
searchSummary, err := searchStats.DecodeSearch()
if err != nil {
  return err
}
embeddingStatus, err := searchStats.DecodeEmbedding()
if err != nil {
  return err
}

reindexSearch, err := reindexResp.DecodeSearch()
if err != nil {
  return err
}
reindexEmbedding, err := reindexResp.DecodeEmbedding()
if err != nil {
  return err
}

_ = searchSummary
_ = embeddingStatus
_ = reindexSearch
_ = reindexEmbedding
```

usage / quota：

```go
usageResp, err := client.GetUsageStatsWithScope(ctx, skillsapi.UsageScope{
  TenantID:  "team-a",
  ProjectID: "ops",
  UserID:    "demo-user",
})
resetResp, err := client.ResetUsageStats(ctx, skillsapi.ResetUsageStatsRequest{
  TenantID:  "team-a",
  ProjectID: "ops",
  UserID:    "demo-user",
})
```

runtime policy 管理：

```go
getPolicyResp, err := client.GetUsagePolicy(ctx)

maxRequests := 50
updatePolicyResp, err := client.UpdateUsagePolicy(ctx, skillsapi.UsagePolicyUpdateRequest{
  QuotaEnabled:       boolPtr(true),
  DefaultMaxRequests: &maxRequests,
  Users: map[string]skillsapi.UsageQuotaLimitConfig{
    "team-a/ops/alice": {MaxRequests: &maxRequests},
  },
})

deletePolicyResp, err := client.DeleteUsagePolicyEntry(ctx, skillsapi.DeleteUsagePolicyEntryRequest{
  Level: "user",
  Key:   "team-a/ops/alice",
})
```

usage ledger：

```go
ledgerResp, err := client.GetUsageLedger(ctx, skillsapi.GetUsageLedgerParams{
  Scope: skillsapi.UsageScope{
    TenantID:  "team-a",
    ProjectID: "ops",
    UserID:    "alice",
  },
  Entrypoint: "execute",
  Skill:      "run_shell_command",
  Limit:      20,
})
```

这些接口通常需要：

- loopback 访问
- 或 `WithAdminToken(...)`

## Session 接口

创建与查询：

```go
createResp, err := client.CreateSession(ctx, skillsapi.CreateSessionRequest{
  UserID: "demo-user",
  Title:  "demo-session",
})

listResp, err := client.ListSessions(ctx, "demo-user")
getResp, err := client.GetSession(ctx, createResp.Session.ID)
historyResp, err := client.GetSessionHistory(ctx, createResp.Session.ID)
statsResp, err := client.GetSessionStats(ctx, "demo-user")
```

更新与状态流转：

```go
title := "updated-title"
state := "idle"

updateResp, err := client.UpdateSession(ctx, sessionID, skillsapi.UpdateSessionRequest{
  Title:   &title,
  State:   &state,
  TagsAdd: []string{"support"},
  Context: map[string]interface{}{"ticket": "INC-42"},
})

searchResp, err := client.SearchSessions(ctx, skillsapi.SearchSessionsRequest{
  UserID: "demo-user",
  Tags:   []string{"support"},
  State:  "idle",
})

archiveResp, err := client.ArchiveSession(ctx, sessionID)
activateResp, err := client.ActivateSession(ctx, sessionID)
closeResp, err := client.CloseSession(ctx, sessionID)
clearResp, err := client.ClearSessionHistory(ctx, sessionID)
```

批量操作：

```go
batchArchiveResp, err := client.BatchArchiveSessions(ctx, []string{sessionA, sessionB})
batchDeleteResp, err := client.BatchDeleteSessions(ctx, []string{sessionA, sessionB})
```

## 错误模型

非 2xx 响应会返回：

- `*skillsapi.APIError`

其中包含：

- `StatusCode`
- `Message`
- `Code`
- `Context`
- `Body`

示例：

```go
if err != nil {
  if apiErr, ok := err.(*skillsapi.APIError); ok && apiErr.StatusCode == 403 {
    // forbidden
  }
}
```

## 当前边界

当前 client 先覆盖最稳定的一组能力：

- 核心查询与执行
- Agent chat
- skill 管理面
- search admin 面
- reload / hot reload 面
- session 管理面

尚未覆盖的接口，后续可继续补：

- 更高层 SDK 封装（例如 tool/agent facade）
- 更严格的 typed SSE event schema
- 跨语言 SDK
