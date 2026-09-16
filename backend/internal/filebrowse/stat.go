package filebrowse

import (
	"context"
	"mime"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/fsscope"
)

// PathRequest 是「作用域 + 相对路径」的通用入参（stat / preview / download 共用）。
type PathRequest struct {
	Scope string
	Path  string
}

// EntryStat 是单路径元信息（GET /fs/stat）。
// type 取值 dir|file|symlink|inaccessible；符号链接解析成功时按目标类型给 dir/file，
// 并同时置 is_symlink=true（与 /fs/list 口径一致）。
type EntryStat struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	AbsPath   string `json:"abs_path,omitempty"`
	Type      string `json:"type"`
	Size      int64  `json:"size"`
	Mtime     int64  `json:"mtime"`
	Ext       string `json:"ext,omitempty"`
	Mime      string `json:"mime,omitempty"`
	IsText    bool   `json:"is_text"`
	IsSymlink bool   `json:"is_symlink"`
	Internal  bool   `json:"internal,omitempty"`
}

// Stat 返回单路径元信息；目录/文件类型不匹配或权限不足给出明确错误。
func (s *Service) Stat(ctx context.Context, req PathRequest) (*EntryStat, error) {
	if err := ctx.Err(); err != nil {
		return nil, fsscopeReadError("stat", err)
	}
	target, ferr := s.resolver.ResolvePath(ctx, req.Scope, req.Path)
	if ferr != nil {
		return nil, ferr
	}
	entry, ferr := describeStat(target)
	if ferr != nil {
		return nil, ferr
	}
	return entry, nil
}

func describeStat(target *fsscope.Target) (*EntryStat, *fsscope.Error) {
	lstat, err := os.Lstat(target.Abs)
	if err != nil {
		return nil, statError(err, target)
	}
	isSymlink := lstat.Mode()&os.ModeSymlink != 0
	info := lstat
	if isSymlink {
		resolved, statErr := os.Stat(target.Abs)
		if statErr != nil {
			return nil, statError(statErr, target)
		}
		info = resolved
	}
	name := info.Name()
	entry := &EntryStat{
		Name:      name,
		Path:      target.Rel,
		AbsPath:   target.Abs,
		Type:      entryType(info, isSymlink),
		Size:      info.Size(),
		Mtime:     info.ModTime().Unix(),
		Ext:       strings.ToLower(filepath.Ext(name)),
		IsSymlink: isSymlink,
		Internal:  isInternalName(name),
	}
	if info.IsDir() {
		entry.Size = -1 // 目录不统计子项数（避免全树扫描）
		entry.Ext = ""
	}
	entry.Mime = MimeForName(name, info.IsDir())
	entry.IsText = !info.IsDir() && IsTextName(name)
	return entry, nil
}

func entryType(info os.FileInfo, isSymlink bool) string {
	switch {
	case info == nil:
		return "inaccessible"
	case info.IsDir():
		return "dir"
	default:
		return "file"
	}
}

func fsscopeReadError(action string, err error) *fsscope.Error {
	return fsscope.NewErrorf(fsscope.CodeFSReadFailed, 500, "%s canceled: %v", action, err)
}

// IsTextName 用扩展名/文件名判定「可能是文本」——这是**列表用的廉价启发式**，
// 不读文件内容；权威的二进制判定见 Preview（NUL 字节 → 非法 UTF-8，与前端
// frontend/src/lib/file-preview/decode.ts 完全同口径）。
func IsTextName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" {
		return false
	}
	if _, ok := textFileNames[lower]; ok {
		return true
	}
	ext := strings.ToLower(filepath.Ext(lower))
	if ext == "" {
		return false
	}
	return textExtensions[ext]
}

var textFileNames = map[string]struct{}{
	"dockerfile":         {},
	"makefile":           {},
	"license":            {},
	"readme":             {},
	"changelog":          {},
	"cmakelists.txt":     {},
	".gitignore":         {},
	".gitattributes":     {},
	".editorconfig":      {},
	".env":               {},
	".npmrc":             {},
	".prettierrc":        {},
	".eslintrc":          {},
	".dockerignore":      {},
	"requirements.txt":   {},
	"go.mod":             {},
	"go.sum":             {},
	"cargo.toml":         {},
	"cargo.lock":         {},
	"package.json":       {},
	"tsconfig.json":      {},
	"vite.config.ts":     {},
	"tailwind.config.js": {},
}

