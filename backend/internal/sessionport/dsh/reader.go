package dsh

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/sessionport"
)

func loadFile(path string, ref sessionport.SessionRef) (*Loaded, error) {
	header, _, events, err := inspectLog(path)
	if err != nil {
		return nil, err
	}
	if ref.SessionID == "" {
		ref.SessionID = header.ID
	}
	ref.CWD = header.CWD
	ref.FormatVersion = header.Version
	ref.FormatStatus = formatStatus(header.Version)
	ref.Readable = header.Version == currentFormat
	ref.EventCount = len(events)
	if updated := lastEventTime(events); !updated.IsZero() {
		ref.UpdatedAt = updated
	}
	ref.Title = sessionTitle(events)
	return &Loaded{Header: header, Events: events, Ref: ref}, nil
}

// List returns the sessions in one detected instance. It parses event bodies so
// titles and last-activity times match what the DSH UI would show.
func List(instance sessionport.Instance) ([]sessionport.SessionRef, error) {
	if strings.TrimSpace(instance.SessionsDir) == "" {
		return nil, fmt.Errorf("DSH sessions 目录为空")
	}
	refs := scanSessions(instance)
	for index := range refs {
		ref := &refs[index]
		if !ref.Readable {
			continue
		}
		header, _, events, err := inspectLog(ref.File)
		if err != nil {
			ref.Readable = false
			ref.Warnings = append(ref.Warnings, err.Error())
			continue
		}
		ref.SessionID = header.ID
		ref.CWD = header.CWD
		ref.EventCount = len(events)
		ref.Title = sessionTitle(events)
		if updated := lastEventTime(events); !updated.IsZero() {
			ref.UpdatedAt = updated
		}
	}
	return refs, nil
}

// DetectInstance probes one DSH root/home/sessions directory.
func DetectInstance(root string, includeEmpty bool) (sessionport.Instance, error) {
	loc, err := resolveLocation(root)
	if err != nil {
		return sessionport.Instance{}, err
	}
	instance := sessionport.Instance{
		AgentID:      agentID,
		InstanceID:   instanceID(loc.Home),
		Root:         loc.Home,
		SessionsDir:  loc.SessionsDir,
		Format:       formatName,
		Backend:      "jsonl",
		Confidence:   0.6,
		Capabilities: sessionport.Capabilities{List: true, Read: true, WriteBundle: true, Apply: "none"},
	}
	refs := scanSessions(instance)
	instance.SessionCount = len(refs)
	instance.TranscriptCount = len(refs)
	instance.Warnings = make([]string, 0)
	highestVersion := 0
	compressions := map[string]struct{}{}
	readable := 0
	for index, ref := range refs {
		if ref.FormatVersion > highestVersion {
			highestVersion = ref.FormatVersion
		}
		if ref.Compression != "" {
			compressions[ref.Compression] = struct{}{}
		}
		if ref.Readable {
			readable++
		}
		if len(ref.Warnings) > 0 {
			instance.Warnings = append(instance.Warnings, ref.SessionID+": "+strings.Join(ref.Warnings, "；"))
		}
		if index == 0 {
			instance.FormatVersion = ref.FormatVersion
		}
	}
	if highestVersion > 0 {
		instance.FormatVersion = highestVersion
		instance.FormatStatus = formatStatus(highestVersion)
	}
	if len(compressions) == 1 {
		for compression := range compressions {
			instance.Compression = compression
		}
	} else if len(compressions) > 1 {
		instance.Compression = "mixed"
	}
	if len(refs) > 0 {
		instance.Confidence = 0.8
		if readable > 0 {
			instance.Confidence = 1.0
		}
	} else if includeEmpty {
		instance.FormatStatus = "empty"
	}
	if !includeEmpty && len(refs) == 0 {
		instance.Warnings = append(instance.Warnings, "sessions 目录中没有 session.v*.jsonl 日志")
	}
	return instance, nil
}

func parseLogReader(reader io.Reader) (Header, []Event, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
	header := Header{}
	headerSeen := false
	events := make([]Event, 0, 128)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if !headerSeen {
			if err := json.Unmarshal([]byte(line), &header); err != nil {
				return Header{}, nil, fmt.Errorf("解析 DSH header 失败: %w", err)
			}
			if header.Type != "session" {
				return Header{}, nil, fmt.Errorf("DSH 日志首行不是 session header")
			}
			headerSeen = true
			continue
		}
		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return header, events, fmt.Errorf("解析 DSH 事件第 %d 行失败: %w", lineNo, err)
		}
		if event.Seq != len(events) {
			return header, events, fmt.Errorf("DSH 事件 seq 不连续: 期望 %d，实际 %d", len(events), event.Seq)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return header, events, err
	}
	if !headerSeen {
		return Header{}, nil, fmt.Errorf("DSH 日志为空")
	}
	return header, events, nil
}

func inspectLog(path string) (Header, int, []Event, error) {
	reader, closeFn, err := openSessionLogReader(path)
	if err != nil {
		return Header{}, 0, nil, err
	}
	defer closeFn()
	header, events, err := parseLogReader(reader)
	if err != nil {
		return header, len(events), events, err
	}
	return header, len(events), events, nil
}

func readZipSession(zipPath, sessionID string, latest bool) (*Loaded, string, error) {
	absolute, err := resolveZipFile(zipPath)
	if err != nil {
		return nil, zipPath, err
	}
	loaded, err := readZipSessionFile(absolute, sessionID, latest)
	if err != nil {
		return nil, absolute, err
	}
	return loaded, absolute, nil
}

// hasDSHData reports whether a path resembles a DSH home. It is intentionally
// cheap and side-effect free.
func hasDSHData(root string) bool {
	loc, err := resolveLocation(root)
	if err != nil {
		return false
	}
	info, err := os.Stat(loc.SessionsDir)
	return err == nil && info.IsDir()
}

func refUpdatedAt(ref sessionport.SessionRef) time.Time {
	if !ref.UpdatedAt.IsZero() {
		return ref.UpdatedAt
	}
	if info, err := os.Stat(ref.File); err == nil {
		return info.ModTime()
	}
	return time.Time{}
}
