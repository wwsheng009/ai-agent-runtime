package knowledge

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// KnowledgeVersion 标识知识库的磁盘契约版本。
//
// 它是数据库文件名的一部分（见 02 技术规格），因此升级版本是让既有索引失效的
// 唯一受支持方式：旧文件不再被打开。
const KnowledgeVersion = 1

// AdapterVersion 参与 signature_hash 与 confidence（04 §4.4）：
// adapter 升级会改变 stable_key，从而触发 full_rebuild_on_adapter_change。
//
// builtin/2：stable_key 补上 namespace 分量（文件所在目录）。builtin/1 把它
// 传成空串，导致跨目录同名同签名符号（`func main() {` 之类）身份塌缩、
// 撞 symbols(workspace_id, stable_key) 唯一约束；升级版本号同时让 builtin/1
// 写出的旧索引自然失效，而不是留下半份错误的身份表。
// builtin/3：轻索引只覆盖顶层声明（04 §2 的 Lazy 原则）。builtin/2 会把函数内的
// `var output bytes.Buffer` 之类局部变量当成符号，同目录下同名同签名的局部变量
// 因此算出同一个 stable_key（实测 5080 行被合并 / 1992 个键），既污染轻索引，
// 又把"身份冲突"的噪声灌进 adapter_conflict 事件。
const AdapterVersion = "builtin/3"

// Confidence 表达一行的产生方式。它是稳定有序的枚举，调用方可单次比较完成过滤。
//
// 顺序有意义：值越大越可信；ConfidenceHeuristic 是任何未经真实解析器产出的下限。
type Confidence uint8

const (
	// ConfidenceUnknown 表示行存在但来源已丢失。
	ConfidenceUnknown Confidence = iota
	// ConfidenceHeuristic 是文本猜测（正则、缩进、glob）。
	ConfidenceHeuristic
	// ConfidenceSyntax 来自真实 parser/lexer 的一遍处理。
	ConfidenceSyntax
	// ConfidenceSemantic 是跨文件解析结果（作用域、类型、导入图）。
	ConfidenceSemantic
)

// String 渲染为 SQLite 行与 JSON 输出使用的小写 token；永不失败、永不返回空串。
func (c Confidence) String() string {
	switch c {
	case ConfidenceHeuristic:
		return "heuristic"
	case ConfidenceSyntax:
		return "syntax"
	case ConfidenceSemantic:
		return "semantic"
	default:
		return "unknown"
	}
}

// Score 是 04 §4.4 source_weight 的归一化映射，写入 refs.confidence（REAL）。
func (c Confidence) Score() float64 {
	switch c {
	case ConfidenceHeuristic:
		return 0.55 // regex_builtin
	case ConfidenceSyntax:
		return 0.80 // tree_sitter_resolved
	case ConfidenceSemantic:
		return 0.95 // lsp_resolved
	default:
		return 0.40 // fts_lexical
	}
}

// ParseConfidence 是 Confidence.String 的逆操作。未知输入返回 ConfidenceUnknown
// 与一个错误，使坏数据显式暴露，而不是被静默降级成"足够可信"。
func ParseConfidence(raw string) (Confidence, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "unknown", "":
		return ConfidenceUnknown, nil
	case "heuristic":
		return ConfidenceHeuristic, nil
	case "syntax":
		return ConfidenceSyntax, nil
	case "semantic":
		return ConfidenceSemantic, nil
	default:
		return ConfidenceUnknown, fmt.Errorf("knowledge: unknown confidence %q", raw)
	}
}

// ConfidenceFromSource 把 refs.source 映射到 confidence 等级（04 §4.4 的 source_weight）。
func ConfidenceFromSource(source RefSource) Confidence {
	switch source {
	case SourceLSP, SourceRuntime:
		return ConfidenceSemantic
	case SourceTreeSitter:
		return ConfidenceSyntax
	case SourceBuiltin, SourceHeuristic:
		return ConfidenceHeuristic
	default:
		return ConfidenceUnknown
	}
}

// NormalizeName 归一化参与 stable_key 的名字片段：压缩空白、统一分隔符。
//
// 大小写规则按语言保留（大小写敏感语言原样保留），因此本函数不改变大小写。
func NormalizeName(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

// SignatureHash 计算 04 §4.4 定义的 signature_hash = sha1(normalized_signature + adapter_version)。
func SignatureHash(signature string) string {
	sum := sha1.Sum([]byte(NormalizeName(signature) + AdapterVersion))
	return hex.EncodeToString(sum[:])
}

// StableKey 计算 04 §4.4 定义的符号稳定身份：
//
//	sha1(language | kind | namespace | owner_qualified_name | qualified_name | signature_hash)
//
// 注意：signature_hash 参与身份，因此支持重载的语言天然区分重载；
// 不参与的语言（无签名信息）传空串即可。
func StableKey(language string, kind SymbolKind, namespace, ownerQualifiedName, qualifiedName, signatureHash string) string {
	parts := []string{
		strings.ToLower(strings.TrimSpace(language)),
		string(kind),
		NormalizeName(namespace),
		NormalizeName(ownerQualifiedName),
		NormalizeName(qualifiedName),
		strings.TrimSpace(signatureHash),
	}
	sum := sha1.Sum([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

// digest 返回定长（16 字节 hex）摘要，用于各类行的 TEXT 主键。
func digest(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:16])
}

// FileID 返回 files 行的稳定主键。
func FileID(workspaceID, relPath string) string {
	return "f_" + digest(workspaceID, normalizeRelPath(relPath))
}

// WorkspaceID 返回 workspaces 行的稳定主键。
//
// 设计上该 id 复用 workspaceregistry 的既有主键（04 §4.3）；在其可用之前，
// 这里用绝对路径派生一个确定性 id，保证同一工作区在不同进程/不同时间
// 得到同一个值——这是单写者仲裁与缓存命中率的前提。
func WorkspaceID(rootPath string) string {
	abs := strings.TrimSpace(rootPath)
	if resolved, err := filepath.Abs(abs); err == nil {
		abs = resolved
	}
	abs = strings.ReplaceAll(abs, `\`, "/")
	if runtime.GOOS == "windows" {
		abs = strings.ToLower(abs)
	}
	return "w_" + digest(abs)
}

// SymbolID 返回 symbols 行的稳定主键（由 stable_key 派生）。
func SymbolID(workspaceID, stableKey string) string {
	return "s_" + digest(workspaceID, stableKey)
}

// RefID 返回 refs 行的稳定主键（同一文件的同一位置同一 kind 视为同一条）。
func RefID(workspaceID, fileID string, line, col int, kind RefKind) string {
	return "r_" + digest(workspaceID, fileID, strconv.Itoa(line), strconv.Itoa(col), string(kind))
}

// EventID 返回 invalidation_events 等事件行的主键。
func EventID(workspaceID, reason string, unixNano int64) string {
	return "e_" + digest(workspaceID, reason, strconv.FormatInt(unixNano, 10))
}

// normalizeRelPath 把路径统一为 workspace 相对的 '/' 分隔形式，
// 保证 Windows 与 POSIX 上索引字节一致（ADR-0006）。
func normalizeRelPath(p string) string {
	normalized := strings.ReplaceAll(strings.TrimSpace(p), `\`, "/")
	normalized = strings.TrimPrefix(normalized, "./")
	normalized = strings.TrimPrefix(normalized, "/")
	return normalized
}
