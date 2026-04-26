package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

var errGrepLimitReached = errors.New("grep match limit reached")

// grepMode defines the output format for search results.
type grepMode string

const (
	grepModeContent      grepMode = "content"       // show matching lines (default)
	grepModeFiles        grepMode = "files"         // list files with matches only
	grepModeFilesWithout grepMode = "files_without" // list files without matches
	grepModeCount        grepMode = "count"         // show match count per file
)

// GrepTool 文件内容搜索工具
type GrepTool struct {
	*toolkit.BaseTool
	sandboxPolicy
	maxMatches  int
	lookPath    func(string) (string, error)
	runCommand  func(context.Context, string, string, []string) ([]byte, error)
	rgAvailable *bool // cached ripgrep availability (nil = not checked yet)
}

// grepOptions holds all parsed search options.
type grepOptions struct {
	pattern              string
	directPatterns       []string
	patterns             []string
	searchPath           string
	searchPaths          []string
	resolvedPath         string
	resolvedPaths        []string
	patternFiles         []string
	resolvedPatternFiles []string
	workingDir           string
	searchTarget         string
	searchScopes         []grepSearchScope
	include              string
	exclude              string
	includeSpecs         []grepGlobPattern
	excludeSpecs         []grepGlobPattern
	globCaseInsensitive  bool
	literal              bool
	ignoreCase           bool
	ignoreCaseRequested  bool
	caseSensitive        bool
	smartCase            bool
	word                 bool
	lineRegexp           bool
	invertMatch          bool
	onlyMatching         bool
	countMatches         bool
	stats                bool
	jsonOutput           bool
	follow               bool
	column               bool
	trim                 bool
	pretty               bool
	lineBuffered          bool
	noLineBuffered        bool
	maxColumns           int
	maxColumnsSet        bool
	maxColumnsPreview    bool
	noMaxColumnsPreview  bool
	context              int
	beforeContext        int
	afterContext         int
	fileType             string
	excludeType          string
	sortBy               string
	sortReverseBy        string
	maxDepth             int
	maxDepthSet          bool
	maxCount             int
	maxCountSet          bool
	maxFilesize          string
	maxFileBytes         int64
	requiresRipgrep      bool
	rgOnlyArgs           []string
	ignoredRGArgs        []string
	ignoredPresentation  []string
	mode                 grepMode
}

type grepSearchScope struct {
	workingDir   string
	searchTarget string
	displayPath  string
}

type grepGlobPattern struct {
	pattern         string
	caseInsensitive bool
}

// fileTypeToGlob maps rg --type names to glob patterns for the builtin engine.
var fileTypeToGlob = map[string]string{
	"go":    "*.go",
	"py":    "*.py",
	"java":  "*.java",
	"js":    "*.js",
	"ts":    "*.ts",
	"tsx":   "*.tsx",
	"jsx":   "*.jsx",
	"rs":    "*.rs",
	"rb":    "*.rb",
	"cpp":   "*.cpp,*.hpp,*.cc,*.cxx",
	"c":     "*.c,*.h",
	"cs":    "*.cs",
	"swift": "*.swift",
	"kt":    "*.kt",
	"scala": "*.scala",
	"php":   "*.php",
	"html":  "*.html,*.htm",
	"css":   "*.css",
	"json":  "*.json",
	"yaml":  "*.yaml,*.yml",
	"toml":  "*.toml",
	"xml":   "*.xml",
	"md":    "*.md,*.markdown",
	"sh":    "*.sh,*.bash",
	"sql":   "*.sql",
	"proto": "*.proto",
	"dart":  "*.dart",
}

var (
	ripgrepMatchLineWithColumnPattern = regexp.MustCompile(`^(.*):([0-9]+):([0-9]+):(.*)$`)
	ripgrepMatchLinePattern           = regexp.MustCompile(`^(.*):([0-9]+):(.*)$`)
	ripgrepContextLinePattern         = regexp.MustCompile(`^(.*)-([0-9]+)-(.*)$`)
	ripgrepStatsMatchesPattern        = regexp.MustCompile(`^\d+ matches$`)
	ripgrepStatsMatchedLinesPattern   = regexp.MustCompile(`^\d+ matched lines$`)
	ripgrepStatsFilesWithPattern      = regexp.MustCompile(`^\d+ files contained matches$`)
	ripgrepStatsFilesSearchedPattern  = regexp.MustCompile(`^\d+ files searched$`)
	ripgrepStatsBytesPrintedPattern   = regexp.MustCompile(`^\d+ bytes printed$`)
	ripgrepStatsBytesSearchedPattern  = regexp.MustCompile(`^\d+ bytes searched$`)
	ripgrepStatsSearchSecsPattern     = regexp.MustCompile(`^[0-9.]+ seconds spent searching$`)
	ripgrepStatsTotalSecsPattern      = regexp.MustCompile(`^[0-9.]+ seconds total$`)
)

func stringOrStringArraySchema(description string) map[string]interface{} {
	return map[string]interface{}{
		"anyOf": []map[string]interface{}{
			{
				"type": "string",
			},
			{
				"type": "array",
				"items": map[string]interface{}{
					"type": "string",
				},
			},
		},
		"description": description,
	}
}

func stringOrIntegerSchema(description string) map[string]interface{} {
	return map[string]interface{}{
		"anyOf": []map[string]interface{}{
			{
				"type": "string",
			},
			{
				"type": "integer",
			},
		},
		"description": description,
	}
}

// NewGrepTool 创建 Grep 工具
func NewGrepTool() *GrepTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"pattern": map[string]interface{}{
				"type":        "string",
				"description": "搜索模式。默认按正则表达式处理；若 literal/fixed_strings=true，则按字面文本处理。若提供 rg_args，也可把 pattern 作为第一个位置参数放在 rg_args 中。",
			},
			"regexp": map[string]interface{}{
				"type":        "string",
				"description": "兼容 rg 的 --regexp/-e 单模式写法；等价于 pattern。",
			},
			"patterns": stringOrStringArraySchema("兼容多模式搜索。可传多个 pattern/regexp，等价于多次使用 rg -e/--regexp。多个模式按 OR 语义匹配。"),
			"pattern_file": map[string]interface{}{
				"type":        "string",
				"description": "兼容 rg 的 --file/-f 单文件写法。文件中每行视作一个 pattern；空行会按 rg 语义视为空正则，可能匹配所有行。",
			},
			"pattern_files": stringOrStringArraySchema("兼容 rg 的多 pattern 文件写法。可传一个或多个文件路径；每个文件中每行视作一个 pattern，等价于多次使用 rg -f/--file。"),
			"path": map[string]interface{}{
				"type":        "string",
				"description": "搜索路径（默认为当前目录）。若提供 rg_args，也可把 path 作为第二个位置参数放在 rg_args 中。",
			},
			"paths":   stringOrStringArraySchema("兼容 rg 的多个搜索根路径。可传字符串数组，等价于 rg pattern path1 path2 ...；也可与 rg_args 的多个位置路径配合使用。"),
			"include": stringOrStringArraySchema("包含的文件名 glob 模式，例如 *.go。支持字符串、字符串数组，或逗号分隔多个模式。"),
			"exclude": stringOrStringArraySchema("排除的文件名 glob 模式，例如 *.test.ts。支持字符串、字符串数组，或逗号分隔多个模式。"),
			"glob":    stringOrStringArraySchema("兼容 rg 的 --glob/-g。可直接按 ripgrep 思路传 glob；以 ! 开头的模式会视为排除模式。"),
			"glob_case_insensitive": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --glob-case-insensitive：让通过 glob/-g 传入的模式按大小写不敏感方式匹配。若使用 rg_args，也支持 --iglob。",
			},
			"literal": map[string]interface{}{
				"type":        "boolean",
				"description": "是否为字面文本搜索，关闭正则特殊字符解释（默认为 false）",
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
				"description": "兼容 rg 的 --smart-case/-S。仅当 pattern 中没有大写字母时自动忽略大小写。",
			},
			"word": map[string]interface{}{
				"type":        "boolean",
				"description": "是否匹配完整单词（默认为 false）。例如搜 err 不会匹配 error/stderr",
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
				"description": "兼容 rg 的 --invert-match/-v：输出不匹配 pattern 的行。",
			},
			"only_matching": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --only-matching/-o：仅输出匹配到的文本片段。若与 invert_match 一起使用，则回退行为会近似 rg，输出整行未命中内容。",
			},
			"count_matches": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --count-matches：进入 count 模式，并统计每个文件中的匹配次数而不是匹配行数。",
			},
			"pcre2": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --pcre2/-P：使用 PCRE2 引擎；该能力当前仅在可用 ripgrep/rg 时支持。",
			},
			"engine": map[string]interface{}{
				"type":        "string",
				"description": "兼容 rg 的 --engine；例如 default、auto-hybrid-regex、pcre2。该能力当前仅在可用 ripgrep/rg 时支持。",
			},
			"multiline": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --multiline/-U：启用跨行匹配；该能力当前仅在可用 ripgrep/rg 时支持。",
			},
			"multiline_dotall": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --multiline-dotall：让 . 在多行模式下匹配换行；该能力当前仅在可用 ripgrep/rg 时支持。",
			},
			"replace": map[string]interface{}{
				"type":        "string",
				"description": "兼容 rg 的 --replace/-r：输出替换后的文本；该能力当前仅在可用 ripgrep/rg 时支持。",
			},
			"passthru": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --passthru：在上下文中穿透输出未匹配行；该能力当前仅在可用 ripgrep/rg 时支持。",
			},
			"crlf": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --crlf：按 CRLF 行尾语义处理；该能力当前仅在可用 ripgrep/rg 时支持。",
			},
			"auto_hybrid_regex": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --auto-hybrid-regex：自动选择正则引擎；该能力当前仅在可用 ripgrep/rg 时支持。",
			},
			"stats": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --stats：返回稳定的统计摘要（如 matches、matched_lines、files_searched、bytes_searched），并在 metadata 中附带结构化 stats。",
			},
			"json": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --json：返回 rg 风格 JSON Lines 事件流（begin/match/context/end/summary）。该能力当前仅在可用 ripgrep/rg 时支持；启用后结果会按 rg 原始语义透传，不再规范化为 path:line[:column]: content。",
			},
			"json_output": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容别名；等价于 json=true，用于按 rg --json 心智请求 JSON Lines 输出。",
			},
			"follow": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --follow/-L：跟随符号链接搜索。该能力当前仅在可用 ripgrep/rg 时支持；无 rg 时会明确提示。",
			},
			"column": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --column：输出 path:line:column: content 形式的列号信息。对 only_matching 会为每个片段输出各自列号；对 invert/context 等无法确定列号的行，会尽量保持稳定格式。",
			},
			"trim": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --trim：仅在输出时去掉匹配行/上下文行前导空白，不改变实际匹配语义；若同时开启 column，列号仍按原始行位置计算。",
			},
			"pretty": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --pretty：这是一个输出展示便利开关；grep 会接受这个熟悉写法，但默认仍保持稳定的结构化输出骨架。",
			},
			"line_buffered": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --line-buffered：逐行缓冲输出。grep 会接受这个熟悉写法，但稳定的工具输出本身不依赖这个开关。",
			},
			"no_line_buffered": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --no-line-buffered：关闭逐行缓冲。grep 会接受这个熟悉写法，但稳定的工具输出本身不依赖这个开关。",
			},
			"max_columns": map[string]interface{}{
				"type":        "integer",
				"description": "兼容 rg 的 --max-columns/-M：当行内容过长时省略或预览长行。grep 会尽量模拟 rg 的长行省略语义；若提供 preview 相关开关，则保留前缀预览。",
			},
			"max_columns_preview": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --max-columns-preview：与 max_columns/-M 配合时显示长行预览而非完全省略。",
			},
			"no_max_columns_preview": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --no-max-columns-preview：显式关闭长行预览。",
			},
			"context": map[string]interface{}{
				"type":        "integer",
				"description": "显示匹配行前后各 N 行上下文（兼容 rg 的 --context/-C）。",
			},
			"before_context": map[string]interface{}{
				"type":        "integer",
				"description": "兼容 rg 的 --before-context/-B：显示匹配行前 N 行。",
			},
			"after_context": map[string]interface{}{
				"type":        "integer",
				"description": "兼容 rg 的 --after-context/-A：显示匹配行后 N 行。",
			},
			"type": map[string]interface{}{
				"type":        "string",
				"description": "按文件类型过滤，例如 go/py/java/ts/rs 等（兼容 rg 的 --type/-t）。",
			},
			"file_type": map[string]interface{}{
				"type":        "string",
				"description": "兼容别名；等价于 type。",
			},
			"type_not": map[string]interface{}{
				"type":        "string",
				"description": "兼容 rg 的 --type-not/-T：排除某类文件类型，例如 test/py/go。",
			},
			"sort": map[string]interface{}{
				"type":        "string",
				"description": "兼容 rg 的 --sort。常用值如 path/modified/accessed/created/none；内置回退引擎原生支持 path 排序，其它时间类排序需要 rg。",
			},
			"sortr": map[string]interface{}{
				"type":        "string",
				"description": "兼容 rg 的 --sortr（倒序排序）。常用值如 path/modified/accessed/created/none；内置回退引擎原生支持 path 倒序，其它时间类排序需要 rg。",
			},
			"sort_reverse": map[string]interface{}{
				"type":        "string",
				"description": "兼容别名；等价于 sortr。",
			},
			"sort_files": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --sort-files。true 时等价于 sort=path；grep 会保持稳定路径排序输出。",
			},
			"no_sort_files": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --no-sort-files。仅用于抵消 sort_files/--sort-files 这类熟悉写法；默认输出本就不是强制路径排序。",
			},
			"max_depth": map[string]interface{}{
				"type":        "integer",
				"description": "目录搜索最大深度（兼容 rg 的 --max-depth）。",
			},
			"max_count": map[string]interface{}{
				"type":        "integer",
				"description": "兼容 rg 的 --max-count/-m。使用 rg 引擎时为每文件上限；内置回退引擎会尽量模拟这一行为。",
			},
			"max_filesize": stringOrIntegerSchema("兼容 rg 的 --max-filesize。可传字节整数，或如 10K/2M/1G 这类大小字符串；内置回退引擎会跳过超出大小限制的文件。"),
			"mode": map[string]interface{}{
				"type":        "string",
				"description": "输出模式：content=显示匹配行（默认），files=仅列出包含匹配的文件，files_without=仅列出不包含匹配的文件，count=统计每个文件的匹配数",
			},
			"files_with_matches": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --files-with-matches/-l；等价于 mode=files。",
			},
			"files_without_match": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --files-without-match；等价于 mode=files_without。",
			},
			"count": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --count/-c；等价于 mode=count。",
			},
			"line_number": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --line-number/-n。grep 输出默认就包含行号，因此该参数仅作为熟悉写法接受，不改变稳定输出骨架。",
			},
			"heading": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --heading：按文件分组显示标题。grep 会接受这个熟悉写法，但默认仍保持稳定的结构化输出骨架。",
			},
			"no_heading": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --no-heading：关闭文件标题。grep 会接受这个熟悉写法，但默认仍保持稳定的结构化输出骨架。",
			},
			"with_filename": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --with-filename/-H。grep 输出默认就包含路径/文件名，因此该参数仅作为熟悉写法接受。",
			},
			"no_line_number": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --no-line-number/-N。为保持稳定结构化输出，该参数会被接受但不会去掉行号。",
			},
			"no_filename": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --no-filename。为保持稳定结构化输出，该参数会被接受但不会去掉路径/文件名。",
			},
			"color": map[string]interface{}{
				"type":        "string",
				"description": "兼容 rg 的 --color=never/always/ansi。grep 输出默认关闭颜色并保持纯文本稳定结构；该参数仅作为熟悉写法接受。",
			},
			"text": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --text/-a。当前 grep 默认按文本扫描常见文件，因此该参数仅作为熟悉写法接受。",
			},
			"binary": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --binary。当前 grep 会尽量保持稳定文本输出；该参数仅作为熟悉写法接受。",
			},
			"hidden": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --hidden。当前 grep 工具默认已经搜索隐藏文件，无需额外指定。",
			},
			"no_ignore": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --no-ignore。当前 grep 工具默认已经忽略 .git/node_modules 之外的大多数 ignore 规则，无需额外指定。",
			},
			"rg_args": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": "string",
				},
				"description": "可选：ripgrep/rg 风格参数列表。支持常见选项与位置参数，例如 [\"-g\", \"*.go\", \"-i\", \"-w\", \"-C\", \"2\", \"pattern\", \"backend\"]、多模式 [\"-e\", \"foo\", \"-e\", \"bar\", \"backend\"]、pattern file [\"-f\", \"patterns.txt\", \"backend\"]、列号/裁剪 [\"--column\", \"--trim\", \"foo\", \"backend\"]、路径排序 [\"--sort\", \"path\", \"foo\", \"backend\"]、计数次数 [\"--count-matches\", \"foo\", \"backend\"] 等；像 -n/-H/-N/--no-filename/--color 这类展示层参数也会被兼容接收。",
			},
		},
		"anyOf": []map[string]interface{}{
			{
				"required": []string{"pattern"},
			},
			{
				"required": []string{"regexp"},
			},
			{
				"required": []string{"patterns"},
			},
			{
				"required": []string{"pattern_file"},
			},
			{
				"required": []string{"pattern_files"},
			},
			{
				"required": []string{"rg_args"},
			},
		},
	}

	return &GrepTool{
		BaseTool: toolkit.NewBaseTool(
			"grep",
			"文件内容搜索（优先使用 ripgrep/rg，不可用时回退到内置扫描；支持常见 rg 风格参数、多路径/单文件路径、路径感知 glob/iglob、glob_case_insensitive、pattern_file/-f、pcre2/-P、multiline/-U、replace/-r、column/trim、count_matches、stats、json、follow、sort/sortr/sort_files、-e 多模式、-v/-x/-l/-o、--files-without-match、--max-filesize 与 rg_args 兼容层）",
			"3.2.0",
			parameters,
			true,
		),
		maxMatches: 100,
		lookPath:   exec.LookPath,
		runCommand: runGrepCommand,
	}
}

