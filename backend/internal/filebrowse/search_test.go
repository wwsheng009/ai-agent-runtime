package filebrowse

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/fsscope"
)

// newSearchService 允许自定义 Limits（单目录枚举上限等），其余夹具复用 service_test.go。
func newSearchService(t *testing.T, root string, limits Limits) *Service {
	t.Helper()
	roots := &fakeRoots{
		workspaces: map[string]string{"wd_test": root},
		sessions:   map[string]string{},
		listed:     []fsscope.WorkspaceRoot{{ID: "wd_test", Path: root, Name: "test-root"}},
	}
	service := NewService(Deps{Roots: roots, Cwd: root, Limits: limits})
	require.NotNil(t, service)
	return service
}

// searchNames / searchPaths 按顺序取出结果中的 name / path。
func searchNames(items []SearchItem) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Name)
	}
	return names
}

func searchPaths(items []SearchItem) []string {
	paths := make([]string, 0, len(items))
	for _, item := range items {
		paths = append(paths, item.Path)
	}
	return paths
}

// searchAll 按游标翻页收集全部结果；用于断言分页完整、稳定、无重复。
func searchAll(t *testing.T, service *Service, req SearchRequest) []SearchItem {
	t.Helper()
	var collected []SearchItem
	cursor := req.Cursor
	for page := 0; page < 50; page++ {
		req.Cursor = cursor
		result, err := service.Search(context.Background(), req)
		require.NoError(t, err)
		collected = append(collected, result.Items...)
		if !result.HasMore {
			require.Empty(t, result.NextCursor, "has_more=false 时不应给 next_cursor")
			return collected
		}
		require.NotEmpty(t, result.NextCursor, "has_more=true 时必须给 next_cursor")
		cursor = result.NextCursor
	}
	t.Fatal("pagination did not terminate")
	return nil
}

func searchCursorPayload(payload string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}

// TestSearchScoringOrder 表驱动固化打分顺序：精确名 > 前缀 > 词边界 > 子串 > 路径段 >
// 路径子串 > 文件名子序列 > 路径子序列（规划 §4.4.3/§6.1）。
func TestSearchScoringOrder(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		dirs  []string
		query string
		kinds string
		want  []string
	}{
		{
			name:  "exact_over_prefix_over_boundary_over_substring",
			files: []string{"menu", "menu.ts", "a-menu.ts", "ymenuz.ts"},
			query: "menu",
			want:  []string{"menu", "menu.ts", "a-menu.ts", "ymenuz.ts"},
		},
		{
			name:  "boundary_segment_beats_plain_substring",
			files: []string{"zz-menu.ts", "xmenuz.ts"},
			query: "menu",
			want:  []string{"zz-menu.ts", "xmenuz.ts"},
		},
		{
			name:  "case_insensitive",
			files: []string{"MENU", "Menu.ts", "a-MENU.ts"},
			query: "menu",
			want:  []string{"MENU", "Menu.ts", "a-MENU.ts"},
		},
		{
			name:  "name_hits_beat_path_hits",
			files: []string{"lib.ts", "docs/lib/README.md", "docs/libx/a.md"},
			query: "lib",
			want:  []string{"lib.ts", "docs/lib/README.md", "docs/libx/a.md"},
		},
		{
			name:  "name_substring_beats_path_segment",
			files: []string{"xcomposer.ts", "src/composer/index.ts"},
			query: "composer",
			want:  []string{"xcomposer.ts", "src/composer/index.ts"},
		},
		{
			name:  "prefix_beats_subsequence",
			files: []string{"cmu.txt", "composer-menu.ts"},
			query: "cmu",
			want:  []string{"cmu.txt", "composer-menu.ts"},
		},
		{
			name:  "name_subsequence_beats_path_subsequence",
			files: []string{"x-y-z.ts", "x/y/z/a.txt"},
			query: "xyz",
			want:  []string{"x-y-z.ts", "x/y/z/a.txt"},
		},
		{
			name:  "camel_case_segment_is_word_boundary",
			files: []string{"useComposerMenu.ts", "wecomposermenu.ts"},
			query: "menu",
			want:  []string{"useComposerMenu.ts", "wecomposermenu.ts"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			for _, file := range testCase.files {
				writeFile(t, filepath.Join(root, file), []byte("x"))
			}
			for _, dir := range testCase.dirs {
				require.NoError(t, os.MkdirAll(filepath.Join(root, dir), 0o755))
			}
			service, _ := newTestService(t, root)
			result, err := service.Search(context.Background(), SearchRequest{
				Scope: "workspace:wd_test",
				Query: testCase.query,
				Kinds: testCase.kinds,
			})
			require.NoError(t, err)
			require.Equal(t, testCase.want, searchPaths(result.Items))
			for _, item := range result.Items {
				require.NotNil(t, item.Match, "非空 q 的命中项必须带 match")
				require.NotEmpty(t, item.Match.Field)
			}
		})
	}
}

