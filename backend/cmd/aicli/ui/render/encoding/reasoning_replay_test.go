package encoding

import "testing"

// 回归：本地不做"猜换行/补空格"启发式——provider 的切块按原样拼接，
// 分行与否完全由 provider 提供的空白决定。
func TestAppendReasoningDeltaPureConcatenation(t *testing.T) {
	body := appendReasoningDelta("", "Inspecting buildSessionActor architecture")
	body = appendReasoningDelta(body, "Designing per-actor coordinator")
	body = appendReasoningDelta(body, "Planning per-actor coordinator instantiation and shutdown")
	want := "Inspecting buildSessionActor architecture" +
		"Designing per-actor coordinator" +
		"Planning per-actor coordinator instantiation and shutdown"
	if body != want {
		t.Fatalf("local line seam heuristic still active\n got: %q\nwant: %q", body, want)
	}
}

func TestAppendReasoningDeltaKeepsIdentifiersAndPunctuationIntact(t *testing.T) {
	chunks := []string{
		"Both greps returned empty: 1. ",
		"146eaa",
		"15-8b",
		"6f-4b",
		"97-ba3b",
		"-29f364",
		"4fc568",
		" not found in gateway-8080.log. 2. ",
		"2026-",
		"08-23T08:",
		"50-08:59 UTC records in that log on Aug 23).",
	}
	var body string
	for _, chunk := range chunks {
		body = appendReasoningDelta(body, chunk)
	}
	want := "Both greps returned empty: 1. 146eaa15-8b6f-4b97-ba3b-29f3644fc568 not found in gateway-8080.log. 2. 2026-08-23T08:50-08:59 UTC records in that log on Aug 23)."
	if body != want {
		t.Fatalf("identifier/punctuation chunk boundary changed visible text\n got: %q\nwant: %q", body, want)
	}
}

// 词内续写（"architect"+"ure"）保持原样。
func TestAppendReasoningDeltaSeamKeepsWordContinuation(t *testing.T) {
	if got := appendReasoningDelta("some architect", "ure here"); got != "some architecture here" {
		t.Fatalf("word continuation broken: %q", got)
	}
}

// 上游已经提供空白时不得重复修改。
func TestAppendReasoningDeltaSeamNoDoubleSeparator(t *testing.T) {
	if got := appendReasoningDelta("line one\n", "line two"); got != "line one\nline two" {
		t.Fatalf("unexpected seam: %q", got)
	}
	if got := appendReasoningDelta("first line ", "second line"); got != "first line second line" {
		t.Fatalf("space seam mangled: %q", got)
	}
}

// 回归：中文（无大小写字母）reasoning 的词级/短语级切块不得每块补行——
// 旧规则用"非小写字母开头"判定新句，对汉字恒成立，导致每个 delta 都被
// 拆成独立一行（实测异常文本："搜索\n方式不对。用户问…\n是否也存在问题"）。
func TestAppendReasoningDeltaCJKWordChunksStayInline(t *testing.T) {
	chunks := []string{
		"搜索",
		"方式不对。用户问\u201c执行换行处理",
		"是否也存在问题\u201d——即 execution 输出",
		"在 UI 中的换行处理。我应该先找到 execution 渲染",
		"路径：git 里",
		"之前",
		"提到的 shell 命令",
		"输出异常",
		"样例（\u201cgo test ./... \u201d）并不是 execution 输出，而是 markdown 代码块中。",
	}
	var body string
	for _, chunk := range chunks {
		body = appendReasoningDelta(body, chunk)
	}
	want := "搜索方式不对。用户问\u201c执行换行处理是否也存在问题\u201d——即 execution 输出" +
		"在 UI 中的换行处理。我应该先找到 execution 渲染路径：git 里之前提到的 shell 命令" +
		"输出异常样例（\u201cgo test ./... \u201d）并不是 execution 输出，而是 markdown 代码块中。"
	if body != want {
		t.Fatalf("CJK deltas wrongly split into lines\n got: %q\nwant: %q", body, want)
	}
}

