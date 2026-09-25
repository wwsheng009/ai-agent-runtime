package commands

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/encoding"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// persistedHistorySeedKind is deliberately narrower than encoding.ItemKind.
// It describes the canonical persisted transcript projection before it is
// merged with the best-effort runtime event log.
type persistedHistorySeedKind uint8

const (
	persistedHistorySeedUser persistedHistorySeedKind = iota
	persistedHistorySeedAssistant
	persistedHistorySeedSupplement
	persistedHistorySeedTool
)

type persistedHistorySeedUnit struct {
	identity         string
	kind             persistedHistorySeedKind
	content          string
	boundaryGroupKey string

	toolCallID string
	toolName   string
	toolOutput string
	toolError  string
	success    bool
	// toolDisplay 是与实时 ChatEvent 等价的 compact 工具结果投影
	// （renderSharedChatToolEvent）。非空时它是 Scene 工具单元格的权威
	// 头部：既用于与实时/日志重放建立的 item 匹配（避免 resume 后又追加
	// 一个原文单元格），也用于把历史种子渲染成与 live 相同的摘要形态。
	toolDisplay string

	// resolvedToolHead 缓存 toolHead() 的结果。匹配是「unit × item」的二次方
	// 扫描，而 toolHead() 要拼接整段工具输出：若每个候选 item 都重建一次，
	// 巨型恢复会话会退化成分配风暴（生产 pprof：4056 个 unit × ~4000 个
	// item，goroutine 长时间卡在 toolHead 的字符串分配上，会话永远不渲染）。
	resolvedToolHead string
}

// seedPersistedHistory reconciles canonical persisted history with the Scene
// model rebuilt from the runtime event log. The log is not durable transcript
// authority: a non-empty Scene can legitimately contain only part of the
// canonical conversation. Every unit therefore has a deterministic identity
// and is either matched to an existing semantic item or imported once.
func (b *chatRuntimeEventBridge) seedPersistedHistory(messages []runtimetypes.Message, header string) {
	b.seedPersistedHistoryWithLoadGrant(messages, header, false)
}

// seedPersistedHistoryForSessionLoad is the session-load variant of
// seedPersistedHistory (/resume, /load, startup restore). It differs in exactly
// one way: the armed replacement is published even when this call imported no
// new unit, because the one-shot replay authorization belongs to the load, not
// to the delta.
//
// Startup replays the runtime event log into the Scene before canonical history
// is seeded, so a session whose log already covers the conversation reconciles
// with seeded=false while native scrollback still belongs to the previous
// process (usually nothing but the shell line that launched aicli). Skipping the
// replacement there leaves the terminal owner without the loaded generation:
// the Scene is correct, the resident native scrollback is not, and the user can
// only see the viewport tail — the session looks unrecoverable even though every
// message was loaded. Regular Scene updates must keep using seedPersistedHistory
// so resize/stream/theme traffic can never mint a destructive replay.
func (b *chatRuntimeEventBridge) seedPersistedHistoryForSessionLoad(messages []runtimetypes.Message, header string) {
	b.seedPersistedHistoryWithLoadGrant(messages, header, true)
}

func (b *chatRuntimeEventBridge) seedPersistedHistoryWithLoadGrant(
	messages []runtimetypes.Message, header string, sessionLoad bool,
) {
	if b == nil || b.renderEncoder == nil || len(messages) == 0 {
		return
	}

	units := buildPersistedHistorySeedUnits(messages)
	if len(units) == 0 {
		return
	}

	b.renderMu.Lock()
	seeded := b.seedPersistedHistoryLocked(units, header)
	b.renderMu.Unlock()
	if !seeded && !sessionLoad {
		// 非会话加载路径：仅在本次实际新增了 header/unit 时发布 snapshot；
		// 否则 Scene 未变，全量 ReplaceTranscriptAction 会触发历史重放动画且
		// 无任何内容更新。
		return
	}
	// 会话加载（/resume、/load、启动恢复，以及首次装配 canonical 历史的
	// /history）在这里请求一次 scrollback 替换：canonical 历史刚装配进 Scene，
	// 这个 replacement snapshot 本身就是授权的携带者（ArmScrollbackReplay
	// 字段），与它授权的 Scene 在同一 action 内原子进入 reducer。reducer 对
	// “快照已安装”的替换同样会授予授权（见 controller_state.go 的
	// ReplaceTranscriptAction 分支），因此即使本次没有新增 unit，装载好的
	// 生成仍然会替换原生 scrollback。正常交互（resize/流式增量/主题切换/写入
	// 恢复）走上面的提前返回，永远拿不到重放授权。
	b.sessionInteractionReplacementSnapshot()
}