// TestSearchIgnoresFixedDirectories：固定忽略名单（大小写不敏感）不返回也不进入；
// .aicli-uploads 是内部项，show_hidden=true 也不返回（规划 §4.4.2/§6.1）。
func TestSearchIgnoresFixedDirectories(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "node_modules", "pkg", "index.js"), []byte("x"))
	writeFile(t, filepath.Join(root, "NODE_MODULES", "pkg", "upper.js"), []byte("x"))
	writeFile(t, filepath.Join(root, ".git", "config"), []byte("x"))
	writeFile(t, filepath.Join(root, "dist", "bundle.js"), []byte("x"))
	writeFile(t, filepath.Join(root, ".aicli-uploads", "chunk.bin"), []byte("x"))
	writeFile(t, filepath.Join(root, "src", "main.ts"), []byte("x"))
	service, _ := newTestService(t, root)
	ctx := context.Background()

	for _, query := range []string{"index", "config", "bundle", "chunk"} {
		result, err := service.Search(ctx, SearchRequest{
			Scope: "workspace:wd_test", Query: query, ShowHidden: true, Kinds: SearchKindBoth,
		})
		require.NoError(t, err)
		require.Empty(t, result.Items, "忽略目录/内部目录不得被搜索（q=%s）", query)
	}

	// 忽略目录本身也不作为目录结果返回。
	result, err := service.Search(ctx, SearchRequest{
		Scope: "workspace:wd_test", Query: "node_modules", ShowHidden: true, Kinds: SearchKindBoth,
	})
	require.NoError(t, err)
	require.Empty(t, searchPaths(result.Items))

	// 正常目录仍可命中。
	result, err = service.Search(ctx, SearchRequest{Scope: "workspace:wd_test", Query: "main"})
	require.NoError(t, err)
	require.Equal(t, []string{"src/main.ts"}, searchPaths(result.Items))
}

// TestSearchHiddenRules：show_hidden=false 跳过点文件且隐藏目录不下钻；
// show_hidden=true 恢复；固定忽略名单与内部项始终优先（规划 §4.4.2/§6.1）。
func TestSearchHiddenRules(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".env"), []byte("x"))
	writeFile(t, filepath.Join(root, ".config", "settings.json"), []byte("x"))
	writeFile(t, filepath.Join(root, "visible.ts"), []byte("x"))
	writeFile(t, filepath.Join(root, ".aicli-uploads", "chunk.bin"), []byte("x"))
	service, _ := newTestService(t, root)
	ctx := context.Background()

	// 父目录不可见则子文件不返回：.config/settings.json 不能被命中。
	result, err := service.Search(ctx, SearchRequest{Scope: "workspace:wd_test", Query: "settings"})
	require.NoError(t, err)
	require.Empty(t, result.Items)

	result, err = service.Search(ctx, SearchRequest{Scope: "workspace:wd_test", Query: "env"})
	require.NoError(t, err)
	require.Empty(t, result.Items)

	result, err = service.Search(ctx, SearchRequest{Scope: "workspace:wd_test", Query: "visible"})
	require.NoError(t, err)
	require.Equal(t, []string{"visible.ts"}, searchPaths(result.Items))

	// show_hidden=true：点文件与隐藏目录下的文件都能命中。
	result, err = service.Search(ctx, SearchRequest{Scope: "workspace:wd_test", Query: "settings", ShowHidden: true})
	require.NoError(t, err)
	require.Equal(t, []string{".config/settings.json"}, searchPaths(result.Items))

	result, err = service.Search(ctx, SearchRequest{Scope: "workspace:wd_test", Query: "env", ShowHidden: true})
	require.NoError(t, err)
	require.Equal(t, []string{".env"}, searchPaths(result.Items))

	// 内部项始终隐藏。
	result, err = service.Search(ctx, SearchRequest{Scope: "workspace:wd_test", Query: "chunk", ShowHidden: true})
	require.NoError(t, err)
	require.Empty(t, result.Items)
}

// TestSearchKindsFileDirBoth：默认 file、未知值收口 file、dir/both 生效，
// 且目录与文件按统一打分排序（score 优先于「文件优先」tie-break，规划 §4.4.3/§6.1）。
func TestSearchKindsFileDirBoth(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "menu"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "x-menu"), 0o755))
	writeFile(t, filepath.Join(root, "menu.ts"), []byte("x"))
	writeFile(t, filepath.Join(root, "x-menu.ts"), []byte("x"))
	service, _ := newTestService(t, root)
	ctx := context.Background()

	result, err := service.Search(ctx, SearchRequest{Scope: "workspace:wd_test", Query: "menu"})
	require.NoError(t, err)
	require.Equal(t, []string{"menu.ts", "x-menu.ts"}, searchPaths(result.Items))

	// 未知 kinds 按 file 收口。
	result, err = service.Search(ctx, SearchRequest{Scope: "workspace:wd_test", Query: "menu", Kinds: "glob"})
	require.NoError(t, err)
	require.Equal(t, []string{"menu.ts", "x-menu.ts"}, searchPaths(result.Items))

	// kinds=dir（大小写不敏感）。
	result, err = service.Search(ctx, SearchRequest{Scope: "workspace:wd_test", Query: "menu", Kinds: "DIR"})
	require.NoError(t, err)
	require.Equal(t, []string{"menu", "x-menu"}, searchPaths(result.Items))

	// kinds=both：目录 "menu" 精确名 1000 > 文件 "menu.ts" 前缀 900；
	// 同分时文件优先（x-menu.ts 与 x-menu 都是 850）。
	result, err = service.Search(ctx, SearchRequest{Scope: "workspace:wd_test", Query: "menu", Kinds: SearchKindBoth})
	require.NoError(t, err)
	require.Equal(t, []string{"menu", "menu.ts", "x-menu.ts", "x-menu"}, searchPaths(result.Items))
	for _, item := range result.Items {
		require.Contains(t, []string{SearchKindFile, SearchKindDir}, item.Type)
	}
}

// TestSearchDepthTruncation：max_depth 截断 → truncated=true 且归因 depth（规划 §6.1）。
func TestSearchDepthTruncation(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "sub", "deep.ts"), []byte("x"))
	service, _ := newTestService(t, root)
	ctx := context.Background()

	result, err := service.Search(ctx, SearchRequest{Scope: "workspace:wd_test", Query: "deep", MaxDepth: 1})
	require.NoError(t, err)
	require.Empty(t, result.Items)
	require.True(t, result.Truncated)
	require.Equal(t, []string{SearchTruncatedDepth}, result.TruncatedReason)

	// 深度够时不截断，同一文件可见。
	result, err = service.Search(ctx, SearchRequest{Scope: "workspace:wd_test", Query: "deep", MaxDepth: 2})
	require.NoError(t, err)
	require.Equal(t, []string{"sub/deep.ts"}, searchPaths(result.Items))
	require.False(t, result.Truncated)
	require.Empty(t, result.TruncatedReason)
}

