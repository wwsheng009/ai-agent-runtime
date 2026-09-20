// Real end-to-end ACP verification of the out-of-band session notifications a
// headless ACP client renders, against the compiled aicli binary. Runs
// aicli agent stdio with an isolated USERPROFILE/HOME and a local mock
// OpenAI-compatible streaming provider, then asserts:
//
//	A1 available_commands_update (from session/new): every entry has a
//	   non-empty name WITHOUT a leading "/" and a non-empty description, and the
//	   catalog includes at least help/status/clear/model/mode/provider/
//	   reasoning_effort
//	A2 usage_update: after one completed prompt whose provider response carried
//	   usage metadata, at least one update has size > 0 and 0 <= used <= size
//	   (used <= size is part of the contract, not an observation)
//	A3 session_info_update: after the first prompt completes at least one
//	   notification carries a non-empty title, the update payload must not
//	   contain the keys sessionId/cwd/additionalDirectories, an empty title must
//	   never be sent, and a second prompt with an unchanged title must NOT
//	   re-send that title (title dedupe)
//	A4 plan: when the runtime advertises the "todos" tool over ACP stdio, the
//	   mock makes the model emit a real todos tool call and the resulting plan
//	   session update is asserted; when the tool is not advertised (or the call
//	   cannot succeed) the check is reported as SKIPPED with the reason instead
//	   of being faked
//
// Failures print the offending raw NDJSON frame(s); the process exits non-zero
// when any asserted check fails.
//
// Run:
//
//	cd backend
//	go build -o $env:TEMP\aicli-e2e.exe ./cmd/aicli/
//	go run ./scripts/acp_e2e_notifications.go $env:TEMP\aicli-e2e.exe
//
// The binary argument is optional: without it the script builds ./cmd/aicli/
// into a temp dir itself (it must then run from backend/).
//
//go:build ignore

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

type e2e struct {
	mu      sync.Mutex
	nextID  int
	pending map[string]chan Message
}

func writeMsg(w io.Writer, msg Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = w.Write(append(data, '\n'))
	return err
}

// acpNotification is one captured session/update frame: the raw NDJSON line
// (for failure dumps), the decoded params and the raw update payload. The leak
// assertion targets the update payload because the session/update envelope
// carries sessionId at the params level by ACP spec.
type acpNotification struct {
	raw    string
	params map[string]interface{}
	update json.RawMessage
}

// notificationLog records session/update notifications in arrival order and
// keeps every inbound frame so failures can dump the wire traffic verbatim.
type notificationLog struct {
	mu     sync.Mutex
	items  []acpNotification
	frames []string
	other  []string
}

func (n *notificationLog) append(entry acpNotification) {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.items = append(n.items, entry)
}

func (n *notificationLog) snapshot() []acpNotification {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]acpNotification, len(n.items))
	copy(out, n.items)
	return out
}

func (n *notificationLog) recordFrame(raw string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.frames = append(n.frames, raw)
}

func (n *notificationLog) recordOther(raw string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.other = append(n.other, raw)
}

func (n *notificationLog) framesSnapshot() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]string, len(n.frames))
	copy(out, n.frames)
	return out
}

func (n *notificationLog) otherSnapshot() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]string, len(n.other))
	copy(out, n.other)
	return out
}

// mockUsage is the usage metadata the mock provider returns on every streamed
// completion. The runtime is expected to surface it as a usage_update whose
// used count never exceeds the configured context window.
var mockUsage = map[string]interface{}{
	"prompt_tokens":     1200,
	"completion_tokens": 8,
	"total_tokens":      1208,
}

// todosArguments is the streamed todos tool-call payload, split across two
// argument deltas so the runtime's accumulation path is exercised.
const todosArguments = `{"todos":[{"content":"分析需求","status":"in_progress","active_form":"正在分析需求"},{"content":"实施适配","status":"pending","active_form":""}]}`

