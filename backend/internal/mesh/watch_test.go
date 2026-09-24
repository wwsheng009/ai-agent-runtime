package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func journalEntryFor(nodeID, sessionID string, seq uint64, ts time.Time, kind string) JournalEntry {
	return JournalEntry{TS: ts.UTC(), NodeID: nodeID, Seq: seq, Kind: kind, SessionID: sessionID}
}

func marshalJournalEntry(t *testing.T, entry JournalEntry) string {
	t.Helper()
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal journal entry: %v", err)
	}
	return string(data)
}

// writeJournalFile 覆盖写一个节点的 journal（回放用）。
func writeJournalFile(t *testing.T, path string, entries ...JournalEntry) {
	t.Helper()
	var buf strings.Builder
	for _, entry := range entries {
		buf.WriteString(marshalJournalEntry(t, entry))
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(buf.String()), 0o600); err != nil {
		t.Fatalf("write journal %s: %v", path, err)
	}
}

// appendJournalRaw 追加原始文本（可以是不完整的一行，用于验证「半行不消费」）。
func appendJournalRaw(t *testing.T, path, text string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open journal %s: %v", path, err)
	}
	defer file.Close()
	if _, err := file.WriteString(text); err != nil {
		t.Fatalf("append journal %s: %v", path, err)
	}
}

func waitForCondition(t *testing.T, timeout time.Duration, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", what)
}

func eventKinds(events []WatchEvent) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		out = append(out, event.Entry.Kind)
	}
	return out
}

// ---------------------------------------------------------------------------
// 回放（CollectJournalEvents）
// ---------------------------------------------------------------------------

func TestCollectJournalEventsMergesNodesAndAppliesSince(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	now := clock.Now()
	nodeA, nodeB := "node-a-1000", "node-b-2000"

	writeJournalFile(t, paths.JournalPath(nodeA),
		journalEntryFor(nodeA, "session_a", 1, now.Add(-30*time.Minute), JournalNodeStarted),
		journalEntryFor(nodeA, "session_a", 2, now.Add(-2*time.Minute), JournalSessionActivated),
	)
	writeJournalFile(t, paths.JournalPath(nodeB),
		journalEntryFor(nodeB, "session_b", 1, now.Add(-5*time.Minute), JournalBusyChanged),
	)

	events, err := CollectJournalEvents(paths, WatchOptions{Since: 10 * time.Minute, Now: clock.Now})
	if err != nil {
		t.Fatalf("CollectJournalEvents: %v", err)
	}
	kinds := eventKinds(events)
	want := []string{JournalBusyChanged, JournalSessionActivated}
	if len(kinds) != len(want) || kinds[0] != want[0] || kinds[1] != want[1] {
		t.Fatalf("kinds = %v, want %v（窗口外的 node.started 不得出现，且按时间合并）", kinds, want)
	}
	if events[0].Entry.NodeID != nodeB || events[1].Entry.NodeID != nodeA {
		t.Fatalf("节点顺序 = %s,%s，want %s,%s", events[0].Entry.NodeID, events[1].Entry.NodeID, nodeB, nodeA)
	}
	if events[1].Entry.SessionID != "session_a" {
		t.Fatalf("session_id = %q, want session_a", events[1].Entry.SessionID)
	}
}