// seedPersistedHistoryLocked is the render-transaction half of history seed.
// It exists so destructive transcript replacement can rebuild the encoder and
// Scene under the same renderMu ownership before publishing one new snapshot.
func (b *chatRuntimeEventBridge) seedPersistedHistoryLocked(units []persistedHistorySeedUnit, header string) bool {
	if b == nil || b.renderEncoder == nil ||
		(len(units) == 0 && strings.TrimSpace(header) == "") {
		return false
	}
	if b.historySeedSeen == nil {
		b.historySeedSeen = make(map[string]struct{})
	}
	seededAny := false
	snapshot := b.renderEncoder.Snapshot()
	// 注意：这里的匹配作用域**不**排除历史增量装载已经认领的 item。全量 reconcile
	// 描述的是完整的 canonical 转录，它的 unit 与屏幕上的 item 按 canonical 顺序
	// 一一配对；排除认领项只会让已经装载过（但 identity 作用域不同）的 unit 落空，
	// 然后被当成缺失内容重复导入。认领集只用于「按页增量装载」，那里的匹配范围
	// 必须排除别人已经认领的单元格（见 seedPersistedHistoryPageLocked）。
	matchedItemIDs := make(map[string]struct{})
	headerSeededNow := false
	// A history header must lead the imported transcript. Once a partial event
	// log already owns the prefix, appending the header would put it in the
	// middle of canonical history, so preserve order and reconcile rows only.
	if (snapshot == nil || len(snapshot.Items) == 0) && strings.TrimSpace(header) != "" {
		headerUnit := persistedHistorySeedUnit{
			identity: persistedHistoryHeaderIdentity(header, units),
			kind:     persistedHistorySeedSupplement,
			content:  header,
		}
		if _, alreadySeeded := b.historySeedSeen[headerUnit.identity]; !alreadySeeded {
			headerUnit.apply(b)
			b.historySeedSeen[headerUnit.identity] = struct{}{}
			headerSeededNow = true
			seededAny = true
		}
	}

	// Reconcile all canonical units before importing any missing one. Besides
	// preserving occurrence order, the first pass lets a missing reasoning or
	// assistant section adopt the exact runtime request identity of its matched
	// sibling. A one-pass append would assign the deterministic persisted key
	// before discovering that the other section already has a live/replayed key.
	if strings.TrimSpace(header) != "" {
		headerUnit := persistedHistorySeedUnit{
			identity: persistedHistoryHeaderIdentity(header, units),
			kind:     persistedHistorySeedSupplement,
			content:  header,
		}
		if _, seeded := b.historySeedSeen[headerUnit.identity]; seeded && !headerSeededNow {
			persistedHistoryUnitMatch(snapshot, headerUnit, matchedItemIDs)
		}
	}
	matchedUnits := make(map[string]*encoding.Item, len(units))
	groupAliases := make(map[string]string)
	for _, unit := range units {
		item := persistedHistoryUnitMatch(snapshot, unit, matchedItemIDs)
		if item == nil {
			continue
		}
		matchedUnits[unit.identity] = item
		b.rememberHistorySeedItem(unit.identity, item.ID)
		if unit.boundaryGroupKey != "" && item.BoundaryGroupKey != "" {
			if _, exists := groupAliases[unit.boundaryGroupKey]; !exists {
				groupAliases[unit.boundaryGroupKey] = item.BoundaryGroupKey
			}
		}
	}
	// representedItemIDs 按 canonical 顺序记录已匹配 unit 的 item 身份。缺失
	// unit 的插入锚点由它计算（见下方 import 循环）：锚点 = 下一条已经存在于
	// 模型中的规范 unit。
	representedItemIDs := make([]string, len(units))
	for index, unit := range units {
		if item := matchedUnits[unit.identity]; item != nil {
			representedItemIDs[index] = item.ID
		}
	}
	insertedItemIDs := make([]string, len(units))
	for index, unit := range units {
		if _, alreadySeeded := b.historySeedSeen[unit.identity]; alreadySeeded {
			continue
		}
		if matchedUnits[unit.identity] != nil {
			b.historySeedSeen[unit.identity] = struct{}{}
			continue
		}
		if alias := groupAliases[unit.boundaryGroupKey]; alias != "" {
			unit.boundaryGroupKey = alias
		}
		// 事件日志重放把同一请求的 reasoning 增量累积进一个单元格，canonical
		// 的单个推理块是该单元格正文的子集。内容已经在屏幕上（只是与同请求的
		// 其它片段合并），再导入会渲染成第二个推理单元格，因此按“已表达”处理。
		if unit.kind == persistedHistorySeedSupplement && unit.boundaryGroupKey != "" &&
			persistedHistoryReasoningRepresented(snapshot, unit.content) {
			b.historySeedSeen[unit.identity] = struct{}{}
			continue
		}
		if unit.kind == persistedHistorySeedTool && strings.TrimSpace(unit.toolCallID) != "" {
			// 同一调用身份已经拥有终态工具单元格（live 总线投影，或事件日志
			// 重放建立的 display_head 单元格），但头部形态与历史投影不同
			// （例如 live 头部含 progress 段标签/实时耗时）。此时历史种子只
			// 是同一行的旧投影，必须视为已导入：再走一次 SubmitToolCall 会在
			// “终态后同 callID 重新发起”的语义下新建第二个单元格，形成重复。
			if item := b.renderEncoder.ToolItemForCall(unit.toolCallID); item != nil && item.Status.Terminal() {
				b.historySeedSeen[unit.identity] = struct{}{}
				continue
			}
		}
		// 缺失 unit 的位置：模型已经含有规范上更晚的内容时（重放覆盖了会话
		// 后半段，例如 resume 时事件日志已包含整场会话），追加到尾部会把旧内容
		// 排到新内容之后——用户看到的“尾部重复块、最后一条消息被顶到中间”正是
		// 这个顺序倒置。此时插入到下一条已表达 unit 之前，保持 canonical 顺序；
		// 模型里没有更晚的规范内容时（事件日志只覆盖前缀）仍追加到尾部。
		insertedItemIDs[index] = unit.applyBefore(b, persistedHistorySuccessorAnchor(representedItemIDs, index))
		b.historySeedSeen[unit.identity] = struct{}{}
		b.claimHistorySeedItem(unit.identity, insertedItemIDs[index])
		seededAny = true
	}
	b.rememberCanonicalHistoryFront(units, representedItemIDs, insertedItemIDs)
	return seededAny
}

