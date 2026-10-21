package commands

import (
	"encoding/json"
	"fmt"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// chat_residency_status.go —— T8b-5：`/debug/chat/status#app_state.residency`
// 的 §15.2 投影 + §15.3 分区内存口径。
//
// 口径：ui 层提供开关/选项/计数器/布局缓存预算；本文件现场补充
//   - `resident`：bridge scene 的驻留窗口（cells/rows/bytes/start seq/gen），
//   - `cold`：写穿裁剪路径固化的冷区累计（cells/bytes/delivered rows/gens），
//   - `evictions`：逐出计数与最后原因（bridge 会计口径优先），
//   - `body_store`：sqlite 写穿 store 自报计数（无 store 时回落 ui 计数），
//   - `memory`：§15.3 分区字节估算（-1 = 当前不可归因，绝不伪装成 0）。
//
// `index`（L2 冷区/窗口账本）目前未在生产路径安装，登记到 `unavailable` 而不是
// 输出零值。

// residencyBodyStoreCounter 是写穿 store 的计数接口（sqlite 适配器实现）。
type residencyBodyStoreCounter interface {
	Stats() runtimechat.SessionCellStoreStats
}

// residentRowsProbeLimit 是 resident.rows 的现场布局上限：超过则不探测并报 -1
// （与 memory 分区的 -1 约定一致），避免轮询端点在超长会话上做 O(transcript) 布局。
const residentRowsProbeLimit = 8192

// chatResidencyStatsForSession 采集 §15.2 快照。state/effects 可为 nil（无 uiActor）。
func chatResidencyStatsForSession(session *ChatSession, state *ui.UIControllerState, effects *ui.HistoryEffectDiagnostics) ui.TranscriptResidencyStats {
	stats := ui.TranscriptResidencyStatsSnapshot()

	if runtime := currentTranscriptResidencyRuntime(); runtime != nil {
		if runtime.StorePath != "" {
			stats.Sources = append(stats.Sources, "runtime.store")
		}
		// StoreErr 既包含真实故障，也包含刻意的惰性跳过（evictPrefix=false）。
		// 只有前者才登记 unavailable；后者属于正常档位，附在 sources 里说明即可。
		if runtime.StoreErr != "" && runtime.Store == nil && runtime.Options.EvictPrefix {
			stats.Unavailable = append(stats.Unavailable, "body_store: "+runtime.StoreErr)
		} else if runtime.StoreErr != "" && runtime.Store == nil {
			stats.Sources = append(stats.Sources, "body_store.lazy_off: "+runtime.StoreErr)
		}
		if counter, ok := runtime.Store.(residencyBodyStoreCounter); ok {
			storeStats := counter.Stats()
			stats.BodyStore = &ui.TranscriptResidencyBodyStoreStats{
				Puts:      storeStats.Puts,
				Gets:      storeStats.Gets,
				PutErrors: storeStats.Errors,
				GetMisses: storeStats.Misses,
			}
			stats.Sources = append(stats.Sources, "store.sqlite")
		}
	} else {
		stats.Unavailable = append(stats.Unavailable, "body_store: residency runtime not installed (disabled or fail-closed)")
	}

	var bridge *chatRuntimeEventBridge
	if session != nil {
		bridge = session.RuntimeEventBridge
	}
	if bridge != nil {
		if accounting, ok := bridge.sceneTrimAccounting(); ok {
			stats.Cold = &ui.TranscriptResidencyColdStats{
				Cells:         accounting.TrimmedCells,
				Bytes:         accounting.TrimmedBytes,
				DeliveredRows: accounting.TrimmedRows,
				Generations:   residencyGenerationList(accounting.GenerationMin, accounting.GenerationMax),
			}
			// 生产逐出口径 = bridge 写穿裁剪；总计数以它为准（ui 计数另有回填路径）。
			if accounting.TrimmedCells > 0 || accounting.Batches > 0 {
				stats.Evictions.Total = uint64(accounting.TrimmedCells)
			}
			if accounting.LastReason != "" {
				stats.Evictions.LastReason = accounting.LastReason
			}
			stats.Sources = append(stats.Sources, "bridge.scene_trim")
			if accounting.RestoredCells > 0 {
				stats.Unavailable = append(stats.Unavailable,
					fmt.Sprintf("fail-closed restore: %d cells restored after write-through failure (batches=%d, errors=%d)",
						accounting.RestoredCells, accounting.Batches, accounting.RestoreErrors))
			}
			if accounting.LastError != "" {
				stats.Unavailable = append(stats.Unavailable, "scene_trim last_error: "+accounting.LastError)
			}
		}
		if snapshot := bridge.sceneSnapshot(); snapshot != nil {
			resident := &ui.TranscriptResidencyResidentStats{
				Cells:          len(snapshot.Cells),
				Rows:           -1,
				Bytes:          ui.EstimateResidentSceneCellsBytes(snapshot.Cells),
				WindowStartSeq: residencyFirstCellSequence(snapshot.Cells),
				Generation:     snapshot.Revision,
			}
			if len(snapshot.Cells) > 0 && len(snapshot.Cells) <= residentRowsProbeLimit {
				resident.Rows = len(scene.LayoutTranscript(snapshot.Cells, snapshot.Revision))
			} else if len(snapshot.Cells) > residentRowsProbeLimit {
				stats.Unavailable = append(stats.Unavailable,
					fmt.Sprintf("resident.rows: cell count %d exceeds probe limit %d", len(snapshot.Cells), residentRowsProbeLimit))
			}
			stats.Resident = resident
			stats.Sources = append(stats.Sources, "bridge.scene")
		}
	} else {
		stats.Unavailable = append(stats.Unavailable, "resident/cold: no runtime event bridge (headless or pre-start session)")
	}

	// L2 索引（T10 A）：bridge 的裁剪策略持有真实冷区账本时输出真实值；
	// 未安装（residency 关闭 / evictPrefix=false / store 不可用）时显式登记。
	var residency *ui.TranscriptResidency
	if bridge != nil {
		if accounting, ok := bridge.sceneTrimAccounting(); ok {
			_ = accounting
		}
		bridge.sceneMu.Lock()
		if policy := bridge.sceneTrimPolicy; policy != nil {
			residency = policy.Residency
		}
		bridge.sceneMu.Unlock()
	}
	if residency != nil && residency.Cold.Len() > 0 {
		stats.Index = &ui.TranscriptResidencyIndexStats{
			PrefixFence: residency.Cold.LastPrefixFence(),
			FoldTarget:  uint64(residency.LastFoldedCellID()),
			Revision:    residency.Window.Generation,
		}
		if stats.Cold != nil {
			stats.Cold.Cells = residency.Cold.Len()
			stats.Cold.Bytes = residency.Cold.TotalBodyBytes()
			stats.Cold.DeliveredRows = residency.Cold.Rows.Total()
			stats.Cold.Generations = residency.Cold.RowGenerations()
		}
		stats.Sources = append(stats.Sources, "bridge.cold_ledger")
	} else {
		stats.Unavailable = append(stats.Unavailable,
			"index: cold ledger not installed (residency disabled, evictPrefix=false, or no evictions yet)")
	}

	stats.Memory = chatResidencyMemoryStatsFor(session, state, effects)
	return stats
}

// residencyGenerationList 把 [min,max] 代区间展开为去重的升序小列表（上限 8 个：
// 诊断只需要看"跨了多少代"，不复制整段历史）。
func residencyGenerationList(minGeneration, maxGeneration uint64) []uint64 {
	if minGeneration == 0 || maxGeneration == 0 {
		return nil
	}
	if minGeneration == maxGeneration {
		return []uint64{minGeneration}
	}
	list := make([]uint64, 0, 4)
	list = append(list, minGeneration)
	if maxGeneration > minGeneration+1 {
		list = append(list, minGeneration+2)
	}
	list = append(list, maxGeneration)
	return list
}

// residencyFirstCellSequence 返回首个非 nil cell 的 Sequence（窗口起点标识）。
func residencyFirstCellSequence(cells []*scene.TranscriptCell) uint64 {
	for _, cell := range cells {
		if cell != nil {
			return cell.Sequence
		}
	}
	return 0
}

// chatResidencyMemoryStatsFor 采集 §15.3 分区口径。字节为估算（见 ui 的估算函数
// 注释）；不可归因的分区保持 -1，并把原因登记到调用方的 unavailable。
func chatResidencyMemoryStatsFor(session *ChatSession, state *ui.UIControllerState, effects *ui.HistoryEffectDiagnostics) *ui.TranscriptResidencyMemoryStats {
	memory := &ui.TranscriptResidencyMemoryStats{
		SceneCellsBytes:      -1,
		AppStateCellsBytes:   -1,
		LedgerBytes:          -1,
		SeedUnitsBytes:       -1,
		SessionMessagesBytes: -1,
		RenderCacheBytes:     -1,
	}
	cacheStats := ui.TranscriptLayoutCacheStatsSnapshot()
	memory.RenderCacheBytes = int64(cacheStats.CellRowsBytes + cacheStats.PlanBytes + cacheStats.FoldOmitBytes)

	if session != nil && session.RuntimeEventBridge != nil {
		if snapshot := session.RuntimeEventBridge.sceneSnapshot(); snapshot != nil {
			memory.SceneCellsBytes = ui.EstimateResidentSceneCellsBytes(snapshot.Cells)
		}
	}
	if state != nil {
		memory.AppStateCellsBytes = ui.EstimateAppStateCellsBytes(state.AppState.Transcript.Cells)
	}
	if effects != nil {
		// 交付账本只有条目数口径（见 HistoryEffectDiagnostics 注释），字节不可得。
		memory.LedgerEntries = effects.Summary.LedgerEntries
	}
	if session != nil && session.RuntimeSession != nil {
		history := session.RuntimeSession.History
		memory.SessionMessages = len(history)
		memory.SessionMessagesBytes = estimateRuntimeMessagesBytes(history)
	}
	if session != nil {
		// 展示历史是独立的第三份数据面（既不等于模型投影，也不等于屏幕窗口）。
		// 它曾长期未计量：18k 条常驻无法在 §15.3 中归因。装载交付后会裁剪到
		// resumeHistoryRetainedAfterDelivery；trimmed 计数是"降级不得静默"的证据。
		displayed := session.resumeHistorySnapshot()
		memory.ResumeHistoryMessages = len(displayed)
		memory.ResumeHistoryBytes = estimateRuntimeMessagesBytes(displayed)
		_, memory.ResumeHistoryTrimmed = resumeHistoryTrimStats(session)
	}
	return memory
}

// estimateRuntimeMessagesBytes 估算会话消息的内存占用（§15.3 归因口径）。
//
// 必须计入 Metadata：早期实现只累加 Role/Content/parts/tool calls，导致
// 18.7k 条的生产会话被估成 60.4 MB，而 pprof 实测同一批消息在 Go 堆中占据
// 157 MB（encoding/json/v2.makeString 122 MB + reflect.mapassign 41.5 MB）。
// 差额几乎全部来自 Metadata —— 它是 map[string]interface{}，其内存表示约为
// JSON 字节数的 3~4 倍（map 桶、string 头、interface 头、boxed 值）。
// 该会话实测 metadata JSON 43.9 MB，其中 tool_metadata 16.6 MB、
// reasoning_details 12.8 MB。归因口径若漏掉它，85% 的堆就无法解释。
func estimateRuntimeMessagesBytes(messages []runtimetypes.Message) int64 {
	const (
		// Go 中 map[string]interface{} 的摊销开销（桶数组 + _type 指针 +
		// string header + interface header）相对 JSON 字节数的经验倍数。
		metadataJSONOverhead = 3
		// 每条消息的固定结构开销（Message 头、切片头、时间戳等）。
		perMessageOverhead = 128
	)
	var total int64
	for index := range messages {
		message := messages[index]
		total += int64(len(message.Role) + len(message.Content) + perMessageOverhead)
		for _, part := range message.ContentParts {
			total += int64(len(part.Text) + len(part.ImageURL) + 32)
		}
		for _, call := range message.ToolCalls {
			total += int64(len(call.Name) + len(call.ID) + 48)
			for key, value := range call.Args {
				total += int64(len(key))
				if text, ok := value.(string); ok {
					total += int64(len(text))
				} else {
					total += 16
				}
			}
		}
		total += estimateRuntimeMetadataBytes(message.Metadata, metadataJSONOverhead)
	}
	return total
}

// estimateRuntimeMetadataBytes 估算单条消息 Metadata 的驻留字节。
//
// 口径：逐个键值累加键名、字符串值、以及容器/标量的近似开销，再乘以
// metadataJSONOverhead —— 因为 map[string]interface{} 的运行时表示远大于
// 序列化后的文本（桶空位、字符串头、interface 头、boxed 标量）。
// 对嵌套 map/slice 递归计算（reasoning_details 与 tool_metadata 都是嵌套结构）。
func estimateRuntimeMetadataBytes(metadata map[string]interface{}, overhead int64) int64 {
	if len(metadata) == 0 {
		return 0
	}
	var raw int64
	for key, value := range metadata {
		raw += int64(len(key)) + estimateRuntimeMetadataValueBytes(value)
	}
	// map 头 + 桶数组空位：按条目数给固定摊销。
	raw += int64(len(metadata)) * 48
	return raw * overhead
}

// estimateRuntimeMetadataValueBytes 递归估算单个 metadata 值的文本量（不含倍数）。
func estimateRuntimeMetadataValueBytes(value interface{}) int64 {
	switch typed := value.(type) {
	case nil:
		return 8
	case string:
		return int64(len(typed)) + 16
	case bool, int, int32, int64, uint, uint32, uint64, float32, float64:
		return 16
	case []interface{}:
		total := int64(24)
		for _, item := range typed {
			total += estimateRuntimeMetadataValueBytes(item)
		}
		return total
	case []string:
		total := int64(24)
		for _, item := range typed {
			total += int64(len(item)) + 16
		}
		return total
	case map[string]interface{}:
		total := int64(48)
		for key, item := range typed {
			total += int64(len(key)) + estimateRuntimeMetadataValueBytes(item)
		}
		return total
	default:
		// 其他结构（例如工具自定义载荷）：退回序列化长度估算。
		if encoded, err := json.Marshal(typed); err == nil {
			return int64(len(encoded))
		}
		return 64
	}
}
