package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// stubListPageLoader serves a fixed catalog in pages, honouring the requested
// offset/query so the loop's paging contract is exercised end to end.
type stubListPageLoader struct {
	items    []FullScreenListItem
	pageSize int
	requests []FullScreenListPageRequest
	failAt   int
}

func (s *stubListPageLoader) LoadPage(_ context.Context, req FullScreenListPageRequest) (FullScreenListPage, error) {
	s.requests = append(s.requests, req)
	if s.failAt > 0 && len(s.requests) == s.failAt {
		return FullScreenListPage{}, errors.New("storage offline")
	}
	filtered := make([]FullScreenListItem, 0, len(s.items))
	for _, item := range s.items {
		if req.Query == "" || strings.Contains(strings.ToLower(item.Title), strings.ToLower(req.Query)) {
			filtered = append(filtered, item)
		}
	}
	offset := min(max(req.Offset, 0), len(filtered))
	end := min(offset+s.pageSize, len(filtered))
	page := FullScreenListPage{Items: append([]FullScreenListItem(nil), filtered[offset:end]...)}
	page.HasMore = end < len(filtered)
	if !page.HasMore {
		page.Total = len(filtered)
	}
	return page, nil
}

func newStubListItems(titles ...string) []FullScreenListItem {
	items := make([]FullScreenListItem, 0, len(titles))
	for _, title := range titles {
		items = append(items, FullScreenListItem{Title: title, SearchText: title, Preview: title + " preview"})
	}
	return items
}

func newPagedListHooks(frames *[]string, readKey func(context.Context) (editorKey, bool, error)) fullScreenListLoopHooks {
	return fullScreenListLoopHooks{
		refreshSize: func() (int, int) { return 80, 12 },
		writeFrame: func(frame string) error {
			*frames = append(*frames, frame)
			return nil
		},
		readKey: readKey,
	}
}

// TestRunFullScreenListLoopLoadsNextPageOnDemand 固化分页加载：列表只持有首页，
// 只有当选中行到达已加载窗口末尾时才向 loader 追加下一页，最终结果索引始终
// 指向真实会话行（与预建列表语义一致）。
func TestRunFullScreenListLoopLoadsNextPageOnDemand(t *testing.T) {
	loader := &stubListPageLoader{pageSize: 2, items: newStubListItems("item 0", "item 1", "item 2", "item 3", "item 4")}

	keys := []editorKey{
		{kind: editorKeyDown},
		{kind: editorKeyDown},
		{kind: editorKeyDown},
		{kind: editorKeyEnd},
		{kind: editorKeyEnter},
	}
	index := 0
	var frames []string
	result, _, err := runFullScreenListLoop(context.Background(), FullScreenListOptions{
		Title:      "恢复历史会话",
		PageLoader: loader,
	}, newPagedListHooks(&frames, func(context.Context) (editorKey, bool, error) {
		if index >= len(keys) {
			return editorKey{kind: editorKeyCancelPopup}, true, nil
		}
		key := keys[index]
		index++
		return key, true, nil
	}))
	if err != nil {
		t.Fatalf("loop failed: %v", err)
	}
	if result.Cancelled || result.Index != 4 {
		t.Fatalf("expected the last loaded row to be confirmed, got %#v", result)
	}
	if len(loader.requests) != 3 {
		t.Fatalf("expected initial page + 2 lazy pages, got %d requests: %#v", len(loader.requests), loader.requests)
	}
	if loader.requests[0].Offset != 0 || loader.requests[1].Offset != 2 || loader.requests[2].Offset != 4 {
		t.Fatalf("unexpected paging offsets: %#v", loader.requests)
	}
	for _, req := range loader.requests {
		if req.Limit != fullScreenListPageRequestLimit {
			t.Fatalf("expected page request limit %d, got %#v", fullScreenListPageRequestLimit, req)
		}
	}
	if !strings.Contains(frames[len(frames)-1], "已加载 5 项") {
		t.Fatalf("expected the loaded window size in the subtitle, got %q", frames[len(frames)-1])
	}
	if strings.Contains(frames[len(frames)-1], "自动加载更多") {
		t.Fatalf("expected no load-more hint once the catalog is exhausted, got %q", frames[len(frames)-1])
	}
}

