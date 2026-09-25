package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
)

// tokenStoreEnvVar 允许测试/高级用户覆盖令牌文件位置。
const tokenStoreEnvVar = "AICLI_MCP_TOKENS_FILE"

// storeVersion 是令牌文件的结构版本。
const storeVersion = 1

// Token 是单个 MCP server 的 OAuth 令牌记录。
type Token struct {
	ServerName   string    `json:"serverName"`
	ServerURL    string    `json:"serverUrl"`
	Resource     string    `json:"resource,omitempty"`
	AuthServer   string    `json:"authorizationServer,omitempty"`
	ClientID     string    `json:"clientId,omitempty"`
	ClientSecret string    `json:"clientSecret,omitempty"`
	RedirectURI  string    `json:"redirectUri,omitempty"`
	AccessToken  string    `json:"accessToken,omitempty"`
	RefreshToken string    `json:"refreshToken,omitempty"`
	TokenType    string    `json:"tokenType,omitempty"`
	Scope        string    `json:"scope,omitempty"`
	ExpiresAt    time.Time `json:"expiresAt,omitempty"`
	ObtainedAt   time.Time `json:"obtainedAt,omitempty"`
}

// Expired 判断令牌是否已过期（skew 为提前判定窗口）。
func (t *Token) Expired(now time.Time, skew time.Duration) bool {
	if t == nil {
		return true
	}
	if t.ExpiresAt.IsZero() {
		// 无过期信息：视为仍有效，由 401 触发强制刷新。
		return false
	}
	return !now.Before(t.ExpiresAt.Add(-skew))
}

// storeData 是令牌文件 JSON 结构。
type storeData struct {
	Version int               `json:"version"`
	Servers map[string]*Token `json:"servers"`
}

// TokenStore 以 JSON 文件持久化令牌；并发安全，写入采用「临时文件 + 原子重命名」。
type TokenStore struct {
	path string

	mu   sync.Mutex
	data storeData
}

// DefaultTokenStorePath 返回默认令牌文件路径（~/.aicli/mcp-tokens.json）。
func DefaultTokenStorePath() string {
	if override := strings.TrimSpace(os.Getenv(tokenStoreEnvVar)); override != "" {
		return override
	}
	return filepath.Join(aiclipaths.DefaultAICLIDir(), "mcp-tokens.json")
}

// NewTokenStore 打开（必要时创建）令牌存储。path 为空时使用默认路径。
func NewTokenStore(path string) (*TokenStore, error) {
	if strings.TrimSpace(path) == "" {
		path = DefaultTokenStorePath()
	}
	store := &TokenStore{
		path: path,
		data: storeData{Version: storeVersion, Servers: map[string]*Token{}},
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

// Path 返回令牌文件路径。
func (s *TokenStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *TokenStore) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = storeData{Version: storeVersion, Servers: map[string]*Token{}}

	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("读取 MCP 令牌文件失败 (%s): %w", s.path, err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	var data storeData
	if err := json.Unmarshal(raw, &data); err != nil {
		return fmt.Errorf("解析 MCP 令牌文件失败 (%s): %w", s.path, err)
	}
	if data.Servers == nil {
		data.Servers = map[string]*Token{}
	}
	if data.Version == 0 {
		data.Version = storeVersion
	}
	s.data = data
	return nil
}

// Get 读取某个 server 的令牌（返回副本）。
func (s *TokenStore) Get(serverName string) (*Token, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	token, ok := s.data.Servers[strings.TrimSpace(serverName)]
	if !ok || token == nil {
		return nil, false
	}
	clone := *token
	return &clone, true
}

// Put 写入（或覆盖）某个 server 的令牌并落盘。
func (s *TokenStore) Put(serverName string, token *Token) error {
	if s == nil {
		return fmt.Errorf("令牌存储不可用")
	}
	key := strings.TrimSpace(serverName)
	if key == "" {
		return fmt.Errorf("server 名不能为空")
	}
	if token == nil {
		return fmt.Errorf("令牌不能为空")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	clone := *token
	clone.ServerName = key
	s.data.Servers[key] = &clone
	return s.saveLocked()
}

// Delete 删除某个 server 的令牌；第二个返回值表示是否确实删除。
func (s *TokenStore) Delete(serverName string) (bool, error) {
	if s == nil {
		return false, fmt.Errorf("令牌存储不可用")
	}
	key := strings.TrimSpace(serverName)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Servers[key]; !ok {
		return false, nil
	}
	delete(s.data.Servers, key)
	return true, s.saveLocked()
}

// DeleteAll 清空全部令牌，返回删除条数。
func (s *TokenStore) DeleteAll() (int, error) {
	if s == nil {
		return 0, fmt.Errorf("令牌存储不可用")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	count := len(s.data.Servers)
	if count == 0 {
		return 0, nil
	}
	s.data.Servers = map[string]*Token{}
	return count, s.saveLocked()
}

// List 返回全部令牌副本，按 server 名排序。
func (s *TokenStore) List() []*Token {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Token, 0, len(s.data.Servers))
	for _, token := range s.data.Servers {
		if token == nil {
			continue
		}
		clone := *token
		out = append(out, &clone)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ServerName < out[j].ServerName })
	return out
}

// saveLocked 原子写盘：先写 0600 临时文件，再重命名覆盖。
// 必须持有 s.mu。
func (s *TokenStore) saveLocked() error {
	s.data.Version = storeVersion
	if s.data.Servers == nil {
		s.data.Servers = map[string]*Token{}
	}
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 MCP 令牌失败: %w", err)
	}
	raw = append(raw, '\n')

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建令牌目录失败 (%s): %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".mcp-tokens-*.tmp")
	if err != nil {
		return fmt.Errorf("创建令牌临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("设置令牌文件权限失败: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入令牌文件失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("刷盘令牌文件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭令牌临时文件失败: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("替换令牌文件失败: %w", err)
	}
	return nil
}

// SameServerURL 比较两个 server URL 是否指向同一目标（忽略尾部斜杠与大小写）。
func SameServerURL(a, b string) bool {
	normalize := func(in string) string {
		return strings.TrimRight(strings.TrimSpace(in), "/")
	}
	return strings.EqualFold(normalize(a), normalize(b))
}
