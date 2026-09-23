package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// aicli import 退出码约定（与 aicli export / aicli stats 对齐）：
//
//	0 = 导入成功（含 --dry-run 预检通过）
//	1 = 参数错误（缺少文件参数、非法的选项组合）
//	2 = 确定性错误（文件不可读/不是 --full 产物、会话已存在且未要求新 ID、写库失败）
const (
	importExitOK      = 0
	importExitUsage   = 1
	importExitFailure = 2
)

// importExitHook 是退出钩子：生产路径为 os.Exit，测试注入以断言退出码。
var importExitHook = os.Exit

func importExit(code int) {
	if code == importExitOK {
		return
	}
	runExitCleanup()
	importExitHook(code)
}

// errImportUsage 标记「命令行用法错误」（退出码 1）；其余错误按确定性失败处理。
var errImportUsage = errors.New("参数错误")

// errImportSessionExists 标记「目标存储已存在同 ID 会话」。默认拒绝导入而不是
// 覆盖：SQLite 的 Save 对已存在 ID 是 upsert 语义，一旦放过就是静默覆盖用户
// 的原会话，属于不可逆的数据损失。
var errImportSessionExists = errors.New("会话已存在")

// 导入溯源写入 session.Metadata.Context（与 compact_* 系列 key 同一命名空间）。
// 这些键让「这份会话从哪来」在导出/列表里可追溯，也便于事后比对导入结果。
const (
	importContextKeySource            = "imported_from"
	importContextKeyImportedAt        = "imported_at"
	importContextKeyOriginalSessionID = "import_original_session_id"
	importContextKeyOriginalState     = "import_original_state"
)

const importCommandLongHelp = `把 aicli export --full 导出的完整 JSON 会话写回会话库。

输入文件必须是由 --full（默认格式）导出的 JSON：包含 session（含 history、
tool_calls、metadata）与 stats。--body/--tools/--trace 是只读 Markdown 投影，
不能用于导入。

会话 ID：
  默认沿用导出文件里的 session.id。若目标会话库中已存在同 ID 会话，导入会
  报错退出且不覆盖任何内容；确认要并存时加 --new-id，导入会自动生成新 ID。

用户归属：
  默认沿用导出文件里的 userId；文件缺少该字段时回退到当前解析出的默认用户。
  --user <id> 可显式覆盖（覆盖时会在摘要里提示）。

落库语义（与直接拷贝数据库文件的区别）：
  - createdAt 保留导出文件里的原值；updatedAt 记为导入时间，便于该会话立刻
    出现在 resume/latest 列表顶部（也会避免刚导入就被 idle 归档）。
  - expiresAt 不保留：带着过期的 expiresAt 落库会让会话在第一次读取时被判过期删除。
  - headOffset 归零、canonicalMessageCount 由存储重算，避免沿用源库的窗口游标。
  - 缺失的 message_id/turn_id 会补齐，已有的保持不变（可重复导入不产生漂移）。
  - state 保留原值；文件里非 active 的状态会在摘要里提示。

结构化输出：
  --json 或 --output json 输出机器可读摘要；全局 --envelope 会额外包成
  {ok,command,data}。失败时同样给出 {ok:false,command:"import",error,details.hint}，
  脚本无需解析中文提示。

退出码：
  0 = 导入成功
  1 = 参数错误
  2 = 确定性错误（文件不可读/格式不支持、会话已存在、写库或读回校验失败）`

const importCommandExampleHelp = `  aicli import ./exports/session_20260923_full.json
  aicli import ./session.json --session-dir ./sessions
  aicli import ./session.json --new-id
  aicli import ./session.json --user alice
  aicli import ./session.json --dry-run --json`

