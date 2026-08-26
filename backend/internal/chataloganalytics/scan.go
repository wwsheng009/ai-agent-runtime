package chataloganalytics

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
)

const (
	defaultListLimit    = 50
	maxListLimit        = 200
	defaultSummaryLimit = 100
	maxSummaryLimit     = 500
	defaultMaxScan      = 800
	maxMaxScan          = 5000
)

var (
	sessionIDRE   = regexp.MustCompile(`^(?:session_)?\d{8}[_-]?\d{6}(?:\.\d+)?(?:_[0-9a-fA-F-]+)?$`)
	yearRE        = regexp.MustCompile(`^\d{4}$`)
	monthOrDayRE  = regexp.MustCompile(`^\d{2}$`)
	artifactNames = map[string]struct{}{
		"local-shell":      {},
		"runtime-http":     {},
		"generated-images": {},
		"toolkit":          {},
		"exports":          {},
	}
)

// SessionDir describes one discovered chat-log session directory.
type SessionDir struct {
	Path      string
	SessionID string
	RelPath   string // path relative to root using forward slashes
	Directory string // date partition or "." for legacy flat layout
	ModTime   time.Time
}

func normalizeLimit(limit, fallback, max int) int {
	if limit <= 0 {
		return fallback
	}
	if limit > max {
		return max
	}
	return limit
}

func normalizeOffset(offset int) int {
	if offset < 0 {
		return 0
	}
	return offset
}

func normalizeMaxScan(maxScan int) int {
	if maxScan <= 0 {
		return defaultMaxScan
	}
	if maxScan > maxMaxScan {
		return maxMaxScan
	}
	return maxScan
}

func looksLikeSessionID(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return false
	}
	if _, blocked := artifactNames[strings.ToLower(name)]; blocked {
		return false
	}
	if yearRE.MatchString(name) || monthOrDayRE.MatchString(name) {
		return false
	}
	if sessionIDRE.MatchString(name) {
		return true
	}
	// tolerate slightly different suffixes but require leading timestamp
	if len(name) >= 15 {
		if matched, _ := regexp.MatchString(`^\d{8}[_-]\d{6}`, name); matched {
			return true
		}
	}
	return false
}

func isSessionDir(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	name := filepath.Base(path)
	if isSessionArtifactName(name) {
		return false
	}
	if !looksLikeSessionID(name) {
		return false
	}
	if _, err := os.Stat(filepath.Join(path, "debug.log")); err == nil {
		return true
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return true
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasPrefix(strings.ToLower(entry.Name()), "chat_") && strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			return true
		}
	}
	return true
}

// sessionArtifactDirSuffixes 是新布局中依附于 <sid>.json 的 artifact 目录后缀，
// 扫描时必须排除，避免被宽松的 looksLikeSessionID 规则误判为会话。
var sessionArtifactDirSuffixes = []string{".http", ".shell", ".images", ".exports", ".events", ".toolkit"}

