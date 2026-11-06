package sessionpurge

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Paths 是参与会话清退的本地数据库。空路径表示该库不存在/不参与。
type Paths struct {
	History        string
	Cells          string
	Runtime        string
	UsageAnalytics string
	Artifacts      string
	AgentControl   string
	Background     string
	Schedule       string
	Team           string
}

// Options 控制按龄会话清退。
type Options struct {
	OlderThan      time.Duration
	IncludeOrphans bool
	IncludeTeam    bool
	DryRun         bool
	SkipRowCounts  bool
	// Profile 输出候选发现、保护检查和各库处理的分阶段耗时，
	// 用于定位 GB 级 SQLite 上的清理瓶颈，不改变删除语义。
	Profile bool
	// FastCandidates 只做按龄候选选择和活跃保护，不扫描明细表寻找 orphan，
	// 也不逐表统计预计删除行数。用于“命令已验证、无需再次预演”的直接执行。
	FastCandidates bool
	Now            time.Time
	// ArtifactSessionRoot 是 session-artifacts 目录，默认取 history 库同目录。
	ArtifactSessionRoot string
}

type TableResult struct {
	Table  string `json:"table"`
	Rows   int64  `json:"rows"`
	Detail string `json:"detail,omitempty"`
}

type StoreResult struct {
	Name         string        `json:"name"`
	Path         string        `json:"path"`
	Status       string        `json:"status"`
	Tables       []TableResult `json:"tables,omitempty"`
	ArtifactDirs int64         `json:"artifact_dirs,omitempty"`
	Error        string        `json:"error,omitempty"`
}

type ProtectedSession struct {
	SessionID string   `json:"session_id"`
	Reasons   []string `json:"reasons"`
}

type Report struct {
	Status              string             `json:"status"`
	GeneratedAt         time.Time          `json:"generated_at"`
	Cutoff              time.Time          `json:"cutoff"`
	OlderThan           string             `json:"older_than"`
	HistorySessions     int                `json:"history_sessions"`
	KnownCandidates     int                `json:"known_candidates"`
	OrphanCandidates    int                `json:"orphan_candidates"`
	ProtectedCandidates int                `json:"protected_candidates"`
	CandidateCount      int                `json:"candidate_count"`
	CandidateSamples    []string           `json:"candidate_samples,omitempty"`
	ProtectedSamples    []ProtectedSession `json:"protected_samples,omitempty"`
	Stores              []StoreResult      `json:"stores,omitempty"`
	Warnings            []string           `json:"warnings,omitempty"`
	DurationMS          int64              `json:"duration_ms"`
	TimingsMS           map[string]int64   `json:"timings_ms,omitempty"`
	FastCandidates      bool               `json:"fast_candidates,omitempty"`
}

type purgeTimer struct {
	enabled bool
	report  *Report
}

func (t *purgeTimer) stage(name string, fn func() error) error {
	if t == nil || !t.enabled {
		return fn()
	}
	started := time.Now()
	defer func() {
		if t.report.TimingsMS == nil {
			t.report.TimingsMS = make(map[string]int64)
		}
		t.report.TimingsMS[name] = time.Since(started).Milliseconds()
	}()
	return fn()
}

func normalizeOptions(opts Options) Options {
	if opts.OlderThan <= 0 {
		opts.OlderThan = 30 * 24 * time.Hour
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now().UTC()
	} else {
		opts.Now = opts.Now.UTC()
	}
	return opts
}

