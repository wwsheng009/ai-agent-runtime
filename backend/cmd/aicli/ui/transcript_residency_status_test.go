package ui

import (
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// transcript_residency_status_test.go —— T8b-5 契约（ui 侧）：
// §15.2 分区由真实计数器派生；§15.3 字节估算随 source 单调增长。

func TestTranscriptResidencyStatsSectionsFromCounters(t *testing.T) {
	before := TranscriptResidencyCountersSnapshot()
	NoteTranscriptResidencyEviction(3)
	NoteTranscriptResidencyWriteThrough(4, 1)
	NoteTranscriptResidencyRematerialize(2, 1, 512, 7*time.Millisecond)

	stats := TranscriptResidencyStatsSnapshot()
	if stats.Evictions == nil {
		t.Fatal("evictions 分区必须输出")
	}
	if stats.Evictions.Total-before.EvictedCells != 3 {
		t.Fatalf("evictions.total 增量不符：%d -> %d", before.EvictedCells, stats.Evictions.Total)
	}
	if stats.Rematerialize == nil {
		t.Fatal("rematerialize 分区必须输出")
	}
	if stats.Rematerialize.Total-before.RematerializedCells != 2 ||
		stats.Rematerialize.Failures-before.RematerializeFailures != 1 ||
		stats.Rematerialize.BytesTotal-before.RematerializeBytes != 512 {
		t.Fatalf("rematerialize 增量不符：%+v（before=%+v）", stats.Rematerialize, before)
	}
	if stats.Rematerialize.MaxMs < 7 {
		t.Fatalf("max_ms 未取历史最大值：%d", stats.Rematerialize.MaxMs)
	}
	if stats.BodyStore == nil ||
		stats.BodyStore.Puts-before.BodyStorePuts != 4 ||
		stats.BodyStore.PutErrors-before.BodyStorePutErrors != 1 {
		t.Fatalf("body_store 分区不符：%+v（before=%+v）", stats.BodyStore, before)
	}
	if len(stats.Sources) == 0 || stats.Note == "" {
		t.Fatalf("快照必须标注来源与口径说明：sources=%v note=%q", stats.Sources, stats.Note)
	}
}

func TestTranscriptResidencyRematerializeInflightGuard(t *testing.T) {
	before := TranscriptResidencyCountersSnapshot().RematerializeInflight
	NoteTranscriptResidencyRematerializeStart()
	NoteTranscriptResidencyRematerializeStart()
	if got := TranscriptResidencyCountersSnapshot().RematerializeInflight - before; got != 2 {
		t.Fatalf("inflight 增量不符：%d", got)
	}
	NoteTranscriptResidencyRematerializeDone()
	NoteTranscriptResidencyRematerializeDone()
	NoteTranscriptResidencyRematerializeDone() // 多余 Done 必须被钳到 0
	if got := TranscriptResidencyCountersSnapshot().RematerializeInflight; got != 0 {
		t.Fatalf("inflight 必须钳到 0：%d", got)
	}
}

func TestEstimateTranscriptCellBytesScalesWithSource(t *testing.T) {
	if EstimateTranscriptCellBytes(nil) != 0 {
		t.Fatal("nil cell 估算必须为 0")
	}
	small := EstimateTranscriptCellBytes(&scene.TranscriptCell{Source: "短"})
	large := EstimateTranscriptCellBytes(&scene.TranscriptCell{Source: "长内容 " + string(make([]byte, 0)) + repeatString("x", 4096)})
	if large <= small {
		t.Fatalf("source 增长必须反映在估算上：small=%d large=%d", small, large)
	}
	sceneCells := []*scene.TranscriptCell{{Source: repeatString("y", 2048)}}
	if got := EstimateResidentSceneCellsBytes(sceneCells); got < 2048 {
		t.Fatalf("scene 估算未计 source 字节：%d", got)
	}
	valueCells := []scene.TranscriptCell{{Source: repeatString("z", 2048)}}
	if got := EstimateAppStateCellsBytes(valueCells); got < 2048 {
		t.Fatalf("appstate 估算未计 source 字节：%d", got)
	}
}

func repeatString(token string, count int) string {
	out := make([]byte, 0, len(token)*count)
	for index := 0; index < count; index++ {
		out = append(out, token...)
	}
	return string(out)
}
