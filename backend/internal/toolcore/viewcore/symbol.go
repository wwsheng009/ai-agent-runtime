package viewcore

import "strings"

// AttachSymbolNote explains that a `symbol` request fell back to the file_path
// window it was given: the read still happened, just not by symbol. It works on
// the metadata/content pair so every surface publishes the same note.
func AttachSymbolNote(metadata map[string]any, content *string, note string) {
	if metadata == nil || strings.TrimSpace(note) == "" {
		return
	}
	metadata["view_symbol_note"] = note
	appendNote(content, note)
}

// SymbolFallbackNote is the note published when a symbol could not be resolved
// but the call still carried a file window to read.
func SymbolFallbackNote(symbol string) string {
	return "[note] symbol=" + quote(symbol) + " 未能解析（索引不可用或符号不存在）；已按 file_path 的行范围读取。"
}

// SymbolFailureMessage is the error published when a symbol could not be
// resolved and the call has nothing else to read.
func SymbolFailureMessage(symbol string) string {
	return "view: symbol " + quote(symbol) + " 无法解析（索引不可用或符号不存在）；请改用 file_path+offset/limit 或 grep"
}

// quote renders a symbol the way the local tool always has (%q), kept here so
// both messages stay in step.
func quote(symbol string) string {
	return "\"" + symbol + "\""
}