func TestCollectJournalEventsFiltersNodeAndSession(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	now := clock.Now()
	nodeA, nodeB := "node-a-1000", "node-b-2000"

	writeJournalFile(t, paths.JournalPath(nodeA),
		journalEntryFor(nodeA, "session_a", 1, now.Add(-time.Minute), JournalNodeStarted),
		journalEntryFor(nodeA, "session_a", 2, now.Add(-time.Minute), JournalBusyChanged),
	)
	writeJournalFile(t, paths.JournalPath(nodeB),
		journalEntryFor(nodeB, "session_b", 1, now.Add(-time.Minute), JournalNodeStarted),
	)

	byNode, err := CollectJournalEvents(paths, WatchOptions{NodeID: "node-a", Now: clock.Now})
	if err != nil {
		t.Fatalf("CollectJournalEvents(node): %v", err)
	}
	if len(byNode) != 2 {
		t.Fatalf("--node node-a 命中 %d 条，want 2", len(byNode))
	}
	for _, event := range byNode {
		if event.Entry.NodeID != nodeA {
			t.Fatalf("--node 过滤泄漏了 %s", event.Entry.NodeID)
		}
	}

	bySession, err := CollectJournalEvents(paths, WatchOptions{SessionID: "session_b", Now: clock.Now})
	if err != nil {
		t.Fatalf("CollectJournalEvents(session): %v", err)
	}
	if len(bySession) != 1 || bySession[0].Entry.NodeID != nodeB {
		t.Fatalf("--session session_b = %+v, want 只命中 node-b", bySession)
	}

	none, err := CollectJournalEvents(paths, WatchOptions{NodeID: "node-a", SessionID: "session_b", Now: clock.Now})
	if err != nil {
		t.Fatalf("CollectJournalEvents(combined): %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("--node 与 --session 同时给出时应取交集，得到 %d 条", len(none))
	}
}

func TestCollectJournalEventsReadsRotatedGenerationOnce(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	now := clock.Now()
	nodeID := "node-rotated-3000"
	live := paths.JournalPath(nodeID)

	first := journalEntryFor(nodeID, "session_r", 1, now.Add(-3*time.Minute), JournalNodeStarted)
	second := journalEntryFor(nodeID, "session_r", 2, now.Add(-2*time.Minute), JournalSessionActivated)
	third := journalEntryFor(nodeID, "session_r", 3, now.Add(-time.Minute), JournalBusyChanged)
	writeJournalFile(t, live, first, second)
	if err := os.Rename(live, live+journalRotatedSuffix); err != nil {
		t.Fatalf("rotate journal: %v", err)
	}
	// 最坏情况：新文件里又出现了旧代的内容（复制残留），去重必须兜住。
	writeJournalFile(t, live, first, second, third)

	events, err := CollectJournalEvents(paths, WatchOptions{Now: clock.Now})
	if err != nil {
		t.Fatalf("CollectJournalEvents: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("回放 %d 条，want 3（轮转代 + 活动文件，按 node_id+seq 去重）", len(events))
	}
	if kinds := eventKinds(events); kinds[0] != JournalNodeStarted || kinds[2] != JournalBusyChanged {
		t.Fatalf("kinds = %v, want 按 seq 递增", kinds)
	}

	liveOnly, err := JournalFiles(paths, false)
	if err != nil {
		t.Fatalf("JournalFiles(live): %v", err)
	}
	if len(liveOnly) != 1 || strings.HasSuffix(liveOnly[0], journalRotatedSuffix) {
		t.Fatalf("live 文件列表 = %v, want 只有 %s（follow 不读轮转代）", liveOnly, live)
	}
}

func TestCollectJournalEventsLimitKeepsNewest(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	now := clock.Now()
	nodeID := "node-limit-4000"
	writeJournalFile(t, paths.JournalPath(nodeID),
		journalEntryFor(nodeID, "s", 1, now.Add(-3*time.Minute), JournalNodeStarted),
		journalEntryFor(nodeID, "s", 2, now.Add(-2*time.Minute), JournalBusyChanged),
		journalEntryFor(nodeID, "s", 3, now.Add(-time.Minute), JournalSessionDeactivated),
	)

	events, err := CollectJournalEvents(paths, WatchOptions{Limit: 2, Now: clock.Now})
	if err != nil {
		t.Fatalf("CollectJournalEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("--limit 2 得到 %d 条", len(events))
	}
	if events[0].Entry.Seq != 2 || events[1].Entry.Seq != 3 {
		t.Fatalf("--limit 应保留最新 N 条，得到 seq %d,%d", events[0].Entry.Seq, events[1].Entry.Seq)
	}
}

// ---------------------------------------------------------------------------
// 实时尾随（WatchJournal）
// ---------------------------------------------------------------------------

func TestWatchJournalFollowEmitsAppendedEntries(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	now := clock.Now()
	nodeID := "node-follow-5000"
	live := paths.JournalPath(nodeID)
	writeJournalFile(t, live, journalEntryFor(nodeID, "session_f", 1, now, JournalNodeStarted))

	var mu sync.Mutex
	var got []JournalEntry
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- WatchJournal(ctx, paths, WatchOptions{Follow: true, Interval: 20 * time.Millisecond, Now: clock.Now},
			func(event WatchEvent) error {
				mu.Lock()
				got = append(got, event.Entry)
				mu.Unlock()
				return nil
			})
	}()

	waitForCondition(t, 3*time.Second, "回放事件到达", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 1
	})

	// 半行：writer 还在写，watch 不得消费（否则这条事件会永久丢失）。
	appendJournalRaw(t, live, `{"ts":"2026-09-24T10:00:01Z","node_id":"`+nodeID+`","seq":2,`)
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	afterPartial := len(got)
	mu.Unlock()
	if afterPartial != 1 {
		t.Fatalf("半行被消费了：已收到 %d 条", afterPartial)
	}

	// 补全后半行 → 恰好出现一次。
	appendJournalRaw(t, live, `"kind":"`+JournalBusyChanged+`"}`+"\n")
	waitForCondition(t, 3*time.Second, "补全后的行被消费", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 2
	})

	third := journalEntryFor(nodeID, "session_f", 3, now.Add(time.Minute), JournalSessionDeactivated)
	appendJournalRaw(t, live, marshalJournalEntry(t, third)+"\n")
	waitForCondition(t, 3*time.Second, "后续事件被消费", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 3
	})

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消后返回 %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("取消后 WatchJournal 未返回")
	}

	mu.Lock()
	defer mu.Unlock()
	if got[1].Seq != 2 || got[2].Seq != 3 {
		t.Fatalf("seq = %d,%d,%d, want 1,2,3", got[0].Seq, got[1].Seq, got[2].Seq)
	}
}

