package commands

import (
	"bufio"
	"context"
	"strings"
	"testing"
	"time"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestSelectStreamModeWithReader_DefaultsToStream(t *testing.T) {
	var selected bool
	stdout, stderr := captureStdoutStderr(t, func() {
		selected = selectStreamModeWithReader(bufio.NewReader(strings.NewReader("\n")))
	})

	if !selected {
		t.Fatal("expected blank input to default to stream mode")
	}
	if stdout != "" {
		t.Fatalf("expected no stdout output, got:\n%s", stdout)
	}
	if !strings.Contains(stderr, "默认: 流式") {
		t.Fatalf("expected prompt to advertise stream default on stderr, got:\n%s", stderr)
	}
}

func TestSelectProviderWithReader_RetriesAfterInvalidChoice(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{
			DefaultProvider: "alpha",
			Items: map[string]config.Provider{
				"alpha": {Enabled: true, Protocol: "openai", BaseURL: "https://alpha.example.com", DefaultModel: "gpt-4.1"},
				"beta":  {Enabled: true, Protocol: "codex", BaseURL: "https://beta.example.com", DefaultModel: "gpt-5"},
			},
		},
	}

	var selected string
	stdout, stderr := captureStdoutStderr(t, func() {
		selected = selectProviderWithReader(cfg, bufio.NewReader(strings.NewReader("9\nbeta\n")))
	})

	if selected != "beta" {
		t.Fatalf("expected retry to return beta, got %q", selected)
	}
	if stdout != "" {
		t.Fatalf("expected no stdout output, got:\n%s", stdout)
	}
	if !strings.Contains(stderr, "无效的选择，请重新输入") {
		t.Fatalf("expected invalid-choice warning on stderr, got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "host=alpha.example.com") || !strings.Contains(stderr, "model=gpt-4.1") {
		t.Fatalf("expected provider summary to include host/model, got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "alpha  protocol=openai | host=alpha.example.com | model=gpt-4.1") {
		t.Fatalf("expected provider name and summary on one line, got:\n%s", stderr)
	}
}

func TestDescribeProviderSelection_FallsBackToResolvedURL(t *testing.T) {
	summary := describeProviderSelection(config.Provider{
		Protocol:     "openai",
		BaseURL:      "https://gateway.local",
		ForwardURL:   "/v1/messages",
		DefaultModel: "gemini-2.0-flash",
	})

	for _, expected := range []string{
		"protocol=openai",
		"host=gateway.local",
		"model=gemini-2.0-flash",
	} {
		if !strings.Contains(summary, expected) {
			t.Fatalf("expected summary to contain %q, got %q", expected, summary)
		}
	}
}

func TestSelectModelWithReader_RetriesAfterInvalidNumericChoice(t *testing.T) {
	provider := config.Provider{
		DefaultModel:    "gpt-4.1",
		SupportedModels: []string{"gpt-4.1", "gpt-4.1-mini"},
	}

	var selected string
	stdout, stderr := captureStdoutStderr(t, func() {
		selected = selectModelWithReader(provider, bufio.NewReader(strings.NewReader("7\n2\n")))
	})

	if selected != "gpt-4.1-mini" {
		t.Fatalf("expected retry to return gpt-4.1-mini, got %q", selected)
	}
	if stdout != "" {
		t.Fatalf("expected no stdout output, got:\n%s", stdout)
	}
	if !strings.Contains(stderr, "无效的选择，请重新输入") {
		t.Fatalf("expected invalid-choice warning on stderr, got:\n%s", stderr)
	}
}

func TestSelectReasoningEffortWithReader_RetriesAfterInvalidChoice(t *testing.T) {
	var selected string
	stdout, stderr := captureStdoutStderr(t, func() {
		selected = selectReasoningEffortWithReader("medium", []string{"low", "medium", "high", "xhigh"}, bufio.NewReader(strings.NewReader("9\nhigh\n")))
	})

	if selected != "high" {
		t.Fatalf("expected retry to return high, got %q", selected)
	}
	if stdout != "" {
		t.Fatalf("expected no stdout output, got:\n%s", stdout)
	}
	if !strings.Contains(stderr, "无效的选择，请重新输入") {
		t.Fatalf("expected invalid-choice warning on stderr, got:\n%s", stderr)
	}
}

func TestSelectReasoningEffortWithReader_DefaultsToFirstOnInitialSelection(t *testing.T) {
	var selected string
	stdout, stderr := captureStdoutStderr(t, func() {
		selected = selectReasoningEffortWithReader("", []string{"high", "max"}, bufio.NewReader(strings.NewReader("\n")))
	})

	if selected != "high" {
		t.Fatalf("expected blank input to default to first option high, got %q", selected)
	}
	if stdout != "" {
		t.Fatalf("expected no stdout output, got:\n%s", stdout)
	}
	if !strings.Contains(stderr, "(默认)") || !strings.Contains(stderr, "请输入选项 (回车默认: high / 输入 0 清空): ") {
		t.Fatalf("expected default-first prompt output on stderr, got:\n%s", stderr)
	}
}

func TestSelectReasoningEffortWithReader_UsesDeepSeekCatalog(t *testing.T) {
	var selected string
	stdout, stderr := captureStdoutStderr(t, func() {
		selected = selectReasoningEffortWithReader("high", []string{"max", "high"}, bufio.NewReader(strings.NewReader("\n")))
	})

	if selected != "high" {
		t.Fatalf("expected current high effort to be preserved, got %q", selected)
	}
	if stdout != "" {
		t.Fatalf("expected no stdout output, got:\n%s", stdout)
	}
	if !strings.Contains(stderr, "max") || !strings.Contains(stderr, "(当前)") {
		t.Fatalf("expected deepseek catalog output on stderr, got:\n%s", stderr)
	}
	first := strings.Index(stderr, "[1] high")
	second := strings.Index(stderr, "[2] max")
	if first == -1 || second == -1 || first > second {
		t.Fatalf("expected stable high->max order, got:\n%s", stderr)
	}
}

func TestSelectReasoningEffortWithReader_PreservesCaseInsensitiveMatch(t *testing.T) {
	var selected string
	stdout, stderr := captureStdoutStderr(t, func() {
		selected = selectReasoningEffortWithReader("medium", []string{"low", "medium", "high", "xhigh"}, bufio.NewReader(strings.NewReader("HIGH\n")))
	})

	if selected != "high" {
		t.Fatalf("expected canonical option high, got %q", selected)
	}
	if stdout != "" {
		t.Fatalf("expected no stdout output, got:\n%s", stdout)
	}
	if strings.Contains(stderr, "无效的选择，请重新输入") {
		t.Fatalf("did not expect invalid-choice warning, got:\n%s", stderr)
	}
}

func TestPromptStartupSessionSelectionWithReader_RetriesAfterInvalidChoice(t *testing.T) {
	storage, err := runtimechat.NewFileStorage(t.TempDir())
	if err != nil {
		t.Fatalf("new file storage: %v", err)
	}
	manager := runtimechat.NewSessionManager(storage, &runtimechat.SessionManagerConfig{
		TTL:             24 * time.Hour,
		MaxHistory:      20,
		CleanupInterval: 0,
		AutoArchive:     false,
	})
	defer manager.Stop()

	ctx := context.Background()
	session, err := manager.Create(ctx, "tester")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	session.ReplaceHistory([]runtimetypes.Message{{Role: "user", Content: "hello", Metadata: runtimetypes.NewMetadata()}})
	if err := manager.Update(ctx, session); err != nil {
		t.Fatalf("update session: %v", err)
	}

	var (
		selected  *runtimechat.Session
		createNew bool
	)
	stdout, stderr := captureStdoutStderr(t, func() {
		selected, createNew, err = promptStartupSessionSelectionWithReader(manager, "tester", ChatSessionListFilter{}, bufio.NewReader(strings.NewReader("9\n1\n")))
	})
	if err != nil {
		t.Fatalf("promptStartupSessionSelectionWithReader: %v", err)
	}
	if createNew {
		t.Fatal("expected existing session to be selected after retry")
	}
	if selected == nil || selected.ID != session.ID {
		t.Fatalf("expected selected session %q, got %#v", session.ID, selected)
	}
	if stdout != "" {
		t.Fatalf("expected no stdout output, got:\n%s", stdout)
	}
	if !strings.Contains(stderr, "无效的选择，请重新输入") {
		t.Fatalf("expected invalid-choice warning on stderr, got:\n%s", stderr)
	}
	for _, expected := range []string{
		"匹配会话:",
		"[1]  恢复最近可恢复会话",
		"[2]  选择历史会话",
		"[3]  新建会话",
	} {
		if !strings.Contains(stderr, expected) {
			t.Fatalf("expected aligned startup selection output to contain %q, got:\n%s", expected, stderr)
		}
	}
}

func TestPromptSelectSessionFromList_RetriesAfterInvalidChoice(t *testing.T) {
	now := time.Now()
	sessions := []*runtimechat.Session{
		{
			ID:        "session-1",
			State:     runtimechat.StateIdle,
			UpdatedAt: now.Add(-time.Minute),
			Metadata:  runtimechat.SessionMetadata{Title: "first", Context: map[string]interface{}{}},
		},
		{
			ID:        "session-2",
			State:     runtimechat.StateActive,
			UpdatedAt: now,
			Metadata:  runtimechat.SessionMetadata{Title: "second", Context: map[string]interface{}{}},
		},
	}

	var selected *runtimechat.Session
	stdout, stderr := captureStdoutStderr(t, func() {
		selected, _, _ = promptSelectSessionFromList(bufio.NewReader(strings.NewReader("9\n2\n")), sessions)
	})

	if selected == nil || selected.ID != "session-2" {
		t.Fatalf("expected session-2 after retry, got %#v", selected)
	}
	if stdout != "" {
		t.Fatalf("expected no stdout output, got:\n%s", stdout)
	}
	if !strings.Contains(stderr, "无效的选择，请重新输入") {
		t.Fatalf("expected invalid-choice warning on stderr, got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "[1 ] session-1") || !strings.Contains(stderr, "[2 ] session-2") {
		t.Fatalf("expected aligned session rows, got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "[idle") || !strings.Contains(stderr, "[active") {
		t.Fatalf("expected aligned state column, got:\n%s", stderr)
	}
}

func TestMaybeSelectStartupSession_PreservesBufferedInputOnSharedReader(t *testing.T) {
	storage, err := runtimechat.NewFileStorage(t.TempDir())
	if err != nil {
		t.Fatalf("new file storage: %v", err)
	}
	manager := runtimechat.NewSessionManager(storage, &runtimechat.SessionManagerConfig{
		TTL:             24 * time.Hour,
		MaxHistory:      20,
		CleanupInterval: 0,
		AutoArchive:     false,
	})
	defer manager.Stop()

	ctx := context.Background()
	session, err := manager.Create(ctx, "tester")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	session.ReplaceHistory([]runtimetypes.Message{{Role: "user", Content: "hello", Metadata: runtimetypes.NewMetadata()}})
	if err := manager.Update(ctx, session); err != nil {
		t.Fatalf("update session: %v", err)
	}

	opts := &chatCommandOptions{
		InputReader: bufio.NewReader(strings.NewReader("3\n继续输入\n")),
	}
	state := &chatPersistenceState{
		runtimeSessionManager: manager,
		sessionUserID:         "tester",
	}

	if err := maybeSelectStartupSession(opts, state); err != nil {
		t.Fatalf("maybeSelectStartupSession: %v", err)
	}
	if state.loadedRuntimeSession != nil {
		t.Fatalf("expected create-new path, got loaded session %+v", state.loadedRuntimeSession)
	}

	nextLine, err := chatOptionInputReader(opts).ReadString('\n')
	if err != nil {
		t.Fatalf("read remaining input: %v", err)
	}
	if nextLine != "继续输入\n" {
		t.Fatalf("expected buffered input to remain for chat loop, got %q", nextLine)
	}
}

func TestResolveChatInteractivePrompts_ConsumeSharedReaderSequentially(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{
			DefaultProvider: "alpha",
			Items: map[string]config.Provider{
				"alpha": {
					Enabled:         true,
					Protocol:        "openai",
					BaseURL:         "https://alpha.example.com",
					DefaultModel:    "gpt-4.1",
					SupportedModels: []string{"gpt-4.1", "gpt-4.1-mini"},
				},
				"beta": {
					Enabled:         true,
					Protocol:        "openai",
					BaseURL:         "https://beta.example.com",
					DefaultModel:    "gpt-5.2",
					SupportedModels: []string{"gpt-5.2", "gpt-5.2-mini"},
				},
			},
		},
	}

	opts := &chatCommandOptions{
		InputReader: bufio.NewReader(strings.NewReader("beta\n2\n1\n")),
	}

	providerName := resolveChatProviderName(cfg, opts, nil)
	if providerName != "beta" {
		t.Fatalf("expected provider beta, got %q", providerName)
	}

	provider := cfg.Providers.Items[providerName]
	modelName := resolveChatModelName(provider, opts, nil)
	if modelName != "gpt-5.2-mini" {
		t.Fatalf("expected model gpt-5.2-mini, got %q", modelName)
	}

	shouldStream := resolveChatStreamMode(opts, nil)
	if shouldStream {
		t.Fatal("expected shared reader to select normal mode after provider/model prompts")
	}
}