// TestSearchDepthTruncationIgnoresEmptyBoundaryDir：max_depth 边界层的空目录不会遗漏任何条目，
// 不得误报 depth 截断；边界层目录有内容时才归因（见 TestSearchDepthTruncation）。
func TestSearchDepthTruncationIgnoresEmptyBoundaryDir(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "top.ts"), []byte("x"))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "empty"), 0o755)) // depth 1，空目录
	service, _ := newTestService(t, root)

	result, err := service.Search(context.Background(), SearchRequest{Scope: "workspace:wd_test", Query: "top", MaxDepth: 1})
	require.NoError(t, err)
	require.Equal(t, []string{"top.ts"}, searchPaths(result.Items))
	require.False(t, result.Truncated, "边界层空目录没有可遗漏的内容")
	require.Empty(t, result.TruncatedReason)
}

// TestSearchScanTruncation：max_scan 截断 → scanned 到顶且归因 scan（规划 §6.1）。
func TestSearchScanTruncation(t *testing.T) {
	root := t.TempDir()
	for index := 0; index < 5; index++ {
		writeFile(t, filepath.Join(root, fmt.Sprintf("f-%d.txt", index)), []byte("x"))
	}
	service, _ := newTestService(t, root)

	result, err := service.Search(context.Background(), SearchRequest{
		Scope: "workspace:wd_test", Query: "f-", MaxScan: 2,
	})
	require.NoError(t, err)
	require.Equal(t, 2, result.Scanned)
	require.True(t, result.Truncated)
	require.Equal(t, []string{SearchTruncatedScan}, result.TruncatedReason)
}

// TestSearchDirEntriesTruncation：单目录超上限按名称序截断并归因 dir_entries（规划 §4.4.2/§6.1）。
func TestSearchDirEntriesTruncation(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt", "e.txt"} {
		writeFile(t, filepath.Join(root, name), []byte("x"))
	}
	service := newSearchService(t, root, Limits{SearchDirEntriesMax: 3})

	result, err := service.Search(context.Background(), SearchRequest{Scope: "workspace:wd_test"})
	require.NoError(t, err)
	require.True(t, result.Truncated)
	require.Equal(t, []string{SearchTruncatedDirEntries}, result.TruncatedReason)
	require.ElementsMatch(t, []string{"a.txt", "b.txt", "c.txt"}, searchPaths(result.Items), "按名称序截断")
}

// TestSearchDirEntriesTruncationSkipsInvisibleOverflow：被单目录上限丢弃的条目若全是忽略目录，
// 不构成遗漏，不得误报 dir_entries 截断（回归：过滤前判定会把「没漏」标成「可能不完整」）。
func TestSearchDirEntriesTruncationSkipsInvisibleOverflow(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "keep.txt"), []byte("x"))
	for _, name := range []string{"dist", "node_modules", "target", "build"} {
		writeFile(t, filepath.Join(root, name, "junk.js"), []byte("x"))
	}
	service := newSearchService(t, root, Limits{SearchDirEntriesMax: 3})

	// 名称序：build < dist < keep.txt < node_modules < target；丢弃的是两个忽略目录。
	result, err := service.Search(context.Background(), SearchRequest{Scope: "workspace:wd_test"})
	require.NoError(t, err)
	require.Equal(t, []string{"keep.txt"}, searchPaths(result.Items))
	require.False(t, result.Truncated, "被丢弃的都是忽略目录：没有遗漏")
	require.Empty(t, result.TruncatedReason)
}

// TestSearchBudgetTruncation 用可注入时钟证明 budget 截断与夹紧：请求 budget_ms=999999
// 仍被夹到 ≤1000ms（否则不会触发），每次取时钟推进 100ms。
func TestSearchBudgetTruncation(t *testing.T) {
	root := t.TempDir()
	for index := 0; index < 20; index++ {
		writeFile(t, filepath.Join(root, fmt.Sprintf("f-%02d.txt", index)), []byte("x"))
	}
	service, _ := newTestService(t, root)
	base := time.Now()
	calls := 0
	service.now = func() time.Time {
		calls++
		return base.Add(time.Duration(calls) * 100 * time.Millisecond)
	}

	result, err := service.Search(context.Background(), SearchRequest{
		Scope: "workspace:wd_test", Query: "f-", BudgetMs: 999999,
	})
	require.NoError(t, err)
	require.True(t, result.Truncated)
	require.Contains(t, result.TruncatedReason, SearchTruncatedBudget)
	require.Less(t, result.Scanned, 20, "超预算必须提前收尾而不是扫完")
	require.GreaterOrEqual(t, result.ElapsedMs, int64(0))
}