// rememberCanonicalHistoryFront 记录 canonical 区域最早的 Scene item 身份。
// 「按页增量装载」把更早的页插到这个 item 之前；它必须是 canonical 顺序意义上
// 的第一条已装载内容，否则补回来的历史会插到会话中间。
func (b *chatRuntimeEventBridge) rememberCanonicalHistoryFront(
	units []persistedHistorySeedUnit, representedItemIDs, insertedItemIDs []string,
) {
	if b == nil {
		return
	}
	for index := range units {
		id := ""
		if index < len(insertedItemIDs) {
			id = insertedItemIDs[index]
		}
		if id == "" && index < len(representedItemIDs) {
			id = representedItemIDs[index]
		}
		if id == "" {
			id = b.historySeedItemByIdentity[units[index].identity]
		}
		if id != "" {
			b.historySeedFrontItemID = id
			return
		}
	}
}

// rememberHistorySeedItem 记录 unit identity → Scene item 身份。增量分页装载
// 在「本页已装载过」时仍需推进插入游标，只有身份表能给出该 unit 的落点。
func (b *chatRuntimeEventBridge) rememberHistorySeedItem(identity, itemID string) {
	if b == nil {
		return
	}
	identity = strings.TrimSpace(identity)
	itemID = strings.TrimSpace(itemID)
	if identity == "" || itemID == "" {
		return
	}
	if b.historySeedItemByIdentity == nil {
		b.historySeedItemByIdentity = make(map[string]string)
	}
	b.historySeedItemByIdentity[identity] = itemID
}

// claimHistorySeedItem 记住「这个 Scene item 已经被某次 seed 认领」。跨页匹配
// 必须排除它们，否则同内容的另一条消息会被误判为已表达而丢弃。
func (b *chatRuntimeEventBridge) claimHistorySeedItem(identity, itemID string) {
	if b == nil || strings.TrimSpace(itemID) == "" {
		return
	}
	b.rememberHistorySeedItem(identity, itemID)
	if b.historySeedClaimedItems == nil {
		b.historySeedClaimedItems = make(map[string]struct{})
	}
	b.historySeedClaimedItems[itemID] = struct{}{}
}

// seedPersistedHistoryPage 增量 reconcile 一个「较早的 canonical 页」：只匹配
// 与导入这一页的 unit，并把它们按 canonical 顺序插到已装载区域之前。
//
// 这是「边读取、边渲染」的数据面入口：调用方每从分页后端取回一页就调用一次，
// 用户看到的历史从最新一页开始逐页补齐，而不是等全量读完才一次性绘制。
func (b *chatRuntimeEventBridge) seedPersistedHistoryPage(messages []runtimetypes.Message) bool {
	if b == nil || b.renderEncoder == nil || len(messages) == 0 {
		return false
	}
	units := buildPersistedHistorySeedUnitsScoped(messages, persistedHistoryPageScope(messages))
	if len(units) == 0 {
		return false
	}
	b.renderMu.Lock()
	seeded := b.seedPersistedHistoryPageLocked(units)
	b.renderMu.Unlock()
	if !seeded {
		return false
	}
	// 增量页只更新语义转录（非授权式快照）：原生 scrollback 的替换授权属于
	// 整次会话装载，由装载收尾的那一次 seed 一次性铸造，不能让每一页都触发
	// 一次销毁式重放。
	b.sessionInteractionSnapshot()
	return true
}