// isSessionArtifactName 判断目录/文件基名是否为新布局的 artifact（<sid>.http 等）。
func isSessionArtifactName(name string) bool {
	lower := strings.ToLower(name)
	for _, suffix := range sessionArtifactDirSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

// isSessionLogFile 判断是否为文件型会话日志（新布局 <sid>.json）。
func isSessionLogFile(entry os.DirEntry) bool {
	if entry.IsDir() {
		return false
	}
	name := entry.Name()
	lower := strings.ToLower(name)
	if !strings.HasSuffix(lower, ".json") || isSessionArtifactName(name) {
		return false
	}
	base := strings.TrimSuffix(name, filepath.Ext(name))
	return base != "" && looksLikeSessionID(base)
}

// sessionPathMatches 判断候选路径是否为会话：目录（旧布局）或 <sid>.json 文件（新布局）。
func sessionPathMatches(candidate string) bool {
	info, err := os.Stat(candidate)
	if err != nil {
		return false
	}
	if info.IsDir() {
		return isSessionDir(candidate)
	}
	name := filepath.Base(candidate)
	if isSessionArtifactName(name) || !strings.HasSuffix(strings.ToLower(name), ".json") {
		return false
	}
	base := strings.TrimSuffix(name, filepath.Ext(name))
	return base != "" && looksLikeSessionID(base)
}

// sessionDebugLogPath 返回会话调试日志路径，兼容新布局文件型会话（<sid>.json → <sid>.debug.log）。
func sessionDebugLogPath(dir SessionDir) string {
	if strings.HasSuffix(strings.ToLower(dir.Path), ".json") {
		return strings.TrimSuffix(dir.Path, filepath.Ext(dir.Path)) + ".debug.log"
	}
	return filepath.Join(dir.Path, "debug.log")
}

func toRelPath(root, full string) string {
	rel, err := filepath.Rel(root, full)
	if err != nil {
		return filepath.ToSlash(full)
	}
	return filepath.ToSlash(rel)
}

func directoryFromRelPath(rel string) string {
	rel = strings.Trim(strings.ReplaceAll(rel, "\\", "/"), "/")
	if rel == "" {
		return "."
	}
	parts := strings.Split(rel, "/")
	if len(parts) <= 1 {
		return "."
	}
	// YYYY/MM/DD/<session>
	if len(parts) >= 4 && yearRE.MatchString(parts[0]) && monthOrDayRE.MatchString(parts[1]) && monthOrDayRE.MatchString(parts[2]) {
		return strings.Join(parts[:3], "/")
	}
	// drop session id leaf
	return strings.Join(parts[:len(parts)-1], "/")
}

// DiscoverSessionDirs walks chat-log root for legacy flat and YYYY/MM/DD layouts.
// Results are sorted by ModTime descending (recent first).
func DiscoverSessionDirs(root string, maxScan int) ([]SessionDir, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, nil
	}
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, nil
	}

	maxScan = normalizeMaxScan(maxScan)
	out := make([]SessionDir, 0, 64)

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		full := filepath.Join(root, name)

		// date partition year
		if yearRE.MatchString(name) {
			if err := walkDatePartitionYear(root, full, name, &out, maxScan); err != nil {
				return out, err
			}
			if len(out) >= maxScan {
				break
			}
			continue
		}

		if isSessionDir(full) {
			mod := time.Time{}
			if fi, statErr := entry.Info(); statErr == nil {
				mod = fi.ModTime()
			}
			rel := toRelPath(root, full)
			out = append(out, SessionDir{
				Path:      full,
				SessionID: name,
				RelPath:   rel,
				Directory: directoryFromRelPath(rel),
				ModTime:   mod,
			})
			if len(out) >= maxScan {
				break
			}
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ModTime.Equal(out[j].ModTime) {
			return out[i].SessionID > out[j].SessionID
		}
		return out[i].ModTime.After(out[j].ModTime)
	})
	if len(out) > maxScan {
		out = out[:maxScan]
	}
	return out, nil
}

