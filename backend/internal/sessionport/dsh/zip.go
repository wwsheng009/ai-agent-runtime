package dsh

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/sessionport"
)

type zipCandidate struct {
	entry  *zip.File
	name   string
	header Header
	events []Event
}

// ZipInspection lists the root sessions contained in an official DSH export
// ZIP. It is read-only and does not select or convert a session.
type ZipInspection struct {
	Sessions []sessionport.SessionRef `json:"sessions"`
	Warnings []string                 `json:"warnings,omitempty"`
}

// InspectZip scans an official DSH export ZIP and returns every root session
// candidate. Subagent and attachment entries are reported as warnings because
// the first import implementation does not migrate their bytes.
func InspectZip(zipPath string) (*ZipInspection, error) {
	absolute, err := resolveZipFile(zipPath)
	if err != nil {
		return nil, err
	}
	candidates, warnings, err := scanZipCandidates(absolute)
	if err != nil {
		return nil, err
	}
	sessions := make([]sessionport.SessionRef, 0, len(candidates))
	for index := range candidates {
		sessions = append(sessions, zipCandidateRef(&candidates[index], absolute, warnings))
	}
	return &ZipInspection{Sessions: sessions, Warnings: warnings}, nil
}

func resolveZipFile(zipPath string) (string, error) {
	absolute, err := filepath.Abs(zipPath)
	if err != nil {
		return zipPath, err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return absolute, fmt.Errorf("读取 DSH 导出 ZIP 失败: %w", err)
	}
	if info.IsDir() {
		return absolute, fmt.Errorf("--zip 需要文件，不是目录: %s", absolute)
	}
	return absolute, nil
}

func readZipSessionFile(zipPath, sessionID string, latest bool) (*Loaded, error) {
	candidates, warnings, err := scanZipCandidates(zipPath)
	if err != nil {
		return nil, err
	}
	selected := &candidates[0]
	if strings.TrimSpace(sessionID) != "" {
		found := false
		for index := range candidates {
			if candidates[index].header.ID == strings.TrimSpace(sessionID) {
				selected = &candidates[index]
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("DSH ZIP 中找不到会话: %s", sessionID)
		}
	} else if latest {
		selected = latestZipCandidate(candidates)
	}
	return &Loaded{
		Header:   selected.header,
		Events:   selected.events,
		Ref:      zipCandidateRef(selected, zipPath, warnings),
		Warnings: warnings,
	}, nil
}

func latestZipCandidate(candidates []zipCandidate) *zipCandidate {
	selected := &candidates[0]
	selectedTime := lastEventTime(selected.events)
	for index := range candidates[1:] {
		candidate := &candidates[index+1]
		updated := lastEventTime(candidate.events)
		if updated.After(selectedTime) {
			selected = candidate
			selectedTime = updated
		}
	}
	return selected
}

func scanZipCandidates(zipPath string) ([]zipCandidate, []string, error) {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, nil, fmt.Errorf("打开 DSH 导出 ZIP 失败: %w", err)
	}
	defer reader.Close()

	candidates := make([]zipCandidate, 0)
	subagentEntries := 0
	attachmentEntries := 0
	for _, entry := range reader.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		name := strings.TrimPrefix(path.Clean(entry.Name), "./")
		base := path.Base(name)
		if strings.HasPrefix(name, "subagents/") {
			subagentEntries++
		}
		if strings.HasPrefix(name, "media/") || strings.HasPrefix(name, "files/") {
			attachmentEntries++
		}
		version, _, ok := parseZipLogBase(base)
		if !ok {
			continue
		}
		// Root log has no directory. Official export uses session.v4.jsonl at
		// the archive root; descendants live under subagents/... .
		if strings.Contains(strings.Trim(name, "/\\"), "/") || strings.Contains(strings.Trim(name, "/\\"), "\\") {
			continue
		}
		header, events, err := readZipEntry(entry)
		if err != nil {
			continue
		}
		candidates = append(candidates, zipCandidate{entry: entry, name: name, header: header, events: events})
		_ = version
	}
	if len(candidates) == 0 {
		return nil, nil, fmt.Errorf("DSH 导出 ZIP 中没有根 session.v*.jsonl 日志")
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].header.Version != candidates[j].header.Version {
			return candidates[i].header.Version > candidates[j].header.Version
		}
		return candidates[i].header.ID < candidates[j].header.ID
	})
	warnings := make([]string, 0)
	if subagentEntries > 0 {
		warnings = append(warnings, fmt.Sprintf("ZIP 包含 %d 个子会话文件，首版默认不导入子会话", subagentEntries))
	}
	if attachmentEntries > 0 {
		warnings = append(warnings, fmt.Sprintf("ZIP 包含 %d 个附件文件，首版暂不迁移附件字节", attachmentEntries))
	}
	return candidates, warnings, nil
}

func zipCandidateRef(candidate *zipCandidate, zipPath string, warnings []string) sessionport.SessionRef {
	ref := sessionport.SessionRef{
		AgentID:       agentID,
		SessionID:     candidate.header.ID,
		CWD:           candidate.header.CWD,
		File:          zipPath + ":" + candidate.name,
		FormatVersion: candidate.header.Version,
		FormatStatus:  formatStatus(candidate.header.Version),
		Compression:   "none",
		SizeBytes:     int64(candidate.entry.UncompressedSize64),
		EventCount:    len(candidate.events),
		Readable:      candidate.header.Version == currentFormat,
	}
	if updated := lastEventTime(candidate.events); !updated.IsZero() {
		ref.UpdatedAt = updated
	} else {
		ref.UpdatedAt = candidate.entry.Modified.UTC()
	}
	ref.Title = sessionTitle(candidate.events)
	if len(warnings) > 0 {
		ref.Warnings = append([]string(nil), warnings...)
	}
	return ref
}

func parseZipLogBase(base string) (int, string, bool) {
	if base == "session.jsonl" {
		return 0, "none", true
	}
	if !strings.HasPrefix(base, "session.v") || !strings.HasSuffix(base, ".jsonl") {
		return 0, "", false
	}
	versionText := strings.TrimSuffix(strings.TrimPrefix(base, "session.v"), ".jsonl")
	if versionText == "" || strings.HasPrefix(versionText, "0") {
		return 0, "", false
	}
	var version int
	if _, err := fmt.Sscanf(versionText, "%d", &version); err != nil || version <= 0 {
		return 0, "", false
	}
	return version, "none", true
}

func readZipEntry(entry *zip.File) (Header, []Event, error) {
	reader, err := entry.Open()
	if err != nil {
		return Header{}, nil, err
	}
	defer reader.Close()
	return parseLogReader(io.Reader(reader))
}
