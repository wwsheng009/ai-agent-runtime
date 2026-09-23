package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const importTestMessageCount = 12

// importTestConversation 构造一段带工具调用与结果的会话：history 里最容易在
// 导入过程中被丢掉的正是 tool_calls / tool_call_id 这组关联。
func importTestConversation(count int) []runtimetypes.Message {
	messages := make([]runtimetypes.Message, 0, count)
	for index := 0; index < count; index++ {
		switch index % 3 {
		case 0:
			messages = append(messages, *runtimetypes.NewUserMessage(fmt.Sprintf("question-%02d", index)))
		case 1:
			assistant := runtimetypes.NewAssistantMessage(fmt.Sprintf("answer-%02d", index))
			assistant.ToolCalls = []runtimetypes.ToolCall{{
				ID:   fmt.Sprintf("call-%02d", index),
				Name: "execute_shell_command",
				Args: map[string]interface{}{"command": "git status --short"},
			}}
			messages = append(messages, *assistant)
		default:
			messages = append(messages, *runtimetypes.NewToolMessage(fmt.Sprintf("call-%02d", index-1), fmt.Sprintf("tool-output-%02d", index)))
		}
	}
	return messages
}

// importTestSeedSource 在 sourceDir 建一个真实会话并写入消息，返回管理器、会话 ID 与用户。
func importTestSeedSource(t *testing.T, sourceDir string, messages []runtimetypes.Message) (*runtimechat.SessionManager, string, string) {
	t.Helper()
	manager, userID, _, err := newChatSessionManager(sourceDir)
	if err != nil {
		t.Fatalf("创建源会话存储失败: %v", err)
	}
	t.Cleanup(manager.Stop)
	// 真实 chat 路径落库前会补齐 message_id/turn_id（types.EnsureHistoryMessageIdentities），
	// 夹具必须同样处理，否则「导入是否保留消息身份」的断言就没有意义。
	runtimetypes.EnsureHistoryMessageIdentities(messages)
	session, err := manager.CreateSession(context.Background(), userID)
	if err != nil {
		t.Fatalf("创建源会话失败: %v", err)
	}
	for index := range messages {
		if err := manager.AddMessage(context.Background(), session.ID, messages[index]); err != nil {
			t.Fatalf("写入源消息 %d 失败: %v", index, err)
		}
	}
	return manager, session.ID, userID
}

// importTestExportFull 走真实导出链路产出 --full JSON：导入端面对的必须是导出端
// 的真实产物，而不是手搓的近似结构。
func importTestExportFull(t *testing.T, manager *runtimechat.SessionManager, sessionDir, sessionID string) string {
	t.Helper()
	outputPath := filepath.Join(t.TempDir(), "export-full.json")
	if _, err := exportChatSession(&ChatSession{
		SessionManager: manager,
		SessionDir:     sessionDir,
		SessionUserID:  "tester",
	}, chatExportOptions{Target: sessionID, Format: chatExportFormatFull, OutputPath: outputPath}); err != nil {
		t.Fatalf("导出会话失败: %v", err)
	}
	return outputPath
}

// importTestTarget 打开一个隔离的目标会话存储。
func importTestTarget(t *testing.T, targetDir string) *ChatSession {
	t.Helper()
	manager, userID, resolvedDir, err := newChatSessionManager(targetDir)
	if err != nil {
		t.Fatalf("创建目标会话存储失败: %v", err)
	}
	t.Cleanup(manager.Stop)
	return &ChatSession{SessionManager: manager, SessionDir: resolvedDir, SessionUserID: userID}
}

// importTestHistory 读回 canonical 历史（StreamHistory 走全量行，不受 hot window 裁剪影响）。
func importTestHistory(t *testing.T, manager *runtimechat.SessionManager, sessionID string) []runtimetypes.Message {
	t.Helper()
	history := make([]runtimetypes.Message, 0, importTestMessageCount)
	if err := manager.StreamHistory(context.Background(), sessionID, func(seq int, message runtimetypes.Message) error {
		history = append(history, message)
		return nil
	}); err != nil {
		t.Fatalf("读取会话 %s 的 canonical 历史失败: %v", sessionID, err)
	}
	return history
}

func importTestHistoryMessageIDs(history []runtimetypes.Message) []string {
	ids := make([]string, 0, len(history))
	for index := range history {
		ids = append(ids, history[index].Metadata.GetString(runtimetypes.MetadataKeyMessageID, ""))
	}
	return ids
}