// TestRunFullScreenListLoopDebouncesSearchReload 固化动态搜索：输入先在已加载
// 窗口内即时过滤，去抖窗口结束后才用新查询重新拉取首页，避免每次按键都打后端。
func TestRunFullScreenListLoopDebouncesSearchReload(t *testing.T) {
	loader := &stubListPageLoader{
		pageSize: 2,
		items:    newStubListItems("alpha", "beta", "alpha two", "gamma"),
	}
	clock := time.Unix(1700000000, 0)
	step := 0
	var frames []string
	result, _, err := runFullScreenListLoop(context.Background(), FullScreenListOptions{
		Title:          "恢复历史会话",
		PageLoader:     loader,
		SearchDebounce: 10 * time.Millisecond,
	}, fullScreenListLoopHooks{
		refreshSize: func() (int, int) { return 80, 12 },
		writeFrame: func(frame string) error {
			frames = append(frames, frame)
			return nil
		},
		now: func() time.Time { return clock },
		readKey: func(context.Context) (editorKey, bool, error) {
			step++
			switch step {
			case 1:
				return editorKey{kind: editorKeyRune, r: 'a'}, true, nil
			case 2:
				return editorKey{kind: editorKeyRune, r: 'l'}, true, nil
			case 3:
				// 去抖窗口内：不得触发后端查询。
				return editorKey{}, false, nil
			case 4:
				clock = clock.Add(50 * time.Millisecond)
				return editorKey{}, false, nil
			case 5:
				return editorKey{kind: editorKeyEnter}, true, nil
			default:
				return editorKey{kind: editorKeyCancelPopup}, true, nil
			}
		},
	})
	if err != nil {
		t.Fatalf("loop failed: %v", err)
	}
	if len(loader.requests) != 2 {
		t.Fatalf("expected one initial page and one debounced search reload, got %#v", loader.requests)
	}
	if loader.requests[0].Query != "" || loader.requests[1].Query != "al" {
		t.Fatalf("unexpected queries: %#v", loader.requests)
	}
	if result.Cancelled || result.Index != 0 {
		t.Fatalf("expected the first search hit to be confirmed, got %#v", result)
	}
	if !strings.Contains(frames[len(frames)-1], "搜索: al") {
		t.Fatalf("expected the search query in the subtitle, got %q", frames[len(frames)-1])
	}
}

// TestRunFullScreenListLoopSurfacesPageLoadErrors 固化加载失败的降级：错误只
// 出现在副标题里，列表继续可导航，且不会因为错误反复重试。
func TestRunFullScreenListLoopSurfacesPageLoadErrors(t *testing.T) {
	loader := &stubListPageLoader{
		pageSize: 2,
		failAt:   2,
		items:    newStubListItems("item 0", "item 1", "item 2"),
	}
	keys := []editorKey{{kind: editorKeyDown}, {kind: editorKeyEnter}}
	index := 0
	var frames []string
	result, _, err := runFullScreenListLoop(context.Background(), FullScreenListOptions{
		Title:      "恢复历史会话",
		PageLoader: loader,
	}, newPagedListHooks(&frames, func(context.Context) (editorKey, bool, error) {
		if index >= len(keys) {
			return editorKey{kind: editorKeyCancelPopup}, true, nil
		}
		key := keys[index]
		index++
		return key, true, nil
	}))
	if err != nil {
		t.Fatalf("loop failed: %v", err)
	}
	if len(loader.requests) != 2 {
		t.Fatalf("a failed page must not be retried in a loop, got %#v", loader.requests)
	}
	last := frames[len(frames)-1]
	if !strings.Contains(last, "加载失败: storage offline") {
		t.Fatalf("expected the load error in the subtitle, got %q", last)
	}
	if result.Cancelled || result.Index != 1 {
		t.Fatalf("navigation must stay usable after a failed page, got %#v", result)
	}
}

// TestRunFullScreenListLoopReusesPreloadedSnapshot 固化「预载首页」契约：
// loader 若已实现 Snapshot 接口，打开列表时不得重复查询。
func TestRunFullScreenListLoopReusesPreloadedSnapshot(t *testing.T) {
	base := &stubListPageLoader{pageSize: 2, items: newStubListItems("item 0", "item 1")}
	loader := stubSnapshotPageLoader{
		stubListPageLoader: base,
		current:            FullScreenListPage{Items: newStubListItems("item 0", "item 1"), HasMore: false, Total: 2},
	}
	var frames []string
	result, _, err := runFullScreenListLoop(context.Background(), FullScreenListOptions{
		Title:      "恢复历史会话",
		PageLoader: loader,
	}, newPagedListHooks(&frames, func(context.Context) (editorKey, bool, error) {
		return editorKey{kind: editorKeyEnter}, true, nil
	}))
	if err != nil {
		t.Fatalf("loop failed: %v", err)
	}
	if len(base.requests) != 0 {
		t.Fatalf("preloaded first page must not be fetched twice, got %#v", base.requests)
	}
	if result.Cancelled || result.Index != 0 {
		t.Fatalf("expected the preloaded row to be confirmed, got %#v", result)
	}
	if !strings.Contains(frames[len(frames)-1], fmt.Sprintf("已加载 %d 项", 2)) {
		t.Fatalf("expected the loaded window size in the subtitle, got %q", frames[len(frames)-1])
	}
}

type stubSnapshotPageLoader struct {
	*stubListPageLoader
	current FullScreenListPage
}

func (s stubSnapshotPageLoader) CurrentPage() FullScreenListPage {
	return s.current
}
