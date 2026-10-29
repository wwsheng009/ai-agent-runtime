package workbuddy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/sessionport"
)

// Detect probes explicit roots or the standard WORKBUDDY_HOME /
// %USERPROFILE%\.workbuddy[-ai] roots. Directories are only accepted after the
// SQLite schema and projects/ layout both validate.
func Detect(roots []string, agent string) ([]sessionport.Instance, error) {
	switch strings.ToLower(strings.TrimSpace(agent)) {
	case "", agentID, "workbuddy-ai":
	default:
		return nil, fmt.Errorf("不支持的 Agent: %s（当前支持 deepseek-harness|workbuddy）", agent)
	}
	explicit := len(roots) > 0
	if !explicit {
		roots = DefaultRoots()
	}
	seen := map[string]struct{}{}
	instances := make([]sessionport.Instance, 0, len(roots))
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		key := strings.ToLower(filepath.Clean(root))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if !explicit {
			if _, err := os.Stat(root); err != nil {
				continue
			}
		}
		instance, err := DetectInstance(root, true, "", "")
		if err != nil {
			if explicit {
				return nil, err
			}
			continue
		}
		instances = append(instances, instance)
	}
	sort.Slice(instances, func(i, j int) bool { return instances[i].Root < instances[j].Root })
	return instances, nil
}