func importTestMetadataContext(t *testing.T, session *runtimechat.Session, key string) string {
	t.Helper()
	if session == nil || session.Metadata.Context == nil {
		return ""
	}
	value, ok := session.Metadata.Context[key]
	if !ok {
		return ""
	}
	return strings.TrimSpace(fmt.Sprintf("%v", value))
}

func TestImportChatSessionRoundTripPreservesIDUserAndHistory(t *testing.T) {
	ctx := context.Background()
	sourceDir := t.TempDir()
	messages := importTestConversation(importTestMessageCount)
	sourceManager, sourceSessionID, sourceUser := importTestSeedSource(t, sourceDir, messages)
	sourceHistory := importTestHistory(t, sourceManager, sourceSessionID)
	if len(sourceHistory) != importTestMessageCount {
		t.Fatalf("源会话应有 %d 条消息，实际 %d 条", importTestMessageCount, len(sourceHistory))
	}
	sourceSession, err := sourceManager.Get(ctx, sourceSessionID)
	if err != nil {
		t.Fatalf("加载源会话失败: %v", err)
	}
	exportPath := importTestExportFull(t, sourceManager, sourceDir, sourceSessionID)

	target := importTestTarget(t, t.TempDir())
	startedAt := time.Now().Add(-time.Second)
	result, err := importChatSessionFromExportFile(ctx, target, importRequest{Path: exportPath})
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}

	if result.SessionID != sourceSessionID {
		t.Fatalf("默认应沿用原会话 ID %s，实际 %s", sourceSessionID, result.SessionID)
	}
	if result.SourceSessionID != sourceSessionID || result.Renamed {
		t.Fatalf("未发生改名，实际 renamed=%v source=%s", result.Renamed, result.SourceSessionID)
	}
	if result.UserID != sourceUser || result.UserOverridden {
		t.Fatalf("默认应沿用原用户 %s，实际 user=%s overridden=%v", sourceUser, result.UserID, result.UserOverridden)
	}
	if result.MessageCount != importTestMessageCount {
		t.Fatalf("摘要消息数应为 %d，实际 %d", importTestMessageCount, result.MessageCount)
	}
	if result.ToolCallCount != 4 {
		t.Fatalf("摘要工具调用数应为 4，实际 %d", result.ToolCallCount)
	}
	if result.State != string(runtimechat.StateActive) {
		t.Fatalf("状态应为 active，实际 %s", result.State)
	}
	if result.DryRun {
		t.Fatal("非 --dry-run 的导入不应标记 dry_run")
	}
	if abs, absErr := filepath.Abs(exportPath); absErr == nil && result.SourceFile != abs {
		t.Fatalf("摘要应记录源文件绝对路径 %s，实际 %s", abs, result.SourceFile)
	}

	loaded, err := target.SessionManager.Get(ctx, sourceSessionID)
	if err != nil {
		t.Fatalf("导入后加载会话失败: %v", err)
	}
	if loaded.UserID != sourceUser {
		t.Fatalf("导入会话归属应为 %s，实际 %s", sourceUser, loaded.UserID)
	}
	if loaded.CanonicalMessageCount != importTestMessageCount {
		t.Fatalf("canonical 消息数应为 %d，实际 %d", importTestMessageCount, loaded.CanonicalMessageCount)
	}
	if loaded.HeadOffset != 0 {
		t.Fatalf("导入后 headOffset 必须归零，实际 %d", loaded.HeadOffset)
	}
	if loaded.ExpiresAt != nil {
		t.Fatalf("导入后不应保留过期时间，实际 %v", loaded.ExpiresAt)
	}
	if !loaded.CreatedAt.Equal(sourceSession.CreatedAt) {
		t.Fatalf("应保留原 createdAt %v，实际 %v", sourceSession.CreatedAt, loaded.CreatedAt)
	}
	if !loaded.UpdatedAt.After(startedAt) {
		t.Fatalf("updatedAt 应刷新为导入时间，实际 %v", loaded.UpdatedAt)
	}
	if source := importTestMetadataContext(t, loaded, importContextKeySource); source == "" {
		t.Fatal("导入会话应记录 imported_from 溯源信息")
	}
	if at := importTestMetadataContext(t, loaded, importContextKeyImportedAt); at == "" {
		t.Fatal("导入会话应记录 imported_at 溯源信息")
	}

	targetHistory := importTestHistory(t, target.SessionManager, sourceSessionID)
	if len(targetHistory) != importTestMessageCount {
		t.Fatalf("导入会话应有 %d 条消息，实际 %d 条", importTestMessageCount, len(targetHistory))
	}
	// 消息身份必须原样保留：重新生成 message_id/turn_id 会让回溯、去重与
	// 增量同步全部错位。
	sourceIDs := importTestHistoryMessageIDs(sourceHistory)
	targetIDs := importTestHistoryMessageIDs(targetHistory)
	for index := range sourceIDs {
		if sourceIDs[index] == "" {
			t.Fatalf("源消息 %d 缺少 message_id", index)
		}
		if sourceIDs[index] != targetIDs[index] {
			t.Fatalf("第 %d 条消息的 message_id 应保持 %s，实际 %s", index, sourceIDs[index], targetIDs[index])
		}
	}
	if len(targetHistory[1].ToolCalls) != 1 || targetHistory[1].ToolCalls[0].ID != "call-01" {
		t.Fatalf("工具调用应随导入保留，实际 %+v", targetHistory[1].ToolCalls)
	}
	if targetHistory[2].ToolCallID != "call-01" || targetHistory[2].Content != "tool-output-02" {
		t.Fatalf("工具结果应随导入保留，实际 %+v", targetHistory[2])
	}

	previews, err := target.SessionManager.ListPreviews(ctx, sourceUser, 50, 0)
	if err != nil {
		t.Fatalf("列出导入后的会话失败: %v", err)
	}
	found := false
	for _, preview := range previews {
		if preview != nil && preview.ID == sourceSessionID {
			found = true
		}
	}
	if !found {
		t.Fatalf("导入的会话应出现在 %s 的会话列表中，实际 %d 条", sourceUser, len(previews))
	}
}