// seedPersistedHistoryPageLocked 是 seedPersistedHistoryPage 的渲染事务半程。
//
// 插入方向：一页较早的历史必须整体落在已装载区域之前，因此按页内逆序逐条
// 「插入到当前游标之前」，插完把游标前移到刚插入的单元格，最终得到页内正序。
func (b *chatRuntimeEventBridge) seedPersistedHistoryPageLocked(units []persistedHistorySeedUnit) bool {
	if b == nil || b.renderEncoder == nil || len(units) == 0 {
		return false
	}
	if b.historySeedSeen == nil {
		b.historySeedSeen = make(map[string]struct{})
	}
	snapshot := b.renderEncoder.Snapshot()
	matched := make(map[string]struct{}, len(b.historySeedClaimedItems))
	for id := range b.historySeedClaimedItems {
		matched[id] = struct{}{}
	}
	cursor := strings.TrimSpace(b.historySeedFrontItemID)
	seededAny := false
	for index := len(units) - 1; index >= 0; index-- {
		unit := units[index]
		if _, alreadySeeded := b.historySeedSeen[unit.identity]; alreadySeeded {
			// 同一页被重复装载（重试/幂等重放）：不重复导入，但仍要把游标
			// 前移到该单元格，后续更早的 unit 才插得到它前面。
			if id := b.historySeedItemByIdentity[unit.identity]; id != "" {
				cursor = id
			}
			continue
		}
		if item := persistedHistoryUnitMatch(snapshot, unit, matched); item != nil {
			// 已在屏幕上（事件日志重放等）：不重复导入，但更早的 unit 必须
			// 插到它之前，canonical 顺序才正确。
			b.historySeedSeen[unit.identity] = struct{}{}
			b.claimHistorySeedItem(unit.identity, item.ID)
			cursor = item.ID
			continue
		}
		if unit.kind == persistedHistorySeedSupplement && unit.boundaryGroupKey != "" &&
			persistedHistoryReasoningRepresented(snapshot, unit.content) {
			b.historySeedSeen[unit.identity] = struct{}{}
			continue
		}
		if unit.kind == persistedHistorySeedTool && strings.TrimSpace(unit.toolCallID) != "" {
			if item := b.renderEncoder.ToolItemForCall(unit.toolCallID); item != nil && item.Status.Terminal() {
				b.historySeedSeen[unit.identity] = struct{}{}
				continue
			}
		}
		insertedID := unit.applyBefore(b, cursor)
		b.historySeedSeen[unit.identity] = struct{}{}
		b.claimHistorySeedItem(unit.identity, insertedID)
		if insertedID != "" {
			cursor = insertedID
		}
		seededAny = true
	}
	if strings.TrimSpace(cursor) != "" {
		b.historySeedFrontItemID = cursor
	}
	return seededAny
}

// persistedHistoryPageScope 为「按页增量装载」派生一个稳定的页作用域：优先用
// 页首消息的持久化身份（分页后端会补齐 message_id），缺失时退回页内容的哈希。
// 作用域只能依赖页自身内容，同一页重复装载（重试）才会得到同一 identity。
func persistedHistoryPageScope(messages []runtimetypes.Message) string {
	if len(messages) == 0 {
		return ""
	}
	if id := strings.TrimSpace(runtimetypes.MessageID(messages[0])); id != "" {
		return "page:" + id
	}
	sum := sha256.Sum256([]byte(messages[0].Role + "\x00" + messages[0].Content))
	return fmt.Sprintf("page:%x:%d", sum[:8], len(messages))
}

// replaceCanonicalHistoryProjection rebuilds the owned transcript from the
// post-mutation canonical history. Backtrack removes durable conversation
// content, so append-only reconciliation is incorrect: old Scene cells must
// disappear before the surviving canonical cells and follow-up command result
// are committed. The caller has already completed the domain mutation and no
// active model run may be rendering into this bridge.
//
// The history-reset marker is persisted in the runtime event log. On a later
// startup replay it discards pre-backtrack event rows before reseeding this
// canonical snapshot, preventing deleted turns from returning to the Scene.
func (b *chatRuntimeEventBridge) replaceCanonicalHistoryProjection(messages []runtimetypes.Message, header string) bool {
	if b == nil {
		return false
	}
	units := buildPersistedHistorySeedUnits(messages)
	b.renderMu.Lock()
	if b.runActive {
		b.renderMu.Unlock()
		return false
	}
	b.resetCanonicalHistoryProjectionLocked()
	b.seedPersistedHistoryLocked(units, header)
	b.appendHistoryResetLog(messages, header)
	b.renderMu.Unlock()
	// 显式 canonical 历史重写（/backtrack、截断、会话加载回放）同样只授权一次
	// 全量重放：旧 Scene 的物理行必须先被替换，再按新 canonical 顺序重建。
	// 授权随 replacement snapshot 一起发布，执行器不可能再用替换前的 Scene
	// 组合出破坏性事务。
	b.sessionInteractionReplacementSnapshot()
	return true
}

