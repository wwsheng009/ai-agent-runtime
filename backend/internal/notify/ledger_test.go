package notify

import (
	"sync"
	"testing"
	"time"
)

func TestMemoryLedgerClaimDedupes(t *testing.T) {
	l := NewMemoryLedger(16)
	if !l.Claim("k1") {
		t.Fatal("首次认领必须成功")
	}
	if l.Claim("k1") {
		t.Fatal("重复认领必须失败")
	}
	if !l.Claim("k2") {
		t.Fatal("不同键互不影响")
	}
	if !l.Claim("") || !l.Claim("") {
		t.Fatal("空键 = 不去重：Claim(\"\") 应恒 true")
	}
}

func TestMemoryLedgerEvictsOldest(t *testing.T) {
	l := NewMemoryLedger(2)
	l.Claim("a")
	l.Claim("b")
	l.Claim("c") // 触发淘汰 a
	if _, ok := l.Receipt("a"); ok {
		t.Fatal("最旧键应被淘汰")
	}
	if l.Claim("b") {
		t.Fatal("未淘汰键仍应保持认领态")
	}
	if !l.Claim("a") {
		t.Fatal("淘汰后同键可重新认领")
	}
}

func TestMemoryLedgerCompleteOnlyForClaimed(t *testing.T) {
	l := NewMemoryLedger(4)
	l.Complete("ghost", Receipt{})
	if _, ok := l.Receipt("ghost"); ok {
		t.Fatal("未认领键的 Complete 必须忽略（不落账）")
	}
	l.Claim("k")
	l.Complete("k", Receipt{Key: "k", At: time.Now(), Outcomes: []ChannelOutcome{{Channel: "c1", OK: true}}})
	r, ok := l.Receipt("k")
	if !ok || len(r.Outcomes) != 1 || !r.Outcomes[0].OK {
		t.Fatalf("回执读取 = %+v ok=%v", r, ok)
	}
}

func TestMemoryLedgerConcurrentClaimSingleWinner(t *testing.T) {
	l := NewMemoryLedger(8)
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.Claim("same-key") {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("并发认领同一键的赢家数 = %d，want 1", wins)
	}
}