// TestSearchClampsOversizedLimits：limit/max_depth/max_scan 超上限夹紧（规划 §4.4.5/§6.1）。
func TestSearchClampsOversizedLimits(t *testing.T) {
	root := t.TempDir()
	for index := 0; index < 5; index++ {
		writeFile(t, filepath.Join(root, fmt.Sprintf("f-%d.txt", index)), []byte("x"))
	}
	service, _ := newTestService(t, root)
	ctx := context.Background()

	result, err := service.Search(ctx, SearchRequest{Scope: "workspace:wd_test", Query: "f-", Limit: 999})
	require.NoError(t, err)
	require.Equal(t, SearchLimitMax, result.Limit, "limit 必须回显夹紧后的值")
	require.LessOrEqual(t, len(result.Items), SearchLimitMax)

	// max_scan=999999 夹到服务上限（默认 50000）：小树全扫完、不截断。
	result, err = service.Search(ctx, SearchRequest{Scope: "workspace:wd_test", Query: "f-", MaxScan: 999999})
	require.NoError(t, err)
	require.Equal(t, 5, result.Scanned)
	require.False(t, result.Truncated)
	require.Equal(t, SearchLimitDefault, result.Limit, "缺省 limit 用默认值")

	// 服务级 ScanMaxEntries 更小时，max_scan 再被它夹紧。
	limited := newSearchService(t, root, Limits{ScanMaxEntries: 3})
	result, err = limited.Search(ctx, SearchRequest{Scope: "workspace:wd_test", Query: "f-", MaxScan: 999999})
	require.NoError(t, err)
	require.Equal(t, 3, result.Scanned)
	require.Contains(t, result.TruncatedReason, SearchTruncatedScan)

	// max_depth=999 夹到 16：17 层深的文件不会被返回（证明没有按 999 深扫）。
	deep := root
	for index := 0; index < 17; index++ {
		deep = filepath.Join(deep, fmt.Sprintf("d%02d", index))
	}
	writeFile(t, filepath.Join(deep, "target.ts"), []byte("x"))
	result, err = service.Search(ctx, SearchRequest{Scope: "workspace:wd_test", Query: "target", MaxDepth: 999, MaxScan: 999999})
	require.NoError(t, err)
	require.Empty(t, result.Items)
	require.Equal(t, []string{SearchTruncatedDepth}, result.TruncatedReason)

	result, err = service.Search(ctx, SearchRequest{Scope: "workspace:wd_test", Query: "target", MaxDepth: SearchMaxDepthMax, MaxScan: 999999})
	require.NoError(t, err)
	require.Empty(t, result.Items)
}

// TestSearchQueryIsLiteral：`*` `?` `(` `[` 等字符按字面匹配，不触发正则/通配（规划 §4.4.1/§6.1）。
func TestSearchQueryIsLiteral(t *testing.T) {
	root := t.TempDir()
	// Windows 文件名不允许 `*`/`?`，因此用「正则/通配语义会命中、字面语义不命中」的对照组固化行为。
	writeFile(t, filepath.Join(root, "a.b.txt"), []byte("x"))
	writeFile(t, filepath.Join(root, "axb.txt"), []byte("x"))
	writeFile(t, filepath.Join(root, "a1.ts"), []byte("x"))
	writeFile(t, filepath.Join(root, "a(x).ts"), []byte("x"))
	writeFile(t, filepath.Join(root, "a[1].ts"), []byte("x"))
	service, _ := newTestService(t, root)

	cases := []struct {
		query string
		want  []string
	}{
		// 正则中 `.` 是任意字符，会额外命中 axb.txt；字面匹配只命中 a.b.txt。
		{"a.b", []string{"a.b.txt"}},
		// 通配 `*.txt` 会命中全部；字面匹配无命中。
		{"*.txt", []string{}},
		// 通配 `a?b` 会命中 axb.txt；字面匹配无命中。
		{"a?b", []string{}},
		{"axb", []string{"axb.txt"}},
		// 正则捕获组 `(x)` 会命中 axb 一类名字；字面匹配只命中含 `(x)` 的名字。
		{"(x)", []string{"a(x).ts"}},
		// 正则字符类 `[1]` 会命中 a1.ts；字面匹配只命中含 `[1]` 的名字。
		{"[1]", []string{"a[1].ts"}},
	}
	for _, testCase := range cases {
		result, err := service.Search(context.Background(), SearchRequest{Scope: "workspace:wd_test", Query: testCase.query})
		require.NoError(t, err)
		require.Equal(t, testCase.want, searchPaths(result.Items), "q=%q 必须按字面匹配", testCase.query)
	}
}

// TestSearchQueryTooLong：q 超 256 rune → 400 query_too_long；边界按 rune 计而非字节（规划 §6.1）。
func TestSearchQueryTooLong(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), []byte("x"))
	service, _ := newTestService(t, root)
	ctx := context.Background()

	_, err := service.Search(ctx, SearchRequest{Scope: "workspace:wd_test", Query: strings.Repeat("a", SearchQueryMaxRunes+1)})
	require.True(t, fsscope.IsCode(err, CodeQueryTooLong))
	target, ok := fsscope.AsError(err)
	require.True(t, ok)
	require.Equal(t, 400, target.HTTPStatus)

	// 256 个多字节 rune（768 字节）仍在限内：上限按 rune 计。
	result, err := service.Search(ctx, SearchRequest{Scope: "workspace:wd_test", Query: strings.Repeat("中", SearchQueryMaxRunes)})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Empty(t, result.Items)
}

// TestSearchEmptyResultShape：items 永不为 null；无截断时 truncated_reason 省略（omitempty）。
func TestSearchEmptyResultShape(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), []byte("x"))
	service, _ := newTestService(t, root)

	result, err := service.Search(context.Background(), SearchRequest{Scope: "workspace:wd_test", Query: "zzz-nomatch"})
	require.NoError(t, err)
	require.NotNil(t, result.Items)
	require.Empty(t, result.Items)
	require.False(t, result.Truncated)
	require.False(t, result.HasMore)
	require.Empty(t, result.NextCursor)
	require.Equal(t, SearchLimitDefault, result.Limit)

	payload, err := json.Marshal(result)
	require.NoError(t, err)
	require.Contains(t, string(payload), `"items":[]`)
	require.Contains(t, string(payload), `"truncated":false`)
	require.NotContains(t, string(payload), "truncated_reason")
	require.NotContains(t, string(payload), "next_cursor")
}

