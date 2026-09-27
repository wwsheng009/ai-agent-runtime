package toolbroker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// task_monitor 的模型面取值域：check 默认 45s、夹取 5s..10min；最长时长夹取
// 5s..1h。越界按夹取处理（与 task_output 的 timeout_ms 同一惯例），结果里回报
// 生效值，模型无需靠猜测。
const (
	taskMonitorCheckAfterDefaultMs = int64(45_000)
	taskMonitorCheckAfterMinMs     = int64(5_000)
	taskMonitorCheckAfterMaxMs     = int64(600_000)
	taskMonitorMaxDurationMinMs    = int64(5_000)
	taskMonitorMaxDurationMaxMs    = int64(3_600_000)
)

func clampMonitorMs(value, min, max int64) int64 {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

// monitorBackgroundTask 挂上一次性的到点巡检：到点时宿主把 job 的当前状态投影成
// 一条 durable inbox 记录 + progress 类唤醒，模型因此不必轮询 task_output。
// 未知 job 走可修复错误；已结束的 job 走「内容结果 + scheduled=false」（终态唤醒
// 本来就会覆盖它）。
func (b *Broker) monitorBackgroundTask(ctx context.Context, resolvedJobID, displayJobID string, args map[string]interface{}) (interface{}, map[string]interface{}, error) {
	checkAfterMs, hasCheck, err := toolArgInt64(ToolTaskMonitor, args, "check_after_ms")
	if err != nil {
		return nil, nil, err
	}
	if !hasCheck || checkAfterMs <= 0 {
		checkAfterMs = taskMonitorCheckAfterDefaultMs
	} else {
		checkAfterMs = clampMonitorMs(checkAfterMs, taskMonitorCheckAfterMinMs, taskMonitorCheckAfterMaxMs)
	}
	maxDurationMs, hasMaxDuration, err := toolArgInt64(ToolTaskMonitor, args, "max_duration_ms")
	if err != nil {
		return nil, nil, err
	}

	// 撤单与排期互斥：含糊的组合（是替换还是两次动作？）会让模型难以从结果推断
	// 生效状态，因此明确要求分两次调用。
	if brokerTaskBoolArg(args, "cancel") {
		if hasCheck || hasMaxDuration {
			return nil, nil, fmt.Errorf("cancel=true cannot be combined with check_after_ms/max_duration_ms; disarm first, then arm a new check")
		}
		cancelled := b.Background.CancelJobMonitors(resolvedJobID)
		metadata := map[string]interface{}{
			toolresult.MetadataKey: toolresult.KindStructured,
			"job_id":               displayJobID,
			"job_alias":            displayJobID,
			"cancelled_monitors":   cancelled,
			"scheduled":            false,
		}
		return map[string]interface{}{
			"job_id":             displayJobID,
			"cancelled_monitors": cancelled,
			"scheduled":          false,
			"message":            fmt.Sprintf("%d monitor(s) disarmed; the job keeps running and its terminal transition still wakes the session", cancelled),
		}, metadata, nil
	}

	options := background.MonitorOptions{CheckAfter: time.Duration(checkAfterMs) * time.Millisecond}
	if hasMaxDuration && maxDurationMs > 0 {
		options.MaxDuration = time.Duration(clampMonitorMs(maxDurationMs, taskMonitorMaxDurationMinMs, taskMonitorMaxDurationMaxMs)) * time.Millisecond
	}

	info, monitorErr := b.Background.ScheduleMonitor(resolvedJobID, options)
	metadata := map[string]interface{}{
		toolresult.MetadataKey: toolresult.KindStructured,
		"job_id":               displayJobID,
		"job_alias":            displayJobID,
	}
	if monitorErr != nil {
		if runtimeerrors.Is(monitorErr, runtimeerrors.ErrJobNotFound) {
			return nil, nil, fmt.Errorf("%w; use the exact job_id returned by background_task instead of guessing an id", monitorErr)
		}
		if errors.Is(monitorErr, background.ErrJobNotRunning) {
			// 已结束不是错误：终态唤醒已经覆盖这次交接，模型只需读结果。
			metadata["scheduled"] = false
			result := map[string]interface{}{
				"job_id":    displayJobID,
				"scheduled": false,
				"message":   "job already finished; nothing to monitor, read the outcome with task_output",
			}
			if job, getErr := b.Background.GetJob(ctx, resolvedJobID); getErr == nil && job != nil {
				metadata["status"] = string(job.Status)
				result["status"] = string(job.Status)
				result["exit_code"] = job.ExitCode
			}
			return result, metadata, nil
		}
		return nil, nil, monitorErr
	}

	metadata["monitor_id"] = info.MonitorID
	metadata["scheduled"] = true
	metadata["check_after_ms"] = info.CheckAfter.Milliseconds()
	if info.MaxDuration > 0 {
		metadata["max_duration_ms"] = info.MaxDuration.Milliseconds()
	}
	result := map[string]interface{}{
		"job_id":         displayJobID,
		"monitor_id":     info.MonitorID,
		"scheduled":      true,
		"check_after_ms": info.CheckAfter.Milliseconds(),
		"message":        "one check wake is scheduled; the terminal transition wakes the session on its own, so no polling is needed. Call task_monitor again for another check, or task_kill to disarm by ending the job.",
	}
	if info.MaxDuration > 0 {
		result["max_duration_ms"] = info.MaxDuration.Milliseconds()
		result["message"] = fmt.Sprintf(
			"one check wake is scheduled and the job is terminated at max_duration_ms if it is still running. Cancel source is %s.",
			background.CancelSourceMonitorMaxDuration)
	}
	return result, metadata, nil
}

// monitorJobIDArg 解析 job_id/task_id 别名参数，两条键都缺时给出可修复错误。
func monitorJobIDArg(args map[string]interface{}) string {
	jobID := strings.TrimSpace(brokerTaskStringArg(args, "job_id"))
	if jobID == "" {
		jobID = strings.TrimSpace(brokerTaskStringArg(args, "task_id"))
	}
	return jobID
}

// brokerTaskBoolArg 读取布尔参数；kinds 表已保证类型正确，字符串分支只是容错
// （历史会话里模型可能把 "true" 塞进 JSON）。
func brokerTaskBoolArg(args map[string]interface{}, key string) bool {
	switch typed := args[key].(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1", "yes":
			return true
		}
	}
	return false
}