// NewImportCommand 创建 `aicli import <file>`：`aicli export --full` 的反向入口。
// 与 export 对称，只构造会话存储所需的最小 ChatSession，不加载 provider/actor/工具面。
func NewImportCommand(getConfig func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "import <file>",
		Short:   "把 aicli export --full 导出的完整 JSON 会话导入会话库",
		Long:    importCommandLongHelp,
		Example: importCommandExampleHelp,
		Args:    cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			importExit(runImportCommand(cmd, getConfig, args))
		},
	}
	cmd.Flags().String("session-dir", "", "目标会话持久化目录（默认: ~/.aicli/sessions）")
	cmd.Flags().String("user", "", "覆盖导入后的会话用户 ID（默认沿用导出文件里的 userId）")
	cmd.Flags().Bool("new-id", false, "目标存储已存在同 ID 会话时自动生成新 ID（默认报错，不覆盖）")
	cmd.Flags().Bool("dry-run", false, "只校验文件与冲突并打印摘要，不写入会话库")
	cmd.Flags().String("output", "", "输出格式（text|json）")
	cmd.Flags().Bool("json", false, "以 JSON 格式输出导入摘要（等价于 --output json）")
	return cmd
}

// importRequest 是导入命令的输入参数（可测试，不依赖 cobra）。
type importRequest struct {
	// Path 是导出文件路径。
	Path string
	// NewID 允许在 ID 冲突（或文件缺少 ID）时生成新 ID。
	NewID bool
	// DryRun 只做校验与冲突判定，不落库。
	DryRun bool
	// UserID 非空时覆盖导出文件里的用户归属。
	UserID string
}

// importSessionResult 是导入摘要：既用于文本输出，也用于 --json。
type importSessionResult struct {
	SessionID        string   `json:"session_id"`
	SourceSessionID  string   `json:"source_session_id,omitempty"`
	Renamed          bool     `json:"renamed"`
	UserID           string   `json:"user_id"`
	UserOverridden   bool     `json:"user_overridden"`
	State            string   `json:"state"`
	MessageCount     int      `json:"message_count"`
	ToolCallCount    int      `json:"tool_call_count"`
	ContentPartCount int      `json:"content_part_count"`
	SourceFile       string   `json:"source_file"`
	SourceFormat     string   `json:"source_format,omitempty"`
	ExportedAt       string   `json:"exported_at,omitempty"`
	StoreDir         string   `json:"store_dir,omitempty"`
	DryRun           bool     `json:"dry_run"`
	Warnings         []string `json:"warnings,omitempty"`
}

func runImportCommand(cmd *cobra.Command, getConfig func() *config.Config, args []string) int {
	filePath := ""
	if len(args) > 0 {
		filePath = strings.TrimSpace(args[0])
	}
	outputOptions, outputErr := resolveStructuredOutputOptions(cmd, "text", "text", "json")
	if outputErr != nil {
		printImportCommandError(outputErr, structuredOutputOptions{})
		return importExitUsage
	}
	if filePath == "" {
		printImportCommandError(fmt.Errorf("%w: 需要指定导入文件（aicli import <file>）", errImportUsage), outputOptions)
		return importExitUsage
	}
	req := importRequest{
		Path:   filePath,
		NewID:  boolFlag(cmd, "new-id"),
		DryRun: boolFlag(cmd, "dry-run"),
		UserID: strings.TrimSpace(stringFlag(cmd, "user")),
	}

	var cfg *config.Config
	if getConfig != nil {
		cfg = getConfig()
	}
	chatSession, err := newChatExportCommandSession(cfg, stringFlag(cmd, "session-dir"), req.UserID)
	if err != nil {
		printImportCommandError(err, outputOptions)
		return importExitFailure
	}
	defer chatSession.SessionManager.Stop()

	result, err := importChatSessionFromExportFile(context.Background(), chatSession, req)
	if err != nil {
		printImportCommandError(err, outputOptions)
		if errors.Is(err, errImportUsage) {
			return importExitUsage
		}
		return importExitFailure
	}
	printImportCommandResult(result, outputOptions)
	return importExitOK
}

