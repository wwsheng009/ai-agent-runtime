package commands

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

// 阶段 F（方案 §12.3 P0，影子模式）：统一输入仲裁器的第一层落地。
//
// 本阶段不改变任何按键行为：KeyHandler 的 Arm/Disarm/Suspend/Resume 与 busy
// capture 的既有路径照旧执行，仲裁器只登记“谁应当拥有 stdin”并在 DebugMode 下
// 输出诊断快照，供 P1（只读替换 chatEscapeInterruptAvailable）与 P2（单一写者）
// 灰度对比使用。
//
// 优先级（§12.1）：modal > busy capture > 会话级 ESC 消费者 > 空闲编辑器；
// 独占级存在时 EscAvailable 只由独占级决定。

type chatInputOwner int

const (
	chatInputOwnerEditor chatInputOwner = iota
	chatInputOwnerESC
	chatInputOwnerBusyCapture
	chatInputOwnerModal
)

func (o chatInputOwner) String() string {
	switch o {
	case chatInputOwnerModal:
		return "modal"
	case chatInputOwnerBusyCapture:
		return "busy_capture"
	case chatInputOwnerESC:
		return "esc_consumer"
	default:
		return "editor"
	}
}

type chatInputArbitrationSnapshot struct {
	ModalDepth   int
	CaptureDepth int
	ESCConsumers int
	Owner        chatInputOwner
	// EscAvailable 表示“此刻按 Esc 应当能中断当前回合”，语义与阶段 B 的
	// chatEscapeInterruptAvailable 一致；modal 独占期间为 false。
	EscAvailable bool
}

type chatInputArbitrator struct {
	mu           sync.Mutex
	modalDepth   int
	captureDepth int
	escConsumers int
}

// enter registers one consumer of the given level and returns its idempotent
// release function.
func (a *chatInputArbitrator) enter(level chatInputOwner) func() {
	if a == nil {
		return func() {}
	}
	a.mu.Lock()
	a.addLocked(level, 1)
	a.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			a.addLocked(level, -1)
			a.mu.Unlock()
		})
	}
}

func (a *chatInputArbitrator) addLocked(level chatInputOwner, delta int) {
	clamp := func(value int) int {
		if value < 0 {
			return 0
		}
		return value
	}
	switch level {
	case chatInputOwnerModal:
		a.modalDepth = clamp(a.modalDepth + delta)
	case chatInputOwnerBusyCapture:
		a.captureDepth = clamp(a.captureDepth + delta)
	case chatInputOwnerESC:
		a.escConsumers = clamp(a.escConsumers + delta)
	}
}

func (a *chatInputArbitrator) snapshot() chatInputArbitrationSnapshot {
	if a == nil {
		return chatInputArbitrationSnapshot{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	owner := chatInputOwnerEditor
	switch {
	case a.modalDepth > 0:
		owner = chatInputOwnerModal
	case a.captureDepth > 0:
		owner = chatInputOwnerBusyCapture
	case a.escConsumers > 0:
		owner = chatInputOwnerESC
	}
	return chatInputArbitrationSnapshot{
		ModalDepth:   a.modalDepth,
		CaptureDepth: a.captureDepth,
		ESCConsumers: a.escConsumers,
		Owner:        owner,
		EscAvailable: a.modalDepth == 0 && (a.captureDepth > 0 || a.escConsumers > 0),
	}
}

// chatInputArbitrationEnforceEnv is the P2a gray switch. Default off: the
// desired state is only compared and logged. When set to a truthy value the
// arbitration result is written back to the KeyHandler, correcting any path
// that touched Arm/Suspend without registering with the arbitrator.
//
// P2b（删除散点调用）落地时应把该开关接入 runtime config
// （`chat.input_arbitration_enforce`），本环境变量是其当前的操作形态。
const chatInputArbitrationEnforceEnv = "AICLI_CHAT_INPUT_ARBITRATION_ENFORCE"

func chatInputArbitrationEnforced() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(chatInputArbitrationEnforceEnv))) {
	case "1", "true", "on", "yes", "enabled":
		return true
	default:
		return false
	}
}

// chatInputArbitrationKeyHandlerState is the single-writer target state for the
// KeyHandler derived from an arbitration snapshot.
type chatInputArbitrationKeyHandlerState struct {
	Arm     bool
	Suspend bool
}

