package ui

import (
	"encoding/json"
	"strings"
	"testing"
)

// transcript_residency_test.go —— 驻留开关的契约测试：默认值、夹紧、快照口径。
// 设计文档：§14.2/§15.1/§15.2。

func resetTranscriptResidencyForTest(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		ConfigureTranscriptResidency(DisabledTranscriptResidencyOptions())
	})
}

// TestTranscriptResidencyDefaults T10-4：总开关默认开启，逐出默认关闭（canary）。
func TestTranscriptResidencyDefaults(t *testing.T) {
	opts := DefaultTranscriptResidencyOptions()
	if !opts.Enabled {
		t.Fatal("T10-4 起总开关默认必须开启")
	}
	if opts.EvictPrefix {
		t.Fatal("逐出必须保持默认关闭（独立灰度开关）")
	}
	// 显式关闭档必须真的关掉两者（回退入口）。
	disabled := DisabledTranscriptResidencyOptions()
	if disabled.Enabled || disabled.EvictPrefix {
		t.Fatalf("显式关闭档必须关闭：%+v", disabled)
	}
	if opts.MaxResidentCells != 512 || opts.MaxResidentRows != 4096 || opts.MaxResidentMiB != 24 {
		t.Fatalf("L1 默认上限不符（§15.1）：cells=%d rows=%d mib=%d",
			opts.MaxResidentCells, opts.MaxResidentRows, opts.MaxResidentMiB)
	}
	if opts.PageCells != 256 || opts.PageMiB != 2 {
		t.Fatalf("页默认值不符（§15.1）：cells=%d mib=%d", opts.PageCells, opts.PageMiB)
	}
	if opts.EvictHighWater != 1.25 || opts.BodyStore != "sqlite" {
		t.Fatalf("水位/写穿默认值不符：water=%v store=%q", opts.EvictHighWater, opts.BodyStore)
	}
}

func TestConfigureTranscriptResidencyClampsInvalidValues(t *testing.T) {
	resetTranscriptResidencyForTest(t)
	ConfigureTranscriptResidency(TranscriptResidencyOptions{
		Enabled:          true,
		EvictPrefix:      true,
		EvictHighWater:   0.5, // <1 → 默认 1.25
		MaxResidentCells: -3,  // <=0 → 默认 512
		BodyStore:        "",  // 空 → 默认 sqlite
	})
	opts := TranscriptResidencyOptionsSnapshot()
	if !opts.Enabled || !opts.EvictPrefix {
		t.Fatalf("显式开启必须生效：%+v", opts)
	}
	if opts.MaxResidentCells != 512 || opts.EvictHighWater != 1.25 || opts.BodyStore != "sqlite" {
		t.Fatalf("非法值未被夹紧到默认：%+v", opts)
	}
}

func TestTranscriptResidencyStatsSnapshotShape(t *testing.T) {
	resetTranscriptResidencyForTest(t)
	ConfigureTranscriptResidency(TranscriptResidencyOptions{Enabled: true})
	stats := TranscriptResidencyStatsSnapshot()
	if !stats.Enabled {
		t.Fatalf("stats.Enabled 未反映配置")
	}
	if stats.Note == "" {
		t.Fatalf("T0 快照必须带 note（标注骨架/零值占位）")
	}
	// 计数器是进程级全局状态：同包其它路径（pager/写穿）可能已累加，
	// 因此这里只断言字段可读（形状契约）；累加语义由
	// TestTranscriptResidencyCountersRecord 用增量断言覆盖。
	_ = stats.Counters
}

func TestTranscriptResidencyEvictPrefixRequiresEnabled(t *testing.T) {
	resetTranscriptResidencyForTest(t)
	ConfigureTranscriptResidency(TranscriptResidencyOptions{Enabled: false, EvictPrefix: true})
	if stats := TranscriptResidencyStatsSnapshot(); stats.EvictPrefix {
		t.Fatalf("enabled=false 时 evict_prefix 必须被钳制为 false")
	}
}

func TestTranscriptResidencyCountersRecord(t *testing.T) {
	before := TranscriptResidencyCountersSnapshot()
	noteTranscriptResidencyEviction(false)
	noteTranscriptResidencyEviction(true)
	noteTranscriptResidencyRematerialize(false)
	noteTranscriptResidencyRematerialize(true)
	noteTranscriptResidencyBodyStorePut(false)
	noteTranscriptResidencyBodyStorePut(true)
	noteTranscriptResidencyBodyStoreGet(false)
	noteTranscriptResidencyBodyStoreGet(true)
	after := TranscriptResidencyCountersSnapshot()
	if after.EvictedCells-before.EvictedCells != 1 ||
		after.EvictionsPinned-before.EvictionsPinned != 1 ||
		after.RematerializedCells-before.RematerializedCells != 1 ||
		after.RematerializeFailures-before.RematerializeFailures != 1 ||
		after.BodyStorePuts-before.BodyStorePuts != 2 ||
		after.BodyStorePutErrors-before.BodyStorePutErrors != 1 ||
		after.BodyStoreGets-before.BodyStoreGets != 2 ||
		after.BodyStoreGetMisses-before.BodyStoreGetMisses != 1 {
		t.Fatalf("计数器增量不符：before=%+v after=%+v", before, after)
	}
}

func TestTranscriptResidencyStatsJSONShape(t *testing.T) {
	resetTranscriptResidencyForTest(t)
	ConfigureTranscriptResidency(TranscriptResidencyOptions{Enabled: true})
	stats := TranscriptResidencyStatsSnapshot()
	raw, err := json.Marshal(stats)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	text := string(raw)
	// 状态契约（设计文档 §15.2）：字段名固定，供 /debug/chat/status 消费。
	for _, key := range []string{`"enabled"`, `"evict_prefix"`, `"options"`, `"counters"`, `"note"`} {
		if !strings.Contains(text, key) {
			t.Fatalf("JSON 缺少 %s：%s", key, text)
		}
	}
}
