package remotetools

// search_schemas.go holds the input schemas the remote search surfaces publish.
//
// One tool name means one parameter list on every transport: ls/glob/grep run
// the same handlers on ssh/http and on envd, so the schema is built once here
// and both backends call these functions instead of growing a second map. The
// grep map used to live inline in registry.go; the envd surface adopting the
// shared handler (S6) is what moved it, byte for byte.

import (
	"encoding/json"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/lscore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/searchcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// lsInputSchema is the one ls parameter surface.
func lsInputSchema() (json.RawMessage, error) {
	return toolkit.SchemaFromParameters(lscore.Parameters())
}

// globInputSchema is the one glob parameter surface.
func globInputSchema() (json.RawMessage, error) {
	return toolkit.SchemaFromParameters(searchcore.GlobParameters())
}

// grepInputSchema is the one grep parameter surface every remote transport
// publishes: two wordings for one parameter is how callers learn that `limit`
// might mean something different per backend.
func grepInputSchema() (json.RawMessage, error) {
	return toolkit.SchemaFromParameters(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"pattern": map[string]interface{}{
				"type":        "string",
				"description": "Regular expression to search for (ripgrep syntax).",
			},
			"regexp": map[string]interface{}{
				"type":        "string",
				"description": "兼容 rg 的 --regexp/-e 单模式写法；等价于 pattern。",
			},
			"patterns": map[string]interface{}{
				"anyOf": []map[string]interface{}{
					{"type": "string"},
					{
						"type":  "array",
						"items": map[string]interface{}{"type": "string"},
					},
				},
				"description": "多个模式按 OR 语义匹配，等价于多次 rg -e。可传字符串数组，也可传单个字符串。",
			},
			"pattern_file": map[string]interface{}{
				"type":        "string",
				"description": "兼容 rg 的 --file/-f：从文件读取模式，每行一个；与 pattern/patterns 合并。空行按空正则处理（匹配所有行，与 rg 一致）。",
			},
			"pattern_files": map[string]interface{}{
				"anyOf": []map[string]interface{}{
					{"type": "string"},
					{
						"type":  "array",
						"items": map[string]interface{}{"type": "string"},
					},
				},
				"description": "pattern_file 的复数拼写，可传数组或逗号分隔字符串；与 pattern_file 合并使用。",
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "File or directory to search. Default: workspace root.",
			},
			"paths": map[string]interface{}{
				"anyOf": []map[string]interface{}{
					{"type": "string"},
					{
						"type":  "array",
						"items": map[string]interface{}{"type": "string"},
					},
				},
				"description": "批量搜索多个文件或目录，按顺序在 path 之后搜索，等价于 rg pattern path1 path2 ...。可传字符串数组，也可传单个字符串（字符串按整条路径处理，不按逗号拆分）。每个根都必须位于工作区内，否则整次调用被拒绝。",
			},
			"glob": map[string]interface{}{
				"type":        "string",
				"description": "Optional file filter, e.g. *.go.",
			},
			"include": map[string]interface{}{
				"anyOf": []map[string]interface{}{
					{"type": "string"},
					{
						"type":  "array",
						"items": map[string]interface{}{"type": "string"},
					},
				},
				"description": "包含的文件名 glob 模式，例如 *.go；无斜杠的模式匹配任意目录下的文件名，含斜杠的模式匹配相对搜索根的路径。可传字符串数组，或逗号分隔的单个字符串（*.{go,ts} 这类 brace 形式不会被拆分）。",
			},
			"exclude": map[string]interface{}{
				"anyOf": []map[string]interface{}{
					{"type": "string"},
					{
						"type":  "array",
						"items": map[string]interface{}{"type": "string"},
					},
				},
				"description": "排除的文件名 glob 模式，例如 *_test.go；语义与 include 相同，且优先于 include。",
			},
			"type": map[string]interface{}{
				"type":        "string",
				"description": "按文件类型过滤，例如 go/py/java/ts/rs 等（兼容 rg 的 --type/-t）。类型名映射到共享 glob 表（go→*.go、cpp→*.cpp,*.hpp,*.cc,*.cxx），未知类型按 *.<type> 处理；它是 include 的并集而不是交集。",
			},
			"file_type": map[string]interface{}{
				"type":        "string",
				"description": "兼容别名；等价于 type。",
			},
			"type_not": map[string]interface{}{
				"type":        "string",
				"description": "排除某类文件类型，例如 go/py（兼容 rg 的 --type-not/-T）；映射规则与 type 相同，并集进 exclude。",
			},
			"type_add": map[string]interface{}{
				"anyOf": []map[string]interface{}{
					{"type": "string"},
					{
						"type":  "array",
						"items": map[string]interface{}{"type": "string"},
					},
				},
				"description": "兼容 rg 的 --type-add：name:glob，例如 foo:*.foo；可在已有类型上追加 glob。可传字符串或字符串数组。",
			},
			"type_clear": map[string]interface{}{
				"anyOf": []map[string]interface{}{
					{"type": "string"},
					{
						"type":  "array",
						"items": map[string]interface{}{"type": "string"},
					},
				},
				"description": "兼容 rg 的 --type-clear：按名称移除类型定义，例如 foo；与 type_add 同名时以清除为准，清除后的名称按未知类型回退为 *.<name>。可传字符串或字符串数组。",
			},
			"sort": map[string]interface{}{
				"type":        "string",
				"description": "兼容 rg 的 --sort：none/path/modified/accessed/created。",
			},
			"sortr": map[string]interface{}{
				"type":        "string",
				"description": "兼容 rg 的 --sortr：与 sort 相同的键，但按倒序排列；与 sort 同时出现时 sortr 优先。",
			},
			"sort_reverse": map[string]interface{}{
				"type":        "string",
				"description": "sortr 的兼容别名，两者等价。",
			},
			"sort_files": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --sort-files：true 等价于 sort=path，false 关闭排序。",
			},
			"no_sort_files": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --no-sort-files：取消 path 排序；modified/accessed/created 不受影响。",
			},
			"case_insensitive": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容别名；等价于 ignore_case=true。",
			},
			"literal": map[string]interface{}{
				"type":        "boolean",
				"description": "是否为字面文本搜索，关闭正则特殊字符解释（默认为 false）。",
			},
			"fixed_strings": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --fixed-strings/-F；等价于 literal=true。",
			},
			"ignore_case": map[string]interface{}{
				"type":        "boolean",
				"description": "是否忽略大小写；兼容 rg 的 --ignore-case/-i。",
			},
			"case_sensitive": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --case-sensitive/-s。true 时会覆盖 ignore_case/smart_case，强制区分大小写。",
			},
			"smart_case": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --smart-case/-S。仅当模式中没有大写字母时自动忽略大小写。",
			},
			"word": map[string]interface{}{
				"type":        "boolean",
				"description": "是否匹配完整单词（默认为 false）。例如搜 err 不会匹配 error/stderr。",
			},
			"word_regexp": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --word-regexp/-w；等价于 word=true。",
			},
			"line_regexp": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --line-regexp/-x：要求整行匹配。",
			},
			"invert_match": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --invert-match/-v：输出不匹配模式的行。",
			},
			"context": map[string]interface{}{
				"type":        "integer",
				"minimum":     0,
				"description": "显示匹配行前后各 N 行上下文（兼容 rg 的 --context/-C，上限 10）。匹配行保持 path:line:text，上下文行用 rg 的 path-line-text，块之间用 -- 分隔。",
			},
			"before_context": map[string]interface{}{
				"type":        "integer",
				"minimum":     0,
				"description": "兼容 rg 的 --before-context/-B：显示匹配行前 N 行（上限 10）。",
			},
			"after_context": map[string]interface{}{
				"type":        "integer",
				"minimum":     0,
				"description": "兼容 rg 的 --after-context/-A：显示匹配行后 N 行（上限 10）。",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"minimum":     1,
				"description": "Maximum matches to return. Default 200.",
			},
			"offset": map[string]interface{}{
				"type":        "integer",
				"minimum":     0,
				"description": "Skip the first N matches, then apply limit. Use the returned next_offset to page.",
			},
			"mode": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"content", "files", "files_without", "count"},
				"description": "输出模式：content=显示匹配行（默认），files=仅列出包含匹配的文件，files_without=仅列出不包含匹配的文件，count=每个文件的匹配行数（path:count）。三种文件级模式（files/files_without/count）下 limit/offset 以「文件」为单位，next_offset/has_more 同口径。files_without 需要读每个候选文件直到首个匹配（或读到 EOF 证明无匹配），因此比其它模式扫描更久，且 max_count=0 时所有候选都算无匹配。",
			},
			"files_with_matches": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --files-with-matches/-l；等价于 mode=files。",
			},
			"files_without_match": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --files-without-match；等价于 mode=files_without。",
			},
			"files_without_matches": map[string]interface{}{
				"type":        "boolean",
				"description": "files_without_match 的复数拼写，两者等价。",
			},
			"count": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --count/-c；等价于 mode=count（统计匹配行数）。",
			},
			"count_matches": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --count-matches：进入 count 模式，但统计的是匹配次数而非匹配行数（同一行出现两次算 2）。在非 count 模式下无效。",
			},
			"glob_case_insensitive": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --glob-case-insensitive：让 glob/include/exclude 的模式按大小写不敏感匹配。不影响搜索 pattern 本身的大小写规则。",
			},
			"ignore_file_case_insensitive": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --ignore-file-case-insensitive：只让 **显式传入的** ignore_file 规则按大小写不敏感匹配。按惯例发现的 .gitignore/.ignore/全局 ignore 文件保持原有语义。",
			},
			"max_count": map[string]interface{}{
				"type":        "integer",
				"minimum":     0,
				"description": "兼容 rg 的 --max-count/-m：每个文件的匹配行上限（count 模式下计数同样受此上限）。显式传 0 表示该文件不产生任何匹配（与 rg -m 0 一致）；不传表示不限。",
			},
			"max_depth": map[string]interface{}{
				"type":        "integer",
				"minimum":     0,
				"description": "目录搜索最大深度（兼容 rg 的 --max-depth）。相对每个搜索根计算：0 表示只搜索该根的直接内容，1 表示再允许下一层目录。",
			},
			"max_filesize": map[string]interface{}{
				"anyOf": []map[string]interface{}{
					{"type": "string"},
					{"type": "integer"},
				},
				"description": "兼容 rg 的 --max-filesize：跳过大于该值的文件。可传字节整数，或 10K/2M/1G 这类大小字符串（1024 进制，可选 B 后缀）。",
			},
			"one_file_system": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --one-file-system：递归时不再下降进入其它文件系统（挂载点），但调用方显式给出的搜索根始终会被搜索。",
			},
			"only_matching": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --only-matching/-o：每个匹配片段单独输出一行，格式 path:line:column:fragment。与 invert_match 同用时回退为整行内容（同 rg -o -v）；count 模式下计数变为匹配次数（同 rg -o -c）；files 模式不受影响；可与 context 同用（同 rg -o -C：命中行输出片段，窗口内其余行整行）。",
			},
			"column": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --column：输出 path:line:column: content，给出匹配在行内的 1-based 字节列号。与 only_matching 同用时每个片段带自己的列号；invert_match 不产生列号（整行不匹配，没有位置可言）。列号按原始行计算，不受 trim 影响。",
			},
			"trim": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --trim：仅在渲染时去掉匹配行/上下文行的前导空白（空格与制表符），不改变匹配语义，也不改变 column 的取值。",
			},
			"max_columns": map[string]interface{}{
				"type":        "integer",
				"minimum":     0,
				"description": "兼容 rg 的 --max-columns/-M：行内容超过该字节数时，整行替换为 [Omitted long matching line]。0 表示不限制。与 max_columns_preview 配合时改为保留前缀。",
			},
			"max_columns_preview": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --max-columns-preview：与 max_columns 配合时保留前缀并在行尾追加 \" [... omitted end of long line]\"，而不是整行省略。",
			},
			"no_max_columns_preview": map[string]interface{}{
				"type":        "boolean",
				"description": "显式关闭 max_columns 的预览模式；与 max_columns_preview 同时出现时它优先。",
			},
			"stats": map[string]interface{}{
				"type":        "boolean",
				"description": "返回统计摘要（metadata.stats）：matches（本页出现次数）、matched_lines（本页去重后的行数）、files_with_matched（本页文件数）、files_scanned（本次实际扫描文件数）。它是**页级**统计而不是全量搜索统计——引擎会在页后一条/一个文件处停止，因此全量数字需要额外一次完整扫描。",
			},
			"hidden": map[string]interface{}{
				"type":        "boolean",
				"description": "包含以 . 开头的隐藏文件与隐藏目录（默认 false = 跳过，与本地工具及 ripgrep 默认一致）。只影响 hidden 层；显式传入的隐藏根目录始终会被搜索。",
			},
			"no_hidden": map[string]interface{}{
				"type":        "boolean",
				"description": "显式关闭隐藏文件：与 hidden 同时出现时 no_hidden 优先。",
			},
			"unrestricted_level": map[string]interface{}{
				"type":        "integer",
				"minimum":     0,
				"maximum":     3,
				"description": "兼容 rg 的 -u/-uu/-uuu/--unrestricted 层级：0=默认，1=不读 ignore 文件，2=同时搜索隐藏文件/目录，3=同时搜索含 NUL 字节的二进制文件（越过 NUL 继续匹配，但输出里的 NUL 显示为 ␀，保证文本可读）。no_hidden 仍然优先于该层级的隐藏步骤；no_ignore=true 等价于 1。",
			},
			"no_ignore": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --no-ignore：完全关闭 ignore 文件（.gitignore/.ignore/.rgignore/.git/info/exclude/祖先/全局都不再生效）。默认行为是尊重这些 ignore 文件。",
			},
			"no_ignore_parent": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --no-ignore-parent：忽略祖先目录中的 ignore 文件。",
			},
			"no_ignore_vcs": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --no-ignore-vcs：忽略版本控制系统的 ignore 规则（.gitignore、.git/info/exclude）。",
			},
			"no_ignore_dot": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --no-ignore-dot：忽略 .ignore/.rgignore 规则。",
			},
			"no_ignore_global": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --no-ignore-global：忽略全局 ignore 文件（~/.config/git/ignore、~/.gitignore_global）。",
			},
			"no_ignore_files": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --no-ignore-files：禁用通过 ignore_file(s) 显式指定的 ignore 文件。",
			},
			"no_messages": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --no-messages：跳过不可读的显式 ignore_file 而不报错。默认（false）下，调用方显式指定的 ignore_file 读不到会直接失败——静默丢弃会改变规则集却不告知。按惯例发现的 .gitignore/.ignore/全局 ignore 文件始终可选，不受该开关影响。",
			},
			// Presentation-only rg spellings. They are accepted so a model can
			// write the flag it knows, and they change nothing: grep's output is
			// always "path:line:content" with one stable skeleton. Which ones were
			// set is reported back in metadata.ignored_presentation, so "understood
			// and deliberately not applied" never looks like "dropped".
			"line_number": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --line-number/-n；grep 输出默认就带行号，因此该参数仅作为熟悉写法接受，不改变输出骨架。",
			},
			"no_line_number": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --no-line-number/-N；grep 的行号是稳定骨架的一部分，该参数被接受但不改变输出。",
			},
			"with_filename": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --with-filename/-H；grep 默认在每条匹配前给出路径，该参数被接受但不改变输出。",
			},
			"no_filename": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --no-filename；grep 的路径前缀是稳定骨架的一部分，该参数被接受但不改变输出。",
			},
			"heading": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --heading；grep 使用逐行 path:line:content 骨架，该参数被接受但不改变输出。",
			},
			"no_heading": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --no-heading；grep 本来就不分组显示，该参数被接受但不改变输出。",
			},
			"pretty": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --pretty；grep 输出是模型可读的稳定文本而非终端美化格式，该参数被接受但不改变输出。",
			},
			"text": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 -a/--text：把二进制文件当文本搜索。二进制处理由共享引擎自己的规则决定（见 binary），该参数被接受但不改变输出。",
			},
			"binary": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --binary：允许在二进制文件中搜索。启用后是否命中由共享引擎的二进制检测决定，该参数本身不改变输出骨架。",
			},
			"line_buffered": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --line-buffered；grep 一次性返回完整窗口而不是流式逐行输出，该参数被接受但不改变输出。",
			},
			"no_line_buffered": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --no-line-buffered；grep 不流式输出，该参数被接受但不改变输出。",
			},
			"block_buffered": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --block-buffered；缓冲策略属于 rg 进程行为，grep 的输出窗口与它无关，该参数被接受但不改变输出。",
			},
			"no_block_buffered": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --no-block-buffered；grep 不依赖缓冲开关，该参数被接受但不改变输出。",
			},
			"null": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --null（NUL 分隔路径）；grep 为了保持稳定文本骨架会将该参数视为兼容提示。",
			},
			"null_data": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --null-data（NUL 作为输入记录分隔）；grep 按行解析文本，该参数被接受但不改变输出。",
			},
			"field_context_separator": map[string]interface{}{
				"type":        "string",
				"description": "兼容 rg 的 --field-context-separator；grep 的上下文行使用固定分隔骨架，该参数被接受但不改变输出。",
			},
			"path_separator": map[string]interface{}{
				"type":        "string",
				"description": "兼容 rg 的 --path-separator；grep 始终输出工作区相对路径，该参数被接受但不改变输出。",
			},
			"color": map[string]interface{}{
				"type":        "string",
				"description": "兼容 rg 的 --color；grep 输出不含 ANSI 颜色，该参数被接受但不改变输出。",
			},
			"pcre2": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --pcre2/-P：用 PCRE2 引擎匹配（支持 lookaround、反向引用）。该能力需要 ripgrep：目标主机上没有 rg 时按内置引擎的既有行为处理（PCRE 专有语法会在编译期报错）。",
			},
			"engine": map[string]interface{}{
				"type":        "string",
				"description": "兼容 rg 的 --engine；default（或留空）走内置引擎且不需要 rg，其他取值（pcre2、auto-hybrid-regex 等）需要 ripgrep。",
			},
			"auto_hybrid_regex": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --auto-hybrid-regex：模式含 PCRE 专有语法时自动切到 PCRE2。该能力需要 ripgrep。",
			},
			"multiline": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --multiline/-U：允许模式跨行匹配。该能力需要 ripgrep；没有 rg 时退回内置逐行引擎。",
			},
			"multiline_dotall": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --multiline-dotall：多行模式下让 . 匹配换行。该能力需要 ripgrep。",
			},
			"crlf": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --crlf：把 CRLF 当作行结束符。内置引擎本来就如此（读取时去掉 CR），该开关让 ripgrep 与它一致，而不是把 CR 留在行尾。",
			},
			"no_config": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --no-config：禁止读取 ripgrep 配置（RIPGREP_CONFIG_PATH）。默认即为「不读配置」，因此 true 与省略等价；只有显式 false 才表示允许继承本机 rg 配置。",
			},
			"replace": map[string]interface{}{
				"type":        "string",
				"description": "兼容 rg 的 --replace/-r：把命中文本替换为给定模板（支持 $1/${name} 捕获组引用）；只有命中行被替换，上下文行保持原文。",
			},
			"passthru": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --passthru：content 模式下输出所有行，匹配行带匹配标记、未匹配行作为上下文；files/count/files_without 模式忽略该参数。",
			},
			"json": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --json：返回 ripgrep 原始 JSON Lines 事件流（begin/match/context/end/summary），不再规范化为 path:line[:column]: content。事件行原样透传，只有事件里的 path 会按本工具的显示路径约定改写；分页以「事件行」为单位，输出保持可解析（不追加任何提示文本）；metadata.stats 直接给出 rg 汇总事件里的计数。没有可用 rg 时会明确报错，而不是降级成别的文本。",
			},
			"json_output": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容别名；等价于 json=true。",
			},
			"head_limit": map[string]interface{}{
				"type":        "integer",
				"description": "本次返回的匹配行上限（分页页大小）：本地拼写，与 limit 是同一个概念，两个拼写同时出现时以 head_limit 为准。缺省沿用 limit/内置上限；0 表示不限（仍受 5000 条安全上限与字节预算保护）。配合 offset 翻页，响应里带 next_offset/has_more。",
			},
			"context_separator": map[string]interface{}{
				"type":        "string",
				"description": "兼容 rg 的 --context-separator：指定上下文块之间的分隔字符串。默认仍为 `--`；空字符串视为未设置（保持默认）。",
			},
			"no_context_separator": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --no-context-separator：关闭上下文块之间的分隔字符串；与 context_separator 同时出现时以本开关为准。",
			},
			"follow": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --follow/-L：跟随符号链接搜索——链接到目录则下钻（按链接自身的路径呈现），链接到文件则搜索该文件。环保护比 rg 更强：同一目录不会被第二个链接重复带入（互指链接必然终止；同一目标被多个链接指向时只搜索一次）。默认随目标平台：Windows 目标默认跟随 NTFS junction，POSIX 目标默认不跟随；显式 follow=false 可关闭。",
			},
			"ignore_file": map[string]interface{}{
				"anyOf": []map[string]interface{}{
					{"type": "string"},
					{
						"type":  "array",
						"items": map[string]interface{}{"type": "string"},
					},
				},
				"description": "兼容 rg 的 --ignore-file：额外指定 ignore 文件；每行规则的作用域是该文件所在目录。可传字符串数组或逗号分隔的单个字符串。",
			},
			"ignore_files": map[string]interface{}{
				"anyOf": []map[string]interface{}{
					{"type": "string"},
					{
						"type":  "array",
						"items": map[string]interface{}{"type": "string"},
					},
				},
				"description": "ignore_file 的复数拼写，两者等价并合并使用。",
			},
		},
		// At least one pattern spelling is required, but which one is a
		// handler-level rule: `patterns` is a valid substitute for `pattern`,
		// so binding `required` to the single-value spelling would reject
		// calls the shared implementation fully supports.
		"required":             []string{},
		"additionalProperties": false,
	})
}
