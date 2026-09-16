package filebrowse

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/fsscope"
)

// 排序键（规划 §5.3）。
const (
	SortTypeThenName = "type_then_name"
	SortNameAsc      = "name_asc"
	SortNameDesc     = "name_desc"
	SortMtimeDesc    = "mtime_desc"
	SortSizeDesc     = "size_desc"
)

const listCursorVersion = 1

// ListRequest 是 GET /fs/list 的入参。
type ListRequest struct {
	Scope      string
	Path       string
	Cursor     string
	Limit      int
	Sort       string
	ShowHidden bool
	DirsFirst  bool
}

// DirInfo 描述当前列出的目录。
type DirInfo struct {
	Path    string `json:"path"`
	AbsPath string `json:"abs_path"`
	Parent  string `json:"parent"`
	IsRoot  bool   `json:"is_root"`
}

// Entry 是单层目录中的一个条目（Path 相对作用域根，分隔符统一 `/`）。
type Entry struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Type      string `json:"type"`
	Size      int64  `json:"size"`
	Mtime     int64  `json:"mtime"`
	Ext       string `json:"ext,omitempty"`
	IsText    bool   `json:"is_text"`
	IsSymlink bool   `json:"is_symlink"`
	Internal  bool   `json:"internal,omitempty"`
}

// ListResult 是 GET /fs/list 的响应体。
type ListResult struct {
	Dir        DirInfo `json:"dir"`
	Entries    []Entry `json:"entries"`
	NextCursor string  `json:"next_cursor,omitempty"`
	HasMore    bool    `json:"has_more"`
	Truncated  bool    `json:"truncated"`
	Sort       string  `json:"sort"`
	Limit      int     `json:"limit"`
}

type listingItem struct {
	entry    Entry
	hidden   bool
	internal bool
}

// List 列出**一层**目录（不做递归、不统计子目录项数），支持游标分页与服务端排序。
func (s *Service) List(ctx context.Context, req ListRequest) (*ListResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, fsscopeReadError("list", err)
	}
	target, ferr := s.resolver.ResolvePath(ctx, req.Scope, req.Path)
	if ferr != nil {
		return nil, ferr
	}
	if ferr := s.requireDir(target); ferr != nil {
		return nil, ferr
	}
	sortKey := normalizeSort(req.Sort)
	dirsFirst := req.DirsFirst || sortKey == SortTypeThenName
	limit := s.clampListLimit(req.Limit)
	cursorEntry, cursorErr := decodeListCursor(req.Cursor, sortKey, target.Rel, dirsFirst)
	if cursorErr != nil {
		return nil, cursorErr
	}

	// 懒清理：本次列出的目录若有上传分片目录，顺带清掉过期会话（规划 §5.4）。
	s.sweepUploadDir(ctx, target.Abs)

	items, truncated, ferr := s.readListing(target)
	if ferr != nil {
		return nil, ferr
	}
	entries := make([]Entry, 0, len(items))
	for _, item := range items {
		if item.internal {
			continue // .aicli-uploads 一律隐藏，即使 show_hidden=true
		}
		if item.hidden && !req.ShowHidden {
			continue
		}
		entries = append(entries, item.entry)
	}
	sortEntries(entries, sortKey, dirsFirst)
	if cursorEntry != nil {
		entries = skipUntilAfterCursor(entries, *cursorEntry, sortKey, dirsFirst)
	}

	result := &ListResult{Dir: describeDir(target), Entries: []Entry{}, Sort: sortKey, Limit: limit, Truncated: truncated}
	if len(entries) > limit { // 多读一项判定 has_more，天然避免 len==limit 的歧义
		result.Entries = entries[:limit]
		result.HasMore = true
		result.NextCursor = encodeListCursor(result.Entries[len(result.Entries)-1], sortKey, target.Rel, dirsFirst)
	} else {
		result.Entries = entries
	}
	return result, nil
}

