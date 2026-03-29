package workspace

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Language 编程语言类型
type Language string

const (
	LangGo      Language = "go"
	LangPython  Language = "python"
	LangJS      Language = "javascript"
	LangTS      Language = "typescript"
	LangJava    Language = "java"
	LangRust    Language = "rust"
	LangCPP     Language = "cpp"
	LangC       Language = "c"
	LangUnknown Language = "unknown"
)

// FileExtension 文件扩展名到语言的映射
var extensionToLang = map[string]Language{
	".go":   LangGo,
	".py":   LangPython,
	".js":   LangJS,
	".ts":   LangTS,
	".tsx":  LangTS,
	".jsx":  LangJS,
	".java": LangJava,
	".rs":   LangRust,
	".cpp":  LangCPP,
	".cc":   LangCPP,
	".cxx":  LangCPP,
	".c":    LangC,
	".h":    LangC,
	".hpp":  LangCPP,
}

// Pattern 文件模式
var (
	// 代码文件模式
	codePatterns = []*regexp.Regexp{
		regexp.MustCompile(`.*\.(go|py|js|ts|tsx|jsx|java|rs|cpp|cc|cxx|c|h|hpp)$`),
	}
	// 忽略模式
	ignorePatterns = []*regexp.Regexp{
		regexp.MustCompile(`^\.git`),
		regexp.MustCompile(`^\.idea`),
		regexp.MustCompile(`^\.vscode`),
		regexp.MustCompile(`^node_modules`),
		regexp.MustCompile(`^vendor`),
		regexp.MustCompile(`^dist`),
		regexp.MustCompile(`^build`),
		regexp.MustCompile(`^\.DS_Store`),
		regexp.MustCompile(`.*\.test\.(go|py|js|ts)$`), // 测试文件
		regexp.MustCompile(`^_\w+`),                    // Go 生成文件
	}
	goFuncPattern          = regexp.MustCompile(`^func\s+(?:\(\w+\s+\*?\w+\)\s+)?(\w+)`)
	goStructPattern        = regexp.MustCompile(`^type\s+(\w+)\s+struct`)
	goInterfacePattern     = regexp.MustCompile(`^type\s+(\w+)\s+interface`)
	goConstPattern         = regexp.MustCompile(`^const\s+(\w+)`)
	goVarPattern           = regexp.MustCompile(`^var\s+(\w+)`)
	pythonClassPattern     = regexp.MustCompile(`^class\s+(\w+)`)
	pythonFuncPattern      = regexp.MustCompile(`^def\s+(\w+)`)
	jsClassPattern         = regexp.MustCompile(`^class\s+(\w+)`)
	jsFuncPattern          = regexp.MustCompile(`^function\s+(\w+)`)
	typeScriptTypePattern  = regexp.MustCompile(`^type\s+(\w+)\s*=`)
	typeScriptIfacePattern = regexp.MustCompile(`^interface\s+(\w+)`)
)

// SymbolType 符号类型
type SymbolType string

const (
	SymbolFunction  SymbolType = "function"
	SymbolClass     SymbolType = "class"
	SymbolStruct    SymbolType = "struct"
	SymbolInterface SymbolType = "interface"
	SymbolVariable  SymbolType = "variable"
	SymbolConstant  SymbolType = "constant"
	SymbolTypeAlias SymbolType = "type_alias"
	SymbolImport    SymbolType = "import"
	SymbolUnknown   SymbolType = "unknown"
)

// Symbol 代码符号
type Symbol struct {
	Name     string     // 符号名称
	Type     SymbolType // 符号类型
	Language Language   // 编程语言
	Line     int        // 符号所在行号
	LineEnd  int        // 符号结束行号（如果有）
	File     string     // 文件路径
}

// SymbolExtractor 符号提取器接口
type SymbolExtractor interface {
	// ExtractSymbols 从文件内容中提取符号
	ExtractSymbols(content string, file string) ([]Symbol, error)
}

// CodeChunk 代码块
type CodeChunk struct {
	FilePath    string   // 文件路径
	StartLine   int      // 起始行号
	EndLine     int      // 结束行号
	Content     string   // 代码内容
	Language    Language // 编程语言
	Symbols     []Symbol // 包含的符号
	ChunkSize   int      // 块大小（字符数）
	SymbolCount int      // 符号数量
}