// resetCanonicalHistoryProjectionLocked clears all derived render state. It
// intentionally does not infer a suffix from display rows: the caller supplies
// canonical source and the next seed recreates stable semantic identities.
// Caller must hold renderMu.
func (b *chatRuntimeEventBridge) resetCanonicalHistoryProjectionLocked() {
	if b == nil {
		return
	}
	b.renderEncoder = encoding.NewEventEncoder()
	// bridge 构造时启用了 reasoning ordering barrier（chat_runtime_events.go
	// newChatRuntimeEventBridge）；重建 encoder 必须恢复同一配置，否则
	// replayEventLog 重放出的 Scene 与 live 路径（assistant.message 的
	// barrier 解除 upsert）不一致，破坏 replay 等价（live/replay cell
	// Revision 漂移）。
	b.renderEncoder.EnableReasoningOrderingBarrier(true)
	b.historySeedSeen = make(map[string]struct{})
	b.historySeedClaimedItems = nil
	b.historySeedItemByIdentity = nil
	b.historySeedFrontItemID = ""
	b.interactionAnchorMu.Lock()
	b.interactionAnchor = nil
	b.interactionAnchorAt = time.Time{}
	b.interactionAnchorSource = ""
	b.pendingInteractionSource = ""
	b.pendingInteractionTail = nil
	b.interactionAnchorMu.Unlock()

	b.sceneMu.Lock()
	b.renderScene = scene.New()
	b.renderMapper = scene.NewChangeSetMapper(b.renderScene)
	b.sceneApplyFailures = 0
	b.sceneLastError = ""
	b.sceneMu.Unlock()
}