// TestSearchCanceledContext：取消的 ctx 立即退出（前端 abort 后服务端尽快返回）。
func TestSearchCanceledContext(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), []byte("x"))
	service, _ := newTestService(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := service.Search(ctx, SearchRequest{Scope: "workspace:wd_test", Query: "a"})
	require.Error(t, err)
	require.True(t, fsscope.IsCode(err, fsscope.CodeFSReadFailed))
}

// TestSearchPaginationIsCompleteAndUnique：游标翻页完整、无重复（非空 q 与空 q 各一例，规划 §6.1）。
func TestSearchPaginationIsCompleteAndUnique(t *testing.T) {
	root := t.TempDir()
	for index := 0; index < 25; index++ {
		writeFile(t, filepath.Join(root, fmt.Sprintf("menu-%02d.ts", index)), []byte("x"))
	}
	service, _ := newTestService(t, root)

	items := searchAll(t, service, SearchRequest{Scope: "workspace:wd_test", Query: "menu", Limit: 10})
	require.Len(t, items, 25)
	seen := map[string]bool{}
	for _, item := range items {
		require.False(t, seen[item.Path], "duplicated item %s", item.Path)
		seen[item.Path] = true
	}

	// 空 q 分页同样稳定：按 (depth asc, mtime desc, path asc)，游标按 path 精确续页。
	emptyItems := searchAll(t, service, SearchRequest{Scope: "workspace:wd_test", Limit: 10})
	require.Len(t, emptyItems, 25)
	seen = map[string]bool{}
	for _, item := range emptyItems {
		require.Zero(t, item.Score, "空 q 不做打分")
		require.Nil(t, item.Match, "空 q 没有命中位置")
		require.False(t, seen[item.Path], "duplicated item %s", item.Path)
		seen[item.Path] = true
	}
}

// TestSearchCandidateAfterCursorOrdering：续页比较键必须与 sortSearchCandidates 一一对应；
// 游标项消失时按同一全序定位起点（空 q：depth/mtime/path；非空 q：score/文件优先/path）。
func TestSearchCandidateAfterCursorOrdering(t *testing.T) {
	file := func(path string, score int, mtime int64) searchCandidate {
		return searchCandidate{
			item:  SearchItem{Path: path, Type: SearchKindFile, Score: score, Mtime: mtime},
			depth: strings.Count(path, "/") + 1,
		}
	}
	dir := func(path string, score int, mtime int64) searchCandidate {
		return searchCandidate{
			item:  SearchItem{Path: path, Type: SearchKindDir, Score: score, Mtime: mtime},
			depth: strings.Count(path, "/") + 1,
		}
	}

	emptyCases := []struct {
		name      string
		candidate searchCandidate
		last      searchCursorPos
		want      bool
	}{
		{"更深者排后", file("a/b/c.ts", 0, 100), searchCursorPos{Path: "a/b.ts", Depth: 2, Mtime: 100, Type: SearchKindFile}, true},
		{"更浅者排前", file("a.ts", 0, 100), searchCursorPos{Path: "a/b.ts", Depth: 2, Mtime: 100, Type: SearchKindFile}, false},
		{"同深度 mtime 更新者排前", file("b.ts", 0, 200), searchCursorPos{Path: "a.ts", Depth: 1, Mtime: 100, Type: SearchKindFile}, false},
		{"同深度 mtime 更旧者排后", file("b.ts", 0, 50), searchCursorPos{Path: "a.ts", Depth: 1, Mtime: 100, Type: SearchKindFile}, true},
		{"同深度同 mtime path 更大者排后", file("b.ts", 0, 100), searchCursorPos{Path: "a.ts", Depth: 1, Mtime: 100, Type: SearchKindFile}, true},
		{"同深度同 mtime path 更小者排前", file("a.ts", 0, 100), searchCursorPos{Path: "b.ts", Depth: 1, Mtime: 100, Type: SearchKindFile}, false},
		{"同一位置返回 false", file("a.ts", 0, 100), searchCursorPos{Path: "a.ts", Depth: 1, Mtime: 100, Type: SearchKindFile}, false},
	}
	for _, testCase := range emptyCases {
		t.Run("empty/"+testCase.name, func(t *testing.T) {
			require.Equal(t, testCase.want, searchCandidateAfterCursor(testCase.candidate, testCase.last, true))
		})
	}

	scoreCases := []struct {
		name      string
		candidate searchCandidate
		last      searchCursorPos
		want      bool
	}{
		{"分数更低者排后", file("a.ts", 800, 0), searchCursorPos{Path: "z.ts", Score: 900, Type: SearchKindFile}, true},
		{"分数更高者排前", file("a.ts", 950, 0), searchCursorPos{Path: "z.ts", Score: 900, Type: SearchKindFile}, false},
		{"同分目录排在文件后", dir("a", 900, 0), searchCursorPos{Path: "z.ts", Score: 900, Type: SearchKindFile}, true},
		{"同分文件排在目录前", file("z.ts", 900, 0), searchCursorPos{Path: "a", Score: 900, Type: SearchKindDir}, false},
		{"同分同类型 path 更大者排后", file("z.ts", 900, 0), searchCursorPos{Path: "a.ts", Score: 900, Type: SearchKindFile}, true},
		{"同一位置返回 false", file("a.ts", 900, 0), searchCursorPos{Path: "a.ts", Score: 900, Type: SearchKindFile}, false},
	}
	for _, testCase := range scoreCases {
		t.Run("scored/"+testCase.name, func(t *testing.T) {
			require.Equal(t, testCase.want, searchCandidateAfterCursor(testCase.candidate, testCase.last, false))
		})
	}
}

