package commands

import "testing"

// 阶段 F P0（影子模式）：仲裁器只做登记与快照，不改变按键行为。
// 优先级：modal > busy capture > ESC 消费者 > 空闲编辑器。

func TestChatInputArbitrationSnapshotMatrix(t *testing.T) {
	arb := &chatInputArbitrator{}

	if snap := arb.snapshot(); snap.Owner != chatInputOwnerEditor || snap.EscAvailable {
		t.Fatalf("idle snapshot must be editor-level without esc, got %#v", snap)
	}

	releaseESC := arb.enter(chatInputOwnerESC)
	if snap := arb.snapshot(); snap.Owner != chatInputOwnerESC || !snap.EscAvailable || snap.ESCConsumers != 1 {
		t.Fatalf("esc consumer snapshot mismatch: %#v", snap)
	}

	releaseCapture := arb.enter(chatInputOwnerBusyCapture)
	if snap := arb.snapshot(); snap.Owner != chatInputOwnerBusyCapture || !snap.EscAvailable || snap.CaptureDepth != 1 {
		t.Fatalf("capture must outrank esc consumer: %#v", snap)
	}

	releaseModal := arb.enter(chatInputOwnerModal)
	if snap := arb.snapshot(); snap.Owner != chatInputOwnerModal || snap.EscAvailable {
		t.Fatalf("modal must own stdin and withdraw the esc promise: %#v", snap)
	}

	releaseModal()
	if snap := arb.snapshot(); snap.Owner != chatInputOwnerBusyCapture || !snap.EscAvailable {
		t.Fatalf("closing modal must restore capture ownership: %#v", snap)
	}

	releaseCapture()
	releaseESC()
	if snap := arb.snapshot(); snap.Owner != chatInputOwnerEditor || snap.EscAvailable {
		t.Fatalf("all levels released must return to editor: %#v", snap)
	}

	// 幂等释放不得把计数压到负数。
	releaseCapture()
	releaseESC()
	releaseModal()
	if snap := arb.snapshot(); snap.ModalDepth != 0 || snap.CaptureDepth != 0 || snap.ESCConsumers != 0 {
		t.Fatalf("repeated release must not underflow levels: %#v", snap)
	}
}

func TestChatInputArbitrationNilSafety(t *testing.T) {
	var arb *chatInputArbitrator
	release := arb.enter(chatInputOwnerESC)
	release()
	if snap := arb.snapshot(); snap.Owner != chatInputOwnerEditor || snap.EscAvailable {
		t.Fatalf("nil arbitrator must stay inert, got %#v", snap)
	}
	if ensureChatInputArbitrator(nil) != nil {
		t.Fatal("nil session must not materialize an arbitrator")
	}
}

func TestBeginChatInputShadowLevelNilSession(t *testing.T) {
	release := beginChatInputShadowLevel(nil, chatInputOwnerModal)
	release()
	release()
}

func TestChatEscapeConsumerShadowTracksArbitration(t *testing.T) {
	session, _ := newEscapeConsumerTestSession(t)
	arb := ensureChatInputArbitrator(session)
	if snap := arb.snapshot(); snap.EscAvailable {
		t.Fatalf("idle session must not advertise esc, got %#v", snap)
	}

	releaseA := startChatEscapeInterruptWatcher(session)
	releaseB := startChatEscapeInterruptWatcher(session)
	if snap := arb.snapshot(); snap.ESCConsumers != 1 || !snap.EscAvailable {
		t.Fatalf("overlapping consumers must register exactly once, got %#v", snap)
	}

	releaseA()
	if snap := arb.snapshot(); snap.ESCConsumers != 1 || !snap.EscAvailable {
		t.Fatalf("surviving consumer must keep the esc level, got %#v", snap)
	}

	releaseB()
	if snap := arb.snapshot(); snap.ESCConsumers != 0 || snap.EscAvailable {
		t.Fatalf("released consumers must clear the esc level, got %#v", snap)
	}
}