// ScanResult 扫描结果
type ScanResult struct {
	Files      []string    // 扫描到的文件列表
	CodeChunks []CodeChunk // 提取的代码块
	TotalSize  int64       // 总大小（字节）
	FileCount  int         // 文件数量
	Path       string      // 扫描路径
}

// WorkspaceConfig 工作区扫描配置
type WorkspaceConfig struct {
	MaxFileSize     int64    // 最大文件大小（字节）
	MaxChunkSize    int      // 最大块大小（字符）
	ChunkOverlap    int      // 块重叠大小（字符）
	IncludePatterns []string // 包含的文件模式
	ExcludePatterns []string // 排除的文件模式
}

// DefaultWorkspaceConfig 默认工作区配置
func DefaultWorkspaceConfig() *WorkspaceConfig {
	return &WorkspaceConfig{
		MaxFileSize:     10 * 1024 * 1024, // 10MB
		MaxChunkSize:    5000,             // 5000 字符
		ChunkOverlap:    200,              // 200 字符重叠
		IncludePatterns: nil,
		ExcludePatterns: nil,
	}
}

// Scanner 工作区扫描器
type Scanner struct {
	config           *WorkspaceConfig
	symbolExtractors map[Language]SymbolExtractor
}

// NewScanner 创建新的扫描器
func NewScanner(config *WorkspaceConfig) *Scanner {
	if config == nil {
		config = DefaultWorkspaceConfig()
	}
	scanner := &Scanner{
		config:           config,
		symbolExtractors: make(map[Language]SymbolExtractor),
	}

	// 注册默认符号提取器
	scanner.RegisterSymbolExtractor(LangGo, &GoSymbolExtractor{})
	scanner.RegisterSymbolExtractor(LangPython, &PythonSymbolExtractor{})
	scanner.RegisterSymbolExtractor(LangJS, &JSSymbolExtractor{})
	scanner.RegisterSymbolExtractor(LangTS, &TSSymbolExtractor{})

	return scanner
}

// RegisterSymbolExtractor 注册符号提取器
func (s *Scanner) RegisterSymbolExtractor(lang Language, extractor SymbolExtractor) {
	s.symbolExtractors[lang] = extractor
}

// Scan 扫描工作区
func (s *Scanner) Scan(path string) (*ScanResult, error) {
	result := &ScanResult{
		Path:       path,
		Files:      make([]string, 0),
		CodeChunks: make([]CodeChunk, 0),
	}

	// 检查路径是否存在
	stat, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("failed to stat path: %w", err)
	}

	if !stat.IsDir() {
		// 扫描单个文件
		return s.scanFile(path)
	}

	// 遍历目录
	err = filepath.Walk(path, func(filePath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// 跳过目录
		if info.IsDir() {
			// 检查是否应该忽略此目录
			relPath, err := filepath.Rel(path, filePath)
			if err == nil {
				if s.shouldIgnore(relPath) {
					return filepath.SkipDir
				}
			}
			return nil
		}

		// 检查文件
		relPath, err := filepath.Rel(path, filePath)
		if err != nil {
			return nil
		}

		if s.shouldIgnore(relPath) {
			return nil
		}

		if !s.isCodeFile(filePath) {
			return nil
		}

		// 检查文件大小
		if info.Size() > s.config.MaxFileSize {
			return nil
		}

		result.Files = append(result.Files, filePath)
		result.TotalSize += info.Size()
		result.FileCount++

		// 扫描文件
		fileResult, err := s.scanFile(filePath)
		if err != nil {
			// 记录错误但继续扫描
			fmt.Printf("Warning: failed to scan file %s: %v\n", filePath, err)
			return nil
		}

		result.CodeChunks = append(result.CodeChunks, fileResult.CodeChunks...)

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to walk directory: %w", err)
	}

	return result, nil
}