// Description returns a dynamic description based on ripgrep availability.
func (g *GrepTool) Description() string {
	if g.isRgAvailable() {
		return "文件内容搜索（使用 ripgrep/rg 引擎，高性能正则和字面搜索）。支持常见 rg 风格参数与别名：glob≈-g、iglob≈--iglob、glob_case_insensitive≈--glob-case-insensitive、pattern_file/pattern_files≈-f/--file、pcre2≈-P/--pcre2、engine≈--engine、multiline≈-U/--multiline、multiline_dotall≈--multiline-dotall、replace≈-r/--replace、passthru≈--passthru、crlf≈--crlf、auto_hybrid_regex≈--auto-hybrid-regex、column≈--column、trim≈--trim、pretty≈--pretty、line_buffered≈--line-buffered、max_columns≈-M/--max-columns、max_columns_preview≈--max-columns-preview、count_matches≈--count-matches、stats≈--stats、json≈--json、follow≈-L/--follow、sort/sortr≈--sort/--sortr、sort_files≈--sort-files、fixed_strings≈-F、ignore_case≈-i、word_regexp≈-w、line_regexp≈-x、invert_match≈-v、only_matching≈-o、context/before_context/after_context≈-C/-B/-A、type≈-t、type_not≈-T、files_with_matches≈-l、files_without_match≈--files-without-match、count≈-c、max_count≈-m、max_filesize≈--max-filesize、patterns/regexp≈多次 -e；支持目录、单文件 path、多路径 paths、路径感知 glob（如 src/**/*.go）、pattern file（每行一个 pattern；空文件按 rg 语义返回无匹配，空行可能匹配所有行，如 -f patterns.txt）、path 排序/倒序、匹配次数统计、stats 摘要、max_depth/max_count 显式 0 语义以及常见短参数组合；也支持通过 rg_args 传入常见 rg 参数列表，例如 rg -P 'foo.*bar' backend、rg --column --trim foo backend、rg --sort path foo backend、rg --count-matches foo backend、rg --stats foo backend、rg --json foo backend。默认结果输出会规范化为稳定的 path:line[:column]: content 形态；像 -n/-H/-N/--no-filename/--color 这类展示层参数会被兼容接收但不改变输出骨架；当请求 json/--json 时，会改为按 rg 原始 JSON Lines 事件流透传输出；像 pcre2/engine/multiline/multiline_dotall/replace/passthru/crlf/auto_hybrid_regex 这类 rg-only 能力会在有 rg 时透传。"
	}
	return "文件内容搜索（使用内置扫描引擎；安装 ripgrep/rg 可获得更好性能）。支持常见 rg 风格参数与别名、多路径/单文件 path、路径感知 glob/iglob、glob_case_insensitive、pattern_file/-f、column/trim、pretty、line_buffered、max_columns/-M/--max-columns、max_columns_preview/--max-columns-preview、count_matches、stats、path 排序/倒序、sort_files、-e 多模式、-v/-x/-l/-o、--files-without-match、--max-filesize、max_depth/max_count 显式 0 语义，以及常见短参数组合，并兼容 rg_args 传参；输出会规范化为稳定的 path:line[:column]: content 形态，因此像 -n/-H/-N/--no-filename/--color、line_number/heading/no_heading/with_filename/no_filename/color 这类展示层参数会被容忍或忽略；像 json/--json、pcre2/-P、engine、multiline/-U、multiline_dotall、replace/-r、passthru、crlf、auto_hybrid_regex、follow/-L/--follow 以及非 path 的时间类 sort/sortr 这类仅 rg 支持的参数会在无 rg 时明确提示；回退引擎会尽量模拟 rg 的常见行为。"
}

// isRgAvailable checks if ripgrep is available on the system, caching the result.
func (g *GrepTool) isRgAvailable() bool {
	if g == nil || g.lookPath == nil {
		return false
	}
	if g.rgAvailable != nil {
		return *g.rgAvailable
	}
	available := false
	if rgPath, err := g.lookPath("rg"); err == nil && strings.TrimSpace(rgPath) != "" {
		available = true
	}
	g.rgAvailable = &available
	return available
}

// Execute 实现 Tool 接口
func (g *GrepTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	opts, err := g.parseOptions(params)
	if err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      err,
		}, nil
	}

	for _, resolvedPath := range opts.resolvedPaths {
		if err := g.checkPath(runtimeexecutor.OpRead, resolvedPath); err != nil {
			return &toolkit.ToolResult{
				Success:    false,
				OutputKind: toolresult.KindText,
				Error:      err,
			}, nil
		}
	}
	for _, resolvedPath := range opts.resolvedPatternFiles {
		if err := g.checkPath(runtimeexecutor.OpRead, resolvedPath); err != nil {
			return &toolkit.ToolResult{
				Success:    false,
				OutputKind: toolresult.KindText,
				Error:      err,
			}, nil
		}
	}

	searchScopes, err := resolveSearchScopes(opts.searchPaths, opts.resolvedPaths)
	if err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      err,
		}, nil
	}
	opts.searchScopes = searchScopes
	if len(searchScopes) > 0 {
		opts.workingDir = searchScopes[0].workingDir
		opts.searchTarget = searchScopes[0].searchTarget
	}
	if err := g.loadPatternFiles(opts); err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      err,
		}, nil
	}
	if opts.requiresRipgrep && !g.isRgAvailable() {
		required := strings.Join(requiredRipgrepFeatures(opts), "、")
		if required == "" {
			required = "当前请求中的部分参数"
		}
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("当前搜索请求包含仅 ripgrep/rg 支持的参数（%s）；请在安装 rg 后重试", required),
		}, nil
	}
	if len(opts.patterns) == 0 {
		if result, used, rgErr := g.searchWithRipgrep(ctx, opts); rgErr != nil {
			return &toolkit.ToolResult{
				Success:    false,
				OutputKind: toolresult.KindText,
				Error:      rgErr,
			}, nil
		} else if used {
			return result, nil
		}
		return buildGrepResult(opts, nil, 0, false, nil), nil
	}

	// Compile regex for builtin engine (rg engine handles pattern natively)
	re, err := compileGrepPattern(opts.patterns, opts.literal, opts.ignoreCase, opts.word, opts.lineRegexp)
	if err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      err,
		}, nil
	}

	if result, used, rgErr := g.searchWithRipgrep(ctx, opts); rgErr != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      rgErr,
		}, nil
	} else if used {
		return result, nil
	}

	return g.searchWithWalker(opts, re), nil
}

type rgCompatArgs struct {
	positionals            []string
	patterns               []string
	patternFiles           []string
	include                []grepGlobPattern
	exclude                []grepGlobPattern
	literal                bool
	hasLiteral             bool
	ignoreCase             bool
	hasIgnoreCase          bool
	caseSensitive          bool
	hasCaseSensitive       bool
	smartCase              bool
	hasSmartCase           bool
	word                   bool
	hasWord                bool
	lineRegexp             bool
	hasLineRegexp          bool
	invertMatch            bool
	hasInvertMatch         bool
	onlyMatching           bool
	hasOnlyMatching        bool
	countMatches           bool
	hasCountMatches        bool
	stats                  bool
	hasStats               bool
	jsonOutput             bool
	hasJSONOutput          bool
	follow                 bool
	hasFollow              bool
	column                 bool
	hasColumn              bool
	trim                   bool
	hasTrim                bool
	pretty                 bool
	hasPretty              bool
	lineBuffered           bool
	hasLineBuffered        bool
	noLineBuffered         bool
	hasNoLineBuffered      bool
	maxColumns             int
	hasMaxColumns          bool
	maxColumnsPreview      bool
	hasMaxColumnsPreview   bool
	noMaxColumnsPreview    bool
	hasNoMaxColumnsPreview bool
	context                int
	hasContext             bool
	beforeContext          int
	hasBeforeContext       bool
	afterContext           int
	hasAfterContext        bool
	fileType               string
	hasFileType            bool
	excludeType            string
	hasExcludeType         bool
	sortBy                 string
	hasSortBy              bool
	sortReverseBy          string
	hasSortReverseBy       bool
	globCaseInsensitive    bool
	hasGlobCaseInsensitive bool
	maxDepth               int
	hasMaxDepth            bool
	maxCount               int
	hasMaxCount            bool
	maxFilesize            string
	hasMaxFilesize         bool
	requiresRipgrep        bool
	rgOnlyArgs             []string
	ignoredArgs            []string
	mode                   grepMode
	hasMode                bool
}