// 阶段 F P1：可用性优先读仲裁器，同时保留位路径兜底——只读替换不得窄化承诺。
func TestChatEscapeAvailabilityReadsArbitratorWithoutNarrowing(t *testing.T) {
	session, _ := newEscapeConsumerTestSession(t)

	if snap, ok := chatInputArbitrationSnapshotOf(session); ok {
		t.Fatalf("fresh session must not have a materialized arbitrator, got %#v", snap)
	}
	// 未接线路径（纯 KeyHandler 位）：兜底仍为可用。
	session.KeyHandler.Arm()
	if !chatEscapeInterruptAvailable(session) {
		t.Fatal("armed key handler bits must keep advertising esc through the fallback")
	}
	session.KeyHandler.Disarm()

	releaseConsumer := startChatEscapeInterruptWatcher(session)
	defer releaseConsumer()
	if !chatInputEscBitsAvailable(session) {
		t.Fatal("active consumer must advertise esc through the bit path")
	}
	if !chatEscapeInterruptAvailable(session) {
		t.Fatal("active consumer must advertise esc through the arbitrator")
	}

	// 制造分叉：modal 登记使仲裁器按目标语义为 false，位路径仍为 true。
	releaseModal := beginChatInputShadowLevel(session, chatInputOwnerModal)
	if snap, ok := chatInputArbitrationSnapshotOf(session); !ok || snap.EscAvailable {
		t.Fatalf("modal must withdraw the arbitrated promise, got ok=%v snap=%#v", ok, snap)
	}
	if !chatInputEscBitsAvailable(session) {
		t.Fatal("active consumer bits must stay available while the modal is open")
	}
	if !chatEscapeInterruptAvailable(session) {
		t.Fatal("P1 read-only replacement must not narrow the bit-path promise")
	}
	releaseModal()
}

func TestChatEscapeAvailabilityWithoutBitsStaysHonest(t *testing.T) {
	session := &ChatSession{}
	releaseModal := beginChatInputShadowLevel(session, chatInputOwnerModal)
	defer releaseModal()
	if chatEscapeInterruptAvailable(session) {
		t.Fatal("modal without any live consumer must not advertise esc")
	}
}

