package toolbroker

import (
	"context"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/background"
)

const (
	// taskOutputWaitDefaultMs 是 wait 模式默认长轮询上限：足够等一个中短任务的
	// 下一步输出，又明显小于常规 provider 请求超时。
	taskOutputWaitDefaultMs = 30_000
	// 边界收敛：太短等于没等，太长会挤占回合预算。
	taskOutputWaitMinMs = 1_000
	taskOutputWaitMaxMs = 120_000
	// 轮询间隔：后台输出落到文件/环形缓冲，250ms 足以覆盖追加感知延迟，
	// 又不会在等待期间制造明显 CPU 占用。
	taskOutputWaitInterval = 250 * time.Millisecond
)

// wait 条件：调用方据此知道「为什么返回」，无需再猜是否该继续等。
const (
	taskOutputConditionImmediate = "immediate"
	taskOutputConditionOutput    = "output"
	taskOutputConditionExit      = "exit"
	taskOutputConditionTimeout   = "timeout"
)

func brokerTaskStringArg(args map[string]interface{}, key string) string {
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}

// jobIDValue prefers the real job id after a mutation and falls back to the
// resolved id when the manager returned no snapshot.
func jobIDValue(job *background.Job, fallback string) string {
	if job != nil && strings.TrimSpace(job.ID) != "" {
		return job.ID
	}
	return fallback
}

// effectiveTaskOutputWaitTimeout 归一化 timeout_ms：<=0 用默认值，超界收敛到边界，
// 保证任何一次等待都是有界的。
func effectiveTaskOutputWaitTimeout(timeoutMs int64) time.Duration {
	switch {
	case timeoutMs <= 0:
		return time.Duration(taskOutputWaitDefaultMs) * time.Millisecond
	case timeoutMs < taskOutputWaitMinMs:
		return time.Duration(taskOutputWaitMinMs) * time.Millisecond
	case timeoutMs > taskOutputWaitMaxMs:
		return time.Duration(taskOutputWaitMaxMs) * time.Millisecond
	default:
		return time.Duration(timeoutMs) * time.Millisecond
	}
}

// readTaskOutputWithWait 实现 task_output 的 wait 语义：
//   - none：立即返回；
//   - output：等到有新输出（offset 之后有字节）或 job 进入终态；
//   - exit：只等到 job 进入终态。
//
// 超时不是错误：返回 timeout 条件与最后一次读取到的状态，模型可据此决定再次
// 等待（继续 offset 增量）还是先做别的事。调用方应优先使用 wait 而不是反复
// 轮询，避免 polling guard 软刹车与多余回合。
func (b *Broker) readTaskOutputWithWait(ctx context.Context, jobID string, offset int64, limit int, waitMode string, timeoutMs int64) (background.TaskOutputResult, string, int64, error) {
	startedAt := time.Now()
	read := func() (background.TaskOutputResult, error) {
		return b.Background.ReadOutput(ctx, background.TaskOutputArgs{JobID: jobID, Offset: offset, Limit: limit})
	}
	match := func(result background.TaskOutputResult) string {
		if background.IsTerminalStatus(background.JobStatus(strings.TrimSpace(result.Status))) {
			return taskOutputConditionExit
		}
		if waitMode == "output" && (result.NextOffset > offset || strings.TrimSpace(result.Output) != "") {
			return taskOutputConditionOutput
		}
		return ""
	}

	result, err := read()
	if err != nil {
		return result, "", 0, err
	}
	if waitMode == "none" {
		return result, taskOutputConditionImmediate, 0, nil
	}
	if condition := match(result); condition != "" {
		return result, condition, time.Since(startedAt).Milliseconds(), nil
	}

	deadline := time.Now().Add(effectiveTaskOutputWaitTimeout(timeoutMs))
	ticker := time.NewTicker(taskOutputWaitInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return result, "", time.Since(startedAt).Milliseconds(), ctx.Err()
		case <-ticker.C:
			result, err = read()
			if err != nil {
				return result, "", time.Since(startedAt).Milliseconds(), err
			}
			if condition := match(result); condition != "" {
				return result, condition, time.Since(startedAt).Milliseconds(), nil
			}
			if time.Now().After(deadline) {
				return result, taskOutputConditionTimeout, time.Since(startedAt).Milliseconds(), nil
			}
		}
	}
}
