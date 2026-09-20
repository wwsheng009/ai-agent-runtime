package supervision

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// P0-B（方案 B 轻量版）：进度汇总通道。
//
// 缺口（S3/R1）：父 agent 在自己的 turn 内只能看到"异常/待办"（digest 的
// lifecycle 行），看不到"批次进行到哪了"——3 个子 agent 里 2 个已完成、1 个仍在
// 跑，这个信息既不在 digest 里，也不值得为它新开一张 durable 表。
//
// 因此本文件只做投影：宿主把已经存在的 batch 控制面（subagentbatch store /
// agentcontrol registry）与 live-only 的 SubagentProgressMirror 聚合成
// ProgressGroup，BuildDigest 再把它渲染成预算受限的 progress 区块。三条硬约束：
//
//  1. 不落新表：读的是各宿主既有的 batch / registry 状态（R1 的风险面不扩大）。
//  2. 不改"成功不唤醒"策略：进度只出现在父 turn 的 preflight digest 内，不产生
//     新的唤醒；唤醒仍由 lifecycle 通知驱动（failures/approvals）。
//  3. 未接线时字节级不变：ProgressSource 为 nil 时不渲染任何内容，老宿主的行为
//     与本次改动前完全一致。
type ProgressSource interface {
	// ListProgress returns one group per batch/team the caller's scope owns.
	// Implementations must be read-only and must not create durable rows.
	ListProgress(ctx context.Context, req ProgressRequest) ([]ProgressGroup, error)
}

// ProgressRequest is the host-facing projection input. RootScopeID and
// ParentSessionID are derived by the host from the caller's own scope (the same
// split the digest uses: RootScopeID + TargetParentSessionID); the model can
// name neither.
type ProgressRequest struct {
	RootScopeID     string
	ParentSessionID string
	// RowLimit caps how many non-terminal tasks one group may carry.
	RowLimit int
}

// ProgressTask is one non-terminal child task of a group.
type ProgressTask struct {
	TaskID         string
	ChildSessionID string
	State          string
	LastProgressAt time.Time
	// LastMessage is the optional live-only detail from the host's
	// SubagentProgressMirror (e.g. the tool a child is currently running).
	LastMessage string
}

// ProgressGroup is one batch/team rollup. Terminal groups stay in the list on
// purpose: "3/3 completed" is exactly the line that tells the parent it can
// converge (close) instead of waiting again.
type ProgressGroup struct {
	GroupID   string
	Label     string
	Total     int
	Completed int
	Failed    int
	Running   int
	Pending   int
	// Skipped counts terminal tasks that were never run (dependency failed,
	// upstream task failed, or an explicit skip). It is separate from Failed so
	// "4 tasks: 1 done, 1 failed, 2 skipped" does not read as two failures.
	Skipped  int
	Terminal bool
	// LastProgressAt is the newest progress/heartbeat stamp the host knows for
	// this group.
	LastProgressAt time.Time
	// RunningTasks carries the non-terminal children, newest progress first.
	RunningTasks []ProgressTask
}

// ProgressSummary is the digest-facing, bounded projection of one group.
type ProgressSummary struct {
	GroupID       string         `json:"group_id,omitempty"`
	Label         string         `json:"label,omitempty"`
	Total         int            `json:"total"`
	Completed     int            `json:"completed"`
	Failed        int            `json:"failed"`
	Running       int            `json:"running"`
	Pending       int            `json:"pending,omitempty"`
	Skipped       int            `json:"skipped,omitempty"`
	Terminal      bool           `json:"terminal,omitempty"`
	ProgressAgeMs int64          `json:"progress_age_ms,omitempty"`
	RunningTasks  []ProgressTask `json:"running_tasks,omitempty"`
	Truncated     bool           `json:"truncated,omitempty"`
}

const (
	// maxProgressGroups bounds how many batches one digest may carry; the
	// remaining groups are dropped with a truncation marker instead of eating
	// the parent's context budget (P0-B token 预算).
	maxProgressGroups = 3
	// maxProgressTasksPerGroup bounds the per-group "who is still running" list.
	maxProgressTasksPerGroup = 4
)