func TestWatchJournalOnceDoesNotFollow(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	nodeID := "node-once-6000"
	live := paths.JournalPath(nodeID)
	writeJournalFile(t, live, journalEntryFor(nodeID, "s", 1, clock.Now(), JournalNodeStarted))

	count := 0
	if err := WatchJournal(context.Background(), paths, WatchOptions{Interval: 20 * time.Millisecond, Now: clock.Now},
		func(WatchEvent) error {
			count++
			return nil
		}); err != nil {
		t.Fatalf("WatchJournal(once): %v", err)
	}
	if count != 1 {
		t.Fatalf("回放 %d 条，want 1", count)
	}
}

func TestCollectJournalEventsDisabledPathsFail(t *testing.T) {
	if _, err := CollectJournalEvents(Paths{}, WatchOptions{}); !errors.Is(err, ErrMeshPathsUnavailable) {
		t.Fatalf("err = %v, want ErrMeshPathsUnavailable", err)
	}
}

// ---------------------------------------------------------------------------
// CLI 入口（退出码与 --json 形状）
// ---------------------------------------------------------------------------

func TestCLIWatchOnceJSON(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	cli := testCLI(paths, clock)
	now := clock.Now()
	nodeID := "node-cli-watch-7000"
	writeJournalFile(t, paths.JournalPath(nodeID),
		journalEntryFor(nodeID, "session_w", 1, now.Add(-2*time.Minute), JournalNodeStarted),
		journalEntryFor(nodeID, "session_w", 2, now.Add(-time.Minute), JournalSessionActivated),
	)

	code, stdout, stderr := runCLI(t, cli, "watch", "--since", "1h", "--once", "--json")
	if code != ExitOK {
		t.Fatalf("watch --once --json exit = %d (stderr %q)", code, stderr)
	}
	out := decodeJSON[watchResult](t, stdout)
	if out.SchemaVersion != SchemaVersion {
		t.Fatalf("schema_version = %d, want %d", out.SchemaVersion, SchemaVersion)
	}
	if out.Counts.Events != 2 || out.Counts.Nodes != 1 || len(out.Events) != 2 {
		t.Fatalf("counts = %+v, events = %d", out.Counts, len(out.Events))
	}
	if out.Events[0].Kind != JournalNodeStarted || out.Events[0].NodeID != nodeID {
		t.Fatalf("events[0] = %+v", out.Events[0])
	}
}

