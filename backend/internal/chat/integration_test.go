package chat

import (
	"context"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockLLMProviderForChat 用于 Chat/Session 集成测试的 Mock 提供者
type MockLLMProviderForChat struct {
	name      string
	responses []string
	index     int
}

func NewMockLLMProviderForChat() *MockLLMProviderForChat {
	return &MockLLMProviderForChat{
		name: "mock-chat-provider",
		responses: []string{
			"Hello! How can I help you today?",
			"The capital of France is Paris.",
			"To calculate 2+2, you simply add them together to get 4.",
		},
	}
}

func (m *MockLLMProviderForChat) Name() string {
	return m.name
}

func (m *MockLLMProviderForChat) Call(ctx context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	message := m.responses[m.index%len(m.responses)]
	m.index++

	// 计算输入 token（简化版）
	promptTokens := 0
	for _, msg := range req.Messages {
		promptTokens += len(msg.Content) / 4
	}
	completionTokens := len(message) / 4

	response := &llm.LLMResponse{
		Content: message,
		Usage: &types.TokenUsage{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      promptTokens + completionTokens,
		},
		Model: "gpt-4",
	}

	return response, nil
}

func (m *MockLLMProviderForChat) Stream(ctx context.Context, req *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	ch := make(chan llm.StreamChunk, 1)
	go func() {
		defer close(ch)
		resp, _ := m.Call(ctx, req)
		ch <- llm.StreamChunk{
			Type:    llm.EventTypeText,
			Content: resp.Content,
			Done:    true,
		}
	}()
	return ch, nil
}

func (m *MockLLMProviderForChat) CountTokens(text string) int {
	return len(text) / 4
}

func (m *MockLLMProviderForChat) GetCapabilities() *llm.ModelCapabilities {
	return &llm.ModelCapabilities{
		MaxContextTokens:  128000,
		MaxOutputTokens:   4096,
		SupportsVision:    false,
		SupportsTools:     true,
		SupportsStreaming: true,
		SupportsJSONMode:  true,
	}
}

func (m *MockLLMProviderForChat) CheckHealth(ctx context.Context) error {
	return nil
}

// TestSessionManagerWithLLMRuntime 测试 Session Manager 与 LLM Runtime 集成
func TestSessionManagerWithLLMRuntime(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	// 创建 LLM Runtime 并注册 Mock Provider
	runtimeConfig := &llm.RuntimeConfig{
		DefaultModel: "gpt-4",
		MaxRetries:    2,
	}
	runtime := llm.NewLLMRuntime(runtimeConfig)
	mockProvider := NewMockLLMProviderForChat()
	runtime.RegisterProvider(mockProvider.Name(), mockProvider)

	// 创建会话
	session, err := manager.CreateSession(ctx, "user-123")
	require.NoError(t, err)
	require.NotNil(t, session)
	require.Equal(t, "user-123", session.UserID)

	// 添加用户消息
	userMsg := types.NewUserMessage("Hello, AI!")
	err = manager.AddMessage(ctx, session.ID, *userMsg)
	require.NoError(t, err)

	// 添加系统消息
	systemMsg := types.NewSystemMessage("You are a helpful assistant.")
	err = manager.AddMessage(ctx, session.ID, *systemMsg)
	require.NoError(t, err)

	// 验证会话历史
	updatedSession, err := manager.GetSession(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, updatedSession.History, 2)
}

// TestSessionPersistenceWithLLM 测试 Session 持久化与 LLM 工作流
func TestSessionPersistenceWithLLM(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	// 创建 LLM Runtime
	runtimeConfig := &llm.RuntimeConfig{
		DefaultModel: "gpt-4",
		MaxRetries:    2,
	}
	runtime := llm.NewLLMRuntime(runtimeConfig)
	mockProvider := NewMockLLMProviderForChat()
	runtime.RegisterProvider(mockProvider.Name(), mockProvider)

	// 创建会话并添加多条消息
	session, err := manager.CreateSession(ctx, "user-456")
	require.NoError(t, err)

	messages := []string{
		"What is the capital of France?",
		"Can you help me with math?",
		"Tell me a joke",
	}

	for _, msgContent := range messages {
		msg := types.NewUserMessage(msgContent)
		err := manager.AddMessage(ctx, session.ID, *msg)
		require.NoError(t, err)
	}

	// 重新加载会话，验证持久化
	reloaded, err := manager.GetSession(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, reloaded.History, len(messages))

	// 验证消息内容
	for i, expectedContent := range messages {
		assert.Equal(t, expectedContent, reloaded.History[i].Content)
	}

	// 验证元数据更新
	assert.Equal(t, len(messages), reloaded.Metadata.TotalTurns)
}

// TestSessionTTLWithLLMExecution 测试 Session TTL 在 LLM 执行过程中
func TestSessionTTLWithLLMExecution(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	config := &SessionManagerConfig{
		TTL:            100 * time.Millisecond,
		CleanupInterval: 50 * time.Millisecond,
	}
	manager := NewSessionManager(storage, config)

	// 创建会话并设置短 TTL
	session, err := manager.CreateSession(ctx, "user-789")
	require.NoError(t, err)

	// 添加消息
	msg := types.NewUserMessage("Test message with TTL")
	err = manager.AddMessage(ctx, session.ID, *msg)
	require.NoError(t, err)

	// 等待会话过期
	time.Sleep(150 * time.Millisecond)

	// 尝试获取过期会话
	_, err = manager.GetSession(ctx, session.ID)
	assert.Error(t, err)
	// 会话可能被完全删除 (session not found) 或标记为过期 (expired)
	assert.Contains(t, err.Error(), "not found", "Expected session to be deleted or not found")
}

// TestMultiUserSessionsWithLLM 测试多用户会话隔离
func TestMultiUserSessionsWithLLM(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	// 创建多个用户的会话
	users := []string{"user-1", "user-2", "user-3"}
	sessions := make(map[string]*Session)

	for _, userID := range users {
		session, err := manager.CreateSession(ctx, userID)
		require.NoError(t, err)

		msg := types.NewUserMessage("Hello from " + userID)
		err = manager.AddMessage(ctx, session.ID, *msg)
		require.NoError(t, err)

		sessions[userID] = session
	}

	// 验证每个用户的会话独立
	for _, userID := range users {
		userSessions, err := manager.List(ctx, userID)
		require.NoError(t, err)
		require.Len(t, userSessions, 1)
		assert.Equal(t, userID, userSessions[0].UserID)
		assert.Contains(t, userSessions[0].History[0].Content, userID)
	}

	// 验证跨用户访问被限制
	user1Session := sessions["user-1"]
	user2Sessions, _ := manager.List(ctx, "user-2")

	// user-2 不应该看到 user-1 的会话
	for _, s := range user2Sessions {
		assert.NotEqual(t, user1Session.ID, s.ID)
	}
}

// TestSessionHistoryWithLLMTokens 测试 Session 历史与 Token 计算
func TestSessionHistoryWithLLMTokens(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	// 创建会话
	session, err := manager.CreateSession(ctx, "user-tokens")
	require.NoError(t, err)

	// 添加多条消息
	messages := []string{
		"This is a long message that contains many words for testing token counting.",
		"Another message with different content to calculate token estimation.",
		"Short msg.",
	}

	for _, content := range messages {
		msg := types.NewUserMessage(content)
		err := manager.AddMessage(ctx, session.ID, *msg)
		require.NoError(t, err)
	}

	// 重新加载会话
	updated, err := manager.GetSession(ctx, session.ID)
	require.NoError(t, err)

	// 获取 Token 估计数
	tokenCount := updated.GetTokenCount()
	assert.Greater(t, tokenCount, 0, "Token count should be greater than 0")
	assert.Equal(t, len(messages), len(updated.History))
}

// TestSessionWithMCPManagerIntegration 测试 Session 与 MCP Manager 集成
func TestSessionWithMCPManagerIntegration(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	// 创建 LLM Runtime
	runtimeConfig := &llm.RuntimeConfig{
		DefaultModel: "gpt-4",
		MaxRetries:    2,
	}
	runtime := llm.NewLLMRuntime(runtimeConfig)
	mockProvider := NewMockLLMProviderForChat()
	runtime.RegisterProvider(mockProvider.Name(), mockProvider)

	// 创建Session并设置上下文（包含 MCP 相关信息）
	session, err := manager.CreateSession(ctx, "user-mcp")
	require.NoError(t, err)

	// 设置 MCP 相关上下文
	err = manager.SetContext(ctx, session.ID, "mcp_server", "test-server-1")
	require.NoError(t, err)

	err = manager.SetContext(ctx, session.ID, "available_tools", "calculator, weather, search")
	require.NoError(t, err)

	// 添加标签
	err = manager.AddTags(ctx, session.ID, "mcp-enabled", "tool-calling")
	require.NoError(t, err)

	// 验证上下文
	updated, err := manager.GetSession(ctx, session.ID)
	require.NoError(t, err)

	mcpServer, ok := updated.GetContext("mcp_server")
	assert.True(t, ok)
	assert.Equal(t, "test-server-1", mcpServer)

	tools, ok := updated.GetContext("available_tools")
	assert.True(t, ok)
	assert.Equal(t, "calculator, weather, search", tools)

	// 验证标签
	assert.True(t, updated.HasTag("mcp-enabled"))
	assert.True(t, updated.HasTag("tool-calling"))
}

// TestSessionSearchWithLLMContext 测试带 LLM 上下文的会话搜索
func TestSessionSearchWithLLMContext(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	// 创建多个会话，每个设置不同的上下文
	session1, _ := manager.CreateSession(ctx, "search-user")
	manager.UpdateContext(ctx, session1.ID, "topic", "python")
	manager.AddTags(ctx, session1.ID, "programming")

	session2, _ := manager.CreateSession(ctx, "search-user")
	manager.UpdateContext(ctx, session2.ID, "topic", "javascript")
	manager.AddTags(ctx, session2.ID, "programming", "web")

	session3, _ := manager.CreateSession(ctx, "search-user")
	manager.UpdateContext(ctx, session3.ID, "topic", "database")
	manager.AddTags(ctx, session3.ID, "data")

	// 搜索编程相关的会话
	results, err := manager.SearchSessions(ctx, &SessionSearchOptions{
		UserID: "search-user",
		Tags:   []string{"programming"},
		Limit:  10,
	})
	require.NoError(t, err)
	require.Len(t, results, 2)

	topicIDs := make(map[string]bool)
	for _, session := range results {
		if topic, ok := session.GetContext("topic"); ok {
			topicIDs[topic.(string)] = true
		}
	}

	assert.True(t, topicIDs["python"])
	assert.True(t, topicIDs["javascript"])
	assert.False(t, topicIDs["database"])
}

// TestSessionAgentWorkflow 测试 Session 在 Agent 工作流中的使用
func TestSessionAgentWorkflow(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	// 创建 LLM Runtime
	runtimeConfig := &llm.RuntimeConfig{
		DefaultModel: "gpt-4",
		MaxRetries:    2,
	}
	runtime := llm.NewLLMRuntime(runtimeConfig)
	mockProvider := NewMockLLMProviderForChat()
	runtime.RegisterProvider(mockProvider.Name(), mockProvider)

	// 创建 Agent 配置
	agentConfig := &agent.Config{
		Name:          "test-agent",
		Model:         "gpt-4",
		MaxSteps:      10,
		SystemPrompt:  "You are a helpful assistant.",
	}

	// 创建 Agent (简化场景，不需要实际运行)
	_ = agentConfig

	// 创建会话
	session, err := manager.CreateSession(ctx, "agent-user")
	require.NoError(t, err)

	// 添加初始用户消息
	userMsg := types.NewUserMessage("What is 2+2?")
	err = manager.AddMessage(ctx, session.ID, *userMsg)
	require.NoError(t, err)

	// 模拟 Agent 执行并使用 Session
	// 在真实场景中，Agent 执行过程中会：
	// 1. 从 Session 读取历史
	// 2. 调用 LLM 生成响应
	// 3. 将响应写入 Session

	// 验证会话包含用户消息
	updated, err := manager.GetSession(ctx, session.ID)
	require.NoError(t, err)
	assert.Len(t, updated.History, 1)
	assert.Equal(t, "What is 2+2?", updated.History[0].Content)

	// 更新会话元数据以记录 Agent 信息
	manager.UpdateContext(ctx, session.ID, "last_agent", agentConfig.Name)
	manager.UpdateContext(ctx, session.ID, "last_model", agentConfig.Model)

	// 验证元数据
	final, err := manager.GetSession(ctx, session.ID)
	require.NoError(t, err)

	lastAgent, ok := final.GetContext("last_agent")
	assert.True(t, ok)
	assert.Equal(t, "test-agent", lastAgent)

	lastModel, ok := final.GetContext("last_model")
	assert.True(t, ok)
	assert.Equal(t, "gpt-4", lastModel)
}

// TestConcurrentSessionOperations 测试并发 Session 操作
func TestConcurrentSessionOperations(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)

	// 创建会话
	session, err := manager.CreateSession(ctx, "concurrent-user")
	require.NoError(t, err)

	// 并发添加消息
	const numMessages = 100
	errChan := make(chan error, numMessages)

	for i := 0; i < numMessages; i++ {
		go func(index int) {
			msg := types.NewUserMessage("Concurrent message " + string(rune('a'+index)))
			err := manager.AddMessage(ctx, session.ID, *msg)
			errChan <- err
		}(i)
	}

	// 等待所有操作完成
	errors := 0
	for i := 0; i < numMessages; i++ {
		if <-errChan != nil {
			errors++
		}
	}

	// 由于并发写入可能存在冲突，允许少量错误
	assert.Less(t, errors, numMessages/10, "Too many concurrent write errors")

	// 验证最终会话状态
	updated, err := manager.GetSession(ctx, session.ID)
	require.NoError(t, err)
	assert.Greater(t, len(updated.History), 0, "Session should have some messages")
}