func TestImportChatSessionRefusesToOverwriteExistingSession(t *testing.T) {
	ctx := context.Background()
	sourceDir := t.TempDir()
	sourceManager, sourceSessionID, sourceUser := importTestSeedSource(t, sourceDir, importTestConversation(importTestMessageCount))
	exportPath := importTestExportFull(t, sourceManager, sourceDir, sourceSessionID)

	target := importTestTarget(t, t.TempDir())
	if _, err := importChatSessionFromExportFile(ctx, target, importRequest{Path: exportPath}); err != nil {
		t.Fatalf("首次导入应成功: %v", err)
	}

	_, err := importChatSessionFromExportFile(ctx, target, importRequest{Path: exportPath})
	if err == nil {
		t.Fatal("同 ID 二次导入必须失败，不能静默覆盖")
	}
	if !errors.Is(err, errImportSessionExists) {
		t.Fatalf("应返回 errImportSessionExists，实际 %v", err)
	}

	// 原会话必须原封不动：既不能被覆盖，也不能被追加。
	loaded, err := target.SessionManager.Get(ctx, sourceSessionID)
	if err != nil {
		t.Fatalf("加载原会话失败: %v", err)
	}
	if loaded.CanonicalMessageCount != importTestMessageCount {
		t.Fatalf("拒绝导入后原会话消息数应保持 %d，实际 %d", importTestMessageCount, loaded.CanonicalMessageCount)
	}
	if len(importTestHistory(t, target.SessionManager, sourceSessionID)) != importTestMessageCount {
		t.Fatal("拒绝导入后原会话历史不应发生变化")
	}
	previews, err := target.SessionManager.ListPreviews(ctx, sourceUser, 50, 0)
	if err != nil {
		t.Fatalf("列出会话失败: %v", err)
	}
	if len(previews) != 1 {
		t.Fatalf("被拒绝的导入不应产生新会话，实际 %d 条", len(previews))
	}
}

