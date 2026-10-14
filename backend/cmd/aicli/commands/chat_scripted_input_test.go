package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func TestSplitChatScriptLinesSkipsBlankAndNormalizesCRLF(t *testing.T) {
	got := splitChatScriptLines("\ufefffirst\r\n\r\n  \nsecond\n")
	require.Equal(t, []string{"first", "second"}, got)
}

func TestLoadChatScriptLinesRejectsNoInteractiveAndEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "script.txt")
	require.NoError(t, os.WriteFile(path, []byte("one\n"), 0o644))

	_, err := loadChatScriptLines(path, true)
	require.ErrorContains(t, err, "--no-interactive")

	empty := filepath.Join(dir, "empty.txt")
	require.NoError(t, os.WriteFile(empty, []byte("\n\n"), 0o644))
	_, err = loadChatScriptLines(empty, false)
	require.ErrorContains(t, err, "没有可提交")

	lines, err := loadChatScriptLines(path, false)
	require.NoError(t, err)
	require.Equal(t, []string{"one"}, lines)

	missing, err := loadChatScriptLines(filepath.Join(dir, "missing.txt"), false)
	require.Error(t, err)
	require.Nil(t, missing)
}

func TestParseChatCommandOptionsLoadsScript(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "script.txt")
	require.NoError(t, os.WriteFile(path, []byte("first\n\nsecond\n"), 0o644))

	cmd := NewChatCommand(func() *config.Config { return nil })
	require.NoError(t, cmd.ParseFlags([]string{"--script", path}))
	opts, err := parseChatCommandOptions(cmd, &config.Config{})
	require.NoError(t, err)
	require.Equal(t, []string{"first", "second"}, opts.ScriptLines)
	require.Equal(t, path, opts.ScriptFile)
}

func TestRunChatLoopScriptedInputSubmitsAllLinesThenExits(t *testing.T) {
	executor := &fakeChatExecutor{output: "ok"}
	session := newInitialPromptLoopTestSession(executor)
	session.ScriptedInput = newChatScriptedInput([]string{"first", "second"})
	var output bytes.Buffer
	session.Interaction = newTestChatInteractionCoordinator(t, session)
	session.Interaction.SetWriter(&output)
	defer session.Interaction.Shutdown()

	runChatLoop(session, false, "")

	want := []string{"first", "second"}
	if !reflect.DeepEqual(executor.prompts, want) {
		t.Fatalf("submitted prompts = %#v, want %#v", executor.prompts, want)
	}
}
