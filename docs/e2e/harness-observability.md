# aicli E2E harness：观测与取证工具集

> 归属：`scripts/aicli-e2e-harness.ps1`，由 01 / 02 / 03 三个 harness dot-source 复用。
> 本文即原 `debug-guide.md` §5.2 的独立成文（2026-09-24 文档整理）。
> 相关：[debug-guide.md](./debug-guide.md)（E2E-DEBUG-01 主线，失败判读见其 §6）、
> [nonloopback-auth-e2e.md](./nonloopback-auth-e2e.md)（E2E-DEBUG-02）、
> [mesh-e2e.md](./mesh-e2e.md)（E2E-DEBUG-03，落地要点见其 §6）。

把「等固定秒数 + 只看终态」换成「采样 + 指纹 + 自动取证」，是这套 E2E 从「能跑」到「失败可判读」的分界：

| 能力 | 函数 | 契约 |
|------|------|------|
| HTTP 原语 | `Invoke-HarnessRequest -Url -TimeoutSec -AsJson` | 显式按 **UTF-8 字节**解码（避免 PowerShell 代码页把中文回复解坏）；**从不抛异常**，统一返回 `ok / status_code / text / json / ms / error`——失败是数据，不是中断 |
| A1 时序采样 | `Start-AicliTimeline -BaseUrl -TimelinePath -IntervalMs -Tag -HarnessPath`、`Stop-AicliTimeline -Job`、`Get-AicliTimelineSample` | 每帧一行 JSONL：`screen_hash / screen_lines / screen_chars`、`app_state / history / pending`、`skipped_sections`、`ms`。只读 `?fast=1` 与屏幕文本，不参与会话调度 |
| A3 稳态判据 | `Wait-AicliScreenStable -BaseUrl -StableSamples -MinLines -TimeoutSec -IntervalMs -TimelinePath -Tag` | 连续 `-StableSamples`（缺省 3）次屏幕指纹不变且行数 ≥ `-MinLines` 才算就绪，返回 `stable / samples / ...`；替代「sleep 两秒后祈祷」 |
| A2 失败诊断包 | `Save-AicliDiagnostics -BaseUrl -DiagDir -Reason -TimelinePath -TimeoutSec` | 一次抓齐 `goroutine.txt` / `executor.json` / `status.json` / `status-fast.json` / `status.txt` / `screen.txt` / `endpoints.json` + timeline 副本，并写 `index.json`（每项 ok/ms/error）与 `reason.txt` |
| B4 清单覆盖门禁 | `Test-AicliEndpointCoverage -EndpointsJson -Asserted -Exempt` | 清单里每个 `enabled=true` 的端点必须命中 `-Asserted`（精确匹配或查询串变体），否则必须在 `-Exempt` 里**带理由**；返回 `ok / missing / exempt / checked` |
| C4 双通道取证 | `Get-AicliUiaScreenText -WindowTitle`、`Save-AicliDualChannelForensics -BaseUrl -DiagDir -WindowTitle -Label` | 同时落盘 HTTP 的「应然帧」（`<label>-http-expected.txt`）与 UI Automation 读到的物理终端帧（`<label>-uia-physical.txt`），用于回答「是没渲染，还是没显示」 |

> A1 的采样在**失败时**最有价值：它给出失败前后的连续帧，而不是一个孤立的终态。
> C4 依赖交互桌面（真实终端窗口）；无桌面时 UIA 侧写 `uia capture unavailable`，
> 不阻断 HTTP 侧取证。
