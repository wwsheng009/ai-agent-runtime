# Team Product Closure execution plan

## Goal
按 `docs/implementation/team-product-closure-implementation-plan-20261122.md` 对当前仓库完成可真实验证的 P0/P1 Team product closure；不把未具备后端契约的 Definition/Run、pause/cancel、budget hard stop、streaming、artifact download 或完整历史伪装成已实现。

## Evidence-first audit
1. 保留现有 dirty changes，先确认 Team 相关 diff、router、store migration、lifecycle/orchestrator、outcome、authorization 与 Web/TUI surfaces。
2. 将 P0/P1 验收表映射到实际代码、测试和明确 unsupported/capability gate；不以类型/枚举/文件存在代替运行契约。
3. 运行 Team 后端、TUI、Web contract/build/i18n 及 `git diff --check`，记录既有无关 memory failure 与本轮结果。

## Implementation boundary
优先完成真实可落地、不会伪造能力的契约闭环：
- Runtime capability/readiness envelope：compatibility mode、supported/unsupported actions、version/event cursor/summary readiness、request/trace metadata。
- 受控 lifecycle action（仅在现有 supervisor/orchestrator/store 能支撑的 start 语义上开放）；expected version、Idempotency-Key、状态转换、durable event、409 conflict；不开放未经验证的 pause/resume/cancel。
- outcome/attempt/result reference 只读 lineage；若持久化/下载契约不足，明确 opaque reference 和 capability gate。
- Web client/types/Details/Results 只消费真实能力，不显示伪 Start/Retry/Download/History 动作；补契约测试。
- 若审计确认某能力仍缺少底层契约，则添加明确 unsupported response/copy 和测试，并在最终验收中列为未实现。

## Verification gates
- `cd backend && go test ./internal/team ./internal/api/runtimeapi ./internal/toolbroker -count=1`
- `cd backend && go test ./cmd/aicli/commands -run 'Team|team' -count=1`
- 若修改 Web：`cd frontend && npm test -- --run src/components/workspace/runtime-teams && npm run build && npm run lint:i18n`
- `git diff --check`
- 最终逐项输出：已实现且验证、已实现但被既有无关测试阻塞、文档要求仍未实现；不得调用 goal complete 直到审计无必做项遗漏。
