package toolbroker

// job_list / job_search (naming contract B1, tool-surface-inventory §4.1):
// the job_* read surface answers "what is running" without a job_id. Both are
// read-only façades over background.Manager.ListJobs — no new storage. The
// command filter in job_search does a case-insensitive substring scan client-
// side, mirroring the store's ListJobs filter surface.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/background"
)

// executeJobList implements job_list: enumerate background jobs filtered by
// session/status with a bounded page. Results are newest-first by creation.
func (b *Broker) executeJobList(ctx context.Context, sessionID string, args map[string]interface{}) (interface{}, map[string]interface{}, error) {
	if b.Background == nil {
		return nil, nil, fmt.Errorf("background manager is not configured")
	}
	filter := background.JobFilter{Limit: 50}
	if value := strings.TrimSpace(brokerTaskStringArg(args, "session_id")); value != "" {
		filter.SessionID = value
	}
	if value, ok, err := toolArgInt64(ToolJobList, args, "limit"); err != nil {
		return nil, nil, err
	} else if ok && value > 0 {
		if value > 200 {
			value = 200
		}
		filter.Limit = int(value)
	}
	statuses, err := jobStatusFilter(args)
	if err != nil {
		return nil, nil, err
	}
	filter.Status = statuses
	jobs, err := b.Background.ListJobs(ctx, filter)
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].CreatedAt.After(jobs[j].CreatedAt)
	})
	payload := make([]map[string]interface{}, 0, len(jobs))
	for _, job := range jobs {
		payload = append(payload, jobSummaryMap(job))
	}
	return map[string]interface{}{
		"jobs":  payload,
		"count": len(payload),
	}, nil, nil
}

// executeJobSearch implements job_search: case-insensitive substring scan over
// job commands (plus id/cwd) within the same bounded filter surface.
func (b *Broker) executeJobSearch(ctx context.Context, sessionID string, args map[string]interface{}) (interface{}, map[string]interface{}, error) {
	if b.Background == nil {
		return nil, nil, fmt.Errorf("background manager is not configured")
	}
	query := strings.ToLower(strings.TrimSpace(brokerTaskStringArg(args, "query")))
	if query == "" {
		return nil, nil, fmt.Errorf("query is required")
	}
	filter := background.JobFilter{Limit: 200}
	if value := strings.TrimSpace(brokerTaskStringArg(args, "session_id")); value != "" {
		filter.SessionID = value
	}
	jobs, err := b.Background.ListJobs(ctx, filter)
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].CreatedAt.After(jobs[j].CreatedAt)
	})
	payload := make([]map[string]interface{}, 0, len(jobs))
	for _, job := range jobs {
		haystack := strings.ToLower(job.Command + "\n" + job.ID + "\n" + job.Cwd)
		if !strings.Contains(haystack, query) {
			continue
		}
		payload = append(payload, jobSummaryMap(job))
		if len(payload) >= 50 {
			break
		}
	}
	return map[string]interface{}{
		"jobs":  payload,
		"count": len(payload),
	}, nil, nil
}

// jobStatusFilter parses the optional status filter (repeatable enum).
func jobStatusFilter(args map[string]interface{}) ([]background.JobStatus, error) {
	raw, ok := args["status"]
	if !ok || raw == nil {
		return nil, nil
	}
	appendStatus := func(value string) (background.JobStatus, error) {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "pending":
			return background.StatusPending, nil
		case "running":
			return background.StatusRunning, nil
		case "completed":
			return background.StatusCompleted, nil
		case "failed":
			return background.StatusFailed, nil
		case "cancelled":
			return background.StatusCancelled, nil
		case "timed_out":
			return background.StatusTimedOut, nil
		case "interrupted":
			return background.StatusInterrupted, nil
		case "expired":
			return background.StatusExpired, nil
		case "abandoned":
			return background.StatusAbandoned, nil
		case "paused":
			return background.StatusPaused, nil
		default:
			return "", fmt.Errorf("unsupported status %q: use pending, running, completed, failed, cancelled, timed_out, interrupted, expired, abandoned or paused", value)
		}
	}
	switch typed := raw.(type) {
	case string:
		status, err := appendStatus(typed)
		if err != nil {
			return nil, err
		}
		return []background.JobStatus{status}, nil
	case []interface{}:
		statuses := make([]background.JobStatus, 0, len(typed))
		for _, item := range typed {
			value, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("status entries must be strings")
			}
			status, err := appendStatus(value)
			if err != nil {
				return nil, err
			}
			statuses = append(statuses, status)
		}
		return statuses, nil
	default:
		return nil, fmt.Errorf("status must be a string or an array of strings")
	}
}

// jobSummaryMap projects one job into the model-visible summary shape (the
// same fields the jobs list HTTP handler exposes, minus output bytes).
func jobSummaryMap(job background.Job) map[string]interface{} {
	summary := map[string]interface{}{
		"id":         job.ID,
		"status":     string(job.Status),
		"kind":       job.Kind,
		"command":    job.Command,
		"created_at": job.CreatedAt,
	}
	if job.SessionID != "" {
		summary["session_id"] = job.SessionID
	}
	if job.Cwd != "" {
		summary["cwd"] = job.Cwd
	}
	if job.FinishedAt != nil {
		summary["finished_at"] = *job.FinishedAt
	}
	if job.ExitCode != nil {
		summary["exit_code"] = *job.ExitCode
	}
	if job.Message != "" {
		summary["message"] = job.Message
	}
	return summary
}