// chatInputArbitrationDesiredKeyHandlerState maps the §12.1 priority model to
// KeyHandler bits:
//   - the handler is armed exactly while an ESC consumer exists (capture never
//     arms it by itself — the availability bits path for capture comes from the
//     InputQueue flag, not from Armed);
//   - L1 busy capture owns stdin: the bypass consumer stays armed but is
//     suspended (its goroutine keeps running; polling is paused).
//
// modal（L0）当前不改变 KeyHandler 位：priority prompt 的 Esc 经 capture 取消
// 链路生效（§12.5），该策略是否收紧留待 P2b 的真实终端数据决定。
func chatInputArbitrationDesiredKeyHandlerState(snapshot chatInputArbitrationSnapshot) chatInputArbitrationKeyHandlerState {
	return chatInputArbitrationKeyHandlerState{
		Arm:     snapshot.ESCConsumers > 0,
		Suspend: snapshot.CaptureDepth > 0,
	}
}

// syncChatInputArbitration compares the desired single-writer state with the
// actual KeyHandler bits. Divergence is always logged in DebugMode; it is only
// corrected when the gray switch is enabled. Scattered Arm/Suspend calls are
// still in place (P2b removes them), so normal paths agree by construction and
// the switch is inert unless some path bypasses the arbitrator.
func syncChatInputArbitration(session *ChatSession) {
	if session == nil || session.KeyHandler == nil || !session.KeyHandler.IsEnabled() {
		return
	}
	snapshot, ok := chatInputArbitrationSnapshotOf(session)
	if !ok {
		return
	}
	kh := session.KeyHandler
	desired := chatInputArbitrationDesiredKeyHandlerState(snapshot)
	actual := chatInputArbitrationKeyHandlerState{Arm: kh.Armed(), Suspend: kh.Suspended()}
	if desired == actual {
		return
	}
	enforced := chatInputArbitrationEnforced()
	if chatSessionDebugMode(session) {
		writeSessionDebugInfo(session, fmt.Sprintf(
			"[input-arbitration] divergence desired_arm=%t desired_suspend=%t actual_arm=%t actual_suspend=%t enforced=%t owner=%s esc=%d capture=%d modal=%d",
			desired.Arm,
			desired.Suspend,
			actual.Arm,
			actual.Suspend,
			enforced,
			snapshot.Owner.String(),
			snapshot.ESCConsumers,
			snapshot.CaptureDepth,
			snapshot.ModalDepth,
		), false)
	}
	if !enforced {
		return
	}
	if desired.Suspend {
		kh.Suspend()
	} else {
		kh.Resume()
	}
	if desired.Arm {
		kh.Arm()
	} else {
		kh.Disarm()
	}
}

func ensureChatInputArbitrator(session *ChatSession) *chatInputArbitrator {
	if session == nil {
		return nil
	}
	session.inputArbitrationMu.Lock()
	defer session.inputArbitrationMu.Unlock()
	if session.inputArbitration == nil {
		session.inputArbitration = &chatInputArbitrator{}
	}
	return session.inputArbitration
}

// chatInputArbitrationSnapshotOf returns the current snapshot when this session
// has already materialized an arbitrator. ok=false means nothing was ever
// registered, so callers fall back to the legacy KeyHandler/capture bits.
func chatInputArbitrationSnapshotOf(session *ChatSession) (chatInputArbitrationSnapshot, bool) {
	if session == nil {
		return chatInputArbitrationSnapshot{}, false
	}
	session.inputArbitrationMu.Lock()
	arb := session.inputArbitration
	session.inputArbitrationMu.Unlock()
	if arb == nil {
		return chatInputArbitrationSnapshot{}, false
	}
	return arb.snapshot(), true
}

// beginChatInputShadowLevel is the P0 shadow hook: it records one consumer and
// returns an idempotent release. It never touches KeyHandler state, so existing
// behavior is unchanged; with DebugMode it also logs the resulting snapshot
// next to the legacy availability answer for later diffing.
func beginChatInputShadowLevel(session *ChatSession, level chatInputOwner) func() {
	arb := ensureChatInputArbitrator(session)
	if arb == nil {
		return func() {}
	}
	release := arb.enter(level)
	logChatInputArbitrationSnapshot(session, "enter", level)
	syncChatInputArbitration(session)
	var once sync.Once
	return func() {
		once.Do(func() {
			release()
			logChatInputArbitrationSnapshot(session, "release", level)
			syncChatInputArbitration(session)
		})
	}
}

func logChatInputArbitrationSnapshot(session *ChatSession, event string, level chatInputOwner) {
	if session == nil || !chatSessionDebugMode(session) {
		return
	}
	snap, ok := chatInputArbitrationSnapshotOf(session)
	if !ok {
		return
	}
	writeSessionDebugInfo(session, fmt.Sprintf(
		"[input-arbitration] event=%s level=%s owner=%s arbitrated_esc=%t bits_esc=%t modal=%d capture=%d esc_consumers=%d",
		event,
		level.String(),
		snap.Owner.String(),
		snap.EscAvailable,
		chatInputEscBitsAvailable(session),
		snap.ModalDepth,
		snap.CaptureDepth,
		snap.ESCConsumers,
	), false)
}