// setMtime 固定文件 mtime：空 q 排序依赖 mtime，测试必须显式设置才能跨文件系统确定。
func setMtime(t *testing.T, path string, mtime time.Time) {
	t.Helper()
	require.NoError(t, os.Chtimes(path, mtime, mtime))
}

// searchCursorJSON 构造 v2 游标载荷：last 字段与排序键一一对应（score/path/depth/mtime/type）。
func searchCursorJSON(version int, scope, query, base, path string, score, depth int, mtime int64, itemType string) string {
	return fmt.Sprintf(`{"v":%d,"scope":%q,"q":%q,"base":%q,"last":{"score":%d,"path":%q,"depth":%d,"mtime":%d,"type":%q}}`,
		version, scope, query, base, score, path, depth, mtime, itemType)
}

// TestSearchCursorInvalidAndStable：游标篡改/版本不符/与当前请求不匹配 → 400 cursor_invalid；
// 同树重复请求结果与游标一致（规划 §4.4.4/§6.1）。
func TestSearchCursorInvalidAndStable(t *testing.T) {
	root := t.TempDir()
	for index := 0; index < 3; index++ {
		writeFile(t, filepath.Join(root, fmt.Sprintf("menu-%d.ts", index)), []byte("x"))
	}
	service, _ := newTestService(t, root)
	ctx := context.Background()

	page, err := service.Search(ctx, SearchRequest{Scope: "workspace:wd_test", Query: "menu", Limit: 1})
	require.NoError(t, err)
	require.True(t, page.HasMore)
	require.NotEmpty(t, page.NextCursor)

	again, err := service.Search(ctx, SearchRequest{Scope: "workspace:wd_test", Query: "menu", Limit: 1})
	require.NoError(t, err)
	require.Equal(t, page.Items, again.Items, "同树重复请求结果一致")
	require.Equal(t, page.NextCursor, again.NextCursor, "同树重复请求游标一致")

	invalid := []struct {
		name   string
		cursor string
	}{
		{"not base64", "!!!not-base64!!!"},
		{"not json", searchCursorPayload("not json at all")},
		{"unsupported version", searchCursorPayload(searchCursorJSON(99, "workspace:wd_test", "menu", "", "menu-0.ts", 900, 1, 0, SearchKindFile))},
		{"scope mismatch", searchCursorPayload(searchCursorJSON(2, "workspace:other", "menu", "", "menu-0.ts", 900, 1, 0, SearchKindFile))},
		{"query mismatch", searchCursorPayload(searchCursorJSON(2, "workspace:wd_test", "other", "", "menu-0.ts", 900, 1, 0, SearchKindFile))},
		{"base mismatch", searchCursorPayload(searchCursorJSON(2, "workspace:wd_test", "menu", "src", "menu-0.ts", 900, 1, 0, SearchKindFile))},
		{"absolute path", searchCursorPayload(searchCursorJSON(2, "workspace:wd_test", "menu", "", "/etc/passwd", 900, 1, 0, SearchKindFile))},
		{"traversal path", searchCursorPayload(searchCursorJSON(2, "workspace:wd_test", "menu", "", "../secret", 900, 1, 0, SearchKindFile))},
		{"encoded traversal path", searchCursorPayload(searchCursorJSON(2, "workspace:wd_test", "menu", "", "a%2f..%2f..%2fsecret", 900, 1, 0, SearchKindFile))},
		{"negative score", searchCursorPayload(searchCursorJSON(2, "workspace:wd_test", "menu", "", "menu-0.ts", -1, 1, 0, SearchKindFile))},
		{"negative depth", searchCursorPayload(searchCursorJSON(2, "workspace:wd_test", "menu", "", "menu-0.ts", 900, -1, 0, SearchKindFile))},
		{"invalid item type", searchCursorPayload(searchCursorJSON(2, "workspace:wd_test", "menu", "", "menu-0.ts", 900, 1, 0, "symlink"))},
		{"too long", strings.Repeat("A", searchCursorMaxBytes+1)},
	}
	for _, testCase := range invalid {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := service.Search(ctx, SearchRequest{Scope: "workspace:wd_test", Query: "menu", Limit: 1, Cursor: testCase.cursor})
			require.True(t, fsscope.IsCode(err, CodeCursorInvalid), "want cursor_invalid, got %v", err)
		})
	}

	// 合法游标 + 同 scope/q/base：继续翻页且不重复上一页。
	next, err := service.Search(ctx, SearchRequest{Scope: "workspace:wd_test", Query: "menu", Limit: 1, Cursor: page.NextCursor})
	require.NoError(t, err)
	require.Len(t, next.Items, 1)
	require.NotEqual(t, page.Items[0].Path, next.Items[0].Path)
}

// TestSearchRejectsUnsafeInput：越界/绝对路径/编码绕过复用 fsscope 错误码（规划 §4.4.6/§6.1）。
func TestSearchRejectsUnsafeInput(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), []byte("x"))
	service, _ := newTestService(t, root)

	cases := []struct {
		name  string
		scope string
		path  string
		code  string
	}{
		{"missing scope", "", "", fsscope.CodeScopeInvalid},
		{"unregistered workspace", "workspace:other", "", fsscope.CodeScopeNotFound},
		{"parent traversal", "workspace:wd_test", "../..", fsscope.CodePathOutsideScope},
		{"encoded traversal", "workspace:wd_test", "..%2f..", fsscope.CodePathOutsideScope},
		{"traversal in middle", "workspace:wd_test", "a/../../b", fsscope.CodePathOutsideScope},
		{"absolute posix", "workspace:wd_test", "/etc", fsscope.CodePathMustBeRelative},
		{"absolute windows", "workspace:wd_test", `C:\Windows`, fsscope.CodePathMustBeRelative},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := service.Search(context.Background(), SearchRequest{Scope: testCase.scope, Path: testCase.path})
			require.True(t, fsscope.IsCode(err, testCase.code), "want %s, got %v", testCase.code, err)
		})
	}
}