// parseOptions extracts and validates all search parameters.
func (g *GrepTool) parseOptions(params map[string]interface{}) (*grepOptions, error) {
	compat, err := parseRGCompatArgs(params)
	if err != nil {
		return nil, err
	}

	patternList := make([]string, 0, 4)
	if value, ok := resolveStringParam(params, "pattern", "regexp"); ok && strings.TrimSpace(value) != "" {
		patternList = append(patternList, strings.TrimSpace(value))
	}
	if values, ok := resolvePatternListParam(params, "patterns"); ok {
		patternList = append(patternList, values...)
	}
	patternList = append(patternList, compat.patterns...)
	patternList = normalizePatternList(patternList)
	patternFiles := make([]string, 0, 4)
	if value, ok := resolveStringParam(params, "pattern_file"); ok && strings.TrimSpace(value) != "" {
		patternFiles = append(patternFiles, strings.TrimSpace(value))
	}
	if values, ok := resolveSearchPathListParam(params, "pattern_files"); ok {
		patternFiles = append(patternFiles, values...)
	}
	patternFiles = append(patternFiles, normalizeSearchPathList(compat.patternFiles)...)
	patternFiles = normalizeSearchPathList(patternFiles)
	searchPaths := make([]string, 0, 4)
	if pathValue, ok := resolveStringParam(params, "path"); ok && strings.TrimSpace(pathValue) != "" {
		searchPaths = append(searchPaths, strings.TrimSpace(pathValue))
	}
	if values, ok := resolveSearchPathListParam(params, "paths"); ok {
		searchPaths = append(searchPaths, values...)
	}

	positionals := append([]string(nil), compat.positionals...)
	if len(patternList) == 0 && len(patternFiles) == 0 && len(positionals) > 0 {
		patternList = append(patternList, strings.TrimSpace(positionals[0]))
		positionals = positionals[1:]
	}
	if len(patternList) == 0 && len(patternFiles) == 0 {
		return nil, fmt.Errorf("pattern 参数缺失或无效")
	}
	pattern := ""
	if len(patternList) > 0 {
		pattern = patternList[0]
	}

	searchPaths = append(searchPaths, normalizeSearchPathList(positionals)...)
	if len(searchPaths) == 0 {
		searchPaths = []string{"."}
	}
	searchPath := searchPaths[0]
	resolvedPaths := make([]string, 0, len(searchPaths))
	for _, path := range searchPaths {
		resolvedPaths = append(resolvedPaths, g.resolvePath(path))
	}
	resolvedPath := resolvedPaths[0]
	resolvedPatternFiles := make([]string, 0, len(patternFiles))
	for _, path := range patternFiles {
		resolvedPatternFiles = append(resolvedPatternFiles, g.resolvePath(path))
	}

	globCaseInsensitive := compat.globCaseInsensitive
	if value, ok := resolveBoolParam(params, "glob_case_insensitive"); ok {
		globCaseInsensitive = value
	}

	includeSpecs := append([]grepGlobPattern(nil), compat.include...)
	excludeSpecs := append([]grepGlobPattern(nil), compat.exclude...)
	if globCaseInsensitive {
		includeSpecs = setGlobPatternsCaseInsensitive(includeSpecs, true)
		excludeSpecs = setGlobPatternsCaseInsensitive(excludeSpecs, true)
	}
	if values, ok := resolveStringListParam(params, "include"); ok {
		includeSpecs = appendGlobPatterns(includeSpecs, values, globCaseInsensitive)
	}
	if values, ok := resolveStringListParam(params, "exclude"); ok {
		excludeSpecs = appendGlobPatterns(excludeSpecs, values, globCaseInsensitive)
	}
	if values, ok := resolveStringListParam(params, "glob"); ok {
		globInclude, globExclude := partitionGlobPatterns(values, globCaseInsensitive)
		includeSpecs = append(includeSpecs, globInclude...)
		excludeSpecs = append(excludeSpecs, globExclude...)
	}
	includeSpecs = normalizeGlobPatterns(includeSpecs)
	excludeSpecs = normalizeGlobPatterns(excludeSpecs)
	includePatterns := globPatternStrings(includeSpecs)
	excludePatterns := globPatternStrings(excludeSpecs)

	literal := compat.literal
	if value, ok := resolveLiteralSearchParam(params); ok {
		literal = value
	}

	ignoreCaseFlag := compat.ignoreCase
	caseSensitiveFlag := compat.caseSensitive
	smartCaseFlag := compat.smartCase
	if value, ok := resolveBoolParam(params, "ignore_case"); ok {
		ignoreCaseFlag = value
	}
	if value, ok := resolveBoolParam(params, "case_sensitive"); ok {
		caseSensitiveFlag = value
	}
	if value, ok := resolveBoolParam(params, "smart_case"); ok {
		smartCaseFlag = value
	}

	ignoreCase := false
	switch {
	case caseSensitiveFlag:
		ignoreCase = false
	case ignoreCaseFlag:
		ignoreCase = true
	case smartCaseFlag:
		ignoreCase = shouldSmartCaseIgnorePatterns(patternList)
	}

	word := compat.word
	if value, ok := resolveBoolParam(params, "word", "word_regexp"); ok {
		word = value
	}
	lineRegexp := compat.lineRegexp
	if value, ok := resolveBoolParam(params, "line_regexp"); ok {
		lineRegexp = value
	}
	invertMatch := compat.invertMatch
	if value, ok := resolveBoolParam(params, "invert_match"); ok {
		invertMatch = value
	}
	onlyMatching := compat.onlyMatching
	if value, ok := resolveBoolParam(params, "only_matching"); ok {
		onlyMatching = value
	}
	countMatches := compat.countMatches
	if value, ok := resolveBoolParam(params, "count_matches"); ok {
		countMatches = value
	}
	statsRequested := compat.stats
	if value, ok := resolveBoolParam(params, "stats"); ok {
		statsRequested = value
	}
	pcre2Requested := false
	if value, ok := resolveBoolParam(params, "pcre2"); ok && value {
		pcre2Requested = true
		compat.requiresRipgrep = true
		compat.rgOnlyArgs = append(compat.rgOnlyArgs, "--pcre2")
	}
	if value, ok := resolveStringParam(params, "engine"); ok && strings.TrimSpace(value) != "" {
		compat.requiresRipgrep = true
		compat.rgOnlyArgs = append(compat.rgOnlyArgs, "--engine", strings.TrimSpace(value))
	}
	if value, ok := resolveBoolParam(params, "multiline"); ok && value {
		compat.requiresRipgrep = true
		compat.rgOnlyArgs = append(compat.rgOnlyArgs, "--multiline")
	}
	if value, ok := resolveBoolParam(params, "multiline_dotall"); ok && value {
		compat.requiresRipgrep = true
		compat.rgOnlyArgs = append(compat.rgOnlyArgs, "--multiline-dotall")
	}
	if value, ok := resolveStringParam(params, "replace"); ok && strings.TrimSpace(value) != "" {
		compat.requiresRipgrep = true
		compat.rgOnlyArgs = append(compat.rgOnlyArgs, "--replace", strings.TrimSpace(value))
	}
	if value, ok := resolveBoolParam(params, "passthru"); ok && value {
		compat.requiresRipgrep = true
		compat.rgOnlyArgs = append(compat.rgOnlyArgs, "--passthru")
	}
	if value, ok := resolveBoolParam(params, "crlf"); ok && value {
		compat.requiresRipgrep = true
		compat.rgOnlyArgs = append(compat.rgOnlyArgs, "--crlf")
	}
	if value, ok := resolveBoolParam(params, "auto_hybrid_regex"); ok && value {
		compat.requiresRipgrep = true
		compat.rgOnlyArgs = append(compat.rgOnlyArgs, "--auto-hybrid-regex")
	}
	jsonOutput := compat.jsonOutput
	if value, ok := resolveBoolParam(params, "json", "json_output"); ok {
		jsonOutput = value
	}
	followSymlinks := compat.follow
	if value, ok := resolveBoolParam(params, "follow"); ok {
		followSymlinks = value
	}
	column := compat.column
	if value, ok := resolveBoolParam(params, "column"); ok {
		column = value
	}
	trimOutput := compat.trim
	if value, ok := resolveBoolParam(params, "trim"); ok {
		trimOutput = value
	}
	pretty := compat.pretty
	if value, ok := resolveBoolParam(params, "pretty"); ok {
		pretty = value
	}
	lineBuffered := compat.lineBuffered
	if value, ok := resolveBoolParam(params, "line_buffered"); ok {
		lineBuffered = value
	}
	noLineBuffered := compat.noLineBuffered
	if value, ok := resolveBoolParam(params, "no_line_buffered"); ok {
		noLineBuffered = value
		if value {
			lineBuffered = false
		}
	}
	maxColumns := compat.maxColumns
	maxColumnsSet := compat.hasMaxColumns
	if value, ok := resolveIntParam(params, "max_columns"); ok {
		maxColumns = value
		maxColumnsSet = true
	}
	if maxColumns < 0 {
		maxColumns = 0
	}
	maxColumnsPreview := compat.maxColumnsPreview
	if value, ok := resolveBoolParam(params, "max_columns_preview"); ok {
		maxColumnsPreview = value
	}
	noMaxColumnsPreview := compat.noMaxColumnsPreview
	if value, ok := resolveBoolParam(params, "no_max_columns_preview"); ok && value {
		maxColumnsPreview = false
		noMaxColumnsPreview = true
	}

	contextLines := 0
	if compat.hasContext {
		contextLines = compat.context
	}
	beforeContext := contextLines
	afterContext := contextLines
	if compat.hasBeforeContext {
		beforeContext = compat.beforeContext
	}
	if compat.hasAfterContext {
		afterContext = compat.afterContext
	}
	if value, ok := resolveIntParam(params, "context"); ok {
		contextLines = value
		beforeContext = value
		afterContext = value
	}
	if value, ok := resolveIntParam(params, "before_context"); ok {
		beforeContext = value
	}
	if value, ok := resolveIntParam(params, "after_context"); ok {
		afterContext = value
	}
	beforeContext = clampContextLines(beforeContext)
	afterContext = clampContextLines(afterContext)
	contextLines = maxInt(beforeContext, afterContext)

	fileType := strings.TrimSpace(compat.fileType)
	if value, ok := resolveStringParam(params, "type", "file_type"); ok && strings.TrimSpace(value) != "" {
		fileType = strings.TrimSpace(value)
	}

	excludeType := strings.TrimSpace(compat.excludeType)
	if value, ok := resolveStringParam(params, "type_not"); ok && strings.TrimSpace(value) != "" {
		excludeType = strings.TrimSpace(value)
	}
	sortBy, err := normalizeRGSortValue(compat.sortBy)
	if err != nil {
		return nil, err
	}
	sortReverseBy, err := normalizeRGSortValue(compat.sortReverseBy)
	if err != nil {
		return nil, err
	}
	if value, ok := resolveStringParam(params, "sort"); ok && strings.TrimSpace(value) != "" {
		sortBy, err = normalizeRGSortValue(value)
		if err != nil {
			return nil, err
		}
		sortReverseBy = ""
	}
	if value, ok := resolveStringParam(params, "sortr", "sort_reverse"); ok && strings.TrimSpace(value) != "" {
		sortReverseBy, err = normalizeRGSortValue(value)
		if err != nil {
			return nil, err
		}
		sortBy = ""
	}
	if value, ok := resolveBoolParam(params, "sort_files"); ok && value {
		sortBy = "path"
		sortReverseBy = ""
	}
	if value, ok := resolveBoolParam(params, "no_sort_files"); ok && value {
		if sortBy == "path" {
			sortBy = ""
		}
		if sortReverseBy == "path" {
			sortReverseBy = ""
		}
	}

	maxDepth := compat.maxDepth
	maxDepthSet := compat.hasMaxDepth
	if value, ok := resolveIntParam(params, "max_depth"); ok {
		maxDepth = value
		maxDepthSet = true
	}
	if maxDepth < 0 {
		maxDepth = 0
	}

	maxCount := compat.maxCount
	maxCountSet := compat.hasMaxCount
	if value, ok := resolveIntParam(params, "max_count"); ok {
		maxCount = value
		maxCountSet = true
	}
	if maxCount < 0 {
		maxCount = 0
	}

	maxFilesize := strings.TrimSpace(compat.maxFilesize)
	if value, ok, err := resolveSizeParam(params, "max_filesize"); err != nil {
		return nil, err
	} else if ok {
		maxFilesize = value
	}
	maxFileBytes := int64(0)
	if maxFilesize != "" {
		parsed, err := parseSizeString(maxFilesize)
		if err != nil {
			return nil, fmt.Errorf("max_filesize 参数无效: %w", err)
		}
		maxFileBytes = parsed
	}

	mode := grepModeContent
	if compat.hasMode {
		mode = compat.mode
	}
	if value, ok := resolveBoolParam(params, "files_with_matches"); ok && value {
		mode = grepModeFiles
	}
	if value, ok := resolveBoolParam(params, "files_without_match", "files_without_matches"); ok && value {
		mode = grepModeFilesWithout
	}
	if value, ok := resolveBoolParam(params, "count"); ok && value {
		mode = grepModeCount
	}
	if countMatches {
		mode = grepModeCount
	}
	modeParam := ""
	if value, ok := resolveStringParam(params, "mode"); ok && strings.TrimSpace(value) != "" {
		modeParam = strings.TrimSpace(value)
		mode = normalizeGrepMode(value)
	}
	switch strings.TrimSpace(strings.ToLower(modeParam)) {
	case "count_matches", "count-matches":
		countMatches = true
	case "count":
		countMatches = false
	}
	if mode != grepModeCount {
		countMatches = false
	}
	requiresRipgrep := compat.requiresRipgrep
	if requiresRipgrepForSort(sortBy) || requiresRipgrepForSort(sortReverseBy) {
		requiresRipgrep = true
	}
	if jsonOutput {
		requiresRipgrep = true
	}
	if pcre2Requested {
		requiresRipgrep = true
	}
	if followSymlinks {
		requiresRipgrep = true
	}
	ignoredPresentation := collectIgnoredPresentationParams(params)

	return &grepOptions{
		pattern:              pattern,
		directPatterns:       append([]string(nil), patternList...),
		patterns:             patternList,
		searchPath:           searchPath,
		searchPaths:          append([]string(nil), searchPaths...),
		resolvedPath:         resolvedPath,
		resolvedPaths:        append([]string(nil), resolvedPaths...),
		patternFiles:         append([]string(nil), patternFiles...),
		resolvedPatternFiles: append([]string(nil), resolvedPatternFiles...),
		include:              strings.Join(includePatterns, ","),
		exclude:              strings.Join(excludePatterns, ","),
		includeSpecs:         append([]grepGlobPattern(nil), includeSpecs...),
		excludeSpecs:         append([]grepGlobPattern(nil), excludeSpecs...),
		globCaseInsensitive:  globCaseInsensitive,
		literal:              literal,
		ignoreCase:           ignoreCase,
		ignoreCaseRequested:  ignoreCaseFlag,
		caseSensitive:        caseSensitiveFlag,
		smartCase:            smartCaseFlag,
		word:                 word,
		lineRegexp:           lineRegexp,
		invertMatch:          invertMatch,
		onlyMatching:         onlyMatching,
		countMatches:         countMatches,
		stats:                statsRequested,
		jsonOutput:           jsonOutput,
		follow:               followSymlinks,
		column:               column,
		trim:                 trimOutput,
		pretty:               pretty,
		lineBuffered:         lineBuffered,
		noLineBuffered:       noLineBuffered,
		maxColumns:           maxColumns,
		maxColumnsSet:        maxColumnsSet,
		maxColumnsPreview:    maxColumnsPreview,
		noMaxColumnsPreview:  noMaxColumnsPreview,
		context:              contextLines,
		beforeContext:        beforeContext,
		afterContext:         afterContext,
		fileType:             fileType,
		excludeType:          excludeType,
		sortBy:               sortBy,
		sortReverseBy:        sortReverseBy,
		maxDepth:             maxDepth,
		maxDepthSet:          maxDepthSet,
		maxCount:             maxCount,
		maxCountSet:          maxCountSet,
		maxFilesize:          maxFilesize,
		maxFileBytes:         maxFileBytes,
		requiresRipgrep:      requiresRipgrep,
		rgOnlyArgs:           append([]string(nil), compat.rgOnlyArgs...),
		ignoredRGArgs:        normalizePatternList(compat.ignoredArgs),
		ignoredPresentation:  ignoredPresentation,
		mode:                 mode,
	}, nil
}

func compileGrepPattern(patterns []string, literal, ignoreCase, word, lineRegexp bool) (*regexp.Regexp, error) {
	if len(patterns) == 0 {
		return nil, fmt.Errorf("pattern 参数缺失或无效")
	}

	exprs := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		expr := pattern
		if literal {
			expr = regexp.QuoteMeta(pattern)
		}
		if word {
			expr = `\b` + expr + `\b`
		}
		if lineRegexp {
			expr = `^(?:` + expr + `)$`
		}
		exprs = append(exprs, expr)
	}

	expr := exprs[0]
	if len(exprs) > 1 {
		expr = `(?:` + strings.Join(exprs, `)|(?:`) + `)`
	}
	if ignoreCase {
		expr = `(?i)` + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, fmt.Errorf("正则表达式无效: %w", err)
	}
	return re, nil
}

func (g *GrepTool) loadPatternFiles(opts *grepOptions) error {
	if opts == nil || len(opts.resolvedPatternFiles) == 0 {
		g.applyEffectiveCaseMode(opts)
		return nil
	}

	effective := append([]string(nil), opts.directPatterns...)
	for _, filePath := range opts.resolvedPatternFiles {
		lines, err := readFileLines(filePath)
		if err != nil {
			return fmt.Errorf("读取 pattern_file 失败 %s: %w", filePath, err)
		}
		effective = append(effective, lines...)
	}
	opts.patterns = effective
	if opts.pattern == "" && len(effective) > 0 {
		opts.pattern = effective[0]
	}
	g.applyEffectiveCaseMode(opts)
	return nil
}

func (g *GrepTool) applyEffectiveCaseMode(opts *grepOptions) {
	if opts == nil {
		return
	}
	switch {
	case opts.caseSensitive:
		opts.ignoreCase = false
	case opts.ignoreCaseRequested:
		opts.ignoreCase = true
	case opts.smartCase:
		opts.ignoreCase = shouldSmartCaseIgnorePatterns(opts.patterns)
	}
}

func resolveSearchScopes(searchPaths, resolvedPaths []string) ([]grepSearchScope, error) {
	scopes := make([]grepSearchScope, 0, len(resolvedPaths))
	prefixOutputs := len(resolvedPaths) > 1
	for i, resolvedPath := range resolvedPaths {
		info, err := os.Stat(resolvedPath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("搜索路径不存在: %s", resolvedPath)
			}
			return nil, fmt.Errorf("无法访问搜索路径 %s: %w", resolvedPath, err)
		}
		scope := grepSearchScope{}
		if info.IsDir() {
			scope.workingDir = resolvedPath
		} else {
			scope.workingDir = filepath.Dir(resolvedPath)
			scope.searchTarget = filepath.Base(resolvedPath)
		}
		if prefixOutputs && i < len(searchPaths) {
			scope.displayPath = normalizeDisplayPath(searchPaths[i])
		}
		scopes = append(scopes, scope)
	}
	return scopes, nil
}

