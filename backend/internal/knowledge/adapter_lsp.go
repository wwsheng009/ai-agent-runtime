package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	knowledgelsp "github.com/wwsheng009/ai-agent-runtime/internal/knowledge/lsp"
	baselsp "github.com/wwsheng009/ai-agent-runtime/internal/lsp"
)

// ErrSemanticUnavailable 表示语义通道不可用（未启用、未启动或已崩溃）。
// 调用方必须降级到索引/启发式路径，不得把错误抛给 agent 循环。
var ErrSemanticUnavailable = errors.New("knowledge: semantic adapter unavailable")

// lspSemanticAdapter 把进程外 LSP 包装成 SemanticAdapter（04 §5 Phase 4 交付 2/3）。
//
// 边界（ADR-0006 §4.4）：本类型内部使用 internal/lsp 的 canonical 转换；
// 对外的入参与出参一律是 canonical（0-based 行 + UTF-8 字节列），
// 协议编码（utf-16 等）绝不出现在返回结构里。
type lspSemanticAdapter struct {
	manager *knowledgelsp.Manager
	root    string
	version string
}

// NewLSPSemanticAdapter 构造语义适配器；manager 为 nil 时 Available() 恒 false。
func NewLSPSemanticAdapter(manager *knowledgelsp.Manager, root string) SemanticAdapter {
	version := "lsp/unversioned"
	if manager != nil {
		version = "lsp/" + strings.TrimSpace(manager.LanguageServerName())
	}
	// root 归一化为绝对路径：相对 root 会让 filepath.Rel 失败，位置路径退化为
	// 绝对路径（live 实测暴露）。这与 lsp.Manager 的锁路径归一化同口径。
	abs := strings.TrimSpace(root)
	if resolved, err := filepath.Abs(abs); err == nil {
		abs = resolved
	}
	return &lspSemanticAdapter{manager: manager, root: abs, version: version}
}

// Name 实现 SemanticAdapter。
func (a *lspSemanticAdapter) Name() RefSource { return SourceLSP }

// Version 实现 SemanticAdapter（参与缓存键与 staleness 判定）。
func (a *lspSemanticAdapter) Version() string { return a.version }

// Available 实现 SemanticAdapter。
func (a *lspSemanticAdapter) Available() bool {
	return a != nil && a.manager != nil && a.manager.Available()
}

// Close 关闭底层语义通道（释放锁文件与子进程）；未启动时是空操作。
// 不在 SemanticAdapter 接口里：语义查询面不需要生命周期方法，持有者
// （进程级缓存 / 组装层）通过类型断言调用。
func (a *lspSemanticAdapter) Close(ctx context.Context) error {
	if a == nil || a.manager == nil {
		return nil
	}
	return a.manager.Close(ctx)
}

// Definition 实现 SemanticAdapter。
func (a *lspSemanticAdapter) Definition(ctx context.Context, file string, line, col int) ([]SemanticLocation, error) {
	return a.query(ctx, "textDocument/definition", file, line, col)
}

// References 实现 SemanticAdapter。
//
// kind 统一为 RefReference：LSP 不区分调用/读写，调用点筛选仍由上层
// （code_callers 的启发式或后续 kind 推断）完成，不在这里假装精确。
func (a *lspSemanticAdapter) References(ctx context.Context, file string, line, col int) ([]SemanticLocation, error) {
	return a.query(ctx, "textDocument/references", file, line, col)
}

