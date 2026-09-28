package commands

import (
	"os"
	"strings"
)

// 忙时命令策略（方案 §5 P1-1/P1-2；档位命名对齐 §3.7.3 降级矩阵 I/S/D/R）。
//
// 该策略描述的是**目标态**：目前只有 immediate 通道骨架（P1），S/D/R 的完整
// 语义分别在 P2-4（副屏）与 P3（反馈文案）落地；灰度关闭时 I/S 一律回退 deferred，
// 保证与既有 chatSlashCommandQueueSafe 白名单逐条等价（P1-7/T18）。
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

// chatBusyCommandEnv 是 P1 灰度开关（默认关闭）：关闭时 I/S 回退 deferred，
// 路由行为与旧白名单逐条等价；打开后首批命令（§4.1）忙时立即可见。
const chatBusyCommandEnv = "AICLI_CHAT_BUSY_COMMAND"

func chatBusyCommandEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(chatBusyCommandEnv))) {
	case "1", "true", "on", "yes", "y", "enabled":
		return true
	default:
		return false
	}
}

// chatSlashCommandBusyPolicyFor 解析一段输入对应的目标忙时策略（fail-closed）：
//
//  1. 非 slash / 空输入 → inherit（不是命令，路由不由本函数决定）；
//  2. 命令名含别名经 catalog 归一，取 spec.BusyPolicy；
//  3. 不接受参数的命令携带参数 → inherit（只放行文档化的裸形式）；
//  4. 子命令级覆盖：`/queue clear` 保持现状（状态变更，不得 immediate）；
//  5. I/S 在灰度关闭时回退 D；D/R 直接返回；
//  6. inherit → 由旧白名单派生：queue-safe→D，否则→R（保证关闭时零行为差异）。
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

	// P2-3：显式启用运行时交互开关后，注册表成为策略的单一事实源（§3.8.2）。
	if chatRuntimeInteractionRegistryActive() {
		return chatBusyPolicyFromRuntimeSpec(text)
	}

	if chatSlashCommandQueueSafe(text) {
		return chatBusyPolicyDeferred
	}
	return chatBusyPolicyReject
}

// chatRuntimeInteractionRegistryActive 判定注册表是否接管路由策略（P2-3）。
// 需要两个开关同时满足，保证灰度可回退：
//   - P1 忙时通道开启（AICLI_CHAT_BUSY_COMMAND）；
//   - P2 开关被**显式设置**（AICLI_CHAT_RUNTIME_INTERACTION 非空，
//     auto/readonly/off 任一）。未显式设置时保持 P1 首批白名单行为（T18 等价）。
func chatRuntimeInteractionRegistryActive() bool {
	if !chatBusyCommandEnabled() {
		return false
	}
	return strings.TrimSpace(os.Getenv(runtimeInteractionEnv)) != ""
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