func parseRGCompatArgs(params map[string]interface{}) (*rgCompatArgs, error) {
	args, ok := resolveStringArrayParam(params, "rg_args")
	if !ok || len(args) == 0 {
		return &rgCompatArgs{}, nil
	}

	compat := &rgCompatArgs{}
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		if arg == "" {
			continue
		}
		if arg == "--" {
			compat.positionals = append(compat.positionals, args[i+1:]...)
			break
		}

		if handled, err := consumeNoOpRGFlag(args, &i, arg, compat); handled {
			if err != nil {
				return nil, err
			}
			continue
		}

		if applyRGBooleanFlag(compat, arg) {
			continue
		}

		if value, handled, err := parseRGFlagWithValue(args, &i, arg); handled {
			if err != nil {
				return nil, err
			}
			applyRGFlagValue(compat, value)
			continue
		}
		if handled, err := parseRGShortFlagCluster(args, &i, arg, compat); handled {
			if err != nil {
				return nil, err
			}
			continue
		}

		if strings.HasPrefix(arg, "-") {
			return nil, fmt.Errorf("暂不支持的 rg_args 选项: %s", arg)
		}
		compat.positionals = append(compat.positionals, arg)
	}

	return compat, nil
}

func consumeNoOpRGFlag(args []string, index *int, arg string, compat *rgCompatArgs) (bool, error) {
	switch arg {
	case "--color":
		if *index+1 >= len(args) {
			return true, fmt.Errorf("rg_args 选项缺少值: %s", arg)
		}
		next := strings.TrimSpace(args[*index+1])
		if next == "" || strings.HasPrefix(next, "-") {
			return true, fmt.Errorf("rg_args 选项缺少值: %s", arg)
		}
		if compat != nil {
			compat.ignoredArgs = appendUniqueString(compat.ignoredArgs, arg+"="+next)
		}
		*index = *index + 1
		return true, nil
	default:
		if isNoOpRGFlag(arg) {
			if compat != nil {
				compat.ignoredArgs = appendUniqueString(compat.ignoredArgs, arg)
			}
			return true, nil
		}
	}
	return false, nil
}

type rgFlagValue struct {
	name     string
	value    string
	intValue int
}

func applyRGBooleanFlag(compat *rgCompatArgs, arg string) bool {
	switch arg {
	case "-F", "--fixed-strings", "--fixed_strings":
		compat.literal = true
		compat.hasLiteral = true
	case "-i", "--ignore-case", "--ignore_case":
		compat.ignoreCase = true
		compat.hasIgnoreCase = true
	case "-s", "--case-sensitive", "--case_sensitive":
		compat.caseSensitive = true
		compat.hasCaseSensitive = true
	case "-S", "--smart-case", "--smart_case":
		compat.smartCase = true
		compat.hasSmartCase = true
	case "-w", "--word-regexp", "--word_regexp":
		compat.word = true
		compat.hasWord = true
	case "-x", "--line-regexp", "--line_regexp":
		compat.lineRegexp = true
		compat.hasLineRegexp = true
	case "-v", "--invert-match", "--invert_match":
		compat.invertMatch = true
		compat.hasInvertMatch = true
	case "-o", "--only-matching", "--only_matching":
		compat.onlyMatching = true
		compat.hasOnlyMatching = true
	case "--count-matches":
		compat.mode = grepModeCount
		compat.hasMode = true
		compat.countMatches = true
		compat.hasCountMatches = true
	case "--stats":
		compat.stats = true
		compat.hasStats = true
	case "--json":
		compat.jsonOutput = true
		compat.hasJSONOutput = true
		compat.requiresRipgrep = true
	case "-L", "--follow":
		compat.follow = true
		compat.hasFollow = true
	case "--column":
		compat.column = true
		compat.hasColumn = true
	case "--trim":
		compat.trim = true
		compat.hasTrim = true
	case "--pretty":
		compat.pretty = true
		compat.hasPretty = true
	case "--line-buffered":
		compat.lineBuffered = true
		compat.hasLineBuffered = true
	case "--no-line-buffered":
		compat.noLineBuffered = true
		compat.hasNoLineBuffered = true
	case "--max-columns-preview":
		compat.maxColumnsPreview = true
		compat.hasMaxColumnsPreview = true
	case "--no-max-columns-preview":
		compat.maxColumnsPreview = false
		compat.hasNoMaxColumnsPreview = true
	case "-U", "--multiline":
		compat.requiresRipgrep = true
		compat.rgOnlyArgs = append(compat.rgOnlyArgs, "--multiline")
	case "--multiline-dotall":
		compat.requiresRipgrep = true
		compat.rgOnlyArgs = append(compat.rgOnlyArgs, "--multiline-dotall")
	case "--passthru":
		compat.requiresRipgrep = true
		compat.rgOnlyArgs = append(compat.rgOnlyArgs, "--passthru")
	case "--crlf":
		compat.requiresRipgrep = true
		compat.rgOnlyArgs = append(compat.rgOnlyArgs, "--crlf")
	case "-l", "--files-with-matches", "--files_with_matches":
		compat.mode = grepModeFiles
		compat.hasMode = true
	case "--files-without-match", "--files_without_match":
		compat.mode = grepModeFilesWithout
		compat.hasMode = true
	case "-c", "--count":
		compat.mode = grepModeCount
		compat.hasMode = true
		compat.countMatches = false
		compat.hasCountMatches = true
	case "-P", "--pcre2":
		compat.requiresRipgrep = true
		compat.rgOnlyArgs = append(compat.rgOnlyArgs, "--pcre2")
	case "--auto-hybrid-regex":
		compat.requiresRipgrep = true
		compat.rgOnlyArgs = append(compat.rgOnlyArgs, "--auto-hybrid-regex")
	case "--glob-case-insensitive":
		compat.globCaseInsensitive = true
		compat.hasGlobCaseInsensitive = true
		compat.include = setGlobPatternsCaseInsensitive(compat.include, true)
		compat.exclude = setGlobPatternsCaseInsensitive(compat.exclude, true)
	case "--sort-files":
		compat.sortBy = "path"
		compat.sortReverseBy = ""
		compat.hasSortBy = true
		compat.hasSortReverseBy = false
	case "--no-sort-files":
		compat.sortBy = ""
		compat.sortReverseBy = ""
		compat.hasSortBy = true
		compat.hasSortReverseBy = true
	default:
		return false
	}
	return true
}

func parseRGShortFlagCluster(args []string, index *int, arg string, compat *rgCompatArgs) (bool, error) {
	if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") || len(arg) <= 2 {
		return false, nil
	}

	rest := arg[1:]
	handled := false
	for pos := 0; pos < len(rest); pos++ {
		flag := "-" + string(rest[pos])
		handled = true
		if isNoOpRGFlag(flag) {
			continue
		}
		if applyRGBooleanFlag(compat, flag) {
			continue
		}

		var valueName string
		switch flag {
		case "-e":
			valueName = "pattern"
		case "-f":
			valueName = "pattern_file"
		case "-r":
			valueName = "replace"
		case "-g":
			valueName = "glob"
		case "-C":
			valueName = "context"
		case "-B":
			valueName = "before_context"
		case "-A":
			valueName = "after_context"
		case "-t":
			valueName = "file_type"
		case "-T":
			valueName = "type_not"
		case "-m":
			valueName = "max_count"
		case "-M":
			valueName = "max_columns"
		default:
			return true, fmt.Errorf("暂不支持的 rg_args 选项: %s", flag)
		}

		rawValue := strings.TrimSpace(rest[pos+1:])
		if rawValue == "" {
			if *index+1 >= len(args) {
				return true, fmt.Errorf("rg_args 选项缺少值: %s", flag)
			}
			*index = *index + 1
			rawValue = strings.TrimSpace(args[*index])
		}

		value := &rgFlagValue{name: valueName, value: rawValue}
		switch valueName {
		case "context", "before_context", "after_context", "max_count", "max_columns":
			parsed, _, err := parseRGIntFlag(valueName, rawValue, flag)
			if err != nil {
				return true, err
			}
			value = parsed
		}
		applyRGFlagValue(compat, value)
		return true, nil
	}

	return handled, nil
}

func applyRGFlagValue(compat *rgCompatArgs, value *rgFlagValue) {
	switch value.name {
	case "pattern":
		compat.patterns = append(compat.patterns, value.value)
	case "pattern_file":
		compat.patternFiles = append(compat.patternFiles, value.value)
	case "glob":
		globInclude, globExclude := partitionGlobPatterns([]string{value.value}, compat.globCaseInsensitive)
		compat.include = append(compat.include, globInclude...)
		compat.exclude = append(compat.exclude, globExclude...)
	case "iglob":
		globInclude, globExclude := partitionGlobPatterns([]string{value.value}, true)
		compat.include = append(compat.include, globInclude...)
		compat.exclude = append(compat.exclude, globExclude...)
	case "context":
		compat.context = value.intValue
		compat.hasContext = true
	case "before_context":
		compat.beforeContext = value.intValue
		compat.hasBeforeContext = true
	case "after_context":
		compat.afterContext = value.intValue
		compat.hasAfterContext = true
	case "file_type":
		compat.fileType = value.value
		compat.hasFileType = true
	case "type_not":
		compat.excludeType = value.value
		compat.hasExcludeType = true
	case "sort":
		compat.sortBy = value.value
		compat.sortReverseBy = ""
		compat.hasSortBy = true
		compat.hasSortReverseBy = false
	case "sortr":
		compat.sortReverseBy = value.value
		compat.sortBy = ""
		compat.hasSortReverseBy = true
		compat.hasSortBy = false
	case "max_depth":
		compat.maxDepth = value.intValue
		compat.hasMaxDepth = true
	case "engine":
		compat.requiresRipgrep = true
		compat.rgOnlyArgs = append(compat.rgOnlyArgs, "--engine", value.value)
	case "replace":
		compat.requiresRipgrep = true
		compat.rgOnlyArgs = append(compat.rgOnlyArgs, "--replace", value.value)
	case "max_count":
		compat.maxCount = value.intValue
		compat.hasMaxCount = true
	case "max_columns":
		compat.maxColumns = value.intValue
		compat.hasMaxColumns = true
	case "max_filesize":
		compat.maxFilesize = value.value
		compat.hasMaxFilesize = true
	}
}

func parseRGFlagWithValue(args []string, index *int, arg string) (*rgFlagValue, bool, error) {
	if raw, ok := strings.CutPrefix(arg, "--regexp="); ok {
		return &rgFlagValue{name: "pattern", value: raw}, true, nil
	}
	if raw, ok := strings.CutPrefix(arg, "--file="); ok {
		return &rgFlagValue{name: "pattern_file", value: raw}, true, nil
	}
	if raw, ok := strings.CutPrefix(arg, "--replace="); ok {
		return &rgFlagValue{name: "replace", value: raw}, true, nil
	}
	if raw, ok := strings.CutPrefix(arg, "--glob="); ok {
		return &rgFlagValue{name: "glob", value: raw}, true, nil
	}
	if raw, ok := strings.CutPrefix(arg, "--iglob="); ok {
		return &rgFlagValue{name: "iglob", value: raw}, true, nil
	}
	if raw, ok := strings.CutPrefix(arg, "--context="); ok {
		return parseRGIntFlag("context", raw, arg)
	}
	if raw, ok := strings.CutPrefix(arg, "--before-context="); ok {
		return parseRGIntFlag("before_context", raw, arg)
	}
	if raw, ok := strings.CutPrefix(arg, "--after-context="); ok {
		return parseRGIntFlag("after_context", raw, arg)
	}
	if raw, ok := strings.CutPrefix(arg, "--type="); ok {
		return &rgFlagValue{name: "file_type", value: raw}, true, nil
	}
	if raw, ok := strings.CutPrefix(arg, "--type-not="); ok {
		return &rgFlagValue{name: "type_not", value: raw}, true, nil
	}
	if raw, ok := strings.CutPrefix(arg, "--sort="); ok {
		return &rgFlagValue{name: "sort", value: raw}, true, nil
	}
	if raw, ok := strings.CutPrefix(arg, "--sortr="); ok {
		return &rgFlagValue{name: "sortr", value: raw}, true, nil
	}
	if raw, ok := strings.CutPrefix(arg, "--max-depth="); ok {
		return parseRGIntFlag("max_depth", raw, arg)
	}
	if raw, ok := strings.CutPrefix(arg, "--engine="); ok {
		return &rgFlagValue{name: "engine", value: raw}, true, nil
	}
	if raw, ok := strings.CutPrefix(arg, "--max-count="); ok {
		return parseRGIntFlag("max_count", raw, arg)
	}
	if raw, ok := strings.CutPrefix(arg, "--max-columns="); ok {
		return parseRGIntFlag("max_columns", raw, arg)
	}
	if raw, ok := strings.CutPrefix(arg, "--max-filesize="); ok {
		return &rgFlagValue{name: "max_filesize", value: raw}, true, nil
	}

	switch arg {
	case "-e", "--regexp", "-f", "--file", "-r", "--replace", "-g", "--glob", "--iglob", "-C", "--context", "-B", "--before-context", "-A", "--after-context", "-t", "--type", "-T", "--type-not", "-m", "--max-count", "-M", "--max-columns", "--max-depth", "--max-filesize", "--engine", "--sort", "--sortr":
		if *index+1 >= len(args) {
			return nil, true, fmt.Errorf("rg_args 选项缺少值: %s", arg)
		}
		*index = *index + 1
		next := strings.TrimSpace(args[*index])
		switch arg {
		case "-e", "--regexp":
			return &rgFlagValue{name: "pattern", value: next}, true, nil
		case "-f", "--file":
			return &rgFlagValue{name: "pattern_file", value: next}, true, nil
		case "-r", "--replace":
			return &rgFlagValue{name: "replace", value: next}, true, nil
		case "-g", "--glob":
			return &rgFlagValue{name: "glob", value: next}, true, nil
		case "--iglob":
			return &rgFlagValue{name: "iglob", value: next}, true, nil
		case "-C", "--context":
			return parseRGIntFlag("context", next, arg)
		case "-B", "--before-context":
			return parseRGIntFlag("before_context", next, arg)
		case "-A", "--after-context":
			return parseRGIntFlag("after_context", next, arg)
		case "-t", "--type":
			return &rgFlagValue{name: "file_type", value: next}, true, nil
		case "-T", "--type-not":
			return &rgFlagValue{name: "type_not", value: next}, true, nil
		case "--sort":
			return &rgFlagValue{name: "sort", value: next}, true, nil
		case "--sortr":
			return &rgFlagValue{name: "sortr", value: next}, true, nil
		case "-m", "--max-count":
			return parseRGIntFlag("max_count", next, arg)
		case "-M", "--max-columns":
			return parseRGIntFlag("max_columns", next, arg)
		case "--max-depth":
			return parseRGIntFlag("max_depth", next, arg)
		case "--max-filesize":
			return &rgFlagValue{name: "max_filesize", value: next}, true, nil
		case "--engine":
			return &rgFlagValue{name: "engine", value: next}, true, nil
		}
	}

	if len(arg) > 2 && strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") {
		switch {
		case strings.HasPrefix(arg, "-e"):
			return &rgFlagValue{name: "pattern", value: arg[2:]}, true, nil
		case strings.HasPrefix(arg, "-f"):
			return &rgFlagValue{name: "pattern_file", value: arg[2:]}, true, nil
		case strings.HasPrefix(arg, "-r"):
			return &rgFlagValue{name: "replace", value: arg[2:]}, true, nil
		case strings.HasPrefix(arg, "-g"):
			return &rgFlagValue{name: "glob", value: arg[2:]}, true, nil
		case strings.HasPrefix(arg, "-C"):
			return parseRGIntFlag("context", arg[2:], arg)
		case strings.HasPrefix(arg, "-B"):
			return parseRGIntFlag("before_context", arg[2:], arg)
		case strings.HasPrefix(arg, "-A"):
			return parseRGIntFlag("after_context", arg[2:], arg)
		case strings.HasPrefix(arg, "-t"):
			return &rgFlagValue{name: "file_type", value: arg[2:]}, true, nil
		case strings.HasPrefix(arg, "-T"):
			return &rgFlagValue{name: "type_not", value: arg[2:]}, true, nil
		case strings.HasPrefix(arg, "-m"):
			return parseRGIntFlag("max_count", arg[2:], arg)
		case strings.HasPrefix(arg, "-M"):
			return parseRGIntFlag("max_columns", arg[2:], arg)
		}
	}

	return nil, false, nil
}