func (a *lspSemanticAdapter) query(ctx context.Context, method, file string, line, col int) ([]SemanticLocation, error) {
	if !a.Available() {
		// 首次查询惰性启动（受 startup_timeout 约束）；失败即降级。
		if a == nil || a.manager == nil {
			return nil, ErrSemanticUnavailable
		}
		// 已降级（崩溃 / 启动失败 / 锁被占）：立即返回，不再尝试重启——
		// 验收门槛要求崩溃后查询"立即降级不阻断"，且逐查询重启会造成重启风暴。
		// 重建由显式 Ensure（生命周期钩子）负责。
		if reason := strings.TrimSpace(a.manager.DegradeReason()); reason != "" {
			return nil, fmt.Errorf("%w: %s", ErrSemanticUnavailable, reason)
		}
		if err := a.manager.Ensure(ctx); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrSemanticUnavailable, err)
		}
	}
	abs := file
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(a.root, filepath.FromSlash(file))
	}
	content, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	doc := baselsp.NewDocument(baselsp.PathToURI(abs), abs, "", content)

	client := a.manager.Client()
	if client == nil {
		return nil, ErrSemanticUnavailable
	}
	reqCtx, cancel := context.WithTimeout(ctx, a.manager.RequestTimeout())
	defer cancel()
	if _, err := client.OpenOrUpdate(reqCtx, abs, content); err != nil {
		return nil, err
	}
	params := map[string]any{
		"textDocument": map[string]any{"uri": baselsp.PathToURI(abs)},
		"position":     doc.Position(client.Encoding(), baselsp.CanonicalPos{Line: line, Column: col}),
	}
	if method == "textDocument/references" {
		params["context"] = map[string]any{"includeDeclaration": true}
	}
	raw, err := client.Call(reqCtx, method, params)
	if err != nil {
		return nil, err
	}
	return a.decodeLocations(raw, client.Encoding())
}

// lspLocation 同时容纳 Location 与 LocationLink 两种返回形状。
type lspLocation struct {
	URI      string   `json:"uri"`
	Range    lspRange `json:"range"`
	TargetID string   `json:"targetUri"`
	Target   lspRange `json:"targetSelectionRange"`
	Full     lspRange `json:"targetRange"`
}

type lspRange struct {
	Start lspPosition `json:"start"`
	End   lspPosition `json:"end"`
}

type lspPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

func (a *lspSemanticAdapter) decodeLocations(raw json.RawMessage, enc baselsp.PositionEncoding) ([]SemanticLocation, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	var locations []lspLocation
	if err := json.Unmarshal(raw, &locations); err != nil {
		// 单对象返回（部分服务器）也接受。
		var single lspLocation
		if err2 := json.Unmarshal(raw, &single); err2 != nil {
			return nil, err
		}
		locations = []lspLocation{single}
	}
	out := make([]SemanticLocation, 0, len(locations))
	cache := map[string][]byte{}
	for _, loc := range locations {
		uri := loc.URI
		if uri == "" {
			uri = loc.TargetID
		}
		if uri == "" {
			continue
		}
		rng := loc.Range
		if loc.URI == "" {
			// LocationLink 形状：优先 targetSelectionRange，缺失时退 targetRange。
			rng = loc.Target
			if rng.Start.Line == 0 && rng.Start.Character == 0 && rng.End.Line == 0 && rng.End.Character == 0 {
				rng = loc.Full
			}
		}
		path := baselsp.URIToPath(uri)
		if path == "" {
			continue
		}
		content, ok := cache[path]
		if !ok {
			read, err := os.ReadFile(path)
			if err != nil {
				continue // 目标不可读：跳过该条，不让整次查询失败。
			}
			content = read
			cache[path] = read
		}
		doc := baselsp.NewDocument(uri, path, "", content)
		pos := doc.Canonical(enc, baselsp.ProtocolPosition{Line: rng.Start.Line, Character: rng.Start.Character})
		out = append(out, SemanticLocation{
			Path:       a.relative(path),
			Line:       pos.Line,
			Col:        pos.Column,
			Kind:       RefReference,
			Snippet:    snippetAt(content, pos),
			Confidence: ConfidenceSemantic,
			Source:     SourceLSP,
		})
	}
	return out, nil
}

func (a *lspSemanticAdapter) relative(path string) string {
	if a.root == "" {
		return normalizeRelPath(path)
	}
	rel, err := filepath.Rel(a.root, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return normalizeRelPath(path)
	}
	return normalizeRelPath(rel)
}

// snippetAt 取 canonical 位置所在行（截断到 200 字符）。
func snippetAt(content []byte, pos baselsp.CanonicalPos) string {
	line := string(baselsp.LineBytes(content, pos.Line))
	line = strings.TrimSpace(line)
	if len(line) > 200 {
		line = line[:200]
	}
	return line
}
