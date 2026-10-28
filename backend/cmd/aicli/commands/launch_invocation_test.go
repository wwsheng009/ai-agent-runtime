package commands

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

// launchInvocationTestCommand 复刻生产 flag 形状：root 持久旗标（--web/--yolo 类）
// 与 chat 旗标（--model/-m、--prompt/-M、--web-token 在 root）都要被分类器认出，
// 否则测试通过而真实 argv 仍然被误切。
func launchInvocationTestCommand() *cobra.Command {
	root := &cobra.Command{Use: "aicli"}
	root.PersistentFlags().Bool("web", false, "")
	root.PersistentFlags().Bool("pprof", false, "")
	root.PersistentFlags().String("web-token", "", "")
	root.PersistentFlags().StringP("config", "c", "", "")
	root.PersistentFlags().Bool("mesh", true, "")
	root.PersistentFlags().Bool("envelope", false, "")

	chat := &cobra.Command{Use: "chat"}
	chat.Flags().StringP("model", "m", "", "")
	chat.Flags().StringP("message", "M", "", "")
	chat.Flags().String("session", "", "")
	chat.Flags().Bool("resume", false, "")
	chat.Flags().Bool("yolo", false, "")
	chat.Flags().Bool("debug", false, "")
	chat.Flags().BoolP("stream", "s", false, "")
	chat.Flags().BoolP("json", "j", false, "")
	chat.Flags().Bool("no-interactive", false, "")
	chat.Flags().Int("budget-tokens", 0, "")
	root.AddCommand(chat)
	return chat
}

func TestSplitLaunchArgvKeepsBooleanFlagsFromEatingTheNextToken(t *testing.T) {
	chat := launchInvocationTestCommand()
	split := splitLaunchArgv([]string{"resume", "session_1", "--web", "--yolo", "--debug", "-m", "gpt-4.1"}, newLaunchFlagIndex(chat))

	require.Equal(t, "resume", split.Entry)
	require.Equal(t, []string{"resume", "session_1"}, split.Positional)
	// --web/--yolo 是布尔：绝不能吃掉后面的 --debug；-m 才吃一个值。
	require.Equal(t, []string{"--web", "--yolo", "--debug", "-m", "gpt-4.1"}, split.Flags)
	require.Equal(t, split.RedactedArgv, []string{"resume", "session_1", "--web", "--yolo", "--debug", "-m", "gpt-4.1"})
}

func TestSplitLaunchArgvDropsSessionTargetsAndOneShotInputsFromRelaunch(t *testing.T) {
	chat := launchInvocationTestCommand()
	argv := []string{"chat", "--web", "--session", "session_1", "--resume", "--prompt", "帮我把测试跑一遍", "-M", "hello", "--yolo", "--json"}
	split := splitLaunchArgv(argv, newLaunchFlagIndex(chat))

	// 记录侧一条不少（--session 的 ID 也在），重放侧只剩环境旗标。
	require.Equal(t, []string{"--web", "--yolo"}, split.Flags)
	require.Contains(t, split.RedactedArgv, "session_1")
	require.Contains(t, split.RedactedArgv, "帮我把测试跑一遍")
}

func TestSplitLaunchArgvRedactsSecretValues(t *testing.T) {
	chat := launchInvocationTestCommand()
	split := splitLaunchArgv([]string{"chat", "--web-token", "super-secret-token", "--web", "-m", "gpt-4.1"}, newLaunchFlagIndex(chat))

	require.NotContains(t, stringsJoin(split.RedactedArgv), "super-secret-token")
	require.Contains(t, split.RedactedArgv, launchRedactedValue)
	// 敏感旗标连 token 都不进重放集（重放也拿不到原值，留着只会误导）。
	require.Equal(t, []string{"--web", "-m", "gpt-4.1"}, split.Flags)

	inline := splitLaunchArgv([]string{"chat", "--web-token=abc123", "--yolo"}, newLaunchFlagIndex(chat))
	require.Equal(t, []string{"chat", "--web-token=" + launchRedactedValue, "--yolo"}, inline.RedactedArgv)
	require.Equal(t, []string{"--yolo"}, inline.Flags)
}

func TestSplitLaunchArgvHandlesInlineEqualsAndCombinedShorts(t *testing.T) {
	chat := launchInvocationTestCommand()
	split := splitLaunchArgv([]string{"chat", "--mesh=false", "-sm", "gpt-4.1", "--budget-tokens", "4000"}, newLaunchFlagIndex(chat))

	require.Equal(t, []string{"--mesh=false", "-sm", "gpt-4.1", "--budget-tokens", "4000"}, split.Flags)
}