// importChatSessionFromExportFile 是导入的核心：读文件 → 校验 → 归属/ID/游标归一化
// → 冲突判定 → 落库 → 读回校验。chatSession 只需提供 SessionManager/SessionUserID/
// SessionDir（见 newChatExportCommandSession）。
func importChatSessionFromExportFile(ctx context.Context, session *ChatSession, req importRequest) (*importSessionResult, error) {
	if session == nil || session.SessionManager == nil {
		return nil, fmt.Errorf("初始化会话存储失败: 会话管理器为空")
	}
	envelope, err := readChatSessionExportFile(req.Path)
	if err != nil {
		return nil, err
	}

	imported := *envelope.Session
	absoluteSource, _ := filepath.Abs(req.Path)

	warnings := make([]string, 0, 4)
	// 归属：显式 --user > 文件里的 userId > 当前解析出的默认用户。
	userID := strings.TrimSpace(req.UserID)
	userOverridden := false
	switch {
	case userID != "":
		fileUserID := strings.TrimSpace(imported.UserID)
		if fileUserID != "" && fileUserID != userID {
			userOverridden = true
			warnings = append(warnings, fmt.Sprintf("导出文件的用户归属为 %s，已按 --user 改写为 %s", fileUserID, userID))
		}
	case strings.TrimSpace(imported.UserID) != "":
		userID = strings.TrimSpace(imported.UserID)
	default:
		userID = strings.TrimSpace(session.SessionUserID)
	}
	if userID == "" {
		return nil, fmt.Errorf("无法确定导入会话的用户归属: 导出文件缺少 userId，且当前环境没有默认用户；请用 --user 指定")
	}

	storage := session.SessionManager.GetStorage()
	sourceSessionID := strings.TrimSpace(imported.ID)
	targetSessionID := sourceSessionID
	renamed := false
	// adoptGeneratedID 在「不能沿用原 ID」时生成新 ID 并记录原因。
	adoptGeneratedID := func(reason string) error {
		generatedID, idErr := newImportSessionID(ctx, storage, userID)
		if idErr != nil {
			return idErr
		}
		targetSessionID = generatedID
		renamed = true
		warnings = append(warnings, fmt.Sprintf("%s，已改用新 ID %s", reason, targetSessionID))
		return nil
	}

	// 沿用原 ID 的前提是它存在且可寻址；两条都不满足时必须显式 --new-id 授权，
	// 否则会落进「写得进读不回」的孤儿记录。
	switch {
	case targetSessionID == "":
		// 没有 ID 的导出文件无法寻址（resume/export 都按 ID 定位），必须显式授权生成。
		if !req.NewID {
			return nil, fmt.Errorf("%w: 导出文件缺少 session.id，无法沿用原 ID 导入；加 --new-id 可生成新 ID 导入", errImportUsage)
		}
		if err := adoptGeneratedID("导出文件缺少 session.id"); err != nil {
			return nil, err
		}
	case !runtimechat.IsAddressableSessionID(imported.ID):
		// 存储层读写两侧都会 sanitizeSessionID（去首尾空白、去尾部分隔符、取路径最后
		// 一段），"dir/abc"、"abc/"、" abc " 这类 ID 写得进去却永远读不回来 —— 会变成
		// 列表里点开必然 404 的孤儿。必须在落库前挡住；--new-id 是不沿用原 ID 的明确
		// 授权，因此允许它改用新 ID 导入。
		if !req.NewID {
			return nil, fmt.Errorf("%w: 导出文件里的 session.id %q 不可寻址（含路径分隔符、首尾空白或占位值，落库后无法按原样读回）；请修正文件里的 ID，或加 --new-id 生成新 ID 导入", errImportUsage, imported.ID)
		}
		if err := adoptGeneratedID(fmt.Sprintf("导出文件里的 session.id %q 不可寻址（无法按原样读回）", sourceSessionID)); err != nil {
			return nil, err
		}
	default:
		exists, existsErr := importTargetSessionExists(ctx, storage, targetSessionID)
		if existsErr != nil {
			return nil, existsErr
		}
		if exists {
			if !req.NewID {
				return nil, fmt.Errorf("%w: %s（导入不会覆盖已有会话）", errImportSessionExists, targetSessionID)
			}
			if err := adoptGeneratedID(fmt.Sprintf("目标存储已存在会话 %s（未覆盖）", sourceSessionID)); err != nil {
				return nil, err
			}
		}
	}

	history := make([]runtimetypes.Message, len(imported.History))
	copy(history, imported.History)
	filledIdentities, remintedIdentities := ensureImportMessageIdentities(history)
	if filledIdentities {
		warnings = append(warnings, "已为缺失 message_id/turn_id 的消息补齐身份")
	}
	if remintedIdentities > 0 {
		warnings = append(warnings, fmt.Sprintf("导出文件里有 %d 条消息复用了重复的 message_id，已重新铸造（读取时相邻同内容消息会被折叠，重复身份会导致静默少消息）", remintedIdentities))
	}
	if incomplete := countImportMessagesMissingToolIdentity(history); incomplete > 0 {
		warnings = append(warnings, fmt.Sprintf("%d 条消息缺少工具调用 ID 或 tool_call_id，存储不会拒绝，但渲染与回放时可能对不上工具结果", incomplete))
	}

	now := time.Now()
	createdAt := imported.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	state := imported.State
	if strings.TrimSpace(string(state)) == "" {
		state = runtimechat.StateActive
	} else if state != runtimechat.StateActive {
		warnings = append(warnings, fmt.Sprintf("导出文件的状态为 %s，导入后保持该状态（会话列表可能默认隐藏它）", state))
	}

	result := &importSessionResult{
		SessionID:        targetSessionID,
		SourceSessionID:  sourceSessionID,
		Renamed:          renamed,
		UserID:           userID,
		UserOverridden:   userOverridden,
		State:            string(state),
		MessageCount:     len(history),
		SourceFile:       absoluteSource,
		SourceFormat:     strings.TrimSpace(envelope.Format),
		StoreDir:         session.SessionDir,
		DryRun:           req.DryRun,
	}
	if !envelope.ExportedAt.IsZero() {
		result.ExportedAt = envelope.ExportedAt.Format(time.RFC3339)
	}
	result.ToolCallCount, result.ContentPartCount = chatImportHistoryStats(history)

	if envelope.Stats.MessageCount > 0 && envelope.Stats.MessageCount != len(history) {
		warnings = append(warnings, fmt.Sprintf("导出文件 stats.message_count=%d 与 history 条数 %d 不一致，文件可能被截断", envelope.Stats.MessageCount, len(history)))
	}
	if len(history) == 0 {
		warnings = append(warnings, "导入的会话没有任何消息")
	}

	if req.DryRun {
		result.Warnings = warnings
		return result, nil
	}

	imported.ID = targetSessionID
	imported.UserID = userID
	imported.State = state
	imported.History = history
	// HistoryLoaded 声明「这份 History 是完整视图」，存储据此写入 canonical 消息。
	imported.HistoryLoaded = true
	// 游标归一化：HeadOffset 是源库 prompt 窗口的相对位置，沿用会让 DroppedHistory()
	// 误报被压缩丢弃的前缀；canonicalMessageCount 由存储重算。
	imported.HeadOffset = 0
	imported.CanonicalMessageCount = len(history)
	imported.CreatedAt = createdAt
	imported.UpdatedAt = now
	imported.PreserveUpdatedAt = false
	// 过期时间不保留：过期会话会在第一次 Get 时被删除（manager.Get → IsExpired）。
	imported.ExpiresAt = nil
	if imported.Metadata.Tags == nil {
		imported.Metadata.Tags = []string{}
	}
	if imported.Metadata.Context == nil {
		imported.Metadata.Context = make(map[string]interface{}, 3)
	}
	imported.Metadata.Context[importContextKeySource] = absoluteSource
	imported.Metadata.Context[importContextKeyImportedAt] = now.UTC().Format(time.RFC3339)
	if renamed && sourceSessionID != "" {
		imported.Metadata.Context[importContextKeyOriginalSessionID] = sourceSessionID
	}
	if originalState := strings.TrimSpace(string(envelope.Session.State)); originalState != "" && originalState != string(state) {
		imported.Metadata.Context[importContextKeyOriginalState] = originalState
	}

	// 与 SessionManager.Create 同一条写路径（storage.Save）：一次性写入 metadata +
	// 全量 canonical history + prompt 投影。
	if err := storage.Save(ctx, &imported); err != nil {
		return nil, fmt.Errorf("写入会话存储失败: %w", err)
	}
	if err := verifyImportedChatSession(ctx, session.SessionManager, targetSessionID, userID, len(history)); err != nil {
		return nil, err
	}
	result.Warnings = warnings
	return result, nil
}