func (s *Service) readListing(target *fsscope.Target) ([]listingItem, bool, *fsscope.Error) {
	dirEntries, err := os.ReadDir(target.Abs)
	if err != nil {
		return nil, false, readDirError(err, target)
	}
	truncated := false
	if len(dirEntries) > s.limits.ScanMaxEntries {
		dirEntries = dirEntries[:s.limits.ScanMaxEntries]
		truncated = true
	}
	items := make([]listingItem, 0, len(dirEntries))
	for _, de := range dirEntries {
		name := de.Name()
		internal := isInternalName(name)
		hidden := internal || IsHiddenName(name)
		entry := Entry{
			Name:      name,
			Path:      listPath(target.Rel, name),
			Type:      "file",
			Size:      -1,
			Mtime:     -1,
			IsSymlink: de.Type()&os.ModeSymlink != 0,
			Internal:  internal,
		}
		info, infoErr := de.Info()
		if infoErr != nil {
			// 权限不足等单项错误：跳过该项并标记 inaccessible，不让整层失败。
			entry.Type = "inaccessible"
			items = append(items, listingItem{entry: entry, hidden: hidden, internal: internal})
			continue
		}
		if !hidden && fileAttributesHidden(info) {
			hidden = true
		}
		entry.Mtime = info.ModTime().Unix()
		switch {
		case info.IsDir():
			entry.Type = "dir"
			entry.Size = -1
		case entry.IsSymlink:
			resolved, statErr := os.Stat(filepath.Join(target.Abs, name))
			if statErr != nil {
				entry.Type = "inaccessible"
				entry.Mtime = -1
				break
			}
			entry.Mtime = resolved.ModTime().Unix()
			if resolved.IsDir() {
				entry.Type = "dir"
			} else {
				entry.Size = resolved.Size()
				entry.IsText = IsTextName(name)
			}
		default:
			entry.Size = info.Size()
			entry.IsText = IsTextName(name)
		}
		if entry.Type == "file" {
			entry.Ext = strings.ToLower(filepath.Ext(name))
		}
		items = append(items, listingItem{entry: entry, hidden: hidden, internal: internal})
	}
	return items, truncated, nil
}

func describeDir(target *fsscope.Target) DirInfo {
	return DirInfo{
		Path:    target.Rel,
		AbsPath: target.Abs,
		Parent:  parentOf(target.Rel),
		IsRoot:  target.Rel == "",
	}
}

func listPath(dirRel, name string) string {
	return path.Join(dirRel, name)
}

func parentOf(rel string) string {
	if rel == "" {
		return ""
	}
	parent := path.Dir(rel)
	if parent == "." {
		return ""
	}
	return parent
}

func readDirError(err error, target *fsscope.Target) *fsscope.Error {
	switch {
	case os.IsNotExist(err):
		return fsscope.NewErrorf(fsscope.CodePathNotFound, 404, "directory does not exist: %s", displayPath(target))
	case os.IsPermission(err):
		return fsscope.NewErrorf(fsscope.CodePathPermission, 403, "permission denied: %s", displayPath(target))
	default:
		return fsscope.NewErrorf(CodeListFailed, 500, "read directory %s failed: %v", displayPath(target), err)
	}
}

func (s *Service) clampListLimit(value int) int {
	if value <= 0 {
		return s.limits.ListLimitDefault
	}
	if value > s.limits.ListLimitMax {
		return s.limits.ListLimitMax
	}
	return value
}

func normalizeSort(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case SortNameAsc:
		return SortNameAsc
	case SortNameDesc:
		return SortNameDesc
	case SortMtimeDesc:
		return SortMtimeDesc
	case SortSizeDesc:
		return SortSizeDesc
	default:
		return SortTypeThenName
	}
}

func sortEntries(entries []Entry, sortKey string, dirsFirst bool) {
	sort.SliceStable(entries, func(i, j int) bool {
		return compareEntries(entries[i], entries[j], sortKey, dirsFirst) < 0
	})
}