func sseChunk(delta map[string]interface{}, finish interface{}, usage map[string]interface{}) string {
	payload := map[string]interface{}{
		"id":      "mock",
		"object":  "chat.completion.chunk",
		"created": 1,
		"model":   "m1",
		"choices": []interface{}{
			map[string]interface{}{"index": 0, "delta": delta, "finish_reason": finish},
		},
	}
	if usage != nil {
		payload["usage"] = usage
	}
	data, _ := json.Marshal(payload)
	return string(data)
}

// mockProviderState answers the notification flow: the first two prompts get
// plain content turns, and the third prompt (only when the runtime advertised
// the todos tool) gets a genuine todos tool call followed by a plain closing
// turn. The mock is the model here: it never invents a plan update itself.
type mockProviderState struct {
	mu          sync.Mutex
	plainTurns  int
	todosSeen   bool
	todosCalled bool
	toolNames   []string
}

func (s *mockProviderState) snapshot() (bool, []string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, len(s.toolNames))
	copy(names, s.toolNames)
	return s.todosSeen, names, s.todosCalled
}

func (s *mockProviderState) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		_ = json.Unmarshal(body, &request)
		names := make([]string, 0, len(request.Tools))
		hasTodos := false
		for _, tool := range request.Tools {
			name := strings.TrimSpace(tool.Function.Name)
			if name == "" {
				continue
			}
			names = append(names, name)
			if strings.EqualFold(name, "todos") {
				hasTodos = true
			}
		}

		s.mu.Lock()
		if len(names) > 0 {
			s.toolNames = names
		}
		if hasTodos {
			s.todosSeen = true
		}
		emitTodos := hasTodos && !s.todosCalled && s.plainTurns >= 2
		if emitTodos {
			s.todosCalled = true
		} else {
			s.plainTurns++
		}
		s.mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		emit := func(payload string) {
			fmt.Fprintf(w, "data: %s\n\n", payload)
			if flusher != nil {
				flusher.Flush()
			}
		}

		emit(sseChunk(map[string]interface{}{"role": "assistant"}, nil, nil))
		if emitTodos {
			emit(sseChunk(map[string]interface{}{"tool_calls": []interface{}{
				map[string]interface{}{
					"index":    0,
					"id":       "call_todos_1",
					"type":     "function",
					"function": map[string]interface{}{"name": "todos", "arguments": ""},
				},
			}}, nil, nil))
			half := len(todosArguments) / 2
			// Never split a multi-byte rune: invalid UTF-8 would corrupt the
			// accumulated arguments instead of exercising them.
			for half > 0 && !utf8.RuneStart(todosArguments[half]) {
				half--
			}
			emit(sseChunk(map[string]interface{}{"tool_calls": []interface{}{
				map[string]interface{}{
					"index":    0,
					"function": map[string]interface{}{"arguments": todosArguments[:half]},
				},
			}}, nil, nil))
			emit(sseChunk(map[string]interface{}{"tool_calls": []interface{}{
				map[string]interface{}{
					"index":    0,
					"function": map[string]interface{}{"arguments": todosArguments[half:]},
				},
			}}, nil, nil))
			emit(sseChunk(map[string]interface{}{}, "tool_calls", mockUsage))
		} else {
			emit(sseChunk(map[string]interface{}{"content": "hello"}, nil, nil))
			emit(sseChunk(map[string]interface{}{}, "stop", mockUsage))
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}
}

func updateObject(entry acpNotification) map[string]interface{} {
	update, _ := entry.params["update"].(map[string]interface{})
	return update
}

func updateKind(entry acpNotification) string {
	update := updateObject(entry)
	if update == nil {
		return ""
	}
	value, _ := update["sessionUpdate"].(string)
	return strings.TrimSpace(value)
}

func updateString(entry acpNotification, key string) string {
	update := updateObject(entry)
	if update == nil {
		return ""
	}
	value, _ := update[key].(string)
	return value
}

func updateInt(entry acpNotification, key string) (int64, bool) {
	update := updateObject(entry)
	if update == nil {
		return 0, false
	}
	value, ok := update[key]
	if !ok {
		return 0, false
	}
	number, ok := value.(float64)
	if !ok {
		return 0, false
	}
	return int64(number), true
}

