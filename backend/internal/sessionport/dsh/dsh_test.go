package dsh

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/wwsheng009/ai-agent-runtime/internal/sessionport"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func testLog(headerID string, events ...string) string {
	header := `{"type":"session","version":4,"id":"` + headerID + `","createdAt":1,"cwd":"C:\\work","isSeeded":false,"delegationDepth":0,"agentPreset":"standard"}`
	if len(events) == 0 {
		return header + "\n"
	}
	return header + "\n" + strings.Join(events, "\n") + "\n"
}

func testEvent(seq int, eventType, data, surfaceOp string) string {
	if surfaceOp == "" {
		return `{"type":"` + eventType + `","seq":` + itoa(seq) + `,"time":` + itoa(seq+10) + `,"data":` + data + `}`
	}
	return `{"type":"` + eventType + `","seq":` + itoa(seq) + `,"time":` + itoa(seq+10) + `,"data":` + data + `,"surfaceOp":` + surfaceOp + `}`
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	pos := len(digits)
	for value > 0 {
		pos--
		digits[pos] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[pos:])
}

func writePlainSession(t *testing.T, root, id, log string) string {
	t.Helper()
	dir := filepath.Join(root, "sessions", "--test--", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}
	path := filepath.Join(dir, "session.v4.jsonl")
	if err := os.WriteFile(path, []byte(log), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	return path
}

func writeZstdSession(t *testing.T, root, id string, frames ...string) string {
	t.Helper()
	dir := filepath.Join(root, "sessions", "--test--", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderCRC(true))
	if err != nil {
		t.Fatalf("new zstd writer: %v", err)
	}
	defer encoder.Close()
	var output []byte
	for _, frame := range frames {
		output = append(output, encoder.EncodeAll([]byte(frame), nil)...)
	}
	path := filepath.Join(dir, "session.v4.jsonl.zstd")
	if err := os.WriteFile(path, output, 0o644); err != nil {
		t.Fatalf("write zstd session: %v", err)
	}
	return path
}

func TestReadSurfaceAndTranscriptViews(t *testing.T) {
	root := t.TempDir()
	log := testLog("session-test",
		testEvent(0, "user/message", `{"id":"u1","role":"user","content":[{"type":"text","text":"old A"}],"source":{"kind":"user"}}`, `"append"`),
		testEvent(1, "user/message", `{"id":"u2","role":"user","content":[{"type":"text","text":"old B"}],"source":{"kind":"user"}}`, `"append"`),
		testEvent(2, "user/message", `{"id":"checkpoint","role":"user","content":[{"type":"text","text":"summary"}],"source":{"kind":"user"}}`, `{"op":"replace","startSeq":0,"endSeq":1}`),
		testEvent(3, "assistant/message", `{"turn":1,"step":1,"message":{"id":"a1","role":"assistant","content":[{"type":"text","text":"answer"}],"source":{"kind":"model","provider":"mock","model":"mock"}},"stream":[]}`, `"append"`),
	)
	writePlainSession(t, root, "session-test", log)

	surface, err := Read(ImportOptions{Root: root, SessionID: "session-test", View: sessionport.ViewSurface})
	if err != nil {
		t.Fatalf("read surface: %v", err)
	}
	if got := len(surface.Session.History); got != 2 {
		t.Fatalf("surface message count = %d, want 2", got)
	}
	if surface.Session.History[0].Content != "summary" || surface.Session.History[1].Content != "answer" {
		t.Fatalf("unexpected surface history: %#v", surface.Session.History)
	}

	transcript, err := Read(ImportOptions{Root: root, SessionID: "session-test", View: sessionport.ViewTranscript})
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if got := len(transcript.Session.History); got != 3 {
		t.Fatalf("transcript message count = %d, want 3", got)
	}
	if transcript.Session.History[0].Content != "old A" || transcript.Session.History[1].Content != "old B" {
		t.Fatalf("unexpected transcript history: %#v", transcript.Session.History)
	}
}

func TestReadToolsAndZstd(t *testing.T) {
	root := t.TempDir()
	header := testLog("session-zstd")
	events := strings.Join([]string{
		testEvent(0, "user/message", `{"id":"u1","role":"user","content":[{"type":"text","text":"run it"}],"source":{"kind":"user"}}`, `"append"`),
		testEvent(1, "assistant/message", `{"turn":1,"step":1,"message":{"id":"a1","role":"assistant","content":[{"type":"text","text":"calling"},{"type":"tool-call","id":"call-1","name":"shell","arguments":"{\"cmd\":\"pwd\"}"}],"source":{"kind":"model","provider":"mock","model":"mock"}},"stream":[]}`, `"append"`),
		testEvent(2, "tool/call", `{"turn":1,"step":1,"callId":"call-1","name":"shell","arguments":"{\"cmd\":\"pwd\"}"}`, ""),
		testEvent(3, "tool/result", `{"turn":1,"step":1,"message":{"id":"r1","role":"tool","toolCallId":"call-1","source":{"kind":"tool","callId":"call-1"},"content":[{"type":"text","text":"C:\\work"}],"isError":false}}`, `"append"`),
	}, "\n") + "\n"
	writeZstdSession(t, root, "session-zstd", header, events)

	imported, err := Read(ImportOptions{Root: root, SessionID: "session-zstd"})
	if err != nil {
		t.Fatalf("read zstd session: %v", err)
	}
	if got := len(imported.Session.History); got != 3 {
		t.Fatalf("message count = %d, want 3", got)
	}
	if got := len(imported.Session.History[1].ToolCalls); got != 1 || imported.Session.History[1].ToolCalls[0].ID != "call-1" {
		t.Fatalf("unexpected tool calls: %#v", imported.Session.History[1].ToolCalls)
	}
	if imported.Session.History[2].Role != "tool" || imported.Session.History[2].ToolCallID != "call-1" {
		t.Fatalf("unexpected tool result: %#v", imported.Session.History[2])
	}
}

func TestReadOfficialZip(t *testing.T) {
	root := t.TempDir()
	log := testLog("session-zip",
		testEvent(0, "user/message", `{"id":"u1","role":"user","content":[{"type":"text","text":"hello"}],"source":{"kind":"user"}}`, `"append"`),
		testEvent(1, "assistant/message", `{"turn":1,"step":1,"message":{"id":"a1","role":"assistant","content":[{"type":"text","text":"world"}],"source":{"kind":"model","provider":"mock","model":"mock"}},"stream":[]}`, `"append"`),
	)
	zipPath := filepath.Join(root, "dsh-session-test.zip")
	file, err := os.Create(zipPath)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	writer := zip.NewWriter(file)
	entry, err := writer.Create("session.v4.jsonl")
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	if _, err := entry.Write([]byte(log)); err != nil {
		t.Fatalf("write zip entry: %v", err)
	}
	if _, err := writer.Create("subagents/child/session.v4.jsonl"); err != nil {
		t.Fatalf("create child entry: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close zip file: %v", err)
	}

	imported, err := Read(ImportOptions{ZipPath: zipPath})
	if err != nil {
		t.Fatalf("read zip: %v", err)
	}
	if imported.Session.ID != "session-zip" || len(imported.Session.History) != 2 {
		t.Fatalf("unexpected zip import: id=%q messages=%d", imported.Session.ID, len(imported.Session.History))
	}
	if len(imported.Warnings) == 0 || !strings.Contains(strings.Join(imported.Warnings, " "), "子会话") {
		t.Fatalf("expected subagent warning, got %#v", imported.Warnings)
	}

	inspected, err := InspectZip(zipPath)
	if err != nil {
		t.Fatalf("inspect zip: %v", err)
	}
	if len(inspected.Sessions) != 1 || inspected.Sessions[0].SessionID != "session-zip" {
		t.Fatalf("unexpected zip inspection: %#v", inspected)
	}
	if !inspected.Sessions[0].Readable || inspected.Sessions[0].FormatVersion != 4 {
		t.Fatalf("unexpected zip session ref: %#v", inspected.Sessions[0])
	}
	if len(inspected.Warnings) == 0 {
		t.Fatal("expected zip inspection warning")
	}
}

func TestDetectAndList(t *testing.T) {
	root := t.TempDir()
	writePlainSession(t, root, "session-a", testLog("session-a"))
	instance, err := DetectInstance(root, false)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if instance.SessionCount != 1 || instance.FormatVersion != 4 || !instance.Capabilities.Read {
		t.Fatalf("unexpected instance: %#v", instance)
	}
	refs, err := List(instance)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(refs) != 1 || refs[0].SessionID != "session-a" || !refs[0].Readable {
		t.Fatalf("unexpected refs: %#v", refs)
	}
}

func TestParseZipLogBase(t *testing.T) {
	cases := []struct {
		name    string
		version int
		ok      bool
	}{
		{name: "session.v4.jsonl", version: 4, ok: true},
		{name: "session.jsonl", version: 0, ok: true},
		{name: "session.v0.jsonl", ok: false},
		{name: "session.v01.jsonl", ok: false},
		{name: "session.v4.jsonl.zstd", ok: false},
	}
	for _, test := range cases {
		version, _, ok := parseZipLogBase(test.name)
		if ok != test.ok || (ok && version != test.version) {
			t.Fatalf("parseZipLogBase(%q) = %d,%v; want %d,%v", test.name, version, ok, test.version, test.ok)
		}
	}
}

func TestReadJSONLPlainUsesNoZstd(t *testing.T) {
	root := t.TempDir()
	log := testLog("session-plain")
	writePlainSession(t, root, "session-plain", log)
	imported, err := Read(ImportOptions{Root: root, SessionID: "session-plain"})
	if err != nil {
		t.Fatalf("read plain: %v", err)
	}
	if imported.Session.ID != "session-plain" {
		t.Fatalf("id = %q", imported.Session.ID)
	}
	if len(imported.Session.History) != 0 {
		t.Fatalf("expected empty history, got %d", len(imported.Session.History))
	}
}

func TestExportBundleRoundTrip(t *testing.T) {
	for _, compression := range []string{"none", "zstd"} {
		t.Run(compression, func(t *testing.T) {
			history := []runtimetypes.Message{
				*runtimetypes.NewUserMessage("list files"),
				{
					Role:    "assistant",
					Content: "I will run it.",
					ToolCalls: []runtimetypes.ToolCall{{
						ID:   "call-1",
						Name: "shell",
						Args: map[string]interface{}{"cmd": "ls"},
					}},
					Metadata: runtimetypes.NewMetadata(),
				},
				*runtimetypes.NewToolMessage("call-1", "a.go\nb.go"),
				*runtimetypes.NewAssistantMessage("done"),
			}
			result, err := ExportBundle(ExportOptions{
				BundleDir:   filepath.Join(t.TempDir(), "bundle"),
				CWD:         `C:\work`,
				Title:       "exported",
				CreatedAt:   time.UnixMilli(1),
				Model:       "mock",
				History:     history,
				Compression: compression,
			})
			if err != nil {
				t.Fatalf("export bundle: %v", err)
			}
			imported, err := Read(ImportOptions{Root: result.BundleDir, SessionID: result.SessionID})
			if err != nil {
				t.Fatalf("read exported bundle: %v", err)
			}
			if got := len(imported.Session.History); got != 4 {
				t.Fatalf("round-trip message count = %d, want 4", got)
			}
			if imported.Session.History[1].ToolCalls[0].ID != "call-1" || imported.Session.History[2].ToolCallID != "call-1" {
				t.Fatalf("round-trip tool chain lost: %#v", imported.Session.History)
			}
		})
	}
}