var textExtensions = map[string]bool{
	".md": true, ".markdown": true, ".mdx": true, ".txt": true, ".text": true, ".log": true,
	".json": true, ".jsonc": true, ".json5": true, ".yaml": true, ".yml": true, ".toml": true,
	".ini": true, ".cfg": true, ".conf": true, ".properties": true, ".env": true, ".csv": true,
	".tsv": true, ".xml": true, ".html": true, ".htm": true, ".css": true, ".scss": true,
	".sass": true, ".less": true, ".svg": true, ".js": true, ".mjs": true, ".cjs": true,
	".jsx": true, ".ts": true, ".tsx": true, ".mts": true, ".cts": true, ".vue": true,
	".svelte": true, ".go": true, ".py": true, ".pyi": true, ".rb": true, ".rs": true,
	".java": true, ".kt": true, ".kts": true, ".c": true, ".h": true, ".cc": true, ".cpp": true,
	".cxx": true, ".hpp": true, ".cs": true, ".php": true, ".swift": true, ".scala": true,
	".m": true, ".mm": true, ".lua": true, ".pl": true, ".pm": true, ".r": true, ".jl": true,
	".dart": true, ".sh": true, ".bash": true, ".zsh": true, ".fish": true, ".ps1": true,
	".psm1": true, ".bat": true, ".cmd": true, ".sql": true, ".graphql": true, ".gql": true,
	".proto": true, ".thrift": true, ".tf": true, ".hcl": true, ".gradle": true, ".patch": true,
	".diff": true, ".srt": true, ".vtt": true, ".tex": true, ".lock": true, ".example": true,
}

var mimeByExt = map[string]string{
	".md": "text/markdown; charset=utf-8", ".markdown": "text/markdown; charset=utf-8",
	".json": "application/json", ".jsonc": "application/json", ".xml": "text/xml; charset=utf-8",
	".html": "text/html; charset=utf-8", ".htm": "text/html; charset=utf-8",
	".css": "text/css; charset=utf-8", ".svg": "image/svg+xml",
	".yaml": "text/yaml; charset=utf-8", ".yml": "text/yaml; charset=utf-8",
	".toml": "text/toml; charset=utf-8", ".csv": "text/csv; charset=utf-8",
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
	".webp": "image/webp", ".bmp": "image/bmp", ".ico": "image/x-icon", ".avif": "image/avif",
	".tif": "image/tiff", ".tiff": "image/tiff", ".pdf": "application/pdf",
	".zip": "application/zip", ".gz": "application/gzip", ".tar": "application/x-tar",
	".7z": "application/x-7z-compressed", ".rar": "application/vnd.rar",
	".mp4": "video/mp4", ".webm": "video/webm", ".mp3": "audio/mpeg", ".wav": "audio/wav",
	".wasm": "application/wasm", ".exe": "application/vnd.microsoft.portable-executable",
}

// MimeForName 按扩展名给出 MIME；目录返回 inode/directory，未知返回 application/octet-stream。
func MimeForName(name string, isDir bool) string {
	if isDir {
		return "inode/directory"
	}
	ext := strings.ToLower(filepath.Ext(name))
	if value, ok := mimeByExt[ext]; ok {
		return value
	}
	if IsTextName(name) {
		return "text/plain; charset=utf-8"
	}
	if value := mime.TypeByExtension(ext); value != "" {
		return value
	}
	return "application/octet-stream"
}

// isTextMime 判定 MIME 是否属于可内联展示的文本类（供预览分流使用）。
func isTextMime(value string) bool {
	lower := strings.ToLower(value)
	return strings.HasPrefix(lower, "text/") ||
		strings.Contains(lower, "json") || strings.Contains(lower, "xml") ||
		strings.Contains(lower, "javascript") || strings.Contains(lower, "yaml")
}

// isImageName 判定图片扩展名（图片走 base64 内联，限额独立）。
func isImageName(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".ico", ".avif", ".tif", ".tiff":
		return true
	default:
		return false
	}
}

// IsHiddenName 判定隐藏项：`.` 前缀（Windows 另加 FILE_ATTRIBUTE_HIDDEN，见 fileAttributesHidden）。
func IsHiddenName(name string) bool {
	name = strings.TrimSpace(name)
	return strings.HasPrefix(name, ".")
}

// isInternalName 判定上传分片等内部目录（一律隐藏，避免用户误删进行中的分片）。
func isInternalName(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), uploadDirName)
}

const fileAttributeHidden = 0x2

// fileAttributesHidden 读取 Windows FILE_ATTRIBUTE_HIDDEN。
// 用反射访问 FileInfo.Sys()，避免为一个判定引入平台专属文件（本包按职责切文件）。
func fileAttributesHidden(info os.FileInfo) bool {
	if runtime.GOOS != "windows" || info == nil {
		return false
	}
	value := reflect.ValueOf(info.Sys())
	if !value.IsValid() || value.Kind() != reflect.Ptr || value.IsNil() {
		return false
	}
	field := value.Elem().FieldByName("FileAttributes")
	if !field.IsValid() || field.Kind() != reflect.Uint32 {
		return false
	}
	return field.Uint()&fileAttributeHidden != 0
}
