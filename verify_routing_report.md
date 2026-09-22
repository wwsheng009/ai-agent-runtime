# main_agent.routing 端到端验证报告

**验证时间**：2026-09-22 10:04-10:05 CST  
**验证实例**：`session_20260922100426_ccJ1P26u` @ `http://127.0.0.1:52919`  
**验证结论**：✅ **PASS — 路由机制端到端生效**

---

## 1. 配置写入

在 `config.yaml` 第 71 行纯插入 43 行（零删除），新增 `main_agent.routing` 配置块：

```yaml
main_agent:
  routing:
    enabled: true
    levels: [easy, normal, hard]
    allow_expert: false
    default_difficulty: normal
    cost_guard_mode: soft
    max_consecutive_expensive_steps: 6
    expensive_levels: [hard]
    max_invalid_reports_per_turn: 3
    downgrade_confirm_steps: 3
    min_dwell_steps: 2
    health_gate:
      respect_provider_health: false
      latch_scope: turn
      on_chain_exhausted: baseline
    profiles:
      easy:   { provider: commandgo, model: poolside/laguna-s-2.1-free, reasoning_effort: max }
      normal: { provider: opencode.ai, model: mimo-v2.5-pro, reasoning_effort: high,
                candidates: [{ provider: opencode.ai, model: deepseek-v4.1-flash }] }
      hard:   { provider: opencode.ai, model: deepseek-v4-pro, reasoning_effort: high }
```

- 备份：`config.yaml.bak-20260922-mainagent` (SHA256 `3B9F...匹配`)
- 校验：静态 11 项全部 PASS（levels/expert/cost_guard/latch_scope/profiles/keys/subset）

---

## 2. 端到端验证（决定性证据）

### 2.1 启动实例

```
port        = 52919
session     = session_20260922100426_ccJ1P26u  ✓
provider    = opencode.ai   ← 加载了新配置（非旧 hanhe）
model       = deepseek-v4.1-flash  ← 基线模型
```

### 2.2 Invoke 调用

```
POST /web/api/invoke
body: {"prompt":"Reply with exactly: hello-ok", "timeout_ms":120000}
→ status = completed, elapsed = 8344ms
→ assistant = hello-ok  ✓
→ usage = {input_tokens:30424, output_tokens:17, total_tokens:30441}
```

### 2.3 HTTP 请求日志（实际调用的 provider/model）

```
provider = (空)
model    = mimo-v2.5-pro           ★★★ 正常 profile 的模型，非基线 ★★★
url      = https://opencode.ai/zen/go/v1/chat/completions
method   = POST
```

**基线** = `opencode.ai/deepseek-v4.1-flash`  
**实际** = `opencode.ai/mimo-v2.5-pro`  
**改道** = `default_difficulty: normal` 的 profile 已成功套用到 turn floor

### 2.4 runtime-events 路由事件（完整证据）

**`main_agent.route_applied`**（turn 开始时）：
```json
{
  "type": "main_agent.route_applied",
  "payload": {
    "baseline_provider": "opencode.ai",
    "baseline_model": "deepseek-v4.1-flash",
    "difficulty": "normal",
    "model": "mimo-v2.5-pro",
    "provider": "opencode.ai",
    "reason": "turn_floor",
    "reasoning_effort": "high",
    "route_changed": true,
    "source": "baseline",
    "step": 0,
    "candidates": [
      {"provider": "opencode.ai", "model": "mimo-v2.5-pro", "primary": true, "eligible": true},
      {"provider": "opencode.ai", "model": "deepseek-v4.1-flash", "eligible": true}
    ]
  }
}
```

**`main_agent.route_cleared`**（turn 结束时）：
```json
{
  "type": "main_agent.route_cleared",
  "payload": {
    "final_difficulty": "normal",
    "restored_provider": "opencode.ai",
    "restored_model": "deepseek-v4.1-flash",
    "restored_effort": "max",
    "steps_total": 1,
    "steps_with_override": 1,
    "cost_guard_trips": 0
  }
}
```

---

## 3. 验证结论

| 检查项 | 状态 | 证据 |
|--------|------|------|
| 配置加载 | ✅ PASS | 实例 provider=opencode.ai（非 hanhe），说明加载了新配置 |
| 路由启用 | ✅ PASS | `route_applied` 事件出现，`route_changed=true` |
| difficulty 映射 | ✅ PASS | `default_difficulty=normal` → profile `mimo-v2.5-pro` |
| turn_floor 触发 | ✅ PASS | `reason: turn_floor`, `step: 0`, `source: baseline` |
| HTTP 实际调用 | ✅ PASS | 请求体 `model=mimo-v2.5-pro`，非基线 `deepseek-v4.1-flash` |
| turn 结束恢复 | ✅ PASS | `route_cleared` 恢复到 `deepseek-v4.1-flash` |
| candidates 竞选 | ✅ PASS | 两个候选均 `eligible=true`，primary 优先选中 |
| cost_guard | ✅ PASS | `cost_guard_trips=0`，软模式未触发 |
| reasoning_effort | ✅ PASS | `reasoning_effort=high`（normal profile 定义） |
| 功能正常 | ✅ PASS | 返回 "hello-ok"，usage 正常 |

---

## 4. 备注

- **PID 16424（用户主实例，端口 54733）** 仍在运行旧配置，**未重启**，**未修改**。如需对主实例生效，需手动重启。
- `main_agent.routing` **无热重载**（`hotReloadAgentRoutingPrefixes` 仅含 `subagents.routing`/`teams.routing`）。
- 验证实例（端口 52919）为新启动的隔离实例，用于安全验证，不影响用户日常工作。