func TestImportChatSessionNewIDKeepsBothSessions(t *testing.T) {
	ctx := context.Background()
	sourceDir := t.TempDir()
	sourceManager, sourceSessionID, sourceUser := importTestSeedSource(t, sourceDir, importTestConversation(importTestMessageCount))
	exportPath := importTestExportFull(t, sourceManager, sourceDir, sourceSessionID)

	target := importTestTarget(t, t.TempDir())
	if _, err := importChatSessionFromExportFile(ctx, target, importRequest{Path: exportPath}); err != nil {
		t.Fatalf("首次导入应成功: %v", err)
	}
	second, err := importChatSessionFromExportFile(ctx, target, importRequest{Path: exportPath, NewID: true})
	if err != nil {
		t.Fatalf("--new-id 导入应成功: %v", err)
	}
	if second.SessionID == sourceSessionID || second.SessionID == "" {
		t.Fatalf("--new-id 应生成新 ID，实际 %s", second.SessionID)
	}
	if !second.Renamed || second.SourceSessionID != sourceSessionID {
		t.Fatalf("应标记改名并保留源 ID，实际 renamed=%v source=%s", second.Renamed, second.SourceSessionID)
	}

	renamed, err := target.SessionManager.Get(ctx, second.SessionID)
	if err != nil {
		t.Fatalf("加载改名后的会话失败: %v", err)
	}
	if got := importTestMetadataContext(t, renamed, importContextKeyOriginalSessionID); got != sourceSessionID {
		t.Fatalf("改名导入应记录原会话 ID %s，实际 %q", sourceSessionID, got)
	}
	if len(importTestHistory(t, target.SessionManager, second.SessionID)) != importTestMessageCount {
		t.Fatal("改名导入的消息条数应与导出文件一致")
	}

	// 两份会话并存，且互相独立。
	previews, err := target.SessionManager.ListPreviews(ctx, sourceUser, 50, 0)
	if err != nil {
		t.Fatalf("列出会话失败: %v", err)
	}
	ids := make(map[string]bool, len(previews))
	for _, preview := range previews {
		if preview != nil {
			ids[preview.ID] = true
		}
	}
	if !ids[sourceSessionID] || !ids[second.SessionID] {
		t.Fatalf("原会话与新会话都应存在，实际 %v", ids)
	}

	// 再来一次：每次都应拿到互不相同的 ID，不能撞车覆盖。
	third, err := importChatSessionFromExportFile(ctx, target, importRequest{Path: exportPath, NewID: true})
	if err != nil {
		t.Fatalf("第三次导入应成功: %v", err)
	}
	if third.SessionID == second.SessionID || third.SessionID == sourceSessionID {
		t.Fatalf("新生成的 ID 不能与已有会话重复，实际 %s", third.SessionID)
	}
}

func TestImportChatSessionUserOverride(t *testing.T) {
	ctx := context.Background()
	sourceDir := t.TempDir()
	sourceManager, sourceSessionID, sourceUser := importTestSeedSource(t, sourceDir, importTestConversation(3))
	exportPath := importTestExportFull(t, sourceManager, sourceDir, sourceSessionID)

	target := importTestTarget(t, t.TempDir())
	const overrideUser = "bob"
	result, err := importChatSessionFromExportFile(ctx, target, importRequest{Path: exportPath, UserID: overrideUser})
	if err != nil {
		t.Fatalf("带 --user 导入应成功: %v", err)
	}
	if result.UserID != overrideUser || !result.UserOverridden {
		t.Fatalf("应记录用户覆盖，实际 user=%s overridden=%v", result.UserID, result.UserOverridden)
	}
	loaded, err := target.SessionManager.Get(ctx, sourceSessionID)
	if err != nil {
		t.Fatalf("加载导入会话失败: %v", err)
	}
	if loaded.UserID != overrideUser {
		t.Fatalf("会话归属应为 %s，实际 %s", overrideUser, loaded.UserID)
	}

	previews, err := target.SessionManager.ListPreviews(ctx, overrideUser, 50, 0)
	if err != nil {
		t.Fatalf("列出 %s 的会话失败: %v", overrideUser, err)
	}
	found := false
	for _, preview := range previews {
		if preview != nil && preview.ID == sourceSessionID {
			found = true
		}
	}
	if !found {
		t.Fatalf("会话应归入 %s", overrideUser)
	}
	if sourceUser != overrideUser {
		other, err := target.SessionManager.ListPreviews(ctx, sourceUser, 50, 0)
		if err != nil {
			t.Fatalf("列出 %s 的会话失败: %v", sourceUser, err)
		}
		if len(other) != 0 {
			t.Fatalf("覆盖用户后原用户不应再看到该会话，实际 %d 条", len(other))
		}
	}
}

func TestImportChatSessionDryRunWritesNothing(t *testing.T) {
	ctx := context.Background()
	sourceDir := t.TempDir()
	sourceManager, sourceSessionID, _ := importTestSeedSource(t, sourceDir, importTestConversation(3))
	exportPath := importTestExportFull(t, sourceManager, sourceDir, sourceSessionID)

	target := importTestTarget(t, t.TempDir())
	result, err := importChatSessionFromExportFile(ctx, target, importRequest{Path: exportPath, DryRun: true})
	if err != nil {
		t.Fatalf("--dry-run 应通过校验: %v", err)
	}
	if !result.DryRun || result.SessionID != sourceSessionID || result.MessageCount != 3 {
		t.Fatalf("预检摘要不正确: %+v", result)
	}
	if _, err := target.SessionManager.GetStorage().Load(ctx, sourceSessionID); !errors.Is(err, runtimechat.ErrSessionNotFound) {
		t.Fatalf("--dry-run 不应写入会话，实际 err=%v", err)
	}
	previews, err := target.SessionManager.ListPreviews(ctx, target.SessionUserID, 50, 0)
	if err != nil {
		t.Fatalf("列出会话失败: %v", err)
	}
	if len(previews) != 0 {
		t.Fatalf("--dry-run 不应产生会话，实际 %d 条", len(previews))
	}
}