func TestSplitLaunchArgvNeverReplaysUnknownFlags(t *testing.T) {
	chat := launchInvocationTestCommand()
	// --totally-unknown 没有被注册：分类器无法判断它是否吃值，吞掉 --yolo 会把
	// 重放命令行变成一条语义完全不同的命令，因此只记录不重放。
	split := splitLaunchArgv([]string{"chat", "--totally-unknown", "--yolo"}, newLaunchFlagIndex(chat))

	require.Equal(t, []string{"--yolo"}, split.Flags)
	require.Contains(t, split.RedactedArgv, "--totally-unknown")
}

func TestSplitLaunchArgvStopsFlagParsingAfterDoubleDash(t *testing.T) {
	chat := launchInvocationTestCommand()
	split := splitLaunchArgv([]string{"chat", "--yolo", "--", "--web", "-m"}, newLaunchFlagIndex(chat))

	require.Equal(t, []string{"--yolo"}, split.Flags)
	require.Equal(t, []string{"chat", "--web", "-m"}, split.Positional)
}

func TestApplyLaunchInvocationToContextWritesCanonicalAndLegacyKeys(t *testing.T) {
	ctx := map[string]interface{}{}
	recorded := time.Date(2026, 10, 28, 9, 30, 0, 0, time.UTC)
	record := &launchInvocationRecord{
		Instance:   "4242@1700000000000000000",
		Entry:      "resume",
		RawArgv:    []string{"resume", "session_1", "--web", "--yolo"},
		Flags:      []string{"--web", "--yolo"},
		Binary:     "E:/tools/aicli.exe",
		Cwd:        "E:/projects/demo",
		RecordedAt: recorded,
	}
	require.True(t, applyLaunchInvocationToContext(ctx, record, recorded))

	require.Equal(t, []string{"--web", "--yolo"}, launchStringListKey(ctx, sessionmeta.LaunchArgs))
	require.Equal(t, "resume", sessionmeta.String(ctx, sessionmeta.LaunchEntry))
	require.Equal(t, "E:/projects/demo", sessionmeta.String(ctx, sessionmeta.LaunchCwd))
	// legacy 别名同值：任何还在读 aicli_* 的读者都能看到同一份事实。
	require.Equal(t, "E:/tools/aicli.exe", ctx[sessionmeta.LegacyAICLILaunchBinary])

	// nil 记录/nil map 都不得写入或删除任何东西（「没有快照」≠「用户没带参数」）。
	before := len(ctx)
	require.False(t, applyLaunchInvocationToContext(ctx, nil, recorded))
	require.False(t, applyLaunchInvocationToContext(nil, record, recorded))
	require.Equal(t, before, len(ctx))
}

func TestLaunchInvocationHistoryDedupesSameProcessInstance(t *testing.T) {
	ctx := map[string]interface{}{}
	record := &launchInvocationRecord{
		Instance:   "1@same",
		Flags:      []string{"--web"},
		RawArgv:    []string{"chat", "--web"},
		Entry:      "chat",
		RecordedAt: time.Now().UTC(),
	}
	applyLaunchInvocationToContext(ctx, record, record.RecordedAt)
	// 同一进程内每次 turn 结束都会 sync：历史必须仍然只有一条。
	applyLaunchInvocationToContext(ctx, record, record.RecordedAt)
	require.Len(t, launchHistoryEntriesFromContext(ctx), 1)

	next := &launchInvocationRecord{Instance: "2@other", Flags: []string{"--yolo"}, Entry: "resume", RecordedAt: record.RecordedAt.Add(time.Minute)}
	applyLaunchInvocationToContext(ctx, next, next.RecordedAt)
	history := launchHistoryEntriesFromContext(ctx)
	require.Len(t, history, 2)
	// 最新在前。
	first, ok := history[0]["instance"].(string)
	require.True(t, ok)
	require.Equal(t, "2@other", first)
}

