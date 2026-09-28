package knowledge

import (
	"sort"
	"sync"
	"time"
)

// LockWaitStats 是锁等待的进程内采样摘要（06 §4 Phase 1 交付 5 的"锁等待 p95"）。
//
// 口径：样本来自本进程**写路径**（execWrite → RetryLockedCtxObserved）的每次
// 锁冲突等待，值为该次调用累计的退避时长；RetryFailures 统计重试耗尽（最终
// 以锁错误失败）的次数。这是进程内、有界、无持久化的观测面：
//   - 进程重启即清零（"当前进程经历过的锁竞争"才是诊断所需的语义）；
//   - 采样环固定 128 条，p95 是**近似分位**（最近样本窗口内）；
//   - 单写者拓扑下正常稳态应接近全零；非零即说明有并发写者或长事务。
type LockWaitStats struct {
	// Samples 是当前窗口内的样本数（≤ 128）。
	Samples int `json:"samples"`
	// P50MS / P95MS / MaxMS 是等待时长分位（毫秒，浮点以保留亚毫秒精度）。
	P50MS float64 `json:"p50_ms"`
	P95MS float64 `json:"p95_ms"`
	MaxMS float64 `json:"max_ms"`
	// RetryFailures 是锁重试耗尽（写操作最终失败）的累计次数。
	RetryFailures int `json:"retry_failures"`
}

// lockWaitSamples 是采样环容量：状态面只需要近期形态，固定上限保证内存有界。
const lockWaitSamples = 128

// lockWaitRecorder 是并发安全的有界锁等待采样器；零值可用。
type lockWaitRecorder struct {
	mu       sync.Mutex
	ring     [lockWaitSamples]time.Duration
	next     int
	count    int
	failures int
}

// observe 记录一次等待（execWrite 单次调用的累计退避）。
func (r *lockWaitRecorder) observe(wait time.Duration) {
	if r == nil || wait <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ring[r.next] = wait
	r.next = (r.next + 1) % lockWaitSamples
	if r.count < lockWaitSamples {
		r.count++
	}
}

// observeFailure 记录一次锁重试耗尽。
func (r *lockWaitRecorder) observeFailure() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failures++
}

// snapshot 返回当前窗口的分位摘要。
func (r *lockWaitRecorder) snapshot() LockWaitStats {
	if r == nil {
		return LockWaitStats{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	stats := LockWaitStats{Samples: r.count, RetryFailures: r.failures}
	if r.count == 0 {
		return stats
	}
	values := make([]time.Duration, r.count)
	copy(values, r.ring[:r.count])
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	stats.P50MS = durationMS(percentile(values, 0.50))
	stats.P95MS = durationMS(percentile(values, 0.95))
	stats.MaxMS = durationMS(values[len(values)-1])
	return stats
}

// percentile 返回升序切片上的最近秩分位（nearest-rank，n≥1）。
func percentile(sorted []time.Duration, q float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(q*float64(len(sorted)) + 0.999999) // ceil(q*n)
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

func durationMS(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}