// scanFile 扫描单个文件
func (s *Scanner) scanFile(filePath string) (*ScanResult, error) {
	result := &ScanResult{
		Path:       filePath,
		Files:      []string{filePath},
		CodeChunks: make([]CodeChunk, 0),
	}

	// 识别语言
	lang := DetectLanguage(filePath)

	// 读取文件
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	stat, _ := os.Stat(filePath)
	result.TotalSize = stat.Size()
	result.FileCount = 1

	// 提取符号
	symbols := make([]Symbol, 0)
	if extractor, exists := s.symbolExtractors[lang]; exists {
		symbols, err = extractor.ExtractSymbols(string(content), filePath)
		if err != nil {
			fmt.Printf("Warning: failed to extract symbols from %s: %v\n", filePath, err)
		}
	}

	// 分块处理
	chunks := s.chunkContent(string(content), filePath, lang, symbols)
	result.CodeChunks = chunks

	return result, nil
}

// shouldIgnore 检查是否应该忽略文件/目录
func (s *Scanner) shouldIgnore(path string) bool {
	// 使用正则表达式检查
	for _, pattern := range ignorePatterns {
		if pattern.MatchString(path) {
			return true
		}
	}

	// 检查自定义排除模式
	for _, pattern := range s.config.ExcludePatterns {
		matched, err := filepath.Match(pattern, path)
		if err == nil && matched {
			return true
		}
	}

	return false
}

// isCodeFile 检查是否是代码文件
func (s *Scanner) isCodeFile(path string) bool {
	// 检查自定义包含模式
	if len(s.config.IncludePatterns) > 0 {
		for _, pattern := range s.config.IncludePatterns {
			matched, err := filepath.Match(pattern, filepath.Base(path))
			if err == nil && matched {
				return true
			}
		}
		return false
	}

	// 使用默认模式
	ext := strings.ToLower(filepath.Ext(path))
	_, exists := extensionToLang[ext]
	return exists
}

// chunkContent 将内容分块
func (s *Scanner) chunkContent(content, filePath string, lang Language, symbols []Symbol) []CodeChunk {
	chunks := make([]CodeChunk, 0)

	// 按行分割
	lines := strings.Split(content, "\n")
	totalLines := len(lines)

	// 简单分块策略：按行数分块
	chunkLines := s.config.MaxChunkSize / 80 // 假设平均每行 80 字符
	if chunkLines < 10 {
		chunkLines = 10 // 最小 10 行
	}
	overlapLines := s.config.ChunkOverlap / 80
	if overlapLines < 0 {
		overlapLines = 0
	}
	if overlapLines >= chunkLines {
		overlapLines = chunkLines - 1
	}

	for start := 0; start < totalLines; {
		end := start + chunkLines
		if end > totalLines {
			end = totalLines
		}

		// 提取内容
		chunkContent := strings.Join(lines[start:end], "\n")
		startLine := start + 1
		endLine := end

		// 查找此块包含的符号
		chunkSymbols := make([]Symbol, 0)
		for _, sym := range symbols {
			if sym.Line >= startLine && sym.Line <= endLine {
				chunkSymbols = append(chunkSymbols, sym)
			}
		}

		chunk := CodeChunk{
			FilePath:    filePath,
			StartLine:   startLine,
			EndLine:     endLine,
			Content:     chunkContent,
			Language:    lang,
			Symbols:     chunkSymbols,
			ChunkSize:   len(chunkContent),
			SymbolCount: len(chunkSymbols),
		}

		chunks = append(chunks, chunk)

		if end >= totalLines {
			break
		}

		nextStart := end - overlapLines // 重叠
		if nextStart <= start {
			nextStart = end
		}
		start = nextStart
	}

	return chunks
}

// DetectLanguage 检测编程语言
func DetectLanguage(filePath string) Language {
	ext := strings.ToLower(filepath.Ext(filePath))
	if lang, exists := extensionToLang[ext]; exists {
		return lang
	}
	return LangUnknown
}

// GoSymbolExtractor Go 语言符号提取器
type GoSymbolExtractor struct{}