func TestCLIWatchHumanOutputCarriesNodePrefix(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	cli := testCLI(paths, clock)
	nodeID := "node-cli-watch-7100"
	writeJournalFile(t, paths.JournalPath(nodeID),
		journalEntryFor(nodeID, "session_w", 1, clock.Now().Add(-time.Minute), JournalNodeStarted),
	)

	code, stdout, stderr := runCLI(t, cli, "watch", "--since", "1h", "--once")
	if code != ExitOK {
		t.Fatalf("watch --once exit = %d (stderr %q)", code, stderr)
	}
	if !strings.Contains(stdout, "["+nodeID+"]") || !strings.Contains(stdout, JournalNodeStarted) {
		t.Fatalf("stdout = %q, want 带节点前缀与事件类型", stdout)
	}
}

func TestCLIWatchUsageErrors(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	cli := testCLI(paths, clock)

	cases := [][]string{
		{"watch", "extra"},
		{"watch", "--since", "bogus", "--once"},
		{"watch", "--limit", "-1", "--once"},
		{"watch", "--interval", "10ms", "--once"},
	}
	for _, args := range cases {
		if code, _, _ := runCLI(t, cli, args...); code != ExitUsage {
			t.Fatalf("%v exit = %d, want %d", args, code, ExitUsage)
		}
	}
}

func TestCLIWatchFilteredTargetMissingIsNotFound(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	cli := testCLI(paths, clock)
	nodeID := "node-cli-watch-7200"
	writeJournalFile(t, paths.JournalPath(nodeID),
		journalEntryFor(nodeID, "session_w", 1, clock.Now().Add(-time.Minute), JournalNodeStarted),
	)

	if code, _, _ := runCLI(t, cli, "watch", "--node", "node-nope", "--once"); code != ExitNotFound {
		t.Fatalf("--node 不存在 exit = %d, want %d", code, ExitNotFound)
	}
	if code, _, _ := runCLI(t, cli, "watch", "--session", "session-nope", "--once"); code != ExitNotFound {
		t.Fatalf("--session 不存在 exit = %d, want %d", code, ExitNotFound)
	}
	if code, _, _ := runCLI(t, cli, "watch", "--node", nodeID, "--session", "session-nope", "--once"); code != ExitNotFound {
		t.Fatalf("交集为空 exit = %d, want %d", code, ExitNotFound)
	}
}

func TestCLIWatchEmptyWindowIsSuccess(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	cli := testCLI(paths, clock)
	nodeID := "node-cli-watch-7300"
	writeJournalFile(t, paths.JournalPath(nodeID),
		journalEntryFor(nodeID, "session_w", 1, clock.Now().Add(-2*time.Hour), JournalNodeStarted),
	)

	code, stdout, stderr := runCLI(t, cli, "watch", "--node", nodeID, "--since", "10m", "--once", "--json")
	if code != ExitOK {
		t.Fatalf("窗口内为空 exit = %d, want %d（stderr %q）", code, ExitOK, stderr)
	}
	out := decodeJSON[watchResult](t, stdout)
	if out.Counts.Events != 0 || len(out.Events) != 0 {
		t.Fatalf("窗口内为空时 events = %+v", out.Events)
	}

	code, stdout, _ = runCLI(t, cli, "watch", "--node", nodeID, "--since", "10m", "--once")
	if code != ExitOK {
		t.Fatalf("人读模式空窗口 exit = %d, want %d", code, ExitOK)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("空窗口 stdout = %q, want 空", stdout)
	}
}

func TestCLIWatchHelpPrintsUsage(t *testing.T) {
	paths := testCLIPaths(t)
	clock := newFakeClock()
	cli := testCLI(paths, clock)

	code, stdout, _ := runCLI(t, cli, "watch", "--help")
	if code != ExitOK {
		t.Fatalf("watch --help exit = %d, want %d", code, ExitOK)
	}
	if !strings.Contains(stdout, "aicli-mesh watch") {
		t.Fatalf("usage 未包含 watch 行：%q", stdout)
	}
}
