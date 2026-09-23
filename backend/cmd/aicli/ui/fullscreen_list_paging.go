package ui

import (
	"context"
	"time"
)

// FullScreenListPageRequest describes one lazy page request. Offset is the
// number of items the list has already loaded for Query, so a loader can keep
// its own cursor consistent with what the user can see.
type FullScreenListPageRequest struct {
	Offset int
	Limit  int
	Query  string
}

// FullScreenListPage is one lazily loaded window of list items.
//
// Total is the known number of matching rows, or <= 0 when unknown: a loader
// that can only learn the total by scanning everything must report the loaded
// count instead of guessing.
type FullScreenListPage struct {
	Items   []FullScreenListItem
	HasMore bool
	Total   int
}

// FullScreenListPageLoader feeds a full-screen list one page at a time.
//
// Callers use it to keep large catalogs out of memory: the list asks for the
// next page only when the user scrolls to the end of what is loaded, and it
// asks for a fresh first page (Offset 0) whenever the debounced search query
// changes. LoadPage runs on the list's own goroutine, so implementations must
// be bounded and must not write to the terminal.
type FullScreenListPageLoader interface {
	LoadPage(ctx context.Context, req FullScreenListPageRequest) (FullScreenListPage, error)
}

// FullScreenListPageSnapshot is an optional loader extension. A loader that has
// already fetched its first page (for example to decide whether the list has
// anything to show at all) implements it so opening the list does not repeat
// the query. Without it the list loads page 0 itself before the first frame.
type FullScreenListPageSnapshot interface {
	CurrentPage() FullScreenListPage
}

// defaultFullScreenListSearchDebounce keeps one keystroke from triggering one
// backend query: the list waits for a short quiet period before reloading the
// first page for the new query.
const defaultFullScreenListSearchDebounce = 180 * time.Millisecond

// fullScreenListPager owns the loaded window of a paged full-screen list.
//
// It deliberately mirrors the list's own item slice: the loader appends to its
// internal slice first and the loop then adopts the same page, so the index a
// FullScreenListResult reports keeps pointing at the row the user confirmed.
type fullScreenListPager struct {
	loader   FullScreenListPageLoader
	debounce time.Duration
	now      func() time.Time

	items   []FullScreenListItem
	hasMore bool
	total   int
	lastErr error

	// loadedQuery is the query the currently loaded items belong to;
	// pendingQuery is the newest query the user typed. They differ while the
	// debounce window is open.
	loadedQuery  string
	pendingQuery string
	reloadAt     time.Time
	// autoLoads counts the consecutive pages fetched for a query that produced
	// no locally visible match. It bounds the "search found nothing yet" case.
	autoLoads int
	seeded    bool
}

func newFullScreenListPager(loader FullScreenListPageLoader, debounce time.Duration, now func() time.Time) *fullScreenListPager {
	if now == nil {
		now = time.Now
	}
	if debounce <= 0 {
		debounce = defaultFullScreenListSearchDebounce
	}
	return &fullScreenListPager{loader: loader, debounce: debounce, now: now}
}

// seed adopts a pre-loaded first page when the loader exposes one. It reports
// whether the loaded window is already populated, so the loop only falls back
// to a synchronous LoadPage when the caller did not preload anything.
func (p *fullScreenListPager) seed() bool {
	if p == nil || p.loader == nil || p.seeded {
		return true
	}
	p.seeded = true
	snapshot, ok := p.loader.(FullScreenListPageSnapshot)
	if !ok {
		return false
	}
	page := snapshot.CurrentPage()
	p.applyPage(page, 0)
	p.loadedQuery = p.pendingQuery
	return true
}

// noteQuery records the query currently shown by the list. The first page for a
// changed query is fetched after the debounce window closes.
func (p *fullScreenListPager) noteQuery(query string) {
	if p == nil || query == p.pendingQuery {
		return
	}
	p.pendingQuery = query
	if query == p.loadedQuery {
		// Typing back to the loaded query needs no reload.
		p.reloadAt = time.Time{}
		return
	}
	p.reloadAt = p.now().Add(p.debounce)
}

// dueQuery reports a query whose debounce window has closed and that is not the
// one currently loaded.
func (p *fullScreenListPager) dueQuery() (string, bool) {
	if p == nil || p.loader == nil || p.reloadAt.IsZero() {
		return "", false
	}
	if p.pendingQuery == p.loadedQuery {
		p.reloadAt = time.Time{}
		return "", false
	}
	if p.now().Before(p.reloadAt) {
		return "", false
	}
	p.reloadAt = time.Time{}
	return p.pendingQuery, true
}

// load fetches one page. offset 0 replaces the window (new query); any other
// offset appends the next page for the loaded query.
func (p *fullScreenListPager) load(ctx context.Context, offset int, query string) error {
	if p == nil || p.loader == nil {
		return nil
	}
	page, err := p.loader.LoadPage(ctx, FullScreenListPageRequest{
		Offset: offset,
		Limit:  fullScreenListPageRequestLimit,
		Query:  query,
	})
	if err != nil {
		p.lastErr = err
		return err
	}
	p.lastErr = nil
	p.applyPage(page, offset)
	if offset <= 0 {
		p.loadedQuery = query
		p.autoLoads = 0
	}
	return nil
}

func (p *fullScreenListPager) applyPage(page FullScreenListPage, offset int) {
	if offset <= 0 {
		p.items = append([]FullScreenListItem(nil), page.Items...)
	} else {
		p.items = append(p.items, page.Items...)
	}
	p.hasMore = page.HasMore
	p.total = page.Total
}

// shouldPrefetch decides whether the current frame must extend the window.
//
// Two triggers, both bounded so a single frame can never scan the whole store:
//   - the highlighted row is the last loaded match (the user keeps scrolling);
//   - the query has no local match yet and this query has not exhausted its
//     automatic top-ups (local matching can be stricter than the loader's).
func (p *fullScreenListPager) shouldPrefetch(state fullScreenListState, matchCount int) bool {
	if p == nil || p.loader == nil || !p.hasMore || p.lastErr != nil {
		return false
	}
	// Never extend a window that the debounced reload is about to replace: the
	// appended rows would belong to the previous query.
	if p.pendingQuery != p.loadedQuery {
		return false
	}
	if matchCount == 0 {
		return p.loadedQuery != "" && p.autoLoads < maxFullScreenListAutoLoads
	}
	return state.selected >= matchCount-1
}

// itemsSnapshot copies the loaded window for the loop. The loop keeps its own
// slice so a later loader append can never alias the frames it already drew.
func (p *fullScreenListPager) itemsSnapshot() []FullScreenListItem {
	if p == nil {
		return nil
	}
	return append([]FullScreenListItem(nil), p.items...)
}

// countAutoLoad records a top-up. Only the "nothing matches yet" case is
// bounded by maxFullScreenListAutoLoads, so ordinary scrolling never eats the
// search budget.
func (p *fullScreenListPager) countAutoLoad(matchCount int) {
	if p != nil && matchCount == 0 {
		p.autoLoads++
	}
}

const (
	// fullScreenListPageRequestLimit is the page size the list asks for. It is
	// independent of the viewport: a short terminal still benefits from having
	// the next few rows ready without another query.
	fullScreenListPageRequestLimit = 40
	// maxFullScreenListAutoLoads bounds consecutive automatic pages while a
	// query has produced no locally visible match.
	maxFullScreenListAutoLoads = 4
)