func TestLaunchInvocationHistoryIsBounded(t *testing.T) {
	ctx := map[string]interface{}{}
	for index := 0; index < launchHistoryMaxEntries+6; index++ {
		record := &launchInvocationRecord{
			Instance:   "instance-" + string(rune('a'+index%26)) + time.Now().String(),
			Flags:      []string{"--web"},
			Entry:      "chat",
			RecordedAt: time.Now().UTC().Add(time.Duration(index) * time.Minute),
		}
		applyLaunchInvocationToContext(ctx, record, record.RecordedAt)
	}
	require.Len(t, launchHistoryEntriesFromContext(ctx), launchHistoryMaxEntries)
}

// TestLaunchInvocationSurvivesJSONRoundTrip 锁住存储往返形态：SQLite/file 把
// metadata_json 反序列化成 []interface{}，读回必须仍是同一份旗标。
func TestLaunchInvocationSurvivesJSONRoundTrip(t *testing.T) {
	ctx := map[string]interface{}{}
	record := &launchInvocationRecord{
		Instance:   "pid@nano",
		Entry:      "chat",
		Flags:      []string{"--web", "--yolo", "-m", "gpt-4.1"},
		RawArgv:    []string{"chat", "--web", "--yolo", "-m", "gpt-4.1"},
		Binary:     "/usr/local/bin/aicli",
		Cwd:        "/tmp/demo",
		RecordedAt: time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC),
	}
	require.True(t, applyLaunchInvocationToContext(ctx, record, record.RecordedAt))

	encoded, err := json.Marshal(ctx)
	require.NoError(t, err)
	var decoded map[string]interface{}
	require.NoError(t, json.Unmarshal(encoded, &decoded))

	read := launchInvocationFromContext(decoded)
	require.NotNil(t, read)
	require.Equal(t, record.Flags, read.Flags)
	require.Equal(t, record.RawArgv, read.RawArgv)
	require.Equal(t, "/tmp/demo", read.Cwd)
	require.Equal(t, "/usr/local/bin/aicli", read.Binary)
	require.Equal(t, "chat", read.Entry)
	require.True(t, read.RecordedAt.Equal(record.RecordedAt))

	require.Nil(t, launchInvocationFromContext(map[string]interface{}{}))
	require.Nil(t, launchInvocationFromContext(nil))
}

func TestSessionRelaunchArgsAndDisplayCommand(t *testing.T) {
	require.Equal(t, []string{"resume", "session_1", "--web", "--yolo"}, sessionRelaunchArgs("session_1", []string{"--web", "--yolo"}))
	require.Nil(t, sessionRelaunchArgs("  ", []string{"--web"}))

	command := formatLaunchCommand("/usr/bin/aicli", "session_1", []string{"--web", "-m", "some model"})
	require.Equal(t, "/usr/bin/aicli resume session_1 --web -m \"some model\"", command)
}

func TestNewLaunchInvocationRecordNeedsArgvSnapshot(t *testing.T) {
	restore := captureLaunchInvocationForTest(t, nil)
	defer restore()
	require.Nil(t, newLaunchInvocationRecord(launchInvocationTestCommand()))

	restore = captureLaunchInvocationForTest(t, []string{"resume", "session_9", "--web", "--web-token", "topsecret", "-m", "gpt-4.1"})
	defer restore()
	record := newLaunchInvocationRecord(launchInvocationTestCommand())
	require.NotNil(t, record)
	require.Equal(t, "resume", record.Entry)
	require.Equal(t, []string{"--web", "-m", "gpt-4.1"}, record.Flags)
	require.NotContains(t, stringsJoin(record.RawArgv), "topsecret")
	require.NotEmpty(t, record.Binary)
	require.NotEmpty(t, record.Cwd)
	require.False(t, record.RecordedAt.IsZero())
}

// captureLaunchInvocationForTest 注入进程 argv 快照并在结束后复原。
func captureLaunchInvocationForTest(t *testing.T, argv []string) func() {
	t.Helper()
	previous := LaunchInvocationArgv()
	SetLaunchInvocation(argv)
	return func() { SetLaunchInvocation(previous) }
}

func stringsJoin(tokens []string) string {
	out := ""
	for _, token := range tokens {
		out += token + "\x00"
	}
	return out
}

func TestLaunchEntryOrDefaultNormalizesRewrittenArgv(t *testing.T) {
	// `aicli --yolo` 被 prependDefaultChatCommand 改写：快照里没有 "chat" 这个词，
	// 展示时补回默认入口，避免详情里显示成空。
	require.Equal(t, "chat", launchEntryOrDefault(""))
	require.Equal(t, "resume", launchEntryOrDefault(" resume "))
}
