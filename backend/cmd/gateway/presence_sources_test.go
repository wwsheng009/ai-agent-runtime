package main

import (
	"testing"
	"time"
)

// TestPresenceRegistryMultiSource 锁定多源在场模型（docs/bots/10 §3.2）：
// 多源并存任一生效即在场；host 源不参与 tail watch；Release 只释放 console 源。
func TestPresenceRegistryMultiSource(t *testing.T) {
	p := newPresenceRegistry(50 * time.Millisecond)
	p.hostTTL = 50 * time.Millisecond

	p.TouchSource("host", "n1", "s1", true) // host 源的 tail 应被忽略
	if !p.Active("n1", "s1") {
		t.Fatal("host 源应计入在场")
	}
	if got := p.TailWatched("n1"); len(got) != 0 {
		t.Fatalf("host 源不得参与 tail watch：%v", got)
	}

	p.Touch("n1", "s1", true)
	if got := p.TailWatched("n1"); len(got) != 1 || got[0] != "s1" {
		t.Fatalf("console tail = %v", got)
	}

	// Release 只释放 console 源：host 源继续生效，tail 清空。
	if n := p.Release("n1", "s1"); n != 1 {
		t.Fatalf("release count = %d, want 1", n)
	}
	if !p.Active("n1", "s1") {
		t.Fatal("释放 console 后 host 源应继续生效")
	}
	if got := p.TailWatched("n1"); len(got) != 0 {
		t.Fatalf("console 释放后 tail 应清空：%v", got)
	}

	// host 源到期后完全失效。
	time.Sleep(80 * time.Millisecond)
	if p.Active("n1", "s1") || p.Count() != 0 {
		t.Fatal("host 源到期后应完全失效")
	}
}