func walkDatePartitionYear(root, yearPath, year string, out *[]SessionDir, maxScan int) error {
	months, err := os.ReadDir(yearPath)
	if err != nil {
		return err
	}
	// newest month/day first for better recency under maxScan
	sort.SliceStable(months, func(i, j int) bool { return months[i].Name() > months[j].Name() })
	for _, monthEntry := range months {
		if !monthEntry.IsDir() || !monthOrDayRE.MatchString(monthEntry.Name()) {
			continue
		}
		monthPath := filepath.Join(yearPath, monthEntry.Name())
		days, err := os.ReadDir(monthPath)
		if err != nil {
			return err
		}
		sort.SliceStable(days, func(i, j int) bool { return days[i].Name() > days[j].Name() })
		for _, dayEntry := range days {
			if !dayEntry.IsDir() || !monthOrDayRE.MatchString(dayEntry.Name()) {
				continue
			}
			dayPath := filepath.Join(monthPath, dayEntry.Name())
			sessions, err := os.ReadDir(dayPath)
			if err != nil {
				return err
			}
			for _, sessEntry := range sessions {
				name := sessEntry.Name()
				full := filepath.Join(dayPath, sessEntry.Name())
				if sessEntry.IsDir() {
					// 排除 <sid>.http/.shell/.images/.exports/.events 等 artifact 目录
					if isSessionArtifactName(name) || !isSessionDir(full) {
						continue
					}
				} else if !isSessionLogFile(sessEntry) {
					// 新布局文件型会话 <sid>.json；跳过 <sid>.debug.log 等其余文件
					continue
				}
				sid := name
				if !sessEntry.IsDir() {
					sid = strings.TrimSuffix(name, filepath.Ext(name))
				}
				mod := time.Time{}
				if fi, statErr := sessEntry.Info(); statErr == nil {
					mod = fi.ModTime()
				}
				rel := toRelPath(root, full)
				*out = append(*out, SessionDir{
					Path:      full,
					SessionID: sid,
					RelPath:   rel,
					Directory: directoryFromRelPath(rel),
					ModTime:   mod,
				})
				if len(*out) >= maxScan {
					return nil
				}
			}
			_ = year // keep year explicit for readability
		}
	}
	return nil
}

// ResolveSessionDir locates a session directory by id under root.
func ResolveSessionDir(root, sessionID string) (SessionDir, bool, error) {
	root = strings.TrimSpace(root)
	sessionID = strings.TrimSpace(sessionID)
	if root == "" || sessionID == "" {
		return SessionDir{}, false, nil
	}
	if strings.ContainsAny(sessionID, `/\`) || filepath.Base(sessionID) != sessionID || !looksLikeSessionID(sessionID) {
		return SessionDir{}, false, nil
	}

	candidates := make([]string, 0, 4)
	if parsed, ok := aiclipaths.ParseTimestampedSessionIDTime(sessionID); ok {
		sidBase := strings.ReplaceAll(sessionID, ".", "_")
		candidates = append(candidates, aiclipaths.JoinDatePartition(root, parsed, sidBase+".json"))
		candidates = append(candidates, aiclipaths.JoinDatePartition(root, parsed, sessionID))
	}
	candidates = append(candidates, filepath.Join(root, sessionID))

	for _, candidate := range candidates {
		if sessionPathMatches(candidate) {
			rel := toRelPath(root, candidate)
			mod := time.Time{}
			if fi, err := os.Stat(candidate); err == nil {
				mod = fi.ModTime()
			}
			return SessionDir{
				Path:      candidate,
				SessionID: sessionID,
				RelPath:   rel,
				Directory: directoryFromRelPath(rel),
				ModTime:   mod,
			}, true, nil
		}
	}

	// fallback: scan (bounded) for relocated sessions
	dirs, err := DiscoverSessionDirs(root, defaultMaxScan)
	if err != nil {
		return SessionDir{}, false, err
	}
	for _, dir := range dirs {
		if dir.SessionID == sessionID || dir.SessionID == strings.ReplaceAll(sessionID, ".", "_") {
			return dir, true, nil
		}
	}
	return SessionDir{}, false, nil
}

func matchDirectoryFilter(dir, filter string) bool {
	filter = strings.Trim(strings.ReplaceAll(strings.TrimSpace(filter), "\\", "/"), "/")
	if filter == "" {
		return true
	}
	dir = strings.Trim(strings.ReplaceAll(dir, "\\", "/"), "/")
	if dir == filter {
		return true
	}
	return strings.HasPrefix(dir, filter+"/")
}

func sessionStartHint(dir SessionDir) time.Time {
	if parsed, ok := aiclipaths.ParseTimestampedSessionIDTime(dir.SessionID); ok {
		return parsed
	}
	return dir.ModTime
}
