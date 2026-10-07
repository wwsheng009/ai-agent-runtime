package ui

import (
	"sync"
	"sync/atomic"
	"time"
)

// planWorkerRequestCapacity 是请求通道容量。reducer 每轮只会有一个在飞请求，
// 容量 1 + 覆盖语义保证派发永不阻塞 actor。
const planWorkerRequestCapacity = 1

// asyncTranscriptPlanWorker 是 P1.2 Stage B2 的 plan worker 句柄，同时实现
// transcriptPlanSink。
//
// 线程约束：RequestTranscriptPlanWindow 会在 actor 持有 c.mu 的 reduce 阶段被调用
// （controller.go 的 reduce 落在 c.mu 内），因此它**绝不能**访问 c.mu/cond —— 只用
// 原子旗标与 channel。worker goroutine 通过 Post 回投结果；Post 会取 c.mu，但那是
// 另一个 goroutine，不构成重入。
type asyncTranscriptPlanWorker struct {
	enabled  uint32 // atomic：1 = worker 在跑，0 = 未启动或已停
	req      chan transcriptPlanWindowRequest
	done     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// RequestTranscriptPlanWindow 非阻塞派发。槽位被未消费的旧请求占用时覆盖它：旧
// 请求的结果永不会回来，reducer 侧 seq 栅栏会把它当作"被更新的请求取代"（在飞
// 请求只会被新的派发覆盖，而新派发只发生在 in-flight 已被结果结算之后，实际路径
// 上覆盖是防御性的）。
func (w *asyncTranscriptPlanWorker) RequestTranscriptPlanWindow(req transcriptPlanWindowRequest) bool {
	if w == nil || atomic.LoadUint32(&w.enabled) == 0 {
		return false
	}
	select {
	case <-w.done:
		return false
	default:
	}
	select {
	case w.req <- req:
		return true
	default:
	}
	select {
	case <-w.req:
	default:
	}
	select {
	case w.req <- req:
		return true
	default:
		return false
	}
}

// planSinkForReduce 在 reduce 前注入 worker 句柄；未配置时返回 nil，reducer 走
// Stage A 的锁内同步规划（默认路径）。
func (c *UIController) planSinkForReduce() transcriptPlanSink {
	if c == nil || !c.planWorkerConfigured {
		return nil
	}
	return &c.planWorker
}

// planInFlightLocked 报告是否还有未结算的委派窗口。Close 之后不再等待：worker 已
// 停、结果不会再回来，等待会让 Close 后的 WaitIdle 挂死（既有 teardown 依赖它）。
// 调用方必须持有 c.mu。
func (c *UIController) planInFlightLocked() bool {
	return !c.closed && c.state.HistoryEffects.planRequestInFlight
}

// startPlanWorker 由 Run 在进入消费循环前调用一次（Run 是单 goroutine）。
func (c *UIController) startPlanWorker() {
	if c == nil || !c.planWorkerConfigured {
		return
	}
	atomic.StoreUint32(&c.planWorker.enabled, 1)
	c.planWorker.wg.Add(1)
	go c.runPlanWorker()
}

func (c *UIController) runPlanWorker() {
	defer c.planWorker.wg.Done()
	for {
		select {
		case <-c.planWorker.done:
			return
		case req := <-c.planWorker.req:
			rows, screenMs := screenTranscriptPlanWindowRequest(req)
			action := HistoryPlanWindowReady{
				seq:             req.Seq,
				planInputsEpoch: req.PlanInputsEpoch,
				inputs:          req.Inputs,
				rows:            rows,
				screenDuration:  screenMs,
			}
			if !c.Post(action) {
				// Close 之后放弃结果：规划随会话关闭一起收敛。
				return
			}
		}
	}
}

// stopPlanWorker 在 Close 时停止 worker：先撤 enabled（让后续派发回退同步），再
// 关闭 done。不关闭请求通道——Run 的排空阶段仍可能在锁内 reduce 里派发，关闭
// 通道会让那些派发 panic（审查生命周期项）。
func (c *UIController) stopPlanWorker() {
	if c == nil || !c.planWorkerConfigured {
		return
	}
	atomic.StoreUint32(&c.planWorker.enabled, 0)
	c.planWorker.stopOnce.Do(func() { close(c.planWorker.done) })
}

// WaitPlanWorker 等待 worker goroutine 退出（测试/teardown 用，避免泄漏断言）。
// timeout<=0 表示无限等待。
func (c *UIController) WaitPlanWorker(timeout time.Duration) bool {
	if c == nil || !c.planWorkerConfigured {
		return true
	}
	done := make(chan struct{})
	go func() {
		c.planWorker.wg.Wait()
		close(done)
	}()
	if timeout <= 0 {
		<-done
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

// AsyncTranscriptPlanEnabled 报告本控制器是否配置了 plan worker（P1.2 Stage B2）。
// 生产 wiring 自检用：commands 侧断言真实会话 actors 不再锁内同步 screening。
func (c *UIController) AsyncTranscriptPlanEnabled() bool {
	if c == nil {
		return false
	}
	return c.planWorkerConfigured
}