// readChatSessionExportFile 读取并校验导出文件。校验只看「能否安全落库」：
// 必须是当前版本的完整导出 envelope，且带 session 对象。
func readChatSessionExportFile(path string) (*chatSessionExportEnvelope, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取导入文件失败: %w", err)
	}
	var envelope chatSessionExportEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("解析导入文件失败: %w（需要 aicli export --full 产出的 JSON）", err)
	}
	if envelope.Version != 1 {
		return nil, fmt.Errorf("不支持的导出文件版本 %d（当前支持 1）；请确认文件由 aicli export --full 生成", envelope.Version)
	}
	if envelope.Session == nil {
		return nil, fmt.Errorf("导出文件缺少 session 字段，无法导入；请用 aicli export --full 重新导出")
	}
	return &envelope, nil
}

// importTargetSessionExists 判断目标存储是否已有该会话。这里只做只读探测：
// 不能走 manager.Get（它会把过期会话直接删掉，导入的冲突判定不该有副作用）。
func importTargetSessionExists(ctx context.Context, storage runtimechat.SessionStorage, sessionID string) (bool, error) {
	if strings.TrimSpace(sessionID) == "" {
		return false, nil
	}
	if _, err := storage.Load(ctx, sessionID); err != nil {
		if errors.Is(err, runtimechat.ErrSessionNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("检查目标会话是否存在失败: %w", err)
	}
	return true, nil
}

// newImportSessionID 生成一个新的会话 ID（沿用 NewSession 的 ID 规则，避免自造格式）。
// 生成后仍要回查一次存储：ID 带时间戳前缀，同一秒内重复调用有极小概率撞车，
// 而 Save 对已存在 ID 是 upsert —— 撞车就等于静默覆盖，必须在这里挡掉。
func newImportSessionID(ctx context.Context, storage runtimechat.SessionStorage, userID string) (string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		candidate := runtimechat.NewSession(userID).ID
		exists, err := importTargetSessionExists(ctx, storage, candidate)
		if err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("生成新会话 ID 失败: 连续生成的候选 ID 均已存在，请重试")
}

// verifyImportedChatSession 读回校验：确认会话可加载、ID/归属正确，且 canonical
// 消息条数与导出文件一致（按行流式统计，不依赖可能被窗口裁剪的 prompt 投影）。
func verifyImportedChatSession(ctx context.Context, manager *runtimechat.SessionManager, sessionID, wantUserID string, wantMessages int) error {
	loaded, err := manager.Get(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("导入后读回校验失败: %w", err)
	}
	if loaded == nil {
		return fmt.Errorf("导入后读回校验失败: 会话 %s 不可加载", sessionID)
	}
	if loaded.ID != sessionID {
		return fmt.Errorf("导入后读回校验失败: 期望会话 %s，实际 %s", sessionID, loaded.ID)
	}
	if wantUserID != "" && loaded.UserID != wantUserID {
		return fmt.Errorf("导入后读回校验失败: 期望用户 %s，实际 %s", wantUserID, loaded.UserID)
	}
	count := 0
	if err := manager.StreamHistory(ctx, sessionID, func(seq int, message runtimetypes.Message) error {
		count++
		return nil
	}); err != nil {
		return fmt.Errorf("导入后读回校验失败: %w", err)
	}
	if count != wantMessages {
		return fmt.Errorf("导入后读回校验失败: 期望 %d 条消息，实际 %d 条", wantMessages, count)
	}
	return nil
}

// ensureImportMessageIdentities 补齐历史里缺失的 message_id/turn_id，并保证身份唯一。
//
// 补齐用现成的 types.EnsureHistoryMessageIdentities（与 chat 落库前同一套规则）；
// 唯一性则必须自己保证：读取/分页路径会折叠「相邻 + 同 message_id + 同 substance」
// 的行（collapseDuplicateTranscriptRows，见 sqlite_storage.go 的读取路径），
// 重复身份不会报错，只会让读出来的消息静默少几条。因此重复出现的身份在这里
// 清空后重新铸造 —— 内容一条不动，只换掉重复的 ID。
//
// 返回 (是否补齐过, 重新铸造的条数)。
func ensureImportMessageIdentities(history []runtimetypes.Message) (bool, int) {
	seen := make(map[string]struct{}, len(history))
	reminted := 0
	for index := range history {
		messageID := strings.TrimSpace(runtimetypes.MessageID(history[index]))
		if messageID == "" {
			continue
		}
		if _, ok := seen[messageID]; ok {
			if history[index].Metadata == nil {
				history[index].Metadata = runtimetypes.NewMetadata()
			}
			history[index].Metadata.Set(runtimetypes.MetadataKeyMessageID, "")
			reminted++
			continue
		}
		seen[messageID] = struct{}{}
	}
	return runtimetypes.EnsureHistoryMessageIdentities(history), reminted
}

// countImportMessagesMissingToolIdentity 统计工具链身份不完整的消息：assistant 的
// tool_calls 缺 ID，或 tool 消息缺 tool_call_id。存储层不校验这类坏数据，但后续
// 渲染/回放会配不上工具结果，所以在摘要里给出提示而不是默默放过。
func countImportMessagesMissingToolIdentity(history []runtimetypes.Message) int {
	incomplete := 0
	for index := range history {
		message := history[index]
		if strings.EqualFold(strings.TrimSpace(message.Role), "tool") {
			if strings.TrimSpace(message.ToolCallID) == "" {
				incomplete++
			}
			continue
		}
		for callIndex := range message.ToolCalls {
			if strings.TrimSpace(message.ToolCalls[callIndex].ID) == "" {
				incomplete++
				break
			}
		}
	}
	return incomplete
}

func chatImportHistoryStats(history []runtimetypes.Message) (toolCalls, contentParts int) {
	for index := range history {
		toolCalls += len(history[index].ToolCalls)
		contentParts += len(history[index].ContentParts)
	}
	return toolCalls, contentParts
}

func printImportCommandResult(result *importSessionResult, outputOptions structuredOutputOptions) {
	if result == nil {
		return
	}
	if isJSONOutputFormat(outputOptions.Format) {
		printCommandJSONOutput("import", outputOptions.Envelope, result)
		return
	}
	if result.DryRun {
		fmt.Println("导入预检通过（未写入）")
	} else {
		fmt.Println("会话已导入")
	}
	printChatSessionMetaRow("Session:", chatDebugValueOrNone(result.SessionID))
	printChatSessionMetaRow("User:", chatDebugValueOrNone(result.UserID))
	printChatSessionMetaRow("State:", chatDebugValueOrNone(result.State))
	printChatSessionMetaRow("Messages:", fmt.Sprintf("%d", result.MessageCount))
	printChatSessionMetaRow("Tool Calls:", fmt.Sprintf("%d", result.ToolCallCount))
	printChatSessionMetaRow("Source File:", chatDebugValueOrNone(result.SourceFile))
	if result.ExportedAt != "" {
		printChatSessionMetaRow("Exported At:", result.ExportedAt)
	}
	if result.StoreDir != "" {
		printChatSessionMetaRow("Session Dir:", resolveAbsoluteChatPath(result.StoreDir))
	}
	for _, warning := range result.Warnings {
		fmt.Printf("提示: %s\n", warning)
	}
	if result.DryRun {
		fmt.Println("提示: --dry-run 未写入任何内容；去掉该参数即执行导入。")
		return
	}
	fmt.Printf("提示: 用 aicli chat --resume 继续该会话（updatedAt 已置为导入时间，它会排在最近会话首位），或用 aicli export %s --full 再次导出。\n", result.SessionID)
}

// importCommandErrorHint 返回该错误的可执行建议；既用于文本提示，也作为 JSON
// 错误载荷的 details.hint，让脚本在失败时同样能拿到「下一步怎么做」。
func importCommandErrorHint(err error) string {
	switch {
	case errors.Is(err, errImportSessionExists):
		return "加 --new-id 让导入自动生成新 ID（两份会话并存）；如需替换原会话，请先自行导出备份并在确认后删除原会话。"
	case errors.Is(err, errImportUsage):
		return "用法: aicli import <file> [--session-dir <dir>] [--user <id>] [--new-id] [--dry-run] [--output text|json]"
	default:
		return ""
	}
}

func printImportCommandError(err error, outputOptions structuredOutputOptions) {
	if err == nil {
		return
	}
	hint := importCommandErrorHint(err)
	if isJSONOutputFormat(outputOptions.Format) {
		details := map[string]interface{}{}
		if hint != "" {
			details["hint"] = hint
		}
		emitCommandError("import", outputOptions.Format, err, details)
		return
	}
	fmt.Fprintf(os.Stderr, "错误: %v\n", err)
	if hint != "" {
		fmt.Fprintf(os.Stderr, "提示: %s\n", hint)
	}
}