// resetRenderPlaneForNewSession clears the bridge render data plane after /new
// creates a fresh runtime session. Unlike replaceCanonicalHistoryProjection it
// does not reseed anything: the new conversation starts with an empty Scene.
// It mirrors replaceCanonicalHistoryProjection's guard and refuses to reset
// while a model run is actively rendering into this bridge.
func (b *chatRuntimeEventBridge) resetRenderPlaneForNewSession() {
	if b == nil {
		return
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	if b.runActive {
		return
	}
	b.resetCanonicalHistoryProjectionLocked()
}

func buildPersistedHistorySeedUnits(messages []runtimetypes.Message) []persistedHistorySeedUnit {
	return buildPersistedHistorySeedUnitsScoped(messages, "")
}

// buildPersistedHistorySeedUnitsScoped 是「按页增量装载」使用的带作用域版本。
// identity 前缀带上页作用域，避免内容完全相同的两条消息分布在不同页时，因为
// 内容哈希 + 页内序号相同而撞进同一个 identity：那样后到的一页会被
// historySeedSeen 直接当成「已装载」跳过，屏幕上少一条真实消息。同一页重复
// 装载仍得到同一 identity（作用域只由页自身内容派生），保持幂等。
func buildPersistedHistorySeedUnitsScoped(messages []runtimetypes.Message, scope string) []persistedHistorySeedUnit {
	toolCalls := indexChatHistoryToolCalls(messages)
	units := make([]persistedHistorySeedUnit, 0, len(messages))
	occurrences := make(map[string]uint64)
	assistantRequestOccurrences := make(map[string]uint64)
	appendUnit := func(unit persistedHistorySeedUnit) {
		base := unit.stableKey()
		if scope != "" {
			base = scope + ":" + base
		}
		occurrences[base]++
		unit.identity = fmt.Sprintf("persisted-history:%s:%d", base, occurrences[base])
		units = append(units, unit)
	}

	for index := range messages {
		message := messages[index]
		role := strings.ToLower(strings.TrimSpace(message.Role))
		content := message.Content
		switch role {
		case "user":
			if strings.TrimSpace(content) != "" {
				appendUnit(persistedHistorySeedUnit{kind: persistedHistorySeedUser, content: content})
			}
		case "assistant":
			var reasoningDisplay string
			if reasoning := finalReasoningBlock(&message); reasoning != nil {
				reasoningDisplay = reasoning.RawDisplayText()
			}
			requestBase := persistedAssistantRequestStableKey(content, reasoningDisplay)
			assistantRequestOccurrences[requestBase]++
			groupKey := fmt.Sprintf("persisted-assistant-request:%s:%d",
				requestBase, assistantRequestOccurrences[requestBase])
			if strings.TrimSpace(reasoningDisplay) != "" {
				appendUnit(persistedHistorySeedUnit{
					kind: persistedHistorySeedSupplement, content: reasoningDisplay,
					boundaryGroupKey: groupKey,
				})
			}
			if strings.TrimSpace(content) != "" {
				appendUnit(persistedHistorySeedUnit{
					kind: persistedHistorySeedAssistant, content: content,
					boundaryGroupKey: groupKey,
				})
			}
		case "tool":
			callID := strings.TrimSpace(message.ToolCallID)
			call := toolCalls[callID]
			output, toolErr := splitChatHistoryToolResult(message)
			if output == "" && toolErr == "" {
				output = content
			}
			name := firstNonEmptyChatValue(strings.TrimSpace(call.Name), callID, "tool")
			if callID == "" {
				fallback := firstNonEmptyChatValue(output, toolErr, content)
				if strings.TrimSpace(fallback) != "" {
					appendUnit(persistedHistorySeedUnit{
						kind:    persistedHistorySeedSupplement,
						content: fmt.Sprintf("[tool] %s", fallback),
					})
				}
				continue
			}
			appendUnit(persistedHistorySeedUnit{
				kind:        persistedHistorySeedTool,
				toolCallID:  callID,
				toolName:    name,
				toolOutput:  output,
				toolError:   toolErr,
				success:     strings.TrimSpace(toolErr) == "",
				toolDisplay: chatHistoryToolDisplay(message, name, call.Args),
			})
		case "system":
			if strings.TrimSpace(content) != "" {
				appendUnit(persistedHistorySeedUnit{kind: persistedHistorySeedSupplement, content: content})
			}
		default:
			if strings.TrimSpace(content) != "" {
				appendUnit(persistedHistorySeedUnit{
					kind:    persistedHistorySeedSupplement,
					content: fmt.Sprintf("[%s] %s", role, content),
				})
			}
		}
	}
	return units
}

func persistedAssistantRequestStableKey(content, reasoning string) string {
	var builder strings.Builder
	for _, value := range []string{content, reasoning} {
		fmt.Fprintf(&builder, "%d:%s|", len(value), value)
	}
	sum := sha256.Sum256([]byte(builder.String()))
	return fmt.Sprintf("%x", sum[:])
}

func (u persistedHistorySeedUnit) stableKey() string {
	var builder strings.Builder
	for _, value := range []string{
		strconv.Itoa(int(u.kind)), u.content, u.toolCallID, u.toolName,
		u.toolOutput, u.toolError, strconv.FormatBool(u.success), u.toolDisplay,
	} {
		fmt.Fprintf(&builder, "%d:%s|", len(value), value)
	}
	sum := sha256.Sum256([]byte(builder.String()))
	return fmt.Sprintf("%x", sum[:])
}

func persistedHistoryHeaderIdentity(header string, units []persistedHistorySeedUnit) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "%d:%s|", len(header), header)
	for _, unit := range units {
		fmt.Fprintf(&builder, "%d:%s|", len(unit.identity), unit.identity)
	}
	sum := sha256.Sum256([]byte(builder.String()))
	return fmt.Sprintf("persisted-history-header:%x", sum[:])
}

func persistedHistoryUnitMatch(snapshot *encoding.RenderModel, unit persistedHistorySeedUnit, matched map[string]struct{}) *encoding.Item {
	if snapshot == nil {
		return nil
	}
	// tool 单元的原文头部在一次扫描内是常量：先算一次，供下面的每个候选
	// item 复用（见 resolvedToolHead 字段说明）。
	if unit.kind == persistedHistorySeedTool {
		unit.resolvedToolHead = unit.toolHead()
	}
	for _, item := range snapshot.Items {
		if item == nil || item.ID == "" {
			continue
		}
		if _, used := matched[item.ID]; used || !unit.matches(item) {
			continue
		}
		matched[item.ID] = struct{}{}
		return item
	}
	return nil
}

func (u persistedHistorySeedUnit) matches(item *encoding.Item) bool {
	if item == nil || !item.Status.Terminal() {
		return false
	}
	switch u.kind {
	case persistedHistorySeedUser:
		return item.Kind == encoding.KindUser && item.Head == u.content
	case persistedHistorySeedAssistant:
		return item.Kind == encoding.KindAssistant && item.Head == u.content
	case persistedHistorySeedSupplement:
		if item.Kind == encoding.KindReasoning && u.boundaryGroupKey != "" {
			return persistedReasoningContentMatches(item.Head, u.content)
		}
		return (item.Kind == encoding.KindSupplement || item.Kind == encoding.KindReasoning || item.Kind == encoding.KindSystem) && item.Head == u.content
	case persistedHistorySeedTool:
		if item.Kind != encoding.KindToolCall {
			return false
		}
		// 优先按实时 compact 投影匹配（live Scene / 事件日志重放建立的
		// item 头部即此形态）；无投影数据时保持旧原文头语义。
		if u.toolDisplay != "" && item.Head == u.toolDisplay {
			return true
		}
		if u.resolvedToolHead != "" {
			return item.Head == u.resolvedToolHead
		}
		return item.Head == u.toolHead()
	default:
		return false
	}
}