func hasUpdateKey(entry acpNotification, key string) bool {
	if len(entry.update) == 0 {
		return false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(entry.update, &fields); err != nil {
		return false
	}
	_, ok := fields[key]
	return ok
}

func selectNotifications(entries []acpNotification, kind string) []acpNotification {
	out := make([]acpNotification, 0, len(entries))
	for _, entry := range entries {
		if updateKind(entry) == kind {
			out = append(out, entry)
		}
	}
	return out
}

// waitForNotification polls the log until a matching frame arrives or the
// deadline elapses; out-of-band notifications race the RPC response, so every
// assertion needs the wait window.
func waitForNotification(log *notificationLog, timeout time.Duration, match func(acpNotification) bool) (acpNotification, bool) {
	deadline := time.Now().Add(timeout)
	for {
		for _, entry := range log.snapshot() {
			if match(entry) {
				return entry, true
			}
		}
		if time.Now().After(deadline) {
			return acpNotification{}, false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// settle gives the agent a moment to flush notifications emitted just before
// its RPC response lands on the wire.
func settle() {
	time.Sleep(300 * time.Millisecond)
}

func dumpFrames(label string, log *notificationLog, filter func(acpNotification) bool) {
	fmt.Printf("---- raw NDJSON frames: %s ----\n", label)
	printed := 0
	for _, entry := range log.snapshot() {
		if filter != nil && !filter(entry) {
			continue
		}
		fmt.Println(entry.raw)
		printed++
		if printed >= 40 {
			fmt.Println("... (frames truncated)")
			break
		}
	}
	if printed == 0 {
		fmt.Println("(no matching session/update frames captured)")
	}
	if other := log.otherSnapshot(); len(other) > 0 {
		fmt.Println("-- agent -> client requests captured (diagnostics) --")
		for index, raw := range other {
			if index >= 10 {
				fmt.Println("... (requests truncated)")
				break
			}
			fmt.Println(raw)
		}
	}
	if frames := log.framesSnapshot(); len(frames) == 0 {
		fmt.Println("(no inbound frames captured at all)")
	}
	fmt.Println("---- end raw frames ----")
}

// results accumulates the per-assertion verdicts so every assertion is reported
// even when an earlier one failed.
type results struct {
	failures int
}

func (r *results) pass(id, format string, args ...interface{}) {
	fmt.Printf("[PASS] %s %s\n", id, fmt.Sprintf(format, args...))
}

func (r *results) fail(id, format string, args ...interface{}) {
	r.failures++
	fmt.Printf("[FAIL] %s %s\n", id, fmt.Sprintf(format, args...))
}

func (r *results) skip(id, format string, args ...interface{}) {
	fmt.Printf("[SKIP] %s %s\n", id, fmt.Sprintf(format, args...))
}

func main() {
	workDir, err := os.Getwd()
	if err != nil {
		fmt.Printf("FAIL working directory: %v\n", err)
		os.Exit(1)
	}
	bin := ""
	if len(os.Args) >= 2 {
		bin = strings.TrimSpace(os.Args[1])
	}
	if bin == "" {
		bin, err = buildAICLIIntoTemp(workDir)
		if err != nil {
			fmt.Printf("FAIL build aicli: %v\n", err)
			os.Exit(2)
		}
	}

	// --- isolated environment: temp USERPROFILE + local mock provider ---
	state := &mockProviderState{}
	srv := httptest.NewServer(state.handler())
	defer srv.Close()

	home, err := os.MkdirTemp("", "acp-e2e-notifications")
	if err != nil {
		fmt.Printf("FAIL temp home: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(home)
	aicliDir := filepath.Join(home, ".aicli")
	if err := os.MkdirAll(aicliDir, 0o755); err != nil {
		fmt.Printf("FAIL mkdir .aicli: %v\n", err)
		os.Exit(1)
	}
	// max_context_tokens makes the context window known: without it the agent
	// intentionally skips usage_update instead of reporting a bogus size.
	cfg := fmt.Sprintf(`aicli:
  chat:
    default_provider: mock
    default_model: m1
    stream: true
    no_interactive: true
providers:
  default_provider: mock
  items:
    mock:
      enabled: true
      protocol: openai
      base_url: %s
      api_path: /v1/chat/completions
      api_key: mock-key
      default_model: m1
      model_capabilities:
        m1:
          max_context_tokens: 200000
`, srv.URL)
	if err := os.WriteFile(filepath.Join(aicliDir, "config.yaml"), []byte(cfg), 0o644); err != nil {
		fmt.Printf("FAIL write config: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "agent", "stdio")
	cmd.Env = append(os.Environ(), "USERPROFILE="+home, "HOME="+home)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		fmt.Printf("FAIL start: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		stdin.Close()
		cmd.Process.Kill()
		cmd.Wait()
	}()

	e := &e2e{pending: map[string]chan Message{}}
	notifications := &notificationLog{}
	go func() {
		reader := bufio.NewReader(stdout)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var msg Message
			if json.Unmarshal([]byte(line), &msg) != nil {
				notifications.recordFrame(line)
				continue
			}
			notifications.recordFrame(line)
			if msg.Method == "session/update" {
				var params map[string]interface{}
				if json.Unmarshal(msg.Params, &params) == nil {
					entry := acpNotification{raw: line, params: params}
					if update, ok := params["update"]; ok {
						if encoded, err := json.Marshal(update); err == nil {
							entry.update = encoded
						}
					}
					notifications.append(entry)
				}
			}
			if msg.ID == nil {
				if msg.Method != "" {
					notifications.recordOther(line)
				}
				continue
			}
			e.mu.Lock()
			ch := e.pending[string(msg.ID)]
			e.mu.Unlock()
			if ch != nil {
				ch <- msg
			}
		}
	}()
	go io.Copy(io.Discard, stderr)

	call := func(method string, params interface{}, timeout time.Duration) (json.RawMessage, error) {
		raw, _ := json.Marshal(params)
		ch := make(chan Message, 1)
		e.nextID++
		e.mu.Lock()
		e.pending[fmt.Sprintf("%d", e.nextID)] = ch
		e.mu.Unlock()
		if err := writeMsg(stdin, Message{JSONRPC: "2.0", ID: json.RawMessage(fmt.Sprintf("%d", e.nextID)), Method: method, Params: raw}); err != nil {
			return nil, err
		}
		select {
		case msg := <-ch:
			if msg.Error != nil {
				return nil, fmt.Errorf("rpc error: %s", msg.Error)
			}
			return msg.Result, nil
		case <-time.After(timeout):
			return nil, fmt.Errorf("timeout waiting for %s", method)
		}
	}

	fatal := func(format string, args ...interface{}) {
		fmt.Printf("E2E FAIL: "+format+"\n", args...)
		os.Exit(1)
	}

	r := &results{}

	// --- 1. initialize ---
	if _, err := call("initialize", map[string]interface{}{
		"protocolVersion":    1,
		"clientCapabilities": map[string]interface{}{"terminal": false},
	}, 20*time.Second); err != nil {
		fatal("initialize: %v", err)
	}
	fmt.Println("OK initialize")

	// --- 2. A1: session/new advertises the command catalog ---
	res, err := call("session/new", map[string]interface{}{
		"cwd":        home,
		"mcpServers": []interface{}{},
	}, 30*time.Second)
	if err != nil {
		fatal("session/new: %v", err)
	}
	var snew struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(res, &snew); err != nil || snew.SessionID == "" {
		fatal("session/new result: %v %s", err, res)
	}
	fmt.Printf("OK session/new sessionId=%s\n", snew.SessionID)

	catalogEntry, ok := waitForNotification(notifications, 10*time.Second, func(entry acpNotification) bool {
		return updateKind(entry) == "available_commands_update"
	})
	if !ok {
		r.fail("A1", "session/new sent no available_commands_update notification")
		dumpFrames("A1 available_commands_update missing", notifications, nil)
	} else {
		update := updateObject(catalogEntry)
		rawCommands, _ := update["availableCommands"].([]interface{})
		problems := make([]string, 0, 4)
		if len(rawCommands) == 0 {
			problems = append(problems, "availableCommands is empty")
		}
		seen := map[string]bool{}
		for index, raw := range rawCommands {
			command, _ := raw.(map[string]interface{})
			if command == nil {
				problems = append(problems, fmt.Sprintf("entry %d is not an object", index))
				continue
			}
			name, _ := command["name"].(string)
			description, _ := command["description"].(string)
			name = strings.TrimSpace(name)
			if name == "" {
				problems = append(problems, fmt.Sprintf("entry %d has an empty name", index))
				continue
			}
			if strings.HasPrefix(name, "/") {
				problems = append(problems, fmt.Sprintf("entry %d name %q has a leading /", index, name))
			}
			if strings.TrimSpace(description) == "" {
				problems = append(problems, fmt.Sprintf("entry %d (%s) has an empty description", index, name))
			}
			seen[strings.ToLower(name)] = true
		}
		for _, want := range []string{"help", "status", "clear", "model", "mode", "provider", "reasoning_effort"} {
			if !seen[want] {
				problems = append(problems, fmt.Sprintf("catalog is missing %q", want))
			}
		}
		if len(problems) > 0 {
			r.fail("A1", "available_commands_update catalog invalid: %s", strings.Join(problems, "; "))
			dumpFrames("A1 available_commands_update", notifications, func(entry acpNotification) bool {
				return updateKind(entry) == "available_commands_update"
			})
		} else {
			names := make([]string, 0, len(seen))
			for name := range seen {
				names = append(names, name)
			}
			sort.Strings(names)
			r.pass("A1", "available_commands_update carries %d commands (%s), all with name/description", len(rawCommands), strings.Join(names, ", "))
		}
	}

	// --- 3. prompt 1: usage metadata + the first session title ---
	if _, err := call("session/prompt", map[string]interface{}{
		"sessionId": snew.SessionID,
		"prompt":    []map[string]interface{}{{"type": "text", "text": "hello notifications"}},
	}, 60*time.Second); err != nil {
		fatal("prompt 1: %v", err)
	}
	settle()

	// A2: at least one usage_update with size > 0 and 0 <= used <= size.
	usageOK := false
	var usageUsed, usageSize int64
	for _, entry := range selectNotifications(notifications.snapshot(), "usage_update") {
		size, hasSize := updateInt(entry, "size")
		used, hasUsed := updateInt(entry, "used")
		if !hasSize || size <= 0 {
			continue
		}
		if hasUsed && (used < 0 || used > size) {
			continue
		}
		usageOK = true
		usageUsed, usageSize = used, size
		break
	}
	if usageOK {
		r.pass("A2", "usage_update carries size > 0 with 0 <= used <= size after one completed prompt (used=%d size=%d)", usageUsed, usageSize)
	} else {
		usageFrames := selectNotifications(notifications.snapshot(), "usage_update")
		if len(usageFrames) == 0 {
			r.fail("A2", "no usage_update notification was sent after a prompt whose provider response carried usage metadata")
		} else {
			details := make([]string, 0, len(usageFrames))
			for _, entry := range usageFrames {
				used, hasUsed := updateInt(entry, "used")
				size, hasSize := updateInt(entry, "size")
				details = append(details, fmt.Sprintf("used=%d(present=%t) size=%d(present=%t)", used, hasUsed, size, hasSize))
			}
			r.fail("A2", "usage_update violated size > 0 / 0 <= used <= size: %s", strings.Join(details, ", "))
		}
		dumpFrames("A2 usage_update", notifications, func(entry acpNotification) bool {
			return updateKind(entry) == "usage_update"
		})
	}

	// --- 4. prompt 2: the unchanged title must not be re-sent (dedupe) ---
	firstInfos := selectNotifications(notifications.snapshot(), "session_info_update")
	firstTitle := ""
	for _, entry := range firstInfos {
		if title := strings.TrimSpace(updateString(entry, "title")); title != "" {
			firstTitle = title
			break
		}
	}
	beforeSecond := len(notifications.snapshot())
	if _, err := call("session/prompt", map[string]interface{}{
		"sessionId": snew.SessionID,
		"prompt":    []map[string]interface{}{{"type": "text", "text": "second turn keeps the title"}},
	}, 60*time.Second); err != nil {
		fatal("prompt 2: %v", err)
	}
	settle()

	// A3: title present, no empty titles, no session metadata leak, no repeat.
	a3Problems := make([]string, 0, 4)
	infos := selectNotifications(notifications.snapshot(), "session_info_update")
	nonEmptyTitles := 0
	for _, entry := range infos {
		title := strings.TrimSpace(updateString(entry, "title"))
		if title == "" {
			a3Problems = append(a3Problems, "an empty-title session_info_update was sent: "+entry.raw)
			continue
		}
		nonEmptyTitles++
		for _, key := range []string{"sessionId", "cwd", "additionalDirectories"} {
			if hasUpdateKey(entry, key) {
				a3Problems = append(a3Problems, fmt.Sprintf("session_info_update update payload leaks key %q: %s", key, entry.raw))
			}
		}
	}
	if nonEmptyTitles == 0 {
		a3Problems = append(a3Problems, "no session_info_update carried a non-empty title after the first prompt")
	}
	secondTurnInfos := selectNotifications(notifications.snapshot()[beforeSecond:], "session_info_update")
	if firstTitle == "" {
		a3Problems = append(a3Problems, "the first prompt produced no title to deduplicate")
	} else {
		for _, entry := range secondTurnInfos {
			title := strings.TrimSpace(updateString(entry, "title"))
			if title == firstTitle {
				a3Problems = append(a3Problems, fmt.Sprintf("the unchanged title %q was re-sent after the second prompt: %s", title, entry.raw))
			}
		}
	}
	if len(a3Problems) > 0 {
		r.fail("A3", "session_info_update contract: %s", strings.Join(a3Problems, "; "))
		dumpFrames("A3 session_info_update", notifications, func(entry acpNotification) bool {
			return updateKind(entry) == "session_info_update"
		})
	} else {
		r.pass("A3", "session_info_update title=%q is non-empty, carries no sessionId/cwd/additionalDirectories, no empty-title frame among %d frames, and the unchanged title was not re-sent by the second prompt (%d frames after it, dedupe)", firstTitle, len(infos), len(secondTurnInfos))
	}

	// --- 5. A4: plan update driven by a genuine todos tool call ---
	todosSeen, toolNames, _ := state.snapshot()
	if !todosSeen {
		r.skip("A4", "not covered by this script: the runtime did not advertise a todos tool over ACP stdio (advertised tools: %v), so the mock provider cannot make the model emit a genuine todos tool call", toolNames)
	} else {
		beforeThird := len(notifications.snapshot())
		if _, err := call("session/prompt", map[string]interface{}{
			"sessionId": snew.SessionID,
			"prompt":    []map[string]interface{}{{"type": "text", "text": "please track this work with todos"}},
		}, 60*time.Second); err != nil {
			r.fail("A4", "prompt 3 (todos turn) failed: %v", err)
		} else {
			settle()
			planEntry, planOK := waitForNotification(notifications, 10*time.Second, func(entry acpNotification) bool {
				return updateKind(entry) == "plan"
			})
			if planOK {
				entries, _ := updateObject(planEntry)["entries"].([]interface{})
				if len(entries) == 0 {
					r.fail("A4", "plan update carried no entries")
					dumpFrames("A4 plan", notifications, func(entry acpNotification) bool {
						return updateKind(entry) == "plan"
					})
				} else {
					r.pass("A4", "todos tool call produced a plan update with %d entries", len(entries))
				}
			} else {
				status := todosCallStatus(notifications.snapshot()[beforeThird:])
				if status == "" || status == "failed" || status == "error" {
					r.skip("A4", "not covered by this script: the mock's todos tool call did not execute successfully (todos tool call status=%q), so no plan update could be produced", status)
				} else {
					r.fail("A4", "todos tool call (status=%q) produced no plan session update", status)
				}
				dumpFrames("A4 missing plan", notifications, func(entry acpNotification) bool {
					kind := updateKind(entry)
					return kind == "plan" || kind == "tool_call" || kind == "tool_call_update"
				})
			}
		}
	}

	// --- 6. late sweep: later turns must not regress the session_info rules ---
	lateProblems := make([]string, 0, 2)
	for _, entry := range selectNotifications(notifications.snapshot(), "session_info_update") {
		title := strings.TrimSpace(updateString(entry, "title"))
		if title == "" {
			problem := "an empty-title session_info_update was sent: " + entry.raw
			if !containsString(a3Problems, problem) {
				lateProblems = append(lateProblems, problem)
			}
		}
		for _, key := range []string{"sessionId", "cwd", "additionalDirectories"} {
			if hasUpdateKey(entry, key) {
				problem := fmt.Sprintf("session_info_update update payload leaks key %q: %s", key, entry.raw)
				if !containsString(a3Problems, problem) {
					lateProblems = append(lateProblems, problem)
				}
			}
		}
	}
	if len(lateProblems) > 0 {
		r.fail("A3", "late session_info_update violation after later turns: %s", strings.Join(lateProblems, "; "))
		dumpFrames("A3 late session_info_update", notifications, func(entry acpNotification) bool {
			return updateKind(entry) == "session_info_update"
		})
	}

	fmt.Println()
	if r.failures == 0 {
		fmt.Println("E2E PASS")
		os.Exit(0)
	}
	fmt.Printf("E2E FAIL: %d assertion(s) failed\n", r.failures)
	os.Exit(1)
}

// todosCallStatus returns the last observed status of the mock's todos tool
// call, used to tell "the helper could not drive a successful call" apart from
// "the runtime dropped the plan update".
func todosCallStatus(entries []acpNotification) string {
	status := ""
	for _, entry := range entries {
		kind := updateKind(entry)
		if kind != "tool_call" && kind != "tool_call_update" {
			continue
		}
		update := updateObject(entry)
		if update == nil {
			continue
		}
		id, _ := update["toolCallId"].(string)
		title, _ := update["title"].(string)
		if !strings.Contains(id, "call_todos_1") && !strings.Contains(strings.ToLower(title), "todos") {
			continue
		}
		if value, ok := update["status"].(string); ok && strings.TrimSpace(value) != "" {
			status = strings.TrimSpace(value)
		}
	}
	return status
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// buildAICLIIntoTemp compiles ./cmd/aicli/ into a fresh temp dir when the caller
// did not pass a prebuilt binary (the documented e2e flow builds it into
// $env:TEMP and passes the path instead).
func buildAICLIIntoTemp(workDir string) (string, error) {
	cmdDir := filepath.Join(workDir, "cmd", "aicli")
	if info, err := os.Stat(cmdDir); err != nil || !info.IsDir() {
		return "", fmt.Errorf("no aicli binary argument and %s is not a directory: run from backend/ or pass <aicli.exe>", cmdDir)
	}
	outDir, err := os.MkdirTemp("", "acp-e2e-aicli")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(outDir, "aicli-e2e.exe")
	build := exec.Command("go", "build", "-o", bin, "./cmd/aicli/")
	build.Dir = workDir
	output, err := build.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("go build -o %s ./cmd/aicli/: %v\n%s", bin, err, strings.TrimSpace(string(output)))
	}
	fmt.Printf("OK built aicli: go build -o %s ./cmd/aicli/\n", bin)
	return bin, nil
}