// 阶段 F P2a：仲裁快照 → KeyHandler 目标态的映射（与散点调用逐位一致）。
func TestChatInputArbitrationDesiredStateMapping(t *testing.T) {
	cases := []struct {
		name     string
		snapshot chatInputArbitrationSnapshot
		want     chatInputArbitrationKeyHandlerState
	}{
		{name: "idle", snapshot: chatInputArbitrationSnapshot{}, want: chatInputArbitrationKeyHandlerState{}},
		{name: "esc consumer", snapshot: chatInputArbitrationSnapshot{ESCConsumers: 1}, want: chatInputArbitrationKeyHandlerState{Arm: true}},
		{name: "capture", snapshot: chatInputArbitrationSnapshot{CaptureDepth: 1}, want: chatInputArbitrationKeyHandlerState{Suspend: true}},
		{name: "capture over esc consumer", snapshot: chatInputArbitrationSnapshot{ESCConsumers: 1, CaptureDepth: 1}, want: chatInputArbitrationKeyHandlerState{Arm: true, Suspend: true}},
		{name: "modal only", snapshot: chatInputArbitrationSnapshot{ModalDepth: 1}, want: chatInputArbitrationKeyHandlerState{}},
		{name: "modal over esc consumer", snapshot: chatInputArbitrationSnapshot{ESCConsumers: 1, ModalDepth: 1}, want: chatInputArbitrationKeyHandlerState{Arm: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := chatInputArbitrationDesiredKeyHandlerState(tc.snapshot); got != tc.want {
				t.Fatalf("desired state = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestChatInputArbitrationDesiredStateMatchesScatteredWiring(t *testing.T) {
	session, kh := newEscapeConsumerTestSession(t)
	current := func() chatInputArbitrationKeyHandlerState {
		return chatInputArbitrationKeyHandlerState{Arm: kh.Armed(), Suspend: kh.Suspended()}
	}
	desired := func() chatInputArbitrationKeyHandlerState {
		snapshot, ok := chatInputArbitrationSnapshotOf(session)
		if !ok {
			t.Fatal("expected a materialized arbitrator")
		}
		return chatInputArbitrationDesiredKeyHandlerState(snapshot)
	}

	release := startChatEscapeInterruptWatcher(session)
	if got, want := current(), desired(); got != want {
		t.Fatalf("consumer wiring diverges from the model: actual=%#v desired=%#v", got, want)
	}
	release()
	if got, want := current(), desired(); got != want {
		t.Fatalf("released wiring diverges from the model: actual=%#v desired=%#v", got, want)
	}
}

func TestChatInputArbitrationSyncCorrectsDriftOnlyWhenEnforced(t *testing.T) {
	session, kh := newEscapeConsumerTestSession(t)
	release := startChatEscapeInterruptWatcher(session)
	defer release()
	if !kh.Armed() {
		t.Fatal("consumer must arm the key handler")
	}

	// 制造漂移：模拟绕过仲裁器的路径直接改位。
	kh.Disarm()

	t.Setenv(chatInputArbitrationEnforceEnv, "0")
	releaseModal := beginChatInputShadowLevel(session, chatInputOwnerModal)
	if kh.Armed() {
		t.Fatal("shadow mode must observe divergence without correcting it")
	}
	releaseModal()

	t.Setenv(chatInputArbitrationEnforceEnv, "1")
	releaseModal = beginChatInputShadowLevel(session, chatInputOwnerModal)
	if !kh.Armed() {
		t.Fatal("enforced mode must restore the desired armed state")
	}
	releaseModal()
}

func TestChatInputArbitrationEnforcedParsing(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{value: "", want: false},
		{value: "0", want: false},
		{value: "false", want: false},
		{value: "off", want: false},
		{value: "1", want: true},
		{value: "true", want: true},
		{value: "ON", want: true},
		{value: " yes ", want: true},
	}
	for _, tc := range cases {
		t.Setenv(chatInputArbitrationEnforceEnv, tc.value)
		if got := chatInputArbitrationEnforced(); got != tc.want {
			t.Fatalf("enforced(%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
}

// 阶段 F P2b：灰度开关打开时 KeyHandler 由仲裁器独占驱动（散点调用跳过），
// 共享引用计数与中断链路保持不变。
func TestChatInputArbitrationSingleWriterDrivesKeyHandlerWhenEnforced(t *testing.T) {
	t.Setenv(chatInputArbitrationEnforceEnv, "1")
	session, kh := newEscapeConsumerTestSession(t)

	releaseA := startChatEscapeInterruptWatcher(session)
	releaseB := startChatEscapeInterruptWatcher(session)
	if !kh.Armed() {
		t.Fatal("single-writer mode must arm the key handler through the arbitrator")
	}

	kh.Notify()
	waitForEscapeInterrupt(t, session)

	releaseA()
	if !kh.Armed() {
		t.Fatal("surviving consumer must keep the handler armed in single-writer mode")
	}
	releaseB()
	if kh.Armed() {
		t.Fatal("releasing the last consumer must disarm the handler in single-writer mode")
	}
	if chatEscapeInterruptAvailable(session) {
		t.Fatal("released consumers must not advertise esc in single-writer mode")
	}
}

// 单写者模式下 capture 独占期：旁路消费者保持 arm 但 suspend，释放后 resume。
func TestChatInputArbitrationSingleWriterSuspendsDuringCapture(t *testing.T) {
	t.Setenv(chatInputArbitrationEnforceEnv, "1")
	session, kh := newEscapeConsumerTestSession(t)
	releaseConsumer := startChatEscapeInterruptWatcher(session)
	defer releaseConsumer()

	releaseCapture := beginChatInputShadowLevel(session, chatInputOwnerBusyCapture)
	if !kh.Armed() || !kh.Suspended() {
		t.Fatalf("capture must suspend the bypass consumer: armed=%v suspended=%v", kh.Armed(), kh.Suspended())
	}
	releaseCapture()
	if !kh.Armed() || kh.Suspended() {
		t.Fatalf("capture release must resume the bypass consumer: armed=%v suspended=%v", kh.Armed(), kh.Suspended())
	}
}