// TestSearchDoesNotFollowSymlinkDirs：符号链接目录一律不跟随、不作为目录结果返回；
// 符号链接文件按解析结果返回（规划 §4.4.2/§6.1）。
func TestSearchDoesNotFollowSymlinkDirs(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret.txt"), []byte("x"))
	writeFile(t, filepath.Join(root, "real", "inside.txt"), []byte("x"))
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unavailable in this environment: %v", err)
	}
	service, _ := newTestService(t, root)
	ctx := context.Background()

	result, err := service.Search(ctx, SearchRequest{
		Scope: "workspace:wd_test", Query: "secret", ShowHidden: true, Kinds: SearchKindBoth,
	})
	require.NoError(t, err)
	require.Empty(t, result.Items, "符号链接目录不得跟随（外部文件不可见）")

	result, err = service.Search(ctx, SearchRequest{
		Scope: "workspace:wd_test", Query: "link", ShowHidden: true, Kinds: SearchKindBoth,
	})
	require.NoError(t, err)
	require.Empty(t, searchPaths(result.Items), "符号链接目录不作为结果返回")

	result, err = service.Search(ctx, SearchRequest{Scope: "workspace:wd_test", Query: "inside"})
	require.NoError(t, err)
	require.Equal(t, []string{"real/inside.txt"}, searchPaths(result.Items))

	require.NoError(t, os.Symlink(filepath.Join(root, "real", "inside.txt"), filepath.Join(root, "filelink.txt")))
	result, err = service.Search(ctx, SearchRequest{Scope: "workspace:wd_test", Query: "filelink"})
	require.NoError(t, err)
	require.Equal(t, []string{"filelink.txt"}, searchPaths(result.Items))
	require.Equal(t, SearchKindFile, result.Items[0].Type)
	require.Greater(t, result.Items[0].Size, int64(0))
}

// TestSearchEmptyQueryShallowFirst：空 q 浅层优先（depth ≤ 2）且不做全量扫描；
// 收集满 limit×4 即停（规划 §4.4.3/§6.1）。
func TestSearchEmptyQueryShallowFirst(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "root.txt"), []byte("x"))                       // depth 1
	writeFile(t, filepath.Join(root, "src", "one.ts"), []byte("x"))                  // depth 2
	writeFile(t, filepath.Join(root, "src", "lib", "two.ts"), []byte("x"))           // depth 3
	writeFile(t, filepath.Join(root, "src", "lib", "deep", "three.ts"), []byte("x")) // depth 4
	service, _ := newTestService(t, root)

	result, err := service.Search(context.Background(), SearchRequest{Scope: "workspace:wd_test"})
	require.NoError(t, err)
	require.Equal(t, []string{"root.txt", "src/one.ts"}, searchPaths(result.Items), "浅层优先")
	require.False(t, result.Truncated, "浅扫是设计性采样，不是截断")

	// 收集满 limit×4 即停：100 个根层文件 + limit=10 → 只扫 40 项就收尾。
	flat := t.TempDir()
	for index := 0; index < 100; index++ {
		writeFile(t, filepath.Join(flat, fmt.Sprintf("f-%03d.txt", index)), []byte("x"))
	}
	flatService, _ := newTestService(t, flat)
	result, err = flatService.Search(context.Background(), SearchRequest{Scope: "workspace:wd_test", Limit: 10})
	require.NoError(t, err)
	require.Len(t, result.Items, 10)
	require.True(t, result.HasMore)
	require.LessOrEqual(t, result.Scanned, 40, "收集满 limit×4 即停，不做全量扫描")
	require.False(t, result.Truncated)
}

// TestSearchEmptyQueryCursorSurvivesRemovedItem：空 q 翻页时游标项被删除（树变化；不承诺快照
// 一致性，但不得因次要键误判而整段漏项）——续页必须按同一 (depth asc, mtime desc, path asc)
// 全序定位起点。
func TestSearchEmptyQueryCursorSurvivesRemovedItem(t *testing.T) {
	root := t.TempDir()
	base := time.Unix(1_700_000_000, 0)
	for _, name := range []string{"a.txt", "b.txt", "aa.txt"} {
		writeFile(t, filepath.Join(root, name), []byte("x"))
	}
	writeFile(t, filepath.Join(root, "sub", "c.txt"), []byte("x"))
	setMtime(t, filepath.Join(root, "a.txt"), base.Add(500*time.Second))
	setMtime(t, filepath.Join(root, "b.txt"), base.Add(400*time.Second))
	setMtime(t, filepath.Join(root, "aa.txt"), base.Add(100*time.Second))
	setMtime(t, filepath.Join(root, "sub", "c.txt"), base.Add(300*time.Second))
	service, _ := newTestService(t, root)
	ctx := context.Background()

	page, err := service.Search(ctx, SearchRequest{Scope: "workspace:wd_test", Limit: 2})
	require.NoError(t, err)
	require.Equal(t, []string{"a.txt", "b.txt"}, searchPaths(page.Items), "depth 1 内按 mtime 降序")
	require.True(t, page.HasMore)
	require.NotEmpty(t, page.NextCursor)

	require.NoError(t, os.Remove(filepath.Join(root, "b.txt")))
	next, err := service.Search(ctx, SearchRequest{Scope: "workspace:wd_test", Limit: 2, Cursor: page.NextCursor})
	require.NoError(t, err)
	require.Equal(t, []string{"aa.txt", "sub/c.txt"}, searchPaths(next.Items),
		"游标项消失后仍按同一排序键续页：aa.txt（path 更小但 mtime 更旧）不得被跳过")
}