func parseRGIntFlag(name, raw, flag string) (*rgFlagValue, bool, error) {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return nil, true, fmt.Errorf("rg_args 选项 %s 的值无效: %q", flag, raw)
	}
	return &rgFlagValue{name: name, value: raw, intValue: value}, true, nil
}

func isNoOpRGFlag(arg string) bool {
	switch arg {
	case "-n", "--line-number", "-H", "--with-filename", "-N", "--no-line-number", "--no-filename", "--no-heading", "--heading", "--hidden", "--no-ignore", "-a", "--text", "--binary", "--color", "--color=never", "-u", "-uu", "-uuu", "--pretty", "--line-buffered", "--no-line-buffered":
		return true
	default:
		if strings.HasPrefix(arg, "--color=") {
			return true
		}
	}
	return false
}

func resolveLiteralSearchParam(params map[string]interface{}) (bool, bool) {
	return resolveBoolParam(params, "literal", "literal_text", "fixed_strings")
}

func resolveBoolParam(params map[string]interface{}, keys ...string) (bool, bool) {
	for _, key := range keys {
		if v, ok := params[key].(bool); ok {
			return v, true
		}
	}
	return false, false
}

func resolveIntParam(params map[string]interface{}, keys ...string) (int, bool) {
	for _, key := range keys {
		switch v := params[key].(type) {
		case float64:
			return int(v), true
		case int:
			return v, true
		case int64:
			return int(v), true
		}
	}
	return 0, false
}

func resolveStringParam(params map[string]interface{}, keys ...string) (string, bool) {
	for _, key := range keys {
		if v, ok := params[key].(string); ok {
			return v, true
		}
	}
	return "", false
}

func resolveStringArrayParam(params map[string]interface{}, keys ...string) ([]string, bool) {
	for _, key := range keys {
		raw, exists := params[key]
		if !exists {
			continue
		}
		switch values := raw.(type) {
		case []string:
			return normalizePatternList(values), true
		case []interface{}:
			result := make([]string, 0, len(values))
			for _, value := range values {
				if text, ok := value.(string); ok {
					result = append(result, strings.TrimSpace(text))
				}
			}
			return normalizePatternList(result), true
		case string:
			return normalizePatternList(strings.Fields(values)), true
		}
	}
	return nil, false
}

func resolveStringListParam(params map[string]interface{}, keys ...string) ([]string, bool) {
	for _, key := range keys {
		raw, exists := params[key]
		if !exists {
			continue
		}
		switch values := raw.(type) {
		case string:
			return splitCommaSeparatedPatterns(values), true
		case []string:
			return normalizePatternList(values), true
		case []interface{}:
			result := make([]string, 0, len(values))
			for _, value := range values {
				if text, ok := value.(string); ok {
					result = append(result, text)
				}
			}
			return normalizePatternList(result), true
		}
	}
	return nil, false
}

func resolvePatternListParam(params map[string]interface{}, keys ...string) ([]string, bool) {
	for _, key := range keys {
		raw, exists := params[key]
		if !exists {
			continue
		}
		switch values := raw.(type) {
		case string:
			return normalizePatternList([]string{values}), true
		case []string:
			return normalizePatternList(values), true
		case []interface{}:
			result := make([]string, 0, len(values))
			for _, value := range values {
				if text, ok := value.(string); ok {
					result = append(result, text)
				}
			}
			return normalizePatternList(result), true
		}
	}
	return nil, false
}

func resolveSearchPathListParam(params map[string]interface{}, keys ...string) ([]string, bool) {
	for _, key := range keys {
		raw, exists := params[key]
		if !exists {
			continue
		}
		switch values := raw.(type) {
		case string:
			return normalizeSearchPathList([]string{values}), true
		case []string:
			return normalizeSearchPathList(values), true
		case []interface{}:
			result := make([]string, 0, len(values))
			for _, value := range values {
				if text, ok := value.(string); ok {
					result = append(result, text)
				}
			}
			return normalizeSearchPathList(result), true
		}
	}
	return nil, false
}

func resolveSizeParam(params map[string]interface{}, keys ...string) (string, bool, error) {
	for _, key := range keys {
		raw, exists := params[key]
		if !exists {
			continue
		}
		switch value := raw.(type) {
		case string:
			trimmed := strings.TrimSpace(value)
			if trimmed == "" {
				return "", false, nil
			}
			return trimmed, true, nil
		case float64:
			return strconv.FormatInt(int64(value), 10), true, nil
		case int:
			return strconv.Itoa(value), true, nil
		case int64:
			return strconv.FormatInt(value, 10), true, nil
		default:
			return "", false, fmt.Errorf("%s 仅支持字符串或整数", key)
		}
	}
	return "", false, nil
}

func splitCommaSeparatedPatterns(value string) []string {
	parts := strings.Split(value, ",")
	return normalizePatternList(parts)
}

func normalizeSearchPathList(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		result = append(result, value)
	}
	return result
}

func normalizeDisplayPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	cleaned := filepath.Clean(raw)
	if cleaned == "." {
		return ""
	}
	return filepath.ToSlash(cleaned)
}

func composeScopeDisplayPath(scope grepSearchScope, relPath string) string {
	if scope.displayPath == "" {
		return filepath.ToSlash(relPath)
	}
	if scope.searchTarget != "" {
		return scope.displayPath
	}
	relPath = filepath.ToSlash(relPath)
	if relPath == "" || relPath == "." {
		return scope.displayPath
	}
	return filepath.ToSlash(filepath.Join(filepath.FromSlash(scope.displayPath), filepath.FromSlash(relPath)))
}

func partitionGlobPatterns(values []string, caseInsensitive bool) ([]grepGlobPattern, []grepGlobPattern) {
	include := make([]grepGlobPattern, 0, len(values))
	exclude := make([]grepGlobPattern, 0, len(values))
	for _, value := range normalizePatternList(values) {
		if strings.HasPrefix(value, "!") {
			value = strings.TrimSpace(strings.TrimPrefix(value, "!"))
			if value != "" {
				exclude = append(exclude, grepGlobPattern{pattern: value, caseInsensitive: caseInsensitive})
			}
			continue
		}
		include = append(include, grepGlobPattern{pattern: value, caseInsensitive: caseInsensitive})
	}
	return include, exclude
}

func appendGlobPatterns(dst []grepGlobPattern, values []string, caseInsensitive bool) []grepGlobPattern {
	for _, value := range normalizePatternList(values) {
		dst = append(dst, grepGlobPattern{pattern: value, caseInsensitive: caseInsensitive})
	}
	return dst
}

func setGlobPatternsCaseInsensitive(values []grepGlobPattern, enabled bool) []grepGlobPattern {
	if !enabled {
		return append([]grepGlobPattern(nil), values...)
	}
	result := make([]grepGlobPattern, len(values))
	copy(result, values)
	for i := range result {
		result[i].caseInsensitive = true
	}
	return result
}

func normalizeGlobPatterns(values []grepGlobPattern) []grepGlobPattern {
	result := make([]grepGlobPattern, 0, len(values))
	for _, value := range values {
		value.pattern = strings.TrimSpace(value.pattern)
		if value.pattern == "" {
			continue
		}
		result = append(result, value)
	}
	return result
}

func globPatternStrings(values []grepGlobPattern) []string {
	result := make([]string, 0, len(values))
	for _, value := range normalizeGlobPatterns(values) {
		result = append(result, value.pattern)
	}
	return result
}

func normalizePatternList(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		result = append(result, value)
	}
	return result
}

func appendUniqueString(values []string, raw string) []string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func collectIgnoredPresentationParams(params map[string]interface{}) []string {
	flags := make([]string, 0, 8)
	if value, ok := resolveBoolParam(params, "line_number"); ok && value {
		flags = appendUniqueString(flags, "line_number")
	}
	if value, ok := resolveBoolParam(params, "heading"); ok && value {
		flags = appendUniqueString(flags, "heading")
	}
	if value, ok := resolveBoolParam(params, "no_heading"); ok && value {
		flags = appendUniqueString(flags, "no_heading")
	}
	if value, ok := resolveBoolParam(params, "with_filename"); ok && value {
		flags = appendUniqueString(flags, "with_filename")
	}
	if value, ok := resolveBoolParam(params, "no_line_number"); ok && value {
		flags = appendUniqueString(flags, "no_line_number")
	}
	if value, ok := resolveBoolParam(params, "no_filename"); ok && value {
		flags = appendUniqueString(flags, "no_filename")
	}
	if value, ok := resolveBoolParam(params, "pretty"); ok && value {
		flags = appendUniqueString(flags, "pretty")
	}
	if value, ok := resolveBoolParam(params, "line_buffered"); ok && value {
		flags = appendUniqueString(flags, "line_buffered")
	}
	if value, ok := resolveBoolParam(params, "no_line_buffered"); ok && value {
		flags = appendUniqueString(flags, "no_line_buffered")
	}
	if value, ok := resolveStringParam(params, "color"); ok && strings.TrimSpace(value) != "" {
		flags = appendUniqueString(flags, "color="+strings.TrimSpace(value))
	}
	if value, ok := resolveBoolParam(params, "text"); ok && value {
		flags = appendUniqueString(flags, "text")
	}
	if value, ok := resolveBoolParam(params, "binary"); ok && value {
		flags = appendUniqueString(flags, "binary")
	}
	if value, ok := resolveBoolParam(params, "hidden"); ok && value {
		flags = appendUniqueString(flags, "hidden")
	}
	if value, ok := resolveBoolParam(params, "no_ignore"); ok && value {
		flags = appendUniqueString(flags, "no_ignore")
	}
	return flags
}

func parseSizeString(raw string) (int64, error) {
	value := strings.TrimSpace(strings.ToUpper(raw))
	if value == "" {
		return 0, fmt.Errorf("不能为空")
	}
	multiplier := int64(1)
	switch {
	case strings.HasSuffix(value, "K"):
		multiplier = 1024
		value = strings.TrimSuffix(value, "K")
	case strings.HasSuffix(value, "M"):
		multiplier = 1024 * 1024
		value = strings.TrimSuffix(value, "M")
	case strings.HasSuffix(value, "G"):
		multiplier = 1024 * 1024 * 1024
		value = strings.TrimSuffix(value, "G")
	case strings.HasSuffix(value, "B"):
		value = strings.TrimSuffix(value, "B")
	}
	base, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("无法解析大小 %q", raw)
	}
	if base < 0 {
		return 0, fmt.Errorf("大小不能为负数")
	}
	return base * multiplier, nil
}

func normalizeRGSortValue(raw string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return "", nil
	}
	switch value {
	case "none", "path", "modified", "accessed", "created":
		return value, nil
	default:
		return "", fmt.Errorf("sort/sortr 参数无效: %q（仅支持 none/path/modified/accessed/created）", raw)
	}
}

func requiresRipgrepForSort(value string) bool {
	switch strings.TrimSpace(value) {
	case "", "none", "path":
		return false
	default:
		return true
	}
}

func normalizeGrepMode(value string) grepMode {
	switch strings.TrimSpace(value) {
	case string(grepModeFiles), "files-with-matches", "files_with_matches":
		return grepModeFiles
	case string(grepModeFilesWithout), "files-without-match", "files_without_match", "files_without_matches":
		return grepModeFilesWithout
	case string(grepModeCount), "count-matches", "count_matches":
		return grepModeCount
	default:
		return grepModeContent
	}
}

func shouldSmartCaseIgnorePatterns(patterns []string) bool {
	for _, pattern := range patterns {
		for _, r := range pattern {
			if unicode.IsUpper(r) {
				return false
			}
		}
	}
	return true
}

func clampContextLines(value int) int {
	if value < 0 {
		return 0
	}
	if value > 10 {
		return 10
	}
	return value
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func hasExplicitZeroMaxCount(opts *grepOptions) bool {
	return opts != nil && opts.maxCountSet && opts.maxCount == 0
}

func hasReachedMaxCount(opts *grepOptions, count int) bool {
	return opts != nil && opts.maxCountSet && count >= opts.maxCount
}

func lineSatisfiesMatch(re *regexp.Regexp, line string, invert bool) bool {
	matched := re.MatchString(line)
	if invert {
		return !matched
	}
	return matched
}

type grepRenderedMatch struct {
	text   string
	column int
}

func trimGrepRenderedText(text string) string {
	return strings.TrimLeftFunc(text, unicode.IsSpace)
}

func truncateGrepTextByBytes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	raw := []byte(text)
	if len(raw) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && (raw[cut]&0xC0) == 0x80 {
		cut--
	}
	if cut <= 0 {
		return ""
	}
	return string(raw[:cut])
}

func applyMaxColumnsToRenderedText(opts *grepOptions, text string) string {
	if opts == nil || !opts.maxColumnsSet || opts.maxColumns <= 0 {
		return text
	}
	if len([]byte(text)) <= opts.maxColumns {
		return text
	}
	if opts.maxColumnsPreview {
		return truncateGrepTextByBytes(text, opts.maxColumns) + " [... omitted end of long line]"
	}
	return "[Omitted long matching line]"
}

func extractRenderedMatches(re *regexp.Regexp, line string, onlyMatching, invert, trim bool) []grepRenderedMatch {
	if !onlyMatching || invert {
		text := line
		if trim {
			text = trimGrepRenderedText(text)
		}
		column := 0
		if !invert {
			if loc := re.FindStringIndex(line); len(loc) == 2 {
				column = loc[0] + 1
			}
		}
		return []grepRenderedMatch{{text: text, column: column}}
	}
	indices := re.FindAllStringIndex(line, -1)
	if len(indices) == 0 {
		text := line
		if trim {
			text = trimGrepRenderedText(text)
		}
		return []grepRenderedMatch{{text: text}}
	}
	matches := make([]grepRenderedMatch, 0, len(indices))
	for _, loc := range indices {
		text := line[loc[0]:loc[1]]
		if trim {
			text = trimGrepRenderedText(text)
		}
		matches = append(matches, grepRenderedMatch{text: text, column: loc[0] + 1})
	}
	return matches
}