func (e *GoSymbolExtractor) ExtractSymbols(content string, file string) ([]Symbol, error) {
	symbols := make([]Symbol, 0)
	scanner := bufio.NewScanner(strings.NewReader(content))
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		if matches := goFuncPattern.FindStringSubmatch(line); len(matches) > 1 {
			symbols = append(symbols, Symbol{
				Name:     matches[1],
				Type:     SymbolFunction,
				Language: LangGo,
				Line:     lineNum,
				File:     file,
			})
		} else if matches := goStructPattern.FindStringSubmatch(line); len(matches) > 1 {
			symbols = append(symbols, Symbol{
				Name:     matches[1],
				Type:     SymbolStruct,
				Language: LangGo,
				Line:     lineNum,
				File:     file,
			})
		} else if matches := goInterfacePattern.FindStringSubmatch(line); len(matches) > 1 {
			symbols = append(symbols, Symbol{
				Name:     matches[1],
				Type:     SymbolInterface,
				Language: LangGo,
				Line:     lineNum,
				File:     file,
			})
		} else if matches := goConstPattern.FindStringSubmatch(line); len(matches) > 1 {
			symbols = append(symbols, Symbol{
				Name:     matches[1],
				Type:     SymbolConstant,
				Language: LangGo,
				Line:     lineNum,
				File:     file,
			})
		} else if matches := goVarPattern.FindStringSubmatch(line); len(matches) > 1 {
			symbols = append(symbols, Symbol{
				Name:     matches[1],
				Type:     SymbolVariable,
				Language: LangGo,
				Line:     lineNum,
				File:     file,
			})
		}
	}

	return symbols, nil
}

// PythonSymbolExtractor Python 语言符号提取器
type PythonSymbolExtractor struct{}

func (e *PythonSymbolExtractor) ExtractSymbols(content string, file string) ([]Symbol, error) {
	symbols := make([]Symbol, 0)
	scanner := bufio.NewScanner(strings.NewReader(content))
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		if matches := pythonClassPattern.FindStringSubmatch(line); len(matches) > 1 {
			symbols = append(symbols, Symbol{
				Name:     matches[1],
				Type:     SymbolClass,
				Language: LangPython,
				Line:     lineNum,
				File:     file,
			})
		} else if matches := pythonFuncPattern.FindStringSubmatch(line); len(matches) > 1 {
			symbols = append(symbols, Symbol{
				Name:     matches[1],
				Type:     SymbolFunction,
				Language: LangPython,
				Line:     lineNum,
				File:     file,
			})
		}
	}

	return symbols, nil
}

// JSSymbolExtractor JavaScript 符号提取器
type JSSymbolExtractor struct{}

func (e *JSSymbolExtractor) ExtractSymbols(content string, file string) ([]Symbol, error) {
	symbols := make([]Symbol, 0)
	scanner := bufio.NewScanner(strings.NewReader(content))
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		if matches := jsClassPattern.FindStringSubmatch(line); len(matches) > 1 {
			symbols = append(symbols, Symbol{
				Name:     matches[1],
				Type:     SymbolClass,
				Language: LangJS,
				Line:     lineNum,
				File:     file,
			})
		} else if matches := jsFuncPattern.FindStringSubmatch(line); len(matches) > 1 {
			symbols = append(symbols, Symbol{
				Name:     matches[1],
				Type:     SymbolFunction,
				Language: LangJS,
				Line:     lineNum,
				File:     file,
			})
		}
	}

	return symbols, nil
}

// TSSymbolExtractor TypeScript 符号提取器
type TSSymbolExtractor struct {
	// TypeScript 继承自 JavaScript 提取器
	JSSymbolExtractor
}

func (e *TSSymbolExtractor) ExtractSymbols(content string, file string) ([]Symbol, error) {
	// 先使用 JS 提取器
	symbols, _ := e.JSSymbolExtractor.ExtractSymbols(content, file)

	// TypeScript 特有的模式
	scanner := bufio.NewScanner(strings.NewReader(content))
	lineNum := 0
	for _, s := range symbols {
		lineNum = s.Line
	}
	if lineNum == 0 {
		lineNum = 0
	} else {
		lineNum++ // 从最后一个符号继续扫描
	}

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		if matches := typeScriptTypePattern.FindStringSubmatch(line); len(matches) > 1 {
			symbols = append(symbols, Symbol{
				Name:     matches[1],
				Type:     SymbolTypeAlias,
				Language: LangTS,
				Line:     lineNum,
				File:     file,
			})
		} else if matches := typeScriptIfacePattern.FindStringSubmatch(line); len(matches) > 1 {
			symbols = append(symbols, Symbol{
				Name:     matches[1],
				Type:     SymbolInterface,
				Language: LangTS,
				Line:     lineNum,
				File:     file,
			})
		}
	}

	return symbols, nil
}
