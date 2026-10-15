# 工具 schema 基线实测（2026-10-15）

> 工具：`backend/cmd/tool-schema-measure`（复现：`go run ./cmd/tool-schema-measure`）
> 口径：紧凑 JSON `{name,description,parameters}` 字节；tokens ≈ bytes/4（对标 dsh-ops `scripts/measure-schemas.mjs`）
> 范围：内置 toolkit（`tools.NewDefaultManagerWithRuntimeConfig(nil, cfg)`，22 个）+ broker 定义（裸 `&Broker{}`，9 个）。**不含 MCP / scheduler / goal**（registry 文档实测请求面 51~53，比本测量多出的正是这些）。
> 重要 caveat：数字为 **manager 原始定义**；生产请求还会经 `optimizeModelToolSurface`（别名收口 + grep 描述/参数压缩），故"alias-collapse only"行仍高估了 grep（21.8KB 为原始值，生产已压缩）。

## 汇总

| 指标 | 值 |
| --- | ---: |
| 原始面 | 31 工具 / 87,241 B / ≈21,811 tok |
| 别名收口后（模拟生产第一道优化） | 27 工具 / 59,209 B / ≈14,803 tok（**-32.1%**） |

## 关键发现

1. **最大单杠杆已经上线**：`optimizeModelToolSurface` 的别名收口（grep>rg、view>read、shell>bash>execute_shell_command）= **-32.1%**（28,032 B）。grep 21,823 B + rg 19,545 B 两项占原始面 **47.4%**——rg 被收口即省近一半中的一项。
2. **收口后的剩余大户**：grep（原始 21.8KB，生产已压缩）、aicli_exec 4.3KB、shell 4.1KB、job_start 2.9KB、view/edit ~2KB。层二短语压缩的现实目标就是这几个，**量级为每工具数百~数千 token，不是上万**。
3. **orchestration 关停 = 11.6%**（broker 9 工具 / 10,085 B）。registry 的 31 个编排工具含 scheduler 定义，本测量未含；真实请求面上 orchestration 占比接近但略高。
4. **族级 scenario**：network off 仅 -1.7%（download/fetch 小）；control off -10.7%。

## scenario 明细

| scenario | tools | bytes | ~tokens | saved | saved % |
| --- | ---: | ---: | ---: | ---: | ---: |
| baseline (all) | 31 | 87,241 | 21,811 | 0 | 0.0% |
| alias-collapse only | 27 | 59,209 | 14,803 | 28,032 | 32.1% |
| orchestration off (broker) | 22 | 77,156 | 19,289 | 10,085 | 11.6% |
| network off (kind) | 29 | 85,763 | 21,441 | 1,478 | 1.7% |
| control off (kind) | 24 | 77,923 | 19,481 | 9,318 | 10.7% |
| orchestration+network+control off | 19 | 73,869 | 18,468 | 13,372 | 15.3% |

## 对照方案目标（§6：多代理 ≥40%、普通 ≥20%）

- 原始面 40% 目标 ≈ 34,900 B：**别名收口已单独贡献 32.1%**（已上线、零新增工作）；
- 其余 levers 合计（orchestration 11.6% + 短语压缩预计 10~20%[待做后实测]）在**多代理场景**（MCP + scheduler 在场）有望叠加达标；**普通单代理场景**（orchestration 已被会话形态隐藏、别名已收口）剩余空间主要只剩短语压缩，**20% 目标可能不达**——需以真实 `tool_schema_tokens` 元数据复核后定档。

## 对实施裁决的影响（plan §10.3 更新依据）

1. 层二（短语压缩）目标收敛为收口后大户：aicli_exec / shell / job_start / view / edit / todos（grep 生产已压缩，复核后如有余量再做）；
2. orchestration 门控收益真实但有限（~11.6%），且**机制已有**（broker DefinitionsForContext 已按会话形态隐藏 team 工具）——不需要新机制，profile 暴露 Capability 域是可选便利；
3. prompt 段所有权（guidance 与工具集同生共死）与 token 无关但仍是第一优先（正确性缺陷）；
4. 本测量工具保留，作为后续"压缩前后对比"的复现入口。

## 增补（2026-10-15 下午：短语压缩落地后）

第二优先项已实施（`optimizeModelToolSurface` 扩展）：`aicli_exec` / `shell` / `view` / `todos` 新增**参数白名单 + 短描述**（28→11 / 12→8 / 6→5 / 描述短语化），`compactToolPropertiesForModel` 为共享实现；golden 断言见 `internal/agent/compact_tool_wire_test.go`（白名单集定格 + 每工具 wire 字节上限）。

生产面估算（测量工具新增输出块，±10%）：

| 口径 | 字节 | 说明 |
| --- | ---: | --- |
| 原始面（31 工具） | 87,241 B | manager 定义，未优化 |
| 别名收口后 | 59,209 B | 已上线（grep>rg、view>read、shell 族） |
| **收口 + 短语压缩后（生产面）** | **~36,754 B** | 含 grep 参数压表 + 4 工具新白名单 |

**累计降幅 ≈57.9%**（相对未优化面）；别名收口后的额外压缩收益 **-37.9%**（22,455 B / ≈5.6k tok）。多代理场景叠加 orchestration 门控（-11.6% 相对原始面）后，方案 §6 的 ≥40% 目标在原始面口径下已由既有优化 + 本次压缩共同达成。

注意：估算中 grep = 3,300 B（生产压表后），与 golden 上限一致口径；`aicli_exec` 保留 11 参数（含 session_dir/log_dir 等低频但模型可能需要的定位参数），如需进一步压缩可再裁，收益约 400~600 B。