func countRenderedMatches(re *regexp.Regexp, line string, onlyMatching, invert bool) int {
	if !onlyMatching || invert {
		return 1
	}
	matches := re.FindAllStringIndex(line, -1)
	if len(matches) == 0 {
		return 1
	}
	return len(matches)
}

func countActualRegexMatches(re *regexp.Regexp, line string, invert bool) int {
	if invert {
		return 0
	}
	matches := re.FindAllStringIndex(line, -1)
	if len(matches) == 0 {
		return 0
	}
	return len(matches)
}

func formatGrepContentLine(filePath string, lineNum, column int, text string, includeColumn bool) string {
	if includeColumn && column > 0 {
		return fmt.Sprintf("%s:%d:%d: %s", filePath, lineNum, column, text)
	}
	return fmt.Sprintf("%s:%d: %s", filePath, lineNum, text)
}

// --- Ripgrep engine ---

func (g *GrepTool) searchWithRipgrep(ctx context.Context, opts *grepOptions) (*toolkit.ToolResult, bool, error) {
	if g == nil || g.lookPath == nil || g.runCommand == nil {
		return nil, false, nil
	}
	rgPath, err := g.lookPath("rg")
	if err != nil || strings.TrimSpace(rgPath) == "" {
		return nil, false, nil
	}

	allLines := make([]string, 0, 32)
	totalMatchCount := 0
	truncated := false
	var aggregatedStats *grepStats

	for _, scope := range opts.searchScopes {
		args := buildRipgrepArgs(opts, scope, g.maxMatches)
		output, err := g.runCommand(ctx, rgPath, scope.workingDir, args)
		if opts.jsonOutput {
			if err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return nil, false, ctxErr
				}
				if !isRipgrepNoMatch(err) {
					if opts.requiresRipgrep {
						return nil, false, fmt.Errorf("ripgrep/rg 执行失败: %w", err)
					}
					return nil, false, nil
				}
			}

			lines, stats := normalizeRipgrepJSONOutput(output, scope, opts.mode)
			if stats != nil {
				if aggregatedStats == nil {
					aggregatedStats = &grepStats{}
				}
				aggregatedStats.add(stats)
			}
			if stats != nil {
				totalMatchCount += stats.Matches
			}
			if len(allLines) < g.maxMatches {
				remaining := g.maxMatches - len(allLines)
				if len(lines) > remaining {
					allLines = append(allLines, lines[:remaining]...)
					truncated = true
				} else {
					allLines = append(allLines, lines...)
				}
			} else if len(lines) > 0 {
				truncated = true
			}
			continue
		}
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, false, ctxErr
			}
			if isRipgrepNoMatch(err) {
				if opts.stats && len(output) > 0 {
					lines, stats := normalizeRipgrepOutputWithStats(output)
					lines = prefixRipgrepLinesForScope(scope, opts.mode, lines)
					totalMatchCount += countRipgrepResults(opts.mode, lines)
					if stats != nil {
						if aggregatedStats == nil {
							aggregatedStats = &grepStats{}
						}
						aggregatedStats.add(stats)
					}
				}
				continue
			}
			if opts.requiresRipgrep {
				return nil, false, fmt.Errorf("ripgrep/rg 执行失败: %w", err)
			}
			return nil, false, nil
		}

		lines, stats := normalizeRipgrepOutputWithStats(output)
		lines = prefixRipgrepLinesForScope(scope, opts.mode, lines)
		totalMatchCount += countRipgrepResults(opts.mode, lines)
		if stats != nil {
			if aggregatedStats == nil {
				aggregatedStats = &grepStats{}
			}
			aggregatedStats.add(stats)
		}
		if len(allLines) < g.maxMatches {
			remaining := g.maxMatches - len(allLines)
			if len(lines) > remaining {
				allLines = append(allLines, lines[:remaining]...)
				truncated = true
			} else {
				allLines = append(allLines, lines...)
			}
		} else if len(lines) > 0 {
			truncated = true
		}
	}

	if totalMatchCount == 0 && len(allLines) == 0 {
		return buildGrepResultWithEngine(opts, nil, 0, false, aggregatedStats, "rg"), true, nil
	}
	return buildGrepResultWithEngine(opts, allLines, totalMatchCount, truncated, aggregatedStats, "rg"), true, nil
}

func buildRipgrepArgs(opts *grepOptions, scope grepSearchScope, maxMatches int) []string {
	args := []string{
		"--line-number",
		"--with-filename",
		"--color=never",
		"--no-heading",
		"--hidden",
		"--no-ignore",
		"--glob", "!.git/**",
		"--glob", "!node_modules/**",
	}

	// Context lines
	switch {
	case opts.beforeContext > 0 && opts.afterContext > 0 && opts.beforeContext == opts.afterContext:
		args = append(args, "--context", fmt.Sprintf("%d", opts.beforeContext))
	case opts.beforeContext > 0 || opts.afterContext > 0:
		if opts.beforeContext > 0 {
			args = append(args, "--before-context", fmt.Sprintf("%d", opts.beforeContext))
		}
		if opts.afterContext > 0 {
			args = append(args, "--after-context", fmt.Sprintf("%d", opts.afterContext))
		}
	}

	// Case insensitive
	if opts.ignoreCase {
		args = append(args, "--ignore-case")
	}

	// Word match
	if opts.word {
		args = append(args, "--word-regexp")
	}
	if opts.lineRegexp {
		args = append(args, "--line-regexp")
	}
	if opts.invertMatch {
		args = append(args, "--invert-match")
	}
	if opts.onlyMatching {
		args = append(args, "--only-matching")
	}
	if opts.jsonOutput {
		args = append(args, "--json")
	}
	if opts.column {
		args = append(args, "--column")
	}
	if opts.trim {
		args = append(args, "--trim")
	}
	if opts.maxColumnsSet {
		args = append(args, "--max-columns", fmt.Sprintf("%d", opts.maxColumns))
	}
	if opts.maxColumnsPreview {
		args = append(args, "--max-columns-preview")
	}
	if opts.noMaxColumnsPreview {
		args = append(args, "--no-max-columns-preview")
	}
	if opts.follow {
		args = append(args, "--follow")
	}
	if opts.stats {
		args = append(args, "--stats")
	}

	// File type (rg native)
	if opts.fileType != "" {
		args = append(args, "--type", opts.fileType)
	}
	if opts.excludeType != "" {
		args = append(args, "--type-not", opts.excludeType)
	}
	if opts.sortBy != "" {
		args = append(args, "--sort", opts.sortBy)
	}
	if opts.sortReverseBy != "" {
		args = append(args, "--sortr", opts.sortReverseBy)
	}
	if opts.globCaseInsensitive {
		args = append(args, "--glob-case-insensitive")
	}

	// Max depth
	if opts.maxDepthSet {
		args = append(args, "--max-depth", fmt.Sprintf("%d", opts.maxDepth))
	}
	if opts.maxFilesize != "" {
		args = append(args, "--max-filesize", opts.maxFilesize)
	}

	// Include / exclude glob
	for _, spec := range normalizeGlobPatterns(opts.includeSpecs) {
		flag := "--glob"
		if spec.caseInsensitive && !opts.globCaseInsensitive {
			flag = "--iglob"
		}
		args = append(args, flag, filepath.ToSlash(spec.pattern))
	}
	for _, spec := range normalizeGlobPatterns(opts.excludeSpecs) {
		flag := "--glob"
		if spec.caseInsensitive && !opts.globCaseInsensitive {
			flag = "--iglob"
		}
		args = append(args, flag, "!"+filepath.ToSlash(spec.pattern))
	}

	// Mode
	switch opts.mode {
	case grepModeFiles:
		args = append(args, "--files-with-matches")
	case grepModeFilesWithout:
		args = append(args, "--files-without-match")
	case grepModeCount:
		if opts.countMatches {
			args = append(args, "--count-matches")
		} else {
			args = append(args, "--count")
		}
		if opts.maxCountSet {
			args = append(args, "--max-count", fmt.Sprintf("%d", opts.maxCount))
		}
	default:
		// content mode: apply max-matches per file
		perFileLimit := maxMatches
		if opts.maxCountSet {
			perFileLimit = opts.maxCount
		}
		if perFileLimit > 0 {
			args = append(args, "--max-count", fmt.Sprintf("%d", perFileLimit))
		} else if opts.maxCountSet {
			args = append(args, "--max-count", "0")
		}
	}
	if (opts.mode == grepModeFiles || opts.mode == grepModeFilesWithout) && opts.maxCountSet {
		args = append(args, "--max-count", fmt.Sprintf("%d", opts.maxCount))
	}

	// Literal search
	if opts.literal {
		args = append(args, "-F")
	}
	if len(opts.rgOnlyArgs) > 0 {
		args = append(args, opts.rgOnlyArgs...)
	}

	for _, patternFile := range opts.resolvedPatternFiles {
		args = append(args, "-f", patternFile)
	}

	if len(opts.resolvedPatternFiles) > 0 || len(opts.directPatterns) != 1 {
		for _, pattern := range opts.directPatterns {
			args = append(args, "-e", pattern)
		}
		if scope.searchTarget != "" {
			args = append(args, "--", scope.searchTarget)
		}
		return args
	}

	args = append(args, "--", opts.directPatterns[0])
	if scope.searchTarget != "" {
		args = append(args, scope.searchTarget)
	}
	return args
}

type grepStats struct {
	Matches          int
	MatchedLines     int
	FilesWithMatches int
	FilesSearched    int
	BytesSearched    int64
}

func (s *grepStats) add(other *grepStats) {
	if s == nil || other == nil {
		return
	}
	s.Matches += other.Matches
	s.MatchedLines += other.MatchedLines
	s.FilesWithMatches += other.FilesWithMatches
	s.FilesSearched += other.FilesSearched
	s.BytesSearched += other.BytesSearched
}

func (s *grepStats) metadataMap() map[string]interface{} {
	if s == nil {
		return nil
	}
	return map[string]interface{}{
		"matches":            s.Matches,
		"matched_lines":      s.MatchedLines,
		"files_with_matches": s.FilesWithMatches,
		"files_searched":     s.FilesSearched,
		"bytes_searched":     s.BytesSearched,
	}
}

func (s *grepStats) renderSummary() string {
	if s == nil {
		return ""
	}
	lines := []string{
		"-- stats --",
		fmt.Sprintf("matches: %d", s.Matches),
		fmt.Sprintf("matched_lines: %d", s.MatchedLines),
		fmt.Sprintf("files_with_matches: %d", s.FilesWithMatches),
		fmt.Sprintf("files_searched: %d", s.FilesSearched),
		fmt.Sprintf("bytes_searched: %d", s.BytesSearched),
	}
	return strings.Join(lines, "\n")
}

func normalizeRipgrepOutput(output []byte) []string {
	lines, _ := normalizeRipgrepOutputWithStats(output)
	return lines
}

func normalizeRipgrepJSONOutput(output []byte, scope grepSearchScope, mode grepMode) ([]string, *grepStats) {
	normalized := strings.TrimRight(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n")
	if strings.TrimSpace(normalized) == "" {
		return nil, nil
	}

	rawLines := strings.Split(normalized, "\n")
	lines := make([]string, 0, len(rawLines))
	var aggregatedStats *grepStats

	for _, rawLine := range rawLines {
		if strings.TrimSpace(rawLine) == "" {
			continue
		}
		if stats := parseRipgrepJSONSummaryLine(rawLine); stats != nil {
			if aggregatedStats == nil {
				aggregatedStats = &grepStats{}
			}
			aggregatedStats.add(stats)
		}

		rewritten := rawLine
		if scope.displayPath != "" {
			if scopedLine, changed := rewriteRipgrepJSONLineForScope(rawLine, scope); changed {
				rewritten = scopedLine
			} else if !isJSONOutputLine(rawLine) {
				rewritten = prefixRipgrepLineForScope(scope, mode, rawLine)
			} else {
				rewritten = rawLine
			}
		}
		lines = append(lines, rewritten)
	}

	return lines, aggregatedStats
}

func normalizeRipgrepOutputWithStats(output []byte) ([]string, *grepStats) {
	normalized := strings.TrimRight(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n")
	if strings.TrimSpace(normalized) == "" {
		return nil, nil
	}
	rawLines := strings.Split(normalized, "\n")
	contentLines, statsLines := splitRipgrepStatsLines(rawLines)
	lines := normalizeRipgrepContentLines(contentLines)
	stats := parseRipgrepStatsLines(statsLines)
	return lines, stats
}

func splitRipgrepStatsLines(rawLines []string) ([]string, []string) {
	end := len(rawLines)
	for end > 0 && strings.TrimSpace(rawLines[end-1]) == "" {
		end--
	}
	start := end
	for start > 0 && isRipgrepStatsLine(strings.TrimSpace(rawLines[start-1])) {
		start--
	}
	if start == end {
		return rawLines[:end], nil
	}
	contentEnd := start
	for contentEnd > 0 && strings.TrimSpace(rawLines[contentEnd-1]) == "" {
		contentEnd--
	}
	return rawLines[:contentEnd], rawLines[start:end]
}

func normalizeRipgrepContentLines(rawLines []string) []string {
	lines := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		if line == "" {
			continue
		}
		// Skip rg context separators (--)
		if line == "--" {
			continue
		}
		if matches := ripgrepMatchLineWithColumnPattern.FindStringSubmatch(line); len(matches) == 5 {
			line = fmt.Sprintf("%s:%s:%s: %s", matches[1], matches[2], matches[3], matches[4])
		} else if matches := ripgrepMatchLinePattern.FindStringSubmatch(line); len(matches) == 4 {
			line = fmt.Sprintf("%s:%s: %s", matches[1], matches[2], matches[3])
		} else if matches := ripgrepContextLinePattern.FindStringSubmatch(line); len(matches) == 4 {
			line = fmt.Sprintf("%s:%s: %s", matches[1], matches[2], matches[3])
		}
		lines = append(lines, line)
	}
	return lines
}

func isRipgrepStatsLine(line string) bool {
	line = strings.TrimSpace(line)
	switch {
	case ripgrepStatsMatchesPattern.MatchString(line):
		return true
	case ripgrepStatsMatchedLinesPattern.MatchString(line):
		return true
	case ripgrepStatsFilesWithPattern.MatchString(line):
		return true
	case ripgrepStatsFilesSearchedPattern.MatchString(line):
		return true
	case ripgrepStatsBytesPrintedPattern.MatchString(line):
		return true
	case ripgrepStatsBytesSearchedPattern.MatchString(line):
		return true
	case ripgrepStatsSearchSecsPattern.MatchString(line):
		return true
	case ripgrepStatsTotalSecsPattern.MatchString(line):
		return true
	default:
		return false
	}
}

func parseRipgrepStatsLines(lines []string) *grepStats {
	if len(lines) == 0 {
		return nil
	}
	stats := &grepStats{}
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasSuffix(line, " matches"):
			stats.Matches = parseLeadingInt(line)
		case strings.HasSuffix(line, " matched lines"):
			stats.MatchedLines = parseLeadingInt(line)
		case strings.HasSuffix(line, " files contained matches"):
			stats.FilesWithMatches = parseLeadingInt(line)
		case strings.HasSuffix(line, " files searched"):
			stats.FilesSearched = parseLeadingInt(line)
		case strings.HasSuffix(line, " bytes searched"):
			stats.BytesSearched = int64(parseLeadingInt(line))
		}
	}
	return stats
}