// BuildProgressSummary queries the source and normalizes the result into the
// bounded projection the digest renders. A nil source returns (nil, false,
// nil): callers keep their previous, progress-free behavior.
func BuildProgressSummary(ctx context.Context, source ProgressSource, req ProgressRequest, limit int, now time.Time) ([]ProgressSummary, bool, error) {
	if source == nil {
		return nil, false, nil
	}
	if now.IsZero() {
		now = timeNow().UTC()
	}
	groups, err := source.ListProgress(ctx, req)
	if err != nil {
		return nil, false, err
	}
	if len(groups) == 0 {
		return nil, false, nil
	}
	groupLimit := maxProgressGroups
	if limit > 0 && limit < groupLimit {
		groupLimit = limit
	}
	taskLimit := maxProgressTasksPerGroup
	if limit > 0 && limit < taskLimit {
		taskLimit = limit
	}
	rowLimit := req.RowLimit
	if rowLimit > 0 && rowLimit < taskLimit {
		taskLimit = rowLimit
	}
	if taskLimit < 1 {
		taskLimit = 1
	}

	truncated := false
	if len(groups) > groupLimit {
		groups = groups[:groupLimit]
		truncated = true
	}
	out := make([]ProgressSummary, 0, len(groups))
	for _, group := range groups {
		summary := ProgressSummary{
			GroupID:   strings.TrimSpace(group.GroupID),
			Label:     strings.TrimSpace(group.Label),
			Total:     group.Total,
			Completed: group.Completed,
			Failed:    group.Failed,
			Running:   group.Running,
			Pending:   group.Pending,
			Skipped:   group.Skipped,
			Terminal:  group.Terminal,
		}
		if !group.LastProgressAt.IsZero() {
			summary.ProgressAgeMs = now.Sub(group.LastProgressAt).Milliseconds()
			if summary.ProgressAgeMs < 0 {
				summary.ProgressAgeMs = 0
			}
		}
		tasks := group.RunningTasks
		if len(tasks) > taskLimit {
			tasks = tasks[:taskLimit]
			summary.Truncated = true
			truncated = true
		}
		for _, task := range tasks {
			normalized := ProgressTask{
				TaskID:         strings.TrimSpace(task.TaskID),
				ChildSessionID: strings.TrimSpace(task.ChildSessionID),
				State:          strings.TrimSpace(task.State),
				LastProgressAt: task.LastProgressAt,
				LastMessage:    strings.TrimSpace(task.LastMessage),
			}
			if normalized.TaskID == "" {
				normalized.TaskID = normalized.ChildSessionID
			}
			if normalized.TaskID == "" && normalized.State == "" && normalized.LastMessage == "" {
				continue
			}
			summary.RunningTasks = append(summary.RunningTasks, normalized)
		}
		out = append(out, summary)
	}
	if len(out) == 0 {
		return nil, false, nil
	}
	return out, truncated, nil
}

// formatProgressText renders the budget-bounded progress block appended to the
// preflight digest text. It returns "" when there is nothing to show, so a
// wired-but-idle host injects exactly what it did before P0-B.
func formatProgressText(digest *Digest, now time.Time) string {
	if digest == nil || len(digest.Progress) == 0 {
		return ""
	}
	if now.IsZero() {
		now = timeNow().UTC()
	}
	var b strings.Builder
	b.WriteString("\nprogress:\n")
	for _, group := range digest.Progress {
		label := group.Label
		if label == "" {
			label = group.GroupID
		}
		if label == "" {
			label = "batch"
		}
		fmt.Fprintf(&b, "- %s: %d/%d completed", label, group.Completed, group.Total)
		if group.Failed > 0 {
			fmt.Fprintf(&b, ", %d failed", group.Failed)
		}
		if group.Pending > 0 {
			fmt.Fprintf(&b, ", %d pending", group.Pending)
		}
		if group.Running > 0 {
			fmt.Fprintf(&b, ", %d running", group.Running)
		}
		if group.Skipped > 0 {
			fmt.Fprintf(&b, ", %d skipped", group.Skipped)
		}
		if group.Terminal {
			// The batch-done row is resolution=closed, so the evaluator only
			// allows inspect on it (plan §4.1.1/§7.1): advising a control action
			// here would send the parent into a call that is always rejected.
			// close_agent addresses the child session ids and is the executable
			// convergence path (same wording as the BatchDone hint).
			b.WriteString(" (terminal; close the finished children with close_agent if they are no longer needed)")
		} else if group.ProgressAgeMs > 0 {
			fmt.Fprintf(&b, " (last progress %s ago)", formatProgressAge(time.Duration(group.ProgressAgeMs)*time.Millisecond))
		}
		for _, task := range group.RunningTasks {
			name := task.TaskID
			if name == "" {
				name = task.ChildSessionID
			}
			fmt.Fprintf(&b, "\n  - %s", name)
			if task.State != "" {
				fmt.Fprintf(&b, ": %s", task.State)
			}
			// The child session id is what the convergence tools address
			// (close_agent, or control_descendant on a still-unresolved row), so
			// print it whenever it differs from the display name; without it the
			// parent has to look the session up before it can act.
			if task.ChildSessionID != "" && task.ChildSessionID != name {
				fmt.Fprintf(&b, "; session=%s", task.ChildSessionID)
			}
			if !task.LastProgressAt.IsZero() {
				fmt.Fprintf(&b, "; last progress %s ago", formatProgressAge(now.Sub(task.LastProgressAt)))
			} else {
				b.WriteString("; no progress yet")
			}
			if task.LastMessage != "" {
				fmt.Fprintf(&b, "; %s", task.LastMessage)
			}
		}
		if group.Truncated {
			b.WriteString("\n  - (more running children omitted; use supervision_descendants for the full matrix)")
		}
		b.WriteString("\n")
	}
	if digest.ProgressTruncated {
		b.WriteString("- (more batches omitted; use supervision_descendants for the full matrix)\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatProgressAge renders a coarse, model-friendly age. Sub-second ages are
// "just now" so a freshly started child does not look stalled.
func formatProgressAge(age time.Duration) string {
	if age < time.Second {
		return "just now"
	}
	switch {
	case age < time.Minute:
		return fmt.Sprintf("%ds", int(age.Seconds()))
	case age < time.Hour:
		return fmt.Sprintf("%dm", int(age.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(age.Hours()))
	}
}
