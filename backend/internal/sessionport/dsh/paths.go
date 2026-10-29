package dsh

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/wwsheng009/ai-agent-runtime/internal/sessionport"
)

const (
	agentID          = string(sessionport.AgentDeepSeekHarness)
	currentFormat    = 4
	formatName       = "deepseek-harness-jsonl"
	sessionLogPrefix = "session"
)

// DefaultHome returns the DSH home directory.
func DefaultHome() string {
	if value := strings.TrimSpace(os.Getenv("DSH_HOME")); value != "" {
		return filepath.Clean(value)
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".dsh")
}

// DefaultRoots returns the standard DSH home plus any explicit environment root.
func DefaultRoots() []string {
	seen := map[string]struct{}{}
	roots := make([]string, 0, 2)
	for _, value := range []string{strings.TrimSpace(os.Getenv("DSH_HOME")), DefaultHome()} {
		if value == "" {
			continue
		}
		clean := filepath.Clean(value)
		if _, ok := seen[strings.ToLower(clean)]; ok {
			continue
		}
		seen[strings.ToLower(clean)] = struct{}{}
		roots = append(roots, clean)
	}
	return roots
}

type location struct {
	Home        string
	SessionsDir string
}

func resolveLocation(root string) (location, error) {
	raw := strings.TrimSpace(root)
	if raw == "" {
		raw = DefaultHome()
	}
	if raw == "" {
		return location{}, fmt.Errorf("无法确定 DeepSeek Harness 数据根：请设置 DSH_HOME 或传 --root")
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return location{}, fmt.Errorf("解析 DSH 数据根失败: %w", err)
	}
	abs = filepath.Clean(abs)
	info, err := os.Stat(abs)
	if err != nil {
		return location{}, fmt.Errorf("DSH 数据根不可读: %w", err)
	}
	if !info.IsDir() {
		if !looksLikeSessionLog(abs) {
			return location{}, fmt.Errorf("DSH 路径不是目录或 session 日志: %s", abs)
		}
		abs = filepath.Dir(abs)
	}
	loc, ok := resolveLocationFromDir(abs)
	if ok {
		return loc, nil
	}
	// A caller may point at a parent that contains a .dsh home.
	for depth := 0; depth < 3; depth++ {
		candidate := filepath.Join(abs, ".dsh")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			if loc, ok := resolveLocationFromDir(candidate); ok {
				return loc, nil
			}
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			break
		}
		abs = parent
	}
	return location{}, fmt.Errorf("目录中没有找到 DSH sessions/: %s", filepath.Clean(root))
}

func resolveLocationFromDir(dir string) (location, bool) {
	candidates := []struct {
		home     string
		sessions string
	}{
		{home: dir, sessions: filepath.Join(dir, "sessions")},
		{home: filepath.Dir(dir), sessions: dir},
	}
	if strings.EqualFold(filepath.Base(dir), "sessions") {
		candidates = candidates[:1]
		candidates[0] = struct{ home, sessions string }{home: filepath.Dir(dir), sessions: dir}
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate.sessions); err == nil && info.IsDir() {
			return location{
				Home:        filepath.Clean(candidate.home),
				SessionsDir: filepath.Clean(candidate.sessions),
			}, true
		}
	}
	// Project directory (--<cwd>--) or session directory: walk up until sessions/.
	current := dir
	for depth := 0; depth < 3; depth++ {
		parent := filepath.Dir(current)
		if strings.EqualFold(filepath.Base(parent), "sessions") {
			if info, err := os.Stat(parent); err == nil && info.IsDir() {
				return location{Home: filepath.Dir(parent), SessionsDir: parent}, true
			}
		}
		if parent == current {
			break
		}
		current = parent
	}
	return location{}, false
}

func looksLikeSessionLog(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return (strings.HasPrefix(base, "session.") || base == "session.jsonl" || base == "session.jsonl.zstd") &&
		strings.Contains(base, ".jsonl")
}

func instanceID(home string) string {
	clean := filepath.Clean(home)
	if defaultHome := DefaultHome(); defaultHome != "" && strings.EqualFold(filepath.Clean(defaultHome), clean) {
		return agentID + ":default"
	}
	sum := sha256.Sum256([]byte(strings.ToLower(clean)))
	return agentID + ":" + hex.EncodeToString(sum[:4])
}

type sessionLogFile struct {
	Path        string
	Version     int
	Compression string
	Size        int64
	ModTime     time.Time
}

func listSessionLogFiles(sessionDir string) []sessionLogFile {
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return nil
	}
	files := make([]sessionLogFile, 0, 2)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		version, compression, ok := parseLogFilename(entry.Name())
		if !ok {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, sessionLogFile{
			Path:        filepath.Join(sessionDir, entry.Name()),
			Version:     version,
			Compression: compression,
			Size:        info.Size(),
			ModTime:     info.ModTime(),
		})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].Version != files[j].Version {
			return files[i].Version > files[j].Version
		}
		return files[i].Path < files[j].Path
	})
	return files
}

