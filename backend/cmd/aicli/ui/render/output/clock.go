package output

import (
	"sync"
	"time"
)

// Clock 抽象时间源。gateway 自己创建的 timeout、TTL 和 close timer 都使用
// 注入的 clock；caller context 的取消仍作为外部输入处理。
type Clock interface {
	Now() time.Time
	NewTimer(time.Duration) ClockTimer
}

// ClockTimer 是 Clock.NewTimer 的返回类型；NewTimer(d<=0) 必须立即 ready。
type ClockTimer interface {
	C() <-chan time.Time
	Stop() bool
}

// SystemClock 使用真实时间。
type SystemClock struct{}

// Now 返回当前时间。
func (SystemClock) Now() time.Time { return time.Now() }

// NewTimer 创建真实 timer。
func (SystemClock) NewTimer(d time.Duration) ClockTimer {
	return &systemClockTimer{t: time.NewTimer(d)}
}

type systemClockTimer struct {
	t *time.Timer
}

func (s *systemClockTimer) C() <-chan time.Time { return s.t.C }
func (s *systemClockTimer) Stop() bool          { return s.t.Stop() }

// FakeClock 是确定性测试时钟：Now() 返回当前虚拟时间，NewTimer 创建
// 手动推进的 timer。Advance 会触发到达到期时间的 timer（对应通道 ready）。
// 与真实 timer 不同，FakeClock 的 timer 是惰性的：即使不 Advance，
// Stop/未读取也不会泄漏 goroutine。
type FakeClock struct {
	mu       sync.Mutex
	now      time.Time
	timers   []*fakeClockTimer
	seq      uint64
	duration time.Duration // 每次 Advance 的固定步长；零值表示任意步长
}

// NewFakeClock 以固定起始时间创建 FakeClock。
func NewFakeClock(start time.Time) *FakeClock {
	return &FakeClock{now: start}
}

// NewFakeClockWithStep 创建固定步长时钟（测试可读性更好）。
func NewFakeClockWithStep(start time.Time, step time.Duration) *FakeClock {
	return &FakeClock{now: start, duration: step}
}

func (f *FakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *FakeClock) NewTimer(d time.Duration) ClockTimer {
	if f == nil {
		return &fakeClockTimer{}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	deadline := f.now.Add(d)
	f.seq++
	t := &fakeClockTimer{clock: f, when: deadline, seq: f.seq, ch: make(chan time.Time, 1)}
	if d <= 0 {
		t.ready = true
		t.ch <- deadline
	}
	f.timers = append(f.timers, t)
	return t
}

// Advance 把时钟推进 d；所有到期 timer 的通道变为 ready（每 timer 只触发一次，
// 触发后从活动集合移除）。
func (f *FakeClock) Advance(d time.Duration) time.Time {
	if f.duration > 0 {
		d = f.duration
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
	remain := f.timers[:0]
	for _, t := range f.timers {
		if t.stop || t.ready {
			// A timer that was already fired (including an immediate timer)
			// must not be retained in the active set. Keeping it here makes a
			// later Advance repeatedly inspect a timer that can never fire.
			continue
		}
		if !t.when.After(f.now) {
			t.ready = true
			select {
			case t.ch <- t.when:
			default:
			}
		} else {
			remain = append(remain, t)
		}
	}
	f.timers = remain
	return f.now
}

// SetNow 直接设置当前时间（不触发 timer，仅测试元数据用）。
func (f *FakeClock) SetNow(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = t
}

func (f *FakeClock) remove(t *fakeClockTimer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removeLocked(t)
}

// removeLocked 调用方必须持有 f.mu。
func (f *FakeClock) removeLocked(t *fakeClockTimer) {
	for i, x := range f.timers {
		if x == t {
			f.timers = append(f.timers[:i], f.timers[i+1:]...)
			break
		}
	}
}

type fakeClockTimer struct {
	clock *FakeClock
	when  time.Time
	seq   uint64
	ready bool
	ch    chan time.Time
	stop  bool
}

func (f *fakeClockTimer) C() <-chan time.Time { return f.ch }

func (f *fakeClockTimer) Stop() bool {
	if f.clock == nil {
		return true
	}
	f.clock.mu.Lock()
	defer f.clock.mu.Unlock()
	if f.stop || f.ready {
		return false
	}
	f.stop = true
	f.clock.removeLocked(f)
	return true
}

// JournalLimit 是有界观察存储（event journal / delivery journal）的容量。
// 生产配置两个字段都为正；超限丢弃最旧已封存观察记录，绝不改变 primary。
type JournalLimit struct {
	MaxItems int
	MaxBytes int // sanitized record/event 的估算 retained bytes
}

// Validate 校验 JournalLimit；测试显式注入大上限，不能用零值暗示无限。
func (l JournalLimit) Validate() error {
	if l.MaxItems <= 0 || l.MaxBytes <= 0 {
		return NewClassifiedError(DeliveryErrorInvalid, "journal limit must be positive")
	}
	return nil
}
