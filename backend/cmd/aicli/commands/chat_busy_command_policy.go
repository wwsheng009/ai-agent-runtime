package commands

import (
	"os"
	"strings"
)

// 忙时命令策略（方案 §5 P1-1/P1-2；档位命名对齐 §3.7.3 降级矩阵 I/S/D/R）。
//
// 该策略描述的是**目标态**：I（inline）/S（副屏与确认门，P2-4）/R（拒绝）均已落地，
// D 的反馈文案仍按 P3 评审。**默认启用**（2026-09-28 决策 D14，见方案 G.16）：未设置
// 总闸即启用；显式设置 AICLI_CHAT_BUSY_COMMAND 为非启用值时才回退旧行为（I/S→D），
// 与既有 chatSlashCommandQueueSafe 白名单逐条等价（P1-7/T18 以显式关闭口径保留）。
type chatBusyCommandPolicy uint8

const (
	chatBusyPolicyInherit   chatBusyCommandPolicy = iota // 未标记：按旧白名单派生（P1 等价基线）
	chatBusyPolicyImmediate                              // I：turn 运行中立即执行（只读、无发送、无状态变更、无屏幕租约）
	chatBusyPolicyScreen                                 // S：副屏只读交互（载体 P2-4 落地前按 D 处理）
	chatBusyPolicyDeferred                               // D：排队到回合结束后执行
	chatBusyPolicyReject                                 // R：忙时拒绝（保持现状）
)

func (p chatBusyCommandPolicy) String() string {
	switch p {
	case chatBusyPolicyImmediate:
		return "immediate"
	case chatBusyPolicyScreen:
		return "screen"
	case chatBusyPolicyDeferred:
		return "deferred"
	case chatBusyPolicyReject:
		return "reject"
	default:
		return "inherit"
	}
}

// chatBusyCommandEnv 是忙时通道总闸，**默认启用**：未设置或空值 = 启用；
// 显式设置为非启用值（0/false/off/no/disable/disabled 等，含拼写错误）时回退旧行为
// （I/S 回退 deferred，与 chatSlashCommandQueueSafe 逐条等价），保留一键回退能力。
const chatBusyCommandEnv = "AICLI_CHAT_BUSY_COMMAND"

func chatBusyCommandEnabled() bool {
	raw, present := os.LookupEnv(chatBusyCommandEnv)
	if !present {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "1", "true", "on", "yes", "y", "enabled", "auto":
		return true
	default:
		// 显式 opt-out：未知值也按关闭处理（fail-closed，宁可回到旧行为）。
		return false
	}
}

// chatSlashCommandBusyPolicyFor 解析一段输入对应的目标忙时策略（fail-closed）：
//
//  1. 非 slash / 空输入 → inherit（不是命令，路由不由本函数决定）；
//  2. 命令名含别名经 catalog 归一，取 spec.BusyPolicy；
//  3. 不接受参数的命令携带参数 → inherit（只放行文档化的裸形式）；
//  4. 子命令级覆盖：`/queue clear` 保持现状（状态变更，不得 immediate）；
//  5. I/S 在总闸显式关闭时回退 D；D/R 直接返回；
//  6. inherit → 默认由注册表接管（§3.8.2）；仅当总闸显式关闭时退回旧白名单派生：
//     queue-safe→D，否则→R（保证显式关闭时零行为差异）。
func chatSlashCommandBusyPolicyFor(text string) chatBusyCommandPolicy {
	name, args, ok := chatBusyCommandNameAndArgs(text)
	if !ok {
		return chatBusyPolicyInherit
	}

	policy := chatBusyPolicyInherit
	if spec, found := chatSlashCommandCatalogMap()[name]; found {
		policy = spec.BusyPolicy
		if len(args) > 0 && !spec.AcceptsArgs {
			policy = chatBusyPolicyInherit
		}
	}
	if override, ok := chatBusyCommandSubcommandPolicy(name, args); ok {
		policy = override
	}

	switch policy {
	case chatBusyPolicyImmediate, chatBusyPolicyScreen:
		if !chatBusyCommandEnabled() {
			return chatBusyPolicyDeferred
		}
		return policy
	case chatBusyPolicyDeferred, chatBusyPolicyReject:
		return policy
	}

	// D14：总闸开启（默认）时注册表即策略的单一事实源（§3.8.2）。
	if chatRuntimeInteractionRegistryActive() {
		return chatBusyPolicyFromRuntimeSpec(text)
	}

	if chatSlashCommandQueueSafe(text) {
		return chatBusyPolicyDeferred
	}
	return chatBusyPolicyReject
}