// DetectInstance probes one WorkBuddy home, workbuddy.db, projects directory or
// session JSONL. dbPath/projectsDir are explicit non-standard-layout overrides.
func DetectInstance(root string, includeEmpty bool, dbPath, projectsDir string) (sessionport.Instance, error) {
	loc, err := resolveLocation(root, dbPath, projectsDir)
	if err != nil {
		return sessionport.Instance{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := openDB(loc.DBPath)
	if err != nil {
		return sessionport.Instance{}, err
	}
	defer db.Close()
	schema, err := dbSchema(db, ctx)
	if err != nil {
		return sessionport.Instance{}, fmt.Errorf("WorkBuddy 数据库签名校验失败: %w", err)
	}
	sessions, err := queryDBSessions(ctx, db)
	if err != nil {
		return sessionport.Instance{}, err
	}
	files := scanProjectFiles(loc.ProjectsDir)
	instance := sessionport.Instance{
		AgentID:       agentID,
		InstanceID:    instanceID(loc.Home),
		Root:          loc.Home,
		SessionsDir:   loc.ProjectsDir,
		DBPath:        loc.DBPath,
		ProjectsDir:   loc.ProjectsDir,
		Format:        formatName,
		FormatVersion: currentFormat,
		FormatStatus:  "current",
		AppVersion:    appVersion(loc.Home),
		SchemaVersion: schema,
		Backend:       "sqlite+jsonl",
		Compression:   "none",
		Confidence:    0.98,
		Capabilities:  sessionport.Capabilities{List: true, Read: true, WriteBundle: true, Apply: "none"},
		Warnings:      make([]string, 0),
	}
	if len(sessions) == 0 && len(files) == 0 {
		if includeEmpty {
			instance.FormatStatus = "empty"
		} else {
			instance.FormatStatus = "empty"
		}
		instance.Confidence = 0.72
		instance.Warnings = append(instance.Warnings, "WorkBuddy DB 和 projects 中没有会话")
		return instance, nil
	}
	if len(files) > 0 {
		if err := validateProjectSignature(files); err != nil {
			instance.Warnings = append(instance.Warnings, "projects JSONL 签名校验失败: "+err.Error())
			instance.Confidence = 0.78
		}
	}
	dbIDs := make(map[string]struct{}, len(sessions))
	missingFiles := 0
	for _, session := range sessions {
		dbIDs[session.ID] = struct{}{}
		if _, ok := newestProjectFile(files, session.ID); !ok {
			missingFiles++
		}
		if busy, reason := runtimeBusy(loc.Home, session.ID); busy {
			instance.Busy = true
			if instance.BusyReason == "" {
				instance.BusyReason = reason
			}
		}
	}
	orphans := 0
	for id := range files {
		if _, ok := dbIDs[id]; !ok {
			orphans++
		}
	}
	instance.SessionCount = len(sessions) + orphans
	instance.TranscriptCount = len(files)
	if missingFiles > 0 {
		instance.Warnings = append(instance.Warnings, fmt.Sprintf("%d 条 DB 会话没有对应的 projects/*.jsonl（可能是云端会话或已清理正文）", missingFiles))
		instance.Confidence = 0.88
	}
	if orphans > 0 {
		instance.Warnings = append(instance.Warnings, fmt.Sprintf("%d 个 JSONL 文件没有 DB 索引（可能是手工复制或索引损坏）", orphans))
		instance.Confidence = 0.84
	}
	return instance, nil
}

// validateProjectSignature parses a small prefix, not the whole transcript. A
// valid WorkBuddy file starts with session-meta and then message/control records.
func validateProjectSignature(files map[string][]sessionFile) error {
	for _, entries := range files {
		for _, file := range entries {
			fh, err := os.Open(file.Path)
			if err != nil {
				continue
			}
			scanner := bufio.NewScanner(fh)
			scanner.Buffer(make([]byte, 8*1024), 256*1024)
			seen := false
			for lineNo := 0; lineNo < 32 && scanner.Scan(); lineNo++ {
				line := strings.TrimPrefix(strings.TrimSpace(scanner.Text()), "\ufeff")
				var value struct {
					Type string `json:"type"`
				}
				if json.Unmarshal([]byte(line), &value) != nil {
					continue
				}
				switch value.Type {
				case "message", "function_call", "function_call_result":
					seen = true
				}
				if seen {
					break
				}
			}
			_ = fh.Close()
			if seen {
				return nil
			}
		}
	}
	return fmt.Errorf("没有可识别的 WorkBuddy 事件")
}

// List returns DB-indexed sessions plus orphan JSONL files. It parses each
// readable log so titles/event counts reflect the actual history.
func List(instance sessionport.Instance) ([]sessionport.SessionRef, error) {
	if strings.TrimSpace(instance.DBPath) == "" || strings.TrimSpace(instance.ProjectsDir) == "" {
		return nil, fmt.Errorf("WorkBuddy 实例缺少 db_path/projects_dir")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := openDB(instance.DBPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if _, err := dbSchema(db, ctx); err != nil {
		return nil, err
	}
	sessions, err := queryDBSessions(ctx, db)
	if err != nil {
		return nil, err
	}
	sortDBSessions(sessions)
	files := scanProjectFiles(instance.ProjectsDir)
	refs := make([]sessionport.SessionRef, 0, len(sessions)+len(files))
	seen := map[string]struct{}{}
	for _, session := range sessions {
		ref := sessionport.SessionRef{
			AgentID:       agentID,
			InstanceID:    instance.InstanceID,
			SessionID:     session.ID,
			Title:         firstNonEmpty(session.CustomTitle, session.Title),
			CWD:           session.CWD,
			FormatVersion: currentFormat,
			FormatStatus:  "current",
			Compression:   "none",
			Readable:      false,
			UpdatedAt:     millisTime(firstNonZero(session.LastActivityAt, session.UpdatedAt, session.CreatedAt)),
		}
		if file, ok := newestProjectFile(files, session.ID); ok {
			ref.File = file.Path
			ref.SizeBytes = file.Size
			seen[session.ID] = struct{}{}
			loaded, loadErr := loadRecords(file.Path, ref)
			if loadErr != nil {
				ref.Warnings = append(ref.Warnings, loadErr.Error())
			} else {
				ref.EventCount = len(loaded.Records)
				if loaded.CWD != "" {
					ref.CWD = loaded.CWD
				}
				if strings.TrimSpace(loaded.Title) != "" {
					ref.Title = firstNonEmpty(ref.Title, loaded.Title)
				}
				if !loaded.Ref.UpdatedAt.IsZero() {
					ref.UpdatedAt = loaded.Ref.UpdatedAt
				}
				ref.Readable = true
			}
		} else {
			ref.Warnings = append(ref.Warnings, "DB 索引存在，但 projects 中没有对应 JSONL 正文")
		}
		if busy, reason := runtimeBusy(instance.Root, session.ID); busy {
			ref.Warnings = append(ref.Warnings, reason)
		}
		refs = append(refs, ref)
	}
	for id, entries := range files {
		if _, ok := seen[id]; ok {
			continue
		}
		ref := sessionport.SessionRef{
			AgentID:       agentID,
			InstanceID:    instance.InstanceID,
			SessionID:     id,
			File:          entries[0].Path,
			SizeBytes:     entries[0].Size,
			FormatVersion: currentFormat,
			FormatStatus:  "current",
			Compression:   "none",
			Readable:      true,
			Warnings:      []string{"projects 中存在 JSONL，但 workbuddy.db 没有对应 sessions 索引"},
		}
		if loaded, loadErr := loadRecords(entries[0].Path, ref); loadErr != nil {
			ref.Readable = false
			ref.Warnings = append(ref.Warnings, loadErr.Error())
		} else {
			ref.EventCount = len(loaded.Records)
			ref.Title = loaded.Title
			ref.CWD = loaded.CWD
			ref.UpdatedAt = loaded.Ref.UpdatedAt
		}
		refs = append(refs, ref)
	}
	sort.SliceStable(refs, func(i, j int) bool {
		if !refs[i].UpdatedAt.Equal(refs[j].UpdatedAt) {
			return refs[i].UpdatedAt.After(refs[j].UpdatedAt)
		}
		return refs[i].SessionID < refs[j].SessionID
	})
	return refs, nil
}

func millisTime(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(value)
}

func firstNonZero(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}