// TestSearchBaseSubPathScopesResults：path 指定 base 子目录时结果只覆盖该子树；返回的 path
// 仍**相对作用域根**（含 base 前缀，与 fs/list 同口径），游标与 base 绑定
// （规划 §4.4.1/§4.4.4/§4.4.6 line 290）。
func TestSearchBaseSubPathScopesResults(t *testing.T) {
	root := t.TempDir()
	base := time.Unix(1_700_000_000, 0)
	writeFile(t, filepath.Join(root, "top.ts"), []byte("x"))
	writeFile(t, filepath.Join(root, "src", "app", "main.ts"), []byte("x"))
	writeFile(t, filepath.Join(root, "src", "lib", "util.ts"), []byte("x"))
	writeFile(t, filepath.Join(root, "docs", "readme.md"), []byte("x"))
	setMtime(t, filepath.Join(root, "src", "app", "main.ts"), base.Add(200*time.Second))
	setMtime(t, filepath.Join(root, "src", "lib", "util.ts"), base.Add(100*time.Second))
	service, _ := newTestService(t, root)
	ctx := context.Background()

	result, err := service.Search(ctx, SearchRequest{Scope: "workspace:wd_test", Path: "src", Query: "util"})
	require.NoError(t, err)
	require.Equal(t, "src", result.Base)
	require.Equal(t, []string{"src/lib/util.ts"}, searchPaths(result.Items), "path 相对作用域根，含 base 前缀")
	require.Equal(t, "util.ts", result.Items[0].Name)

	// 空 q 浅扫同样只覆盖 base 子树（base 外条目不可见），路径前缀不变。
	empty, err := service.Search(ctx, SearchRequest{Scope: "workspace:wd_test", Path: "src"})
	require.NoError(t, err)
	require.Equal(t, []string{"src/app/main.ts", "src/lib/util.ts"}, searchPaths(empty.Items))

	// 同一 base 的游标可续页；换 base 复用旧游标 → 400 cursor_invalid（不静默按新 base 解释）。
	paged, err := service.Search(ctx, SearchRequest{Scope: "workspace:wd_test", Path: "src", Limit: 1})
	require.NoError(t, err)
	require.True(t, paged.HasMore)
	_, err = service.Search(ctx, SearchRequest{Scope: "workspace:wd_test", Limit: 1, Cursor: paged.NextCursor})
	require.True(t, fsscope.IsCode(err, CodeCursorInvalid), "want cursor_invalid, got %v", err)
}

// TestSearchMatchOffsetsUseRuneOffsets：match.start/end 是 rune 偏移（前端可直接切片高亮）；
// 注释同时固化「不做 Unicode NFC/NFD 归一化」的已知限制（规划 §4.4.3 与 §7-R9）。
func TestSearchMatchOffsetsUseRuneOffsets(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "café-menu.ts"), []byte("x"))
	service, _ := newTestService(t, root)

	result, err := service.Search(context.Background(), SearchRequest{Scope: "workspace:wd_test", Query: "é-m"})
	require.NoError(t, err)
	require.Len(t, result.Items, 1)
	match := result.Items[0].Match
	require.NotNil(t, match)
	require.Equal(t, "name", match.Field)
	require.Equal(t, 3, match.Start)
	require.Equal(t, 6, match.End)
	require.Equal(t, "é-m", string([]rune(result.Items[0].Name)[match.Start:match.End]))

	// 路径命中同样用完整相对路径的 rune 偏移。
	writeFile(t, filepath.Join(root, "日本語", "メモ.txt"), []byte("x"))
	result, err = service.Search(context.Background(), SearchRequest{Scope: "workspace:wd_test", Query: "メモ"})
	require.NoError(t, err)
	require.Len(t, result.Items, 1)
	match = result.Items[0].Match
	require.NotNil(t, match)
	require.Equal(t, "name", match.Field)
	// name 命中时偏移相对 name，而不是相对 path。
	require.Equal(t, "メモ", string([]rune(result.Items[0].Name)[match.Start:match.End]))

	// 目录名命中时 field=path，偏移相对完整相对路径的 rune 索引。
	result, err = service.Search(context.Background(), SearchRequest{Scope: "workspace:wd_test", Query: "日本語"})
	require.NoError(t, err)
	require.Len(t, result.Items, 1)
	match = result.Items[0].Match
	require.NotNil(t, match)
	require.Equal(t, "path", match.Field)
	require.Equal(t, "日本語", string([]rune(result.Items[0].Path)[match.Start:match.End]))
}

// TestSearchReadDirErrorMapping：base 目录读失败时的错误码映射，与 /fs/list 同口径；
// 未知 IO 错误用 fs_search_failed(500)（规划 §4.4.1 错误码表）。
func TestSearchReadDirErrorMapping(t *testing.T) {
	target := &fsscope.Target{Rel: "src", Abs: `C:\nowhere\src`}

	cases := []struct {
		name     string
		err      error
		wantCode string
		wantHTTP int
	}{
		{"not_exist", os.ErrNotExist, fsscope.CodePathNotFound, 404},
		{"permission", os.ErrPermission, fsscope.CodePathPermission, 403},
		{"io_error", errors.New("disk exploded"), CodeSearchFailed, 500},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := searchReadDirError(testCase.err, target)
			require.NotNil(t, result)
			require.Equal(t, testCase.wantCode, result.Code)
			require.Equal(t, testCase.wantHTTP, result.HTTPStatus)
		})
	}
}