// chatRuntimeInteractionRegistryActive 判定注册表是否接管路由策略（P2-3）。
// 默认启用：总闸开启（默认）即由注册表解析；AICLI_CHAT_RUNTIME_INTERACTION 只作为
// 三级开关的全局档（auto/readonly/off），不再充当第二道启用门。显式关闭总闸时回退
// P1 首批白名单（T18 等价），保证一键回退。
func chatRuntimeInteractionRegistryActive() bool {
	return chatBusyCommandEnabled()
}

// chatBusyPolicyFromRuntimeSpec 把注册表声明（经三级开关降级后）映射到 P1 四档：
// block→R；inline→I；screen→S（仅首批白名单，P2-4b）/其余 screen→D；
// prompt/queue→D（prompt 载体见后续增量）；未登记→D。
func chatBusyPolicyFromRuntimeSpec(text string) chatBusyCommandPolicy {
	spec, registered := resolveRuntimeCommandSpec(text)
	if !registered {
		return chatBusyPolicyDeferred
	}
	effective := runtimeCommandWithSwitch(spec, runtimeSwitchTableFromEnv())
	switch effective.Mode {
	case runtimeModeBlock:
		return chatBusyPolicyReject
	case runtimeModeInline:
		return chatBusyPolicyImmediate
	case runtimeModeScreen:
		if busyScreenCommandFirstBatch(effective) {
			return chatBusyPolicyScreen
		}
		return chatBusyPolicyDeferred
	case runtimeModePrompt:
		// P2-4b-3：首批 prompt 档（/queue clear）复用 S 档路由形态把行交给宿主，
		// 由宿主内的确认门决定执行/拒绝/降级；路由层不新增档位，避免默认分支
		// fail-closed 误伤输入。
		if busyPromptCommandWhitelisted(text) {
			return chatBusyPolicyScreen
		}
		return chatBusyPolicyDeferred
	default:
		return chatBusyPolicyDeferred
	}
}

// chatBusyCommandNameAndArgs 归一命令名（小写，保留前导 `/`）与参数。
// ok=false 表示输入不是 slash 命令。
func chatBusyCommandNameAndArgs(text string) (string, []string, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return "", nil, false
	}
	name := strings.ToLower(fields[0])
	if !strings.HasPrefix(name, "/") {
		return "", nil, false
	}
	return name, fields[1:], true
}

// chatInputCommandBusyPolicy 是 chatInputCommandQueuable 的策略化等价物（P1-3
// 起由输入队列路由使用）：Ready 状态沿用旧语义（deferred，交主循环/队列处理），
// 忙时按目标策略解析；非 slash 输入返回 inherit（由非命令路径处理）。
func chatInputCommandBusyPolicy(session *ChatSession, text string) chatBusyCommandPolicy {
	if !isSlashCommandInput(text) {
		return chatBusyPolicyInherit
	}
	if session != nil && session.Interaction != nil && session.Interaction.IsReady() {
		return chatBusyPolicyDeferred
	}
	return chatSlashCommandBusyPolicyFor(text)
}

// chatBusyCommandSubcommandPolicy 返回子命令级覆盖；ok=false 表示无覆盖。
func chatBusyCommandSubcommandPolicy(name string, args []string) (chatBusyCommandPolicy, bool) {
	if name == "/queue" && len(args) > 0 && strings.EqualFold(strings.TrimSpace(args[0]), "clear") {
		// 清空队列属于状态变更，即使 /queue 整体标记为 immediate 也必须走旧判定
		//（queue-safe 白名单将其判为 reject），避免忙时丢队列数据。
		return chatBusyPolicyInherit, true
	}
	return chatBusyPolicyInherit, false
}