// Run 选择超过 OlderThan 未活动的会话，并跨 history/runtime/analytics/artifacts/
// agent-control/background/schedule（可选 team）执行级联删除。
//
// DryRun=true 时只统计，不写入任何库。
func Run(ctx context.Context, paths Paths, opts Options) (Report, error) {
	started := time.Now()
	opts = normalizeOptions(opts)
	cutoff := opts.Now.Add(-opts.OlderThan)
	report := Report{
		Status:         "planned",
		GeneratedAt:    opts.Now,
		Cutoff:         cutoff,
		OlderThan:      opts.OlderThan.String(),
		FastCandidates: opts.FastCandidates,
	}
	timer := &purgeTimer{enabled: opts.Profile, report: &report}
	if !opts.DryRun {
		report.Status = "pruned"
		if root := artifactSessionRoot(paths.History, opts.ArtifactSessionRoot); root != "" {
			if err := cleanupArtifactPurgeRetries(root, opts.Now, artifactPurgeRetryAge); err != nil {
				report.Warnings = append(report.Warnings, "artifact retry cleanup failed: "+err.Error())
			}
		}
	}

	var history []historySession
	if err := timer.stage("candidate_history", func() (err error) {
		history, err = loadHistorySessions(ctx, paths.History)
		return err
	}); err != nil {
		return report, err
	}
	report.HistorySessions = len(history)
	historyIDs := make(map[string]struct{}, len(history))
	candidates := make(map[string]string)
	for _, session := range history {
		historyIDs[session.ID] = struct{}{}
		if !session.HasTime || !session.UpdatedAt.Before(cutoff) {
			continue
		}
		if session.ExpiresAt.After(opts.Now) {
			continue
		}
		candidates[session.ID] = "history"
		report.KnownCandidates++
	}

	if opts.IncludeOrphans && !opts.FastCandidates {
		var lastSeen map[string]time.Time
		if err := timer.stage("candidate_last_seen", func() (err error) {
			lastSeen, err = loadLastSeen(ctx, paths, opts)
			return err
		}); err != nil {
			return report, err
		}
		for sessionID, seen := range lastSeen {
			if _, exists := historyIDs[sessionID]; exists {
				continue
			}
			if seen.Before(cutoff) {
				candidates[sessionID] = "orphan"
				report.OrphanCandidates++
			}
		}
	}

	var protection map[string][]string
	if err := timer.stage("protection", func() (err error) {
		protection, err = loadProtection(ctx, paths, candidates, opts, cutoff, timer)
		return err
	}); err != nil {
		return report, err
	}
	finalIDs := make([]string, 0, len(candidates))
	protectedSamples := make([]ProtectedSession, 0)
	for sessionID := range candidates {
		reasons := protection[sessionID]
		if len(reasons) > 0 {
			report.ProtectedCandidates++
			if len(protectedSamples) < 50 {
				protectedSamples = append(protectedSamples, ProtectedSession{
					SessionID: sessionID,
					Reasons:   append([]string(nil), reasons...),
				})
			}
			continue
		}
		finalIDs = append(finalIDs, sessionID)
	}
	sort.Strings(finalIDs)
	sort.Slice(protectedSamples, func(i, j int) bool {
		return protectedSamples[i].SessionID < protectedSamples[j].SessionID
	})
	report.ProtectedSamples = protectedSamples
	report.CandidateCount = len(finalIDs)
	if len(finalIDs) > 20 {
		report.CandidateSamples = append([]string(nil), finalIDs[:20]...)
	} else {
		report.CandidateSamples = append([]string(nil), finalIDs...)
	}
	if len(finalIDs) == 0 {
		report.DurationMS = time.Since(started).Milliseconds()
		return report, nil
	}

	storeRuns := []struct {
		name string
		path string
		fn   func(context.Context, string, []string, Options) (StoreResult, error)
	}{
		{"history", paths.History, purgeHistoryStore},
		{"cells", paths.Cells, purgeCellsStore},
		{"runtime", paths.Runtime, purgeRuntimeStore},
		{"analytics", paths.UsageAnalytics, purgeAnalyticsStore},
		{"artifacts", paths.Artifacts, purgeArtifactsStore},
		{"agent-control", paths.AgentControl, purgeAgentControlStore},
		{"background", paths.Background, purgeBackgroundStore},
		{"schedule", paths.Schedule, purgeScheduleStore},
	}
	if opts.IncludeTeam {
		storeRuns = append(storeRuns, struct {
			name string
			path string
			fn   func(context.Context, string, []string, Options) (StoreResult, error)
		}{"team", paths.Team, purgeTeamStore})
	} else if strings.TrimSpace(paths.Team) != "" {
		report.Warnings = append(report.Warnings, "team store skipped; pass --include-team to include teams/teammates")
	}
	if strings.TrimSpace(paths.Schedule) == "" {
		report.Warnings = append(report.Warnings, "schedule store path is empty; schedule rows were not purged")
	}

	for _, run := range storeRuns {
		var result StoreResult
		if err := timer.stage("store_"+run.name, func() (err error) {
			storeOpts := opts
			if opts.FastCandidates && !opts.DryRun {
				storeOpts.SkipRowCounts = true
			}
			result, err = run.fn(ctx, run.path, finalIDs, storeOpts)
			return err
		}); err != nil {
			result.Status = "failed"
			result.Error = err.Error()
			report.Stores = append(report.Stores, result)
			report.Status = "partial"
			report.DurationMS = time.Since(started).Milliseconds()
			return report, fmt.Errorf("%s purge: %w", run.name, err)
		}
		report.Stores = append(report.Stores, result)
	}
	report.DurationMS = time.Since(started).Milliseconds()
	return report, nil
}