func parseLogFilename(name string) (int, string, bool) {
	if name == "session.jsonl" {
		return 0, "none", true
	}
	compression := "none"
	base := name
	if strings.HasSuffix(base, ".zstd") {
		compression = "zstd"
		base = strings.TrimSuffix(base, ".zstd")
	}
	if base == "session.jsonl" {
		return 0, compression, true
	}
	if !strings.HasPrefix(base, "session.v") || !strings.HasSuffix(base, ".jsonl") {
		return 0, "", false
	}
	versionText := strings.TrimSuffix(strings.TrimPrefix(base, "session.v"), ".jsonl")
	if versionText == "" || strings.HasPrefix(versionText, "0") {
		return 0, "", false
	}
	version, err := strconv.Atoi(versionText)
	if err != nil || version <= 0 {
		return 0, "", false
	}
	return version, compression, true
}

func decodeSegment(segment string) (string, error) {
	if segment == "" {
		return "", fmt.Errorf("空 session 目录名")
	}
	var builder strings.Builder
	for i := 0; i < len(segment); {
		if segment[i] != '~' {
			builder.WriteByte(segment[i])
			i++
			continue
		}
		if i+5 > len(segment) {
			return "", fmt.Errorf("非法 session 目录编码: %s", segment)
		}
		value, err := strconv.ParseUint(segment[i+1:i+5], 16, 16)
		if err != nil {
			return "", fmt.Errorf("非法 session 目录编码: %s", segment)
		}
		builder.WriteRune(rune(value))
		i += 5
	}
	return builder.String(), nil
}

// readHeaderFile reads only the first JSONL record, decompressing only the
// needed prefix for zstd artifacts.
func readHeaderFile(path string) (Header, error) {
	reader, closeFn, err := openSessionLogReader(path)
	if err != nil {
		return Header{}, err
	}
	defer closeFn()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var header Header
		if err := json.Unmarshal([]byte(line), &header); err != nil {
			return Header{}, fmt.Errorf("解析 DSH header 失败: %w", err)
		}
		if header.Type != "session" {
			return Header{}, fmt.Errorf("DSH 日志首行不是 session header")
		}
		return header, nil
	}
	if err := scanner.Err(); err != nil {
		return Header{}, err
	}
	return Header{}, fmt.Errorf("DSH 日志为空")
}

func openSessionLogReader(path string) (io.Reader, func() error, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	if !strings.HasSuffix(strings.ToLower(path), ".zstd") {
		return file, file.Close, nil
	}
	decoder, err := zstd.NewReader(file, zstd.WithDecoderConcurrency(1))
	if err != nil {
		_ = file.Close()
		return nil, nil, fmt.Errorf("打开 DSH zstd 日志失败: %w", err)
	}
	return decoder, func() error {
		decoder.Close()
		return file.Close()
	}, nil
}

func formatStatus(version int) string {
	switch {
	case version == currentFormat:
		return "current"
	case version > 0 && version < currentFormat:
		return "migration-required"
	case version == 0:
		return "migration-required"
	default:
		return "unsupported"
	}
}

func scanSessions(instance sessionport.Instance) []sessionport.SessionRef {
	refs := make([]sessionport.SessionRef, 0)
	projects, err := os.ReadDir(instance.SessionsDir)
	if err != nil {
		return refs
	}
	for _, project := range projects {
		if !project.IsDir() || !strings.HasPrefix(project.Name(), "--") {
			continue
		}
		projectDir := filepath.Join(instance.SessionsDir, project.Name())
		sessions, err := os.ReadDir(projectDir)
		if err != nil {
			continue
		}
		for _, session := range sessions {
			if !session.IsDir() {
				continue
			}
			sessionDir := filepath.Join(projectDir, session.Name())
			files := listSessionLogFiles(sessionDir)
			if len(files) == 0 {
				continue
			}
			file := files[0]
			header, err := readHeaderFile(file.Path)
			ref := sessionport.SessionRef{
				AgentID:       agentID,
				InstanceID:    instance.InstanceID,
				SessionID:     session.Name(),
				File:          file.Path,
				FormatVersion: file.Version,
				FormatStatus:  formatStatus(file.Version),
				Compression:   file.Compression,
				SizeBytes:     file.Size,
				UpdatedAt:     file.ModTime,
				Readable:      err == nil && header.Version == currentFormat,
			}
			if err == nil {
				ref.SessionID = header.ID
				ref.CWD = header.CWD
			} else {
				ref.Warnings = append(ref.Warnings, err.Error())
			}
			refs = append(refs, ref)
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		if !refs[i].UpdatedAt.Equal(refs[j].UpdatedAt) {
			return refs[i].UpdatedAt.After(refs[j].UpdatedAt)
		}
		return refs[i].SessionID < refs[j].SessionID
	})
	return refs
}
