package scene

// RenderText 是渲染层切换的文本投影层（unified plan §14.6 P4–P6 的
// presenter 最小闭环：Scene 快照 → 最终文本行）。
//
// 输入与 LayoutTranscript 相同（语义 cell 数组 + policyVersion），输出是
// presenter/审计可直接消费的最终文本行序列：boundary gap 行投影为空字符串
// ""，cell 内容行投影为原始 source 行（保留内部空行，不做 TrimSpace）。
// 该投影不含 ANSI/样式/宽度换行——样式与 width-aware DisplayLines 属于
// presenter 层，本函数只承诺“行结构与语义内容”与 LayoutTranscript 一致，
// 供渲染层切换时对照旧路径输出（旧路径完整块 = [可选 gap 空行] + 内容行）。
//
// 约束：
//   - 空 cell / 被过滤 cell 不产生行（INV-GAP-05，委托 LayoutTranscript）；
//   - 首 cell 前无 gap（transcript 不以空行开头）；
//   - gap 决策全部来自 boundary.ResolveGap 规则表（INV-GAP-03），本层无特例；
//   - 无状态纯函数：replay 与 live 复用同一投影（§2.3 不变量 9）。
// 注意：gap 位置判定请以 LayoutTranscript 的 LayoutRow.Gap 为准（本投影
// 只把 gap 行投影为空字符串，语义上不与 cell 内部空行区分；内部空行属于
// source，见 §7.2）。parity 测试与 /debug 审计对照旧路径空行时，应与
// row.Gap 联合使用（旧路径完整块 = [可选 gap 空行] + 内容行，gap 空行
// 位置 == row.Gap>0 的行序）。
func RenderText(cells []*TranscriptCell, policyVersion uint64) []string {
	rows := LayoutTranscript(cells, policyVersion)
	if len(rows) == 0 {
		return nil
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.Gap > 0 {
			out = append(out, "")
			continue
		}
		out = append(out, row.Text)
	}
	return out
}