// 中文句子边界同样不做本地补行：即使前一块以句号收尾，也按 provider 原样拼接。
func TestAppendReasoningDeltaCJKNoLocalLineInsertion(t *testing.T) {
	if got := appendReasoningDelta("检查统一渲染器架构。", "设计协调器"); got != "检查统一渲染器架构。设计协调器" {
		t.Fatalf("CJK sentence seam still inserted: %q", got)
	}
	if got := appendReasoningDelta("先确认状态", "当前目录"); got != "先确认状态当前目录" {
		t.Fatalf("CJK word seam wrongly split: %q", got)
	}
}

// 回归：provider 提供的空白（含真实 \n）与裸星号串（****）必须逐字节保留，
// 不被本地启发式吞掉或插入——对应实测异常样例
// "breaksReviewing test coverage for reasoning hard breaks****Inspecting Markdown\n..."。
func TestAppendReasoningDeltaPreservesProviderWhitespaceAndAsterisks(t *testing.T) {
	chunks := []string{
		"Inspecting MarkdownFormatter space insertion",
		"Planning unit tests for CJK hard breaks",
		"Reviewing test coverage for reasoning hard breaks",
		"****",
		"Inspecting Markdown",
		"\ndetection logic",
	}
	var body string
	for _, chunk := range chunks {
		body = appendReasoningDelta(body, chunk)
	}
	want := "Inspecting MarkdownFormatter space insertion" +
		"Planning unit tests for CJK hard breaks" +
		"Reviewing test coverage for reasoning hard breaks" +
		"****" +
		"Inspecting Markdown" +
		"\ndetection logic"
	if body != want {
		t.Fatalf("provider deltas not preserved verbatim\n got: %q\nwant: %q", body, want)
	}
}

// 尾部重复投递（最后一块被重发）不得让正文翻倍。
func TestAppendReasoningDeltaDuplicateTailDropped(t *testing.T) {
	got := appendReasoningDelta("alpha\nbeta\n", "beta\n")
	if got != "alpha\nbeta\n" {
		t.Fatalf("duplicate tail appended: %q", got)
	}
}

// 整段重放：已累积多块后从头重新流一遍相同增量，应全部丢弃，正文保持不变。
func TestSkipReplayedReasoningDeltaDropsWholeReplay(t *testing.T) {
	e := NewEventEncoder()
	key := "req-1"
	chunks := []string{
		"Inspecting buildSessionActor architecture",
		"Designing per-actor coordinator",
		"Planning per-actor coordinator instantiation",
		"Replacing host.SubagentCoordinator usage",
	}
	for _, chunk := range chunks {
		if e.skipReplayedReasoningDelta(key, chunk) {
			t.Fatalf("first pass chunk %q wrongly skipped", chunk)
		}
	}
	for i, chunk := range chunks {
		if !e.skipReplayedReasoningDelta(key, chunk) {
			t.Fatalf("replay chunk %d (%q) not skipped", i, chunk)
		}
	}
	// 重放结束后到达的新增量必须正常放行。
	if e.skipReplayedReasoningDelta(key, "Final new thought") {
		t.Fatal("new content after replay wrongly skipped")
	}
}

// 重放中途出现新内容：后续增量恢复正常累积（只丢弃与已提交序列一致的部分）。
func TestSkipReplayedReasoningDeltaBrokenReplayResumes(t *testing.T) {
	e := NewEventEncoder()
	key := "req-2"
	chunks := []string{"A thought", "B thought", "C thought"}
	for _, chunk := range chunks {
		e.skipReplayedReasoningDelta(key, chunk)
	}
	if !e.skipReplayedReasoningDelta(key, chunks[0]) || !e.skipReplayedReasoningDelta(key, chunks[1]) {
		t.Fatal("expected replay prefix to be skipped")
	}
	if e.skipReplayedReasoningDelta(key, "D diverges") {
		t.Fatal("diverging chunk wrongly skipped")
	}
}