func parseRipgrepJSONSummaryLine(line string) *grepStats {
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		return nil
	}
	typeValue, _ := payload["type"].(string)
	if typeValue != "summary" {
		return nil
	}
	data, _ := payload["data"].(map[string]interface{})
	statsMap, _ := data["stats"].(map[string]interface{})
	return parseRipgrepJSONStatsObject(statsMap)
}

func parseRipgrepJSONStatsObject(statsMap map[string]interface{}) *grepStats {
	if len(statsMap) == 0 {
		return nil
	}
	return &grepStats{
		Matches:          jsonNumberToInt(statsMap["matches"]),
		MatchedLines:     jsonNumberToInt(statsMap["matched_lines"]),
		FilesWithMatches: jsonNumberToInt(statsMap["searches_with_match"]),
		FilesSearched:    jsonNumberToInt(statsMap["searches"]),
		BytesSearched:    jsonNumberToInt64(statsMap["bytes_searched"]),
	}
}

func rewriteRipgrepJSONLineForScope(line string, scope grepSearchScope) (string, bool) {
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		return "", false
	}
	data, ok := payload["data"].(map[string]interface{})
	if !ok || data == nil {
		return "", false
	}
	pathMap, ok := data["path"].(map[string]interface{})
	if !ok || pathMap == nil {
		return "", false
	}
	text, _ := pathMap["text"].(string)
	if strings.TrimSpace(text) == "" {
		return "", false
	}
	pathMap["text"] = composeScopeDisplayPath(scope, text)
	rewritten, err := json.Marshal(payload)
	if err != nil {
		return "", false
	}
	return string(rewritten), true
}

func isJSONOutputLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	return trimmed != "" && strings.HasPrefix(trimmed, "{") && json.Valid([]byte(trimmed))
}

func parseLeadingInt(line string) int {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) == 0 {
		return 0
	}
	raw := strings.ReplaceAll(fields[0], ",", "")
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return value
}

func jsonNumberToInt(value interface{}) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case json.Number:
		parsed, err := v.Int64()
		if err == nil {
			return int(parsed)
		}
	}
	return 0
}

func jsonNumberToInt64(value interface{}) int64 {
	switch v := value.(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case json.Number:
		parsed, err := v.Int64()
		if err == nil {
			return parsed
		}
	}
	return 0
}

func prefixRipgrepLinesForScope(scope grepSearchScope, mode grepMode, lines []string) []string {
	if scope.displayPath == "" {
		return lines
	}
	prefixed := make([]string, len(lines))
	for i, line := range lines {
		prefixed[i] = prefixRipgrepLineForScope(scope, mode, line)
	}
	return prefixed
}

func prefixRipgrepLineForScope(scope grepSearchScope, mode grepMode, line string) string {
	switch mode {
	case grepModeFiles, grepModeFilesWithout:
		return composeScopeDisplayPath(scope, line)
	case grepModeCount:
		idx := strings.LastIndex(line, ":")
		if idx <= 0 {
			return composeScopeDisplayPath(scope, line)
		}
		return composeScopeDisplayPath(scope, line[:idx]) + line[idx:]
	default:
		if matches := ripgrepMatchLineWithColumnPattern.FindStringSubmatch(line); len(matches) == 5 {
			return fmt.Sprintf("%s:%s:%s: %s", composeScopeDisplayPath(scope, matches[1]), matches[2], matches[3], matches[4])
		}
		if matches := ripgrepMatchLinePattern.FindStringSubmatch(line); len(matches) == 4 {
			return fmt.Sprintf("%s:%s: %s", composeScopeDisplayPath(scope, matches[1]), matches[2], matches[3])
		}
	}
	return line
}

func countRipgrepResults(mode grepMode, lines []string) int {
	switch mode {
	case grepModeCount:
		total := 0
		for _, line := range lines {
			idx := strings.LastIndex(line, ":")
			if idx <= 0 {
				total++
				continue
			}
			if value, err := strconv.Atoi(strings.TrimSpace(line[idx+1:])); err == nil {
				total += value
			} else {
				total++
			}
		}
		return total
	default:
		return len(lines)
	}
}

func isRipgrepNoMatch(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == 1
}

func runGrepCommand(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binaryPath, args...)
	cmd.Dir = workingDir
	return cmd.CombinedOutput()
}

// --- Builtin walker engine ---

type grepMatch struct {
	filePath string // relative path
	absPath  string
	lineNum  int
	column   int
	line     string
}

type fileMatchCount struct {
	filePath string
	count    int
}

func (g *GrepTool) searchWithWalker(opts *grepOptions, re *regexp.Regexp) *toolkit.ToolResult {
	switch opts.mode {
	case grepModeFiles:
		return g.walkerSearchFiles(opts, re)
	case grepModeFilesWithout:
		return g.walkerSearchFilesWithout(opts, re)
	case grepModeCount:
		return g.walkerSearchCount(opts, re)
	default:
		return g.walkerSearchContent(opts, re)
	}
}

func (g *GrepTool) walkerSearchContent(opts *grepOptions, re *regexp.Regexp) *toolkit.ToolResult {
	matches := make([]grepMatch, 0, 16)
	matchCount := 0
	var stats *grepStats
	if opts.stats {
		stats = &grepStats{}
	}

	err := g.walkFiles(opts, func(path string, relPath string) error {
		if hasExplicitZeroMaxCount(opts) {
			if stats != nil {
				if info, statErr := os.Stat(path); statErr == nil {
					stats.FilesSearched++
					stats.BytesSearched += info.Size()
				}
			}
			return nil
		}
		lines, err := readFileLines(path)
		if err != nil {
			return nil
		}
		if stats != nil {
			if info, statErr := os.Stat(path); statErr == nil {
				stats.FilesSearched++
				stats.BytesSearched += info.Size()
			}
		}

		perFileCount := 0
		matchedLinesThisFile := 0
		actualMatchesThisFile := 0
		for i, line := range lines {
			if !lineSatisfiesMatch(re, line, opts.invertMatch) {
				continue
			}
			matchedLinesThisFile++
			actualMatchesThisFile += countActualRegexMatches(re, line, opts.invertMatch)
			for _, rendered := range extractRenderedMatches(re, line, opts.onlyMatching, opts.invertMatch, opts.trim) {
				matches = append(matches, grepMatch{
					filePath: relPath,
					absPath:  path,
					lineNum:  i + 1,
					column:   rendered.column,
					line:     rendered.text,
				})
				matchCount++
				perFileCount++
				if matchCount >= g.maxMatches {
					return errGrepLimitReached
				}
				if hasReachedMaxCount(opts, perFileCount) {
					break
				}
			}
			if matchCount >= g.maxMatches {
				return errGrepLimitReached
			}
			if hasReachedMaxCount(opts, perFileCount) {
				break
			}
		}
		if stats != nil && matchedLinesThisFile > 0 {
			stats.FilesWithMatches++
			stats.MatchedLines += matchedLinesThisFile
			stats.Matches += actualMatchesThisFile
		}
		return nil
	})

	if err != nil && !errors.Is(err, errGrepLimitReached) {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("搜索失败: %w", err),
		}
	}

	// Build output with optional context
	var results []string
	if (opts.beforeContext > 0 || opts.afterContext > 0) && len(matches) > 0 && !opts.onlyMatching {
		results = g.buildContextOutput(opts, matches)
	} else {
		results = make([]string, len(matches))
		for i, m := range matches {
			renderedLine := applyMaxColumnsToRenderedText(opts, m.line)
			results[i] = formatGrepContentLine(m.filePath, m.lineNum, m.column, renderedLine, opts.column)
		}
	}

	truncated := errors.Is(err, errGrepLimitReached)
	return buildGrepResult(opts, results, matchCount, truncated, stats)
}

func (g *GrepTool) walkerSearchFiles(opts *grepOptions, re *regexp.Regexp) *toolkit.ToolResult {
	fileSet := make(map[string]struct{})
	results := make([]string, 0, 16)
	var stats *grepStats
	if opts.stats {
		stats = &grepStats{}
	}

	err := g.walkFiles(opts, func(path string, relPath string) error {
		if hasExplicitZeroMaxCount(opts) {
			if stats != nil {
				if info, statErr := os.Stat(path); statErr == nil {
					stats.FilesSearched++
					stats.BytesSearched += info.Size()
				}
			}
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer file.Close()
		if stats != nil {
			if info, statErr := os.Stat(path); statErr == nil {
				stats.FilesSearched++
				stats.BytesSearched += info.Size()
			}
		}

		scanner := bufio.NewScanner(file)
		matchedLinesThisFile := 0
		actualMatchesThisFile := 0
		for scanner.Scan() {
			line := scanner.Text()
			if lineSatisfiesMatch(re, line, opts.invertMatch) {
				if _, exists := fileSet[relPath]; !exists {
					fileSet[relPath] = struct{}{}
					results = append(results, relPath)
				}
				if stats == nil {
					return nil // found at least one match, no need to scan further
				}
				matchedLinesThisFile++
				actualMatchesThisFile += countActualRegexMatches(re, line, opts.invertMatch)
			}
		}
		if stats != nil && matchedLinesThisFile > 0 {
			stats.FilesWithMatches++
			stats.MatchedLines += matchedLinesThisFile
			stats.Matches += actualMatchesThisFile
		}
		return nil
	})

	if err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("搜索失败: %w", err),
		}
	}

	return buildGrepResult(opts, results, len(results), false, stats)
}

func (g *GrepTool) walkerSearchFilesWithout(opts *grepOptions, re *regexp.Regexp) *toolkit.ToolResult {
	fileSet := make(map[string]struct{})
	results := make([]string, 0, 16)
	var stats *grepStats
	if opts.stats {
		stats = &grepStats{}
	}

	err := g.walkFiles(opts, func(path string, relPath string) error {
		if hasExplicitZeroMaxCount(opts) {
			if stats != nil {
				if info, statErr := os.Stat(path); statErr == nil {
					stats.FilesSearched++
					stats.BytesSearched += info.Size()
				}
			}
			if _, exists := fileSet[relPath]; !exists {
				fileSet[relPath] = struct{}{}
				results = append(results, relPath)
			}
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer file.Close()
		if stats != nil {
			if info, statErr := os.Stat(path); statErr == nil {
				stats.FilesSearched++
				stats.BytesSearched += info.Size()
			}
		}

		hasMatch := false
		scanner := bufio.NewScanner(file)
		matchedLinesThisFile := 0
		actualMatchesThisFile := 0
		for scanner.Scan() {
			line := scanner.Text()
			if lineSatisfiesMatch(re, line, opts.invertMatch) {
				hasMatch = true
				if stats == nil {
					break
				}
				matchedLinesThisFile++
				actualMatchesThisFile += countActualRegexMatches(re, line, opts.invertMatch)
			}
		}
		if stats != nil && matchedLinesThisFile > 0 {
			stats.FilesWithMatches++
			stats.MatchedLines += matchedLinesThisFile
			stats.Matches += actualMatchesThisFile
		}
		if !hasMatch {
			if _, exists := fileSet[relPath]; !exists {
				fileSet[relPath] = struct{}{}
				results = append(results, relPath)
			}
		}
		return nil
	})

	if err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("搜索失败: %w", err),
		}
	}

	return buildGrepResult(opts, results, len(results), false, stats)
}

func (g *GrepTool) walkerSearchCount(opts *grepOptions, re *regexp.Regexp) *toolkit.ToolResult {
	counts := make([]fileMatchCount, 0, 16)
	var stats *grepStats
	if opts.stats {
		stats = &grepStats{}
	}

	err := g.walkFiles(opts, func(path string, relPath string) error {
		if hasExplicitZeroMaxCount(opts) {
			if stats != nil {
				if info, statErr := os.Stat(path); statErr == nil {
					stats.FilesSearched++
					stats.BytesSearched += info.Size()
				}
			}
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer file.Close()
		if stats != nil {
			if info, statErr := os.Stat(path); statErr == nil {
				stats.FilesSearched++
				stats.BytesSearched += info.Size()
			}
		}

		count := 0
		scanner := bufio.NewScanner(file)
		matchedLinesThisFile := 0
		actualMatchesThisFile := 0
		for scanner.Scan() {
			line := scanner.Text()
			if lineSatisfiesMatch(re, line, opts.invertMatch) {
				matchedLinesThisFile++
				actualMatchesThisFile += countActualRegexMatches(re, line, opts.invertMatch)
				if opts.countMatches {
					count += countRenderedMatches(re, line, true, opts.invertMatch)
				} else {
					count += countRenderedMatches(re, line, opts.onlyMatching, opts.invertMatch)
				}
				if hasReachedMaxCount(opts, count) {
					break
				}
			}
		}
		if stats != nil && matchedLinesThisFile > 0 {
			stats.FilesWithMatches++
			stats.MatchedLines += matchedLinesThisFile
			stats.Matches += actualMatchesThisFile
		}
		if count > 0 {
			counts = append(counts, fileMatchCount{filePath: relPath, count: count})
		}
		return nil
	})

	if err != nil {
		return &toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("搜索失败: %w", err),
		}
	}

	results := make([]string, len(counts))
	totalMatches := 0
	for i, fc := range counts {
		results[i] = fmt.Sprintf("%s:%d", fc.filePath, fc.count)
		totalMatches += fc.count
	}

	return buildGrepResult(opts, results, totalMatches, false, stats)
}

