package tools

// viewWindowMetadata mirrors the text path's window metadata for derived
// renders (documents, notebooks). Both surfaces must publish the same
// continuation fields, otherwise the model cannot tell a line-truncated window
// from a byte-budget one and tail reads lose their resolved offset
// (analysis §3.6/§3.9 window semantics).
func viewWindowMetadata(p ViewFileRequest, readMeta viewReadResult) map[string]interface{} {
	windowOffset := p.Offset
	windowLimit := p.Limit
	if readMeta.Tail {
		// Tail reads report the resolved absolute window, not the negative
		// request: continuation metadata must be usable as-is.
		windowOffset = readMeta.WindowStart
		windowLimit = readMeta.LinesRead
	}
	metadata := map[string]interface{}{
		"offset":       windowOffset,
		"limit":        windowLimit,
		"lines_read":   readMeta.LinesRead,
		"eof":          readMeta.EOF,
		"is_truncated": readMeta.HasMore,
	}
	if readMeta.Tail {
		metadata["tail"] = true
		metadata["tail_lines"] = readMeta.LinesRead
	}
	if readMeta.EmptyFile {
		metadata["empty"] = true
	}
	if readMeta.ReaderClampedLines > 0 {
		metadata["reader_clamped_lines"] = readMeta.ReaderClampedLines
		metadata["reader_clamped_bytes"] = readMeta.ReaderClampedBytes
	}
	if readMeta.ByteBudgetApplied {
		metadata["byte_budget_applied"] = true
	}
	if readMeta.Encoding != "" && readMeta.Encoding != fileEncodingUTF8.String() {
		metadata["encoding"] = readMeta.Encoding
	}
	if readMeta.LongLinesTruncated > 0 {
		metadata["long_lines_truncated"] = readMeta.LongLinesTruncated
		metadata["hidden_bytes"] = readMeta.HiddenBytes
	}
	if readMeta.TotalLinesKnown {
		metadata["total_lines"] = readMeta.TotalLines
	}
	return metadata
}
