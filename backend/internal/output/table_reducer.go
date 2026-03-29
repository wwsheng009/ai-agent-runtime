package output

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"strings"
)

// TableReducer 压缩通用表格输出（pipe/csv/tsv）。
type TableReducer struct{}

// Name 返回 reducer 名称。
func (r *TableReducer) Name() string {
	return "table_summary"
}

// Reduce 识别结构化表格输出并提炼列/行摘要。
func (r *TableReducer) Reduce(_ context.Context, input ReducedInput) (*Envelope, bool, error) {
	table, ok := parseTable(input.Text)
	if !ok {
		return nil, false, nil
	}

	summary := fmt.Sprintf("Parsed %s table: %d rows x %d columns.", table.Format, len(table.Rows), len(table.Columns))
	if len(table.Columns) > 0 {
		summary += "\nColumns: " + strings.Join(table.Columns, ", ")
	}
	if samples := summarizeTableRows(table.Rows, 3); len(samples) > 0 {
		summary += "\nSample rows: " + strings.Join(samples, " | ")
	}

	return &Envelope{
		ToolName:   input.Raw.ToolName,
		ToolCallID: input.Raw.ToolCallID,
		Summary:    summary,
		Error:      strings.TrimSpace(input.Raw.Error),
		Metadata: map[string]interface{}{
			"table_format": table.Format,
			"row_count":    len(table.Rows),
			"column_count": len(table.Columns),
			"columns":      append([]string(nil), table.Columns...),
		},
	}, true, nil
}

type parsedTable struct {
	Format  string
	Columns []string
	Rows    [][]string
}

func parseTable(content string) (parsedTable, bool) {
	lines := normalizedNonEmptyLines(content)
	if len(lines) < 2 {
		return parsedTable{}, false
	}

	if table, ok := parsePipeTable(lines); ok {
		return table, true
	}

	for _, delimiter := range []struct {
		char   rune
		format string
	}{
		{char: '\t', format: "tsv"},
		{char: ',', format: "csv"},
		{char: ';', format: "delimited"},
	} {
		if table, ok := parseDelimitedTable(lines, delimiter.char, delimiter.format); ok {
			return table, true
		}
	}

	return parsedTable{}, false
}

func parsePipeTable(lines []string) (parsedTable, bool) {
	header := splitPipeRow(lines[0])
	if len(header) < 2 {
		return parsedTable{}, false
	}

	startIndex := 1
	if len(lines) > 1 && isPipeDivider(lines[1], len(header)) {
		startIndex = 2
	}

	rows := make([][]string, 0, len(lines)-startIndex)
	for _, line := range lines[startIndex:] {
		row := splitPipeRow(line)
		if len(row) != len(header) {
			if len(rows) == 0 {
				return parsedTable{}, false
			}
			break
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return parsedTable{}, false
	}

	return parsedTable{
		Format:  "pipe",
		Columns: normalizeColumns(header),
		Rows:    rows,
	}, true
}

func splitPipeRow(line string) []string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return nil
	}
	trimmed = strings.TrimPrefix(trimmed, "|")
	trimmed = strings.TrimSuffix(trimmed, "|")
	if !strings.Contains(trimmed, "|") {
		return nil
	}

	parts := strings.Split(trimmed, "|")
	row := make([]string, 0, len(parts))
	for _, part := range parts {
		row = append(row, strings.TrimSpace(part))
	}
	return row
}

func isPipeDivider(line string, expectedColumns int) bool {
	parts := splitPipeRow(line)
	if len(parts) != expectedColumns {
		return false
	}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		part = strings.Trim(part, "-:")
		if part != "" {
			return false
		}
	}
	return true
}

func parseDelimitedTable(lines []string, delimiter rune, format string) (parsedTable, bool) {
	reader := csv.NewReader(strings.NewReader(strings.Join(lines, "\n")))
	reader.Comma = delimiter
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true
	reader.LazyQuotes = true

	records := make([][]string, 0, len(lines))
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return parsedTable{}, false
		}
		if len(record) < 2 {
			return parsedTable{}, false
		}
		normalized := make([]string, 0, len(record))
		for _, field := range record {
			normalized = append(normalized, strings.TrimSpace(field))
		}
		records = append(records, normalized)
	}

	if len(records) < 2 {
		return parsedTable{}, false
	}

	expectedFields := len(records[0])
	if expectedFields < 2 {
		return parsedTable{}, false
	}

	rows := make([][]string, 0, len(records)-1)
	for _, record := range records[1:] {
		if len(record) != expectedFields {
			return parsedTable{}, false
		}
		rows = append(rows, record)
	}
	if len(rows) == 0 {
		return parsedTable{}, false
	}

	return parsedTable{
		Format:  format,
		Columns: normalizeColumns(records[0]),
		Rows:    rows,
	}, true
}

func normalizeColumns(columns []string) []string {
	normalized := make([]string, 0, len(columns))
	for index, column := range columns {
		column = summarizeLine(column, 40)
		if column == "" {
			column = fmt.Sprintf("col_%d", index+1)
		}
		normalized = append(normalized, column)
	}
	return normalized
}

func summarizeTableRows(rows [][]string, limit int) []string {
	samples := make([]string, 0, limit)
	for _, row := range rows {
		if len(samples) >= limit {
			break
		}
		cells := make([]string, 0, len(row))
		for _, cell := range row {
			text := summarizeLine(cell, 32)
			if text == "" {
				text = "-"
			}
			cells = append(cells, text)
		}
		samples = append(samples, strings.Join(cells, " | "))
	}
	return samples
}