// buildContextOutput produces output with context lines around each match.
func (g *GrepTool) buildContextOutput(opts *grepOptions, matches []grepMatch) []string {
	// Group matches by file for context rendering
	type fileMatches struct {
		relPath string
		lines   []string // all file lines
		matches []grepMatch
	}

	fileGroups := make(map[string]*fileMatches)
	fileOrder := make([]string, 0)

	for _, m := range matches {
		fg, ok := fileGroups[m.filePath]
		if !ok {
			lines, err := readFileLines(m.absPath)
			if err != nil {
				continue
			}
			fg = &fileMatches{relPath: m.filePath, lines: lines}
			fileGroups[m.filePath] = fg
			fileOrder = append(fileOrder, m.filePath)
		}
		fg.matches = append(fg.matches, m)
	}

	results := make([]string, 0, len(matches)*2)
	before := opts.beforeContext
	after := opts.afterContext

	for _, fname := range fileOrder {
		fg := fileGroups[fname]
		if len(results) > 0 {
			results = append(results, "--")
		}

		// Collect all line numbers to display (match lines + context)
		showLines := make(map[int]bool)
		for _, m := range fg.matches {
			start := m.lineNum - before
			if start < 1 {
				start = 1
			}
			end := m.lineNum + after
			if end > len(fg.lines) {
				end = len(fg.lines)
			}
			for i := start; i <= end; i++ {
				showLines[i] = true
			}
		}

		// Render lines in order
		prevLine := 0
		for lineNum := 1; lineNum <= len(fg.lines); lineNum++ {
			if !showLines[lineNum] {
				continue
			}
			if prevLine > 0 && lineNum > prevLine+1 {
				results = append(results, fmt.Sprintf("%s--", fg.relPath))
			}
			prefix := " "
			matchColumn := 0
			for _, m := range fg.matches {
				if m.lineNum == lineNum {
					prefix = ">" // marker for match line
					if m.column > 0 && (matchColumn == 0 || m.column < matchColumn) {
						matchColumn = m.column
					}
					break
				}
			}
			renderedLine := fg.lines[lineNum-1]
			if opts.trim {
				renderedLine = trimGrepRenderedText(renderedLine)
			}
			renderedLine = applyMaxColumnsToRenderedText(opts, renderedLine)
			if opts.column && prefix == ">" && matchColumn > 0 {
				results = append(results, fmt.Sprintf("%s%s%d:%d: %s", fg.relPath, prefix, lineNum, matchColumn, renderedLine))
			} else {
				results = append(results, fmt.Sprintf("%s%s%d: %s", fg.relPath, prefix, lineNum, renderedLine))
			}
			prevLine = lineNum
		}
	}

	return results
}

type grepFileCandidate struct {
	path    string
	relPath string
}

// walkFiles walks the directory tree, calling fn for each file that passes filters.
func (g *GrepTool) walkFiles(opts *grepOptions, fn func(path string, relPath string) error) error {
	candidates, err := g.collectFileCandidates(opts)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		if err := fn(candidate.path, candidate.relPath); err != nil {
			return err
		}
	}
	return nil
}

func (g *GrepTool) collectFileCandidates(opts *grepOptions) ([]grepFileCandidate, error) {
	includeGlobs := resolveIncludeGlobs(opts)
	excludeGlobs := resolveExcludeGlobs(opts)
	candidates := make([]grepFileCandidate, 0, 32)
	for _, scope := range opts.searchScopes {
		if scope.searchTarget != "" {
			targetPath := filepath.Join(scope.workingDir, scope.searchTarget)
			info, err := os.Stat(targetPath)
			if err != nil {
				return nil, err
			}
			if info.IsDir() {
				return nil, fmt.Errorf("搜索目标不是文件: %s", targetPath)
			}
			if !shouldIncludeFileByInfo(targetPath, scope.searchTarget, info, includeGlobs, excludeGlobs, opts.maxFileBytes) {
				continue
			}
			candidates = append(candidates, grepFileCandidate{
				path:    targetPath,
				relPath: composeScopeDisplayPath(scope, scope.searchTarget),
			})
			continue
		}

		walkErr := filepath.Walk(scope.workingDir, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if info == nil {
				return nil
			}

			if info.IsDir() {
				if shouldSkipGrepDir(info.Name()) {
					return filepath.SkipDir
				}
				// Check max_depth
				if opts.maxDepthSet {
					rel, err := filepath.Rel(scope.workingDir, path)
					if err == nil && rel != "." {
						depth := strings.Count(filepath.ToSlash(rel), "/") + 1
						if depth > opts.maxDepth {
							return filepath.SkipDir
						}
					}
				}
				return nil
			}

			relPath, err := filepath.Rel(scope.workingDir, path)
			if err != nil {
				relPath = path
			}
			if !shouldIncludeFileByInfo(path, relPath, info, includeGlobs, excludeGlobs, opts.maxFileBytes) {
				return nil
			}

			candidates = append(candidates, grepFileCandidate{
				path:    path,
				relPath: composeScopeDisplayPath(scope, relPath),
			})
			return nil
		})
		if walkErr != nil {
			return nil, walkErr
		}
	}
	if opts.sortBy == "path" || opts.sortReverseBy == "path" {
		reverse := opts.sortReverseBy == "path"
		sort.SliceStable(candidates, func(i, j int) bool {
			left := normalizeGlobPath(candidates[i].relPath)
			right := normalizeGlobPath(candidates[j].relPath)
			if left == right {
				if reverse {
					return candidates[i].path > candidates[j].path
				}
				return candidates[i].path < candidates[j].path
			}
			if reverse {
				return left > right
			}
			return left < right
		})
	}
	return candidates, nil
}

// resolveIncludeGlobs builds the include glob list from include and type parameters.
func resolveIncludeGlobs(opts *grepOptions) []grepGlobPattern {
	globs := make([]grepGlobPattern, 0, len(opts.includeSpecs)+4)

	if opts.fileType != "" {
		globs = appendFileTypeGlobs(globs, opts.fileType)
	}

	globs = append(globs, opts.includeSpecs...)
	return normalizeGlobPatterns(globs)
}

// resolveExcludeGlobs builds the exclude glob list.
func resolveExcludeGlobs(opts *grepOptions) []grepGlobPattern {
	globs := make([]grepGlobPattern, 0, len(opts.excludeSpecs)+4)
	if opts.excludeType != "" {
		globs = appendFileTypeGlobs(globs, opts.excludeType)
	}
	globs = append(globs, opts.excludeSpecs...)
	return normalizeGlobPatterns(globs)
}

func appendFileTypeGlobs(globs []grepGlobPattern, fileType string) []grepGlobPattern {
	if patterns, ok := fileTypeToGlob[fileType]; ok {
		for _, p := range strings.Split(patterns, ",") {
			globs = append(globs, grepGlobPattern{pattern: strings.TrimSpace(p)})
		}
		return globs
	}
	// Unknown type: treat as glob pattern fallback
	return append(globs, grepGlobPattern{pattern: "*." + fileType})
}

func shouldIncludeFileByInfo(path string, relPath string, info os.FileInfo, includeGlobs, excludeGlobs []grepGlobPattern, maxFileBytes int64) bool {
	if info == nil {
		return false
	}
	if maxFileBytes > 0 && info.Size() > maxFileBytes {
		return false
	}
	if strings.TrimSpace(relPath) == "" {
		relPath = filepath.Base(path)
	}
	if !matchAnyGlob(relPath, includeGlobs) {
		return false
	}
	if len(excludeGlobs) > 0 && matchAnyGlob(relPath, excludeGlobs) {
		return false
	}
	return true
}

// matchAnyGlob checks if a relative path matches any of the glob patterns.
// If patterns is empty, everything matches.
func matchAnyGlob(relPath string, patterns []grepGlobPattern) bool {
	if len(patterns) == 0 {
		return true
	}
	relPath = normalizeGlobPath(relPath)
	for _, pattern := range normalizeGlobPatterns(patterns) {
		if matchGrepGlobPattern(relPath, pattern) {
			return true
		}
	}
	return false
}

func matchGrepGlobPattern(relPath string, pattern grepGlobPattern) bool {
	pat := normalizeGlobPath(pattern.pattern)
	if pat == "" {
		return false
	}
	candidatePath := relPath
	candidateBase := path.Base(candidatePath)
	if pattern.caseInsensitive {
		pat = strings.ToLower(pat)
		candidatePath = strings.ToLower(candidatePath)
		candidateBase = strings.ToLower(candidateBase)
	}
	if strings.Contains(pat, "/") || strings.Contains(pat, "**") {
		matched, err := matchGlobPattern(pat, candidatePath)
		return err == nil && matched
	}
	matched, err := path.Match(pat, candidateBase)
	return err == nil && matched
}

func normalizeGlobPath(value string) string {
	value = strings.TrimSpace(filepath.ToSlash(value))
	value = strings.TrimPrefix(value, "./")
	value = strings.TrimPrefix(value, "/")
	if value == "" || value == "." {
		return ""
	}
	return path.Clean(value)
}

func shouldSkipGrepDir(name string) bool {
	switch strings.TrimSpace(name) {
	case ".git", "node_modules":
		return true
	default:
		return false
	}
}

// readFileLines reads an entire file into a slice of lines.
func readFileLines(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

// --- Result building ---

func grepCaseMode(opts *grepOptions) string {
	switch {
	case opts == nil:
		return "default"
	case opts.caseSensitive:
		return "case_sensitive"
	case opts.smartCase:
		return "smart_case"
	case opts.ignoreCaseRequested || opts.ignoreCase:
		return "ignore_case"
	default:
		return "default"
	}
}

func grepPatternSource(opts *grepOptions) string {
	if opts == nil {
		return "direct"
	}
	hasDirect := len(opts.directPatterns) > 0
	hasPatternFiles := len(opts.patternFiles) > 0
	switch {
	case hasDirect && hasPatternFiles:
		return "mixed"
	case hasPatternFiles:
		return "pattern_file"
	case len(opts.directPatterns) > 1:
		return "multiple_patterns"
	default:
		return "direct"
	}
}

func grepSearchScopeKind(opts *grepOptions) string {
	if opts == nil || len(opts.searchScopes) == 0 {
		return "none"
	}
	if len(opts.searchScopes) > 1 {
		return "multi_path"
	}
	if opts.searchScopes[0].searchTarget != "" {
		return "single_file"
	}
	return "single_path"
}

func grepOutputFormat(opts *grepOptions) string {
	if opts == nil {
		return "normalized_text"
	}
	if opts.jsonOutput {
		return "rg_passthrough"
	}
	return "normalized_text"
}

func requiredRipgrepFeatures(opts *grepOptions) []string {
	if opts == nil {
		return nil
	}
	features := make([]string, 0, 8)
	if opts.jsonOutput {
		features = appendUniqueString(features, "json/--json")
	}
	if opts.follow {
		features = appendUniqueString(features, "follow/-L/--follow")
	}
	if requiresRipgrepForSort(opts.sortBy) {
		features = appendUniqueString(features, "sort="+opts.sortBy)
	}
	if requiresRipgrepForSort(opts.sortReverseBy) {
		features = appendUniqueString(features, "sortr="+opts.sortReverseBy)
	}
	for i := 0; i < len(opts.rgOnlyArgs); i++ {
		arg := strings.TrimSpace(opts.rgOnlyArgs[i])
		if arg == "" {
			continue
		}
		switch arg {
		case "--pcre2":
			features = appendUniqueString(features, "pcre2/-P/--pcre2")
		case "--engine":
			if i+1 < len(opts.rgOnlyArgs) {
				features = appendUniqueString(features, "engine="+strings.TrimSpace(opts.rgOnlyArgs[i+1]))
				i++
			} else {
				features = appendUniqueString(features, "engine")
			}
		case "--replace":
			if i+1 < len(opts.rgOnlyArgs) {
				features = appendUniqueString(features, "replace/-r")
				i++
			} else {
				features = appendUniqueString(features, "replace/-r")
			}
		case "--multiline":
			features = appendUniqueString(features, "multiline/-U/--multiline")
		case "--multiline-dotall":
			features = appendUniqueString(features, "multiline-dotall")
		case "--passthru":
			features = appendUniqueString(features, "passthru")
		case "--crlf":
			features = appendUniqueString(features, "crlf")
		case "--auto-hybrid-regex":
			features = appendUniqueString(features, "auto-hybrid-regex")
		default:
			features = appendUniqueString(features, arg)
		}
	}
	return features
}

func buildGrepResult(opts *grepOptions, results []string, matchCount int, truncated bool, stats *grepStats) *toolkit.ToolResult {
	output := "未找到匹配的内容"
	if opts != nil && opts.jsonOutput {
		output = ""
	}
	if len(results) > 0 {
		output = strings.Join(results, "\n")
		if truncated && (opts == nil || !opts.jsonOutput) {
			output += fmt.Sprintf("\n\n(结果已截断，显示前 %d 个匹配)", len(results))
		}
	}
	if opts != nil && opts.stats && opts.jsonOutput {
		// json/--json 模式下保留 rg 原始 JSON Lines 语义，不再追加自定义 stats 摘要块。
	} else if opts != nil && opts.stats && stats != nil {
		summary := stats.renderSummary()
		if summary != "" {
			output += "\n\n" + summary
		}
	}

	engine := "builtin"
	if opts != nil {
		// engine will be set to "rg" by the caller if rg was used
	}

	metadata := map[string]interface{}{
		"pattern":                opts.pattern,
		"patterns":               opts.patterns,
		"pattern_files":          opts.patternFiles,
		"path":                   opts.searchPath,
		"paths":                  opts.searchPaths,
		"include":                opts.include,
		"exclude":                opts.exclude,
		"glob_case_insensitive":  opts.globCaseInsensitive,
		"literal":                opts.literal,
		"fixed_strings":          opts.literal,
		"ignore_case":            opts.ignoreCase,
		"case_sensitive":         opts.caseSensitive,
		"smart_case":             opts.smartCase,
		"word":                   opts.word,
		"word_regexp":            opts.word,
		"line_regexp":            opts.lineRegexp,
		"invert_match":           opts.invertMatch,
		"only_matching":          opts.onlyMatching,
		"count_matches":          opts.countMatches,
		"stats_requested":        opts.stats,
		"json_output_requested":  opts.jsonOutput,
		"follow":                 opts.follow,
		"column":                 opts.column,
		"trim":                   opts.trim,
		"pretty":                 opts.pretty,
		"line_buffered":          opts.lineBuffered,
		"no_line_buffered":       opts.noLineBuffered,
		"max_columns":            opts.maxColumns,
		"max_columns_explicit":   opts.maxColumnsSet,
		"max_columns_preview":    opts.maxColumnsPreview,
		"no_max_columns_preview": opts.noMaxColumnsPreview,
		"context":                opts.context,
		"before_context":         opts.beforeContext,
		"after_context":          opts.afterContext,
		"type":                   opts.fileType,
		"type_not":               opts.excludeType,
		"sort":                   opts.sortBy,
		"sortr":                  opts.sortReverseBy,
		"sort_files":             opts.sortBy == "path" && opts.sortReverseBy == "",
		"max_depth":              opts.maxDepth,
		"max_depth_explicit":     opts.maxDepthSet,
		"max_count":              opts.maxCount,
		"max_count_explicit":     opts.maxCountSet,
		"max_filesize":           opts.maxFilesize,
		"requires_ripgrep":       opts.requiresRipgrep,
		"rg_only_args":           opts.rgOnlyArgs,
		"ignored_rg_args":        opts.ignoredRGArgs,
		"ignored_presentation":   opts.ignoredPresentation,
		"case_mode":              grepCaseMode(opts),
		"pattern_source":         grepPatternSource(opts),
		"pattern_count":          len(opts.patterns),
		"search_scope_count":     len(opts.searchScopes),
		"search_scope_kind":      grepSearchScopeKind(opts),
		"normalized_output":      !opts.jsonOutput,
		"output_format":          grepOutputFormat(opts),
		"mode":                   string(opts.mode),
		"match_count":            matchCount,
		"truncated":              truncated,
		"engine":                 engine,
	}
	if stats != nil {
		metadata["stats"] = stats.metadataMap()
	}

	return &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    output,
		Metadata:   metadata,
	}
}

func buildGrepResultWithEngine(opts *grepOptions, results []string, matchCount int, truncated bool, stats *grepStats, engine string) *toolkit.ToolResult {
	result := buildGrepResult(opts, results, matchCount, truncated, stats)
	result.Metadata["engine"] = engine
	return result
}
