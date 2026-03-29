package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// OpenAIProvider OpenAI Embeddings 提供者
type OpenAIProvider struct {
	apiKey     string
	model      string // e.g., "text-embedding-3-small", "text-embedding-3-large"
	baseURL    string
	dim        int
	httpClient *http.Client
}

// OpenAIEmbeddingRequest OpenAI Embedding 请求
type OpenAIEmbeddingRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

// OpenAIEmbeddingResponse OpenAI Embedding 响应
type OpenAIEmbeddingResponse struct {
	Object string    `json:"object"`
	Data   []struct {
		Object    string    `json:"object"`
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Model string `json:"model"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

// NewOpenAIProvider 创建 OpenAI Embedding 提供者
func NewOpenAIProvider(apiKey, model string) (*OpenAIProvider, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("API key is required")
	}

	if model == "" {
		model = "text-embedding-3-small"
	}

	dim := 1536 // text-embedding-3-small 默认维度
	if model == "text-embedding-3-large" {
		dim = 3072
	}

	return &OpenAIProvider{
		apiKey:  apiKey,
		model:   model,
		baseURL: "https://api.openai.com/v1/embeddings",
		dim:     dim,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}, nil
}

// SetBaseURL 设置自定义 Base URL
func (p *OpenAIProvider) SetBaseURL(baseURL string) {
	p.baseURL = baseURL
}

// SetTimeout 设置请求超时
func (p *OpenAIProvider) SetTimeout(timeout time.Duration) {
	p.httpClient.Timeout = timeout
}

// Generate 生成文本的嵌入向量
func (p *OpenAIProvider) Generate(text string) (*Embedding, error) {
	if text == "" {
		return nil, fmt.Errorf("text cannot be empty")
	}

	// 构建请求
	reqBody := OpenAIEmbeddingRequest{
		Model: p.model,
		Input: text,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// 创建 HTTP 请求
	req, err := http.NewRequest("POST", p.baseURL, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	// 发送请求
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// 读取响应
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	// 解析响应
	var openaiResp OpenAIEmbeddingResponse
	if err := json.Unmarshal(body, &openaiResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if len(openaiResp.Data) == 0 {
		return nil, fmt.Errorf("no embeddings returned")
	}

	// 创建嵌入向量
	embedding := &Embedding{
		Vector: openaiResp.Data[0].Embedding,
	}

	// 归一化
	embedding.Normalize()

	return embedding, nil
}

// GenerateWithContext 在上下文中生成嵌入向量
func (p *OpenAIProvider) GenerateWithContext(ctx context.Context, text string) (*Embedding, error) {
	if text == "" {
		return nil, fmt.Errorf("text cannot be empty")
	}

	// 构建请求
	reqBody := OpenAIEmbeddingRequest{
		Model: p.model,
		Input: text,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// 创建 HTTP 请求
	req, err := http.NewRequestWithContext(ctx, "POST", p.baseURL, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	// 发送请求
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// 检查上下文是否已取消
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// 读取响应
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	// 解析响应
	var openaiResp OpenAIEmbeddingResponse
	if err := json.Unmarshal(body, &openaiResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if len(openaiResp.Data) == 0 {
		return nil, fmt.Errorf("no embeddings returned")
	}

	// 创建嵌入向量
	embedding := &Embedding{
		Vector: openaiResp.Data[0].Embedding,
	}

	// 归一化
	embedding.Normalize()

	return embedding, nil
}

// GenerateBatch 批量生成嵌入向量
func (p *OpenAIProvider) GenerateBatch(texts []string) ([]*Embedding, error) {
	if len(texts) == 0 {
		return []*Embedding{}, nil
	}

	embeddings := make([]*Embedding, len(texts))

	// 对于批量请求，可以使用 OpenAI 的批量 API 或并发请求
	// 这里实现一种简化版本：并发请求
	type result struct {
		index    int
		embedding *Embedding
		err      error
	}

	resultChan := make(chan result, len(texts))

	// 启动并发 goroutine
	for i, text := range texts {
		go func(idx int, txt string) {
			emb, err := p.Generate(txt)
			resultChan <- result{
				index:     idx,
				embedding: emb,
				err:       err,
			}
		}(i, text)
	}

	// 收集结果
	for i := 0; i < len(texts); i++ {
		res := <-resultChan
		if res.err != nil {
			return nil, fmt.Errorf("failed to generate embedding for text %d: %w", res.index, res.err)
		}
		embeddings[res.index] = res.embedding
	}

	return embeddings, nil
}

// GetDimension 获取向量维度
func (p *OpenAIProvider) GetDimension() int {
	return p.dim
}

// GetModel 获取模型名称
func (p *OpenAIProvider) GetModel() string {
	return p.model
}

// CheckHealth 检查提供者健康状况
func (p *OpenAIProvider) CheckHealth(ctx context.Context) error {
	// 发送一个简单的测试请求
	testText := "health check"

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	_, err := p.GenerateWithContext(ctx, testText)
	if err != nil {
		return fmt.Errorf("OpenAI provider health check failed: %w", err)
	}

	return nil
}

// OpenAIProviderConfig OpenAI 提供者配置
type OpenAIProviderConfig struct {
	APIKey  string `json:"apiKey" yaml:"apiKey"`
	Model   string `json:"model" yaml:"model"`
	BaseURL string `json:"baseURL" yaml:"baseURL"`
	Timeout int    `json:"timeout" yaml:"timeout"` // 超时时间（秒）
}

// NewOpenAIProviderFromConfig 从配置创建提供者
func NewOpenAIProviderFromConfig(config *OpenAIProviderConfig) (*OpenAIProvider, error) {
	if config == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	provider, err := NewOpenAIProvider(config.APIKey, config.Model)
	if err != nil {
		return nil, err
	}

	if config.BaseURL != "" {
		provider.SetBaseURL(config.BaseURL)
	}

	if config.Timeout > 0 {
		provider.SetTimeout(time.Duration(config.Timeout) * time.Second)
	}

	return provider, nil
}