func TestReadChatSessionExportFileValidatesEnvelope(t *testing.T) {
	dir := t.TempDir()
	writeFile := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("写入 %s 失败: %v", name, err)
		}
		return path
	}

	if _, err := readChatSessionExportFile(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("不存在的文件应报错")
	}
	// --body/--tools/--trace 是 Markdown 投影，不含 session，不能导入。
	if _, err := readChatSessionExportFile(writeFile("body.md", "# 会话\n\n- user: hi\n")); err == nil {
		t.Fatal("Markdown 投影不应被当作可导入文件")
	}
	if _, err := readChatSessionExportFile(writeFile("v2.json", `{"version":2,"session":{"id":"s1"}}`)); err == nil || !strings.Contains(err.Error(), "版本") {
		t.Fatalf("不支持的版本应报错，实际 %v", err)
	}
	if _, err := readChatSessionExportFile(writeFile("no-session.json", `{"version":1,"stats":{"message_count":1}}`)); err == nil || !strings.Contains(err.Error(), "session") {
		t.Fatalf("缺少 session 字段应报错，实际 %v", err)
	}
}

func TestImportChatSessionGeneratesIDWhenFileHasNoSessionID(t *testing.T) {
	ctx := context.Background()
	session := runtimechat.NewSession("tester")
	session.ID = ""
	session.History = importTestConversation(3)
	envelope := chatSessionExportEnvelope{
		Version:    1,
		ExportedAt: time.Now().UTC(),
		Format:     "full",
		Session:    session,
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("编码测试导出文件失败: %v", err)
	}
	path := filepath.Join(t.TempDir(), "no-session-id.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("写入测试导出文件失败: %v", err)
	}

	target := importTestTarget(t, t.TempDir())
	if _, err := importChatSessionFromExportFile(ctx, target, importRequest{Path: path}); !errors.Is(err, errImportUsage) {
		t.Fatalf("缺 ID 且未授权时应按参数错误拒绝，实际 %v", err)
	}

	result, err := importChatSessionFromExportFile(ctx, target, importRequest{Path: path, NewID: true})
	if err != nil {
		t.Fatalf("--new-id 应允许导入缺 ID 的文件: %v", err)
	}
	if !result.Renamed || result.SessionID == "" || result.SourceSessionID != "" {
		t.Fatalf("缺 ID 文件应生成新 ID，实际 %+v", result)
	}
	history := importTestHistory(t, target.SessionManager, result.SessionID)
	if len(history) != 3 {
		t.Fatalf("应导入 3 条消息，实际 %d 条", len(history))
	}
	for index := range history {
		if history[index].Metadata.GetString(runtimetypes.MetadataKeyMessageID, "") == "" {
			t.Fatalf("第 %d 条消息应补齐 message_id", index)
		}
	}
	if len(result.Warnings) == 0 {
		t.Fatal("缺 ID 且补身份时应在摘要里给出提示")
	}
}

func TestImportChatSessionPreservesNonActiveState(t *testing.T) {
	ctx := context.Background()
	session := runtimechat.NewSession("tester")
	session.ID = "session-archived-import"
	session.State = runtimechat.StateArchived
	session.History = importTestConversation(3)
	envelope := chatSessionExportEnvelope{
		Version:    1,
		ExportedAt: time.Now().UTC(),
		Format:     "full",
		Session:    session,
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("编码测试导出文件失败: %v", err)
	}
	path := filepath.Join(t.TempDir(), "archived.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("写入测试导出文件失败: %v", err)
	}

	target := importTestTarget(t, t.TempDir())
	result, err := importChatSessionFromExportFile(ctx, target, importRequest{Path: path})
	if err != nil {
		t.Fatalf("导入 archived 会话应成功: %v", err)
	}
	if result.State != string(runtimechat.StateArchived) {
		t.Fatalf("状态应保留 archived，实际 %s", result.State)
	}
	loaded, err := target.SessionManager.Get(ctx, session.ID)
	if err != nil {
		t.Fatalf("加载导入会话失败: %v", err)
	}
	if loaded.State != runtimechat.StateArchived {
		t.Fatalf("落库状态应为 archived，实际 %s", loaded.State)
	}
	hasStateWarning := false
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "archived") {
			hasStateWarning = true
		}
	}
	if !hasStateWarning {
		t.Fatalf("非 active 状态应在摘要里提示，实际 %v", result.Warnings)
	}
}