// compareEntries 是全序比较函数：游标分页的正确性依赖它（比较结果必须稳定且自洽）。
func compareEntries(a, b Entry, sortKey string, dirsFirst bool) int {
	if dirsFirst {
		aDir, bDir := a.Type == "dir", b.Type == "dir"
		if aDir != bDir {
			if aDir {
				return -1
			}
			return 1
		}
	}
	var primary int
	switch sortKey {
	case SortMtimeDesc:
		primary = compareInt64(b.Mtime, a.Mtime)
	case SortSizeDesc:
		primary = compareInt64(b.Size, a.Size)
	case SortNameDesc:
		primary = -compareName(a.Name, b.Name)
	default:
		primary = compareName(a.Name, b.Name)
	}
	if primary != 0 {
		return primary
	}
	if value := compareName(a.Name, b.Name); value != 0 {
		return value
	}
	switch {
	case a.Name < b.Name:
		return -1
	case a.Name > b.Name:
		return 1
	default:
		return 0
	}
}

func compareName(a, b string) int {
	lowerA, lowerB := strings.ToLower(a), strings.ToLower(b)
	switch {
	case lowerA < lowerB:
		return -1
	case lowerA > lowerB:
		return 1
	default:
		return 0
	}
}

func compareInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// listCursor 是不透明游标的内容；命中排序键 + 目录 + 排序方式，避免跨目录/跨排序误用。
type listCursor struct {
	Version   int    `json:"v"`
	Sort      string `json:"s"`
	DirsFirst bool   `json:"d,omitempty"`
	Dir       string `json:"dir"`
	Name      string `json:"n"`
	IsDir     bool   `json:"t,omitempty"`
	Mtime     int64  `json:"m,omitempty"`
	Size      int64  `json:"z,omitempty"`
}

func encodeListCursor(entry Entry, sortKey, dirRel string, dirsFirst bool) string {
	payload, err := json.Marshal(listCursor{
		Version:   listCursorVersion,
		Sort:      sortKey,
		DirsFirst: dirsFirst,
		Dir:       dirRel,
		Name:      entry.Name,
		IsDir:     entry.Type == "dir",
		Mtime:     entry.Mtime,
		Size:      entry.Size,
	})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

// decodeListCursor 解析游标；失败返回 400 cursor_invalid（不静默回退到第一页，避免前端死循环）。
func decodeListCursor(raw, sortKey, dirRel string, dirsFirst bool) (*Entry, *fsscope.Error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	invalid := func(message string) *fsscope.Error {
		return fsscope.NewError(CodeCursorInvalid, 400, message)
	}
	if len(raw) > 4096 {
		return nil, invalid("cursor is too long")
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, invalid("cursor is not valid base64url")
	}
	var cursor listCursor
	if err := json.Unmarshal(payload, &cursor); err != nil {
		return nil, invalid("cursor payload is not valid JSON")
	}
	if cursor.Version != listCursorVersion {
		return nil, invalid("unsupported cursor version")
	}
	if cursor.Sort != sortKey || cursor.Dir != dirRel || cursor.DirsFirst != dirsFirst {
		return nil, invalid("cursor does not match the current listing")
	}
	if strings.TrimSpace(cursor.Name) == "" {
		return nil, invalid("cursor is missing the last entry name")
	}
	entry := Entry{Name: cursor.Name, Type: "file", Mtime: cursor.Mtime, Size: cursor.Size}
	if cursor.IsDir {
		entry.Type = "dir"
		entry.Size = -1
	}
	return &entry, nil
}

func skipUntilAfterCursor(entries []Entry, cursor Entry, sortKey string, dirsFirst bool) []Entry {
	for index, entry := range entries {
		if compareEntries(entry, cursor, sortKey, dirsFirst) > 0 {
			return entries[index:]
		}
	}
	return nil
}