func persistedReasoningContentMatches(head, content string) bool {
	if head == content {
		return true
	}
	if strings.TrimSpace(content) == "" {
		return false
	}
	return persistedReasoningBody(head) == persistedReasoningContentBody(content)
}

// persistedReasoningContentBody 归一化 canonical 推理正文。
func persistedReasoningContentBody(content string) string {
	return strings.TrimLeft(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
}

// persistedReasoningBody 返回 reasoning 单元格的语义正文：去掉首行分隔线
// （"…… reasoning ……"）与尾部 "end reasoning" 分隔线。重放建立的单元格头部
// 可能带展示分隔线，canonical 块不带。
func persistedReasoningBody(head string) string {
	// 只在确有 CR 时才复制整段头部：该函数在「unit × item」扫描里被逐 item
	// 调用，无条件 ReplaceAll 会为每个候选复制一份（可能很大的）正文。
	if strings.IndexByte(head, '\r') >= 0 {
		head = strings.ReplaceAll(head, "\r\n", "\n")
	}
	firstLF := strings.IndexByte(head, '\n')
	if firstLF < 0 {
		return ""
	}
	body := strings.TrimLeft(head[firstLF+1:], "\n")
	if lastLF := strings.LastIndexByte(body, '\n'); lastLF >= 0 &&
		strings.Contains(strings.ToLower(body[lastLF+1:]), "end reasoning") {
		body = body[:lastLF]
	}
	return body
}

// persistedHistoryReasoningContainmentMinBytes 是“正文包含即已表达”判定的最小
// 长度：过短的正文（分隔线残留、占位文本）可能在无关的长推理里偶然出现，从而
// 让一条真正缺失的推理块被误判为已表达。真实的 canonical 推理块远超此阈值。
const persistedHistoryReasoningContainmentMinBytes = 64

// persistedReasoningBodyContains 报告重放累积出的推理单元格是否已经表达了这条
// canonical 推理块。事件日志把同一请求的 reasoning 记为增量片段，重放把它们
// 累积成一个单元格，其正文是 canonical 单块正文的超集（多片段合并）。
func persistedReasoningBodyContains(head, content string) bool {
	needle := persistedReasoningContentBody(content)
	if len(needle) < persistedHistoryReasoningContainmentMinBytes {
		return false
	}
	normalized := head
	if strings.IndexByte(head, '\r') >= 0 {
		normalized = strings.ReplaceAll(head, "\r\n", "\n")
	}
	if strings.Contains(normalized, needle) {
		return true
	}
	body := persistedReasoningBody(head)
	return body != "" && strings.Contains(body, needle)
}

// persistedHistoryReasoningRepresented 报告模型里是否已经存在表达了该推理块的
// 终态 reasoning 单元格（精确相等，或累积单元格包含该块正文）。
func persistedHistoryReasoningRepresented(snapshot *encoding.RenderModel, content string) bool {
	if snapshot == nil || strings.TrimSpace(content) == "" {
		return false
	}
	for _, item := range snapshot.Items {
		if item == nil || !item.Status.Terminal() || item.Kind != encoding.KindReasoning {
			continue
		}
		if persistedReasoningContentMatches(item.Head, content) ||
			persistedReasoningBodyContains(item.Head, content) {
			return true
		}
	}
	return false
}

// persistedHistorySuccessorAnchor 返回 index 之后第一条已表达 unit 的 item
// 身份，用作缺失 unit 的插入锚点。空字符串表示模型里没有更晚的规范内容，缺失
// unit 可以安全追加到模型尾部。
func persistedHistorySuccessorAnchor(representedItemIDs []string, index int) string {
	for next := index + 1; next < len(representedItemIDs); next++ {
		if id := representedItemIDs[next]; id != "" {
			return id
		}
	}
	return ""
}

func (u persistedHistorySeedUnit) toolHead() string {
	result := u.toolOutput
	if strings.TrimSpace(result) == "" {
		result = u.toolError
	}
	if result == "" {
		return u.toolName
	}
	return u.toolName + "\n" + result
}

func (u persistedHistorySeedUnit) apply(b *chatRuntimeEventBridge) {
	if b == nil || b.renderEncoder == nil {
		return
	}
	switch u.kind {
	case persistedHistorySeedUser:
		b.applyChangeSet(b.renderEncoder.SubmitUserInput(u.content))
	case persistedHistorySeedAssistant:
		b.applyChangeSet(b.renderEncoder.SubmitAssistantWithBoundaryGroup(u.content, u.boundaryGroupKey))
	case persistedHistorySeedSupplement:
		if u.boundaryGroupKey != "" {
			// 带请求身份的 supplement 是重建的 reasoning（persisted-
			// assistant-request:*）。必须以与 live 路径一致的
			// KindReasoning + divider Head 导入，否则恢复会话后
			// reasoning 正文会退化为普通 supplement 文本，丢失
			// "…… reasoning ……" 与 "…… end reasoning ……" 分隔线。
			b.applyChangeSet(b.renderEncoder.SubmitReasoningWithBoundaryGroup(u.content, u.boundaryGroupKey))
		} else {
			b.applyChangeSet(b.renderEncoder.SubmitSupplementWithBoundaryGroup(u.content, u.boundaryGroupKey))
		}
	case persistedHistorySeedTool:
		// A persisted tool result is final history, not a viewport-only running
		// row. Establish the stable call identity before the result so the
		// encoder maps both mutations to one committed tool-chain Scene cell.
		b.applyChangeSet(b.renderEncoder.SubmitToolCall(u.toolCallID, u.toolName, nil))
		if strings.TrimSpace(u.toolDisplay) != "" {
			// 与实时链路同源：live 通过 SubmitToolResultDisplay 注入
			// compact 摘要，历史种子必须复用同一入口，否则 resume 会把
			// 原文当作工具输出再渲染一份未摘要单元格。
			b.applyChangeSet(b.renderEncoder.SubmitToolResultDisplay(u.toolCallID, u.toolDisplay))
			return
		}
		b.applyChangeSet(b.renderEncoder.SubmitToolResult(
			u.toolCallID, u.toolName, u.toolOutput, u.toolError, u.success,
		))
	}
}

// applyBefore 与 apply 语义相同，但把缺失单元格插入到 anchorItemID 之前；
// 锚点为空时退化为 apply（追加到模型尾部）。
//
// resume 时事件日志可能已经重放会话后半段，缺失的规范 unit 必须插到下一条已
// 表达 unit 之前，模型顺序才与 canonical 顺序一致（否则旧内容被追加到新内容
// 之后，最后一条消息被顶到中间）。锚定路径复用与 apply 相同的终态构造，只有
// 工具单元格需要额外登记 callID，后续 SubmitToolResult* 才能就地归并。
// 返回新单元格的 Scene item 身份（追加路径退化为模型尾部），供「按页增量装载」
// 推进插入游标。
func (u persistedHistorySeedUnit) applyBefore(b *chatRuntimeEventBridge, anchorItemID string) string {
	if b == nil || b.renderEncoder == nil {
		return ""
	}
	if strings.TrimSpace(anchorItemID) == "" {
		u.apply(b)
		if tail := b.renderEncoder.Tail(); tail != nil {
			return tail.ItemID
		}
		return ""
	}
	switch u.kind {
	case persistedHistorySeedUser:
		return applyChangeSetItemID(b, b.renderEncoder.SubmitPersistedHistoryCellBefore(
			encoding.KindUser, u.content, "", anchorItemID,
		))
	case persistedHistorySeedAssistant:
		return applyChangeSetItemID(b, b.renderEncoder.SubmitPersistedHistoryCellBefore(
			encoding.KindAssistant, u.content, u.boundaryGroupKey, anchorItemID,
		))
	case persistedHistorySeedSupplement:
		kind := encoding.KindSupplement
		if u.boundaryGroupKey != "" {
			// 带请求身份的 supplement 是重建的 reasoning，必须与 live 路径
			// 一致地导入为 KindReasoning + divider Head（见 apply）。
			kind = encoding.KindReasoning
		}
		return applyChangeSetItemID(b, b.renderEncoder.SubmitPersistedHistoryCellBefore(
			kind, u.content, u.boundaryGroupKey, anchorItemID,
		))
	case persistedHistorySeedTool:
		itemID := applyChangeSetItemID(b, b.renderEncoder.SubmitPersistedHistoryToolCallBefore(
			u.toolCallID, u.toolName, anchorItemID,
		))
		if strings.TrimSpace(u.toolDisplay) != "" {
			b.applyChangeSet(b.renderEncoder.SubmitToolResultDisplay(u.toolCallID, u.toolDisplay))
			return itemID
		}
		b.applyChangeSet(b.renderEncoder.SubmitToolResult(
			u.toolCallID, u.toolName, u.toolOutput, u.toolError, u.success,
		))
		return itemID
	}
	return ""
}

// applyChangeSetItemID 提交一次增量变更集并返回它新增/更新的单元格身份。
func applyChangeSetItemID(b *chatRuntimeEventBridge, cs *encoding.ChangeSet) string {
	if b == nil {
		return ""
	}
	b.applyChangeSet(cs)
	if cs == nil || len(cs.Changes) == 0 || cs.Changes[0].Item == nil {
		return ""
	}
	return cs.Changes[0].Item.ID
}
