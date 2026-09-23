package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

const (
	// resumePickerPageSize is how many sessions one lazy page asks for. It only
	// bounds a single query; it never bounds how many sessions a user can reach.
	resumePickerPageSize = 40
	// resumePickerScanPageSize is the metadata scan page size.
	resumePickerScanPageSize = 200
	// resumePickerScanBudget caps how many metadata rows one LoadPage may scan.
	// Highly selective filters (workspace, provider, query) must not turn one
	// keystroke into a full-table scan: the list simply asks again for the next
	// page when the user keeps scrolling.
	resumePickerScanBudget = 4000
)

// resumePickerSessionLoader feeds the /resume full-screen picker page by page.
//
// Performance contract:
//   - only session metadata is read (ListMetadataPage); history is never loaded
//     for rows the user has not confirmed;
//   - the catalog order is the storage order (updated_at DESC, id ASC), so
//     pagination resumes exactly where the previous page stopped;
//   - every LoadPage scans at most resumePickerScanBudget metadata rows;
//   - filter.Limit is ignored on purpose: --session-limit constrains text
//     listings, and inheriting it here is what used to cut /resume off after 20
//     sessions.
type resumePickerSessionLoader struct {
	manager   *runtimechat.SessionManager
	userID    string
	currentID string
	current   *runtimechat.Session
	filter    ChatSessionListFilter
	now       time.Time

	query      string
	scanOffset int
	exhausted  bool
	// pending holds metadata rows that were scanned but not consumed yet: a page
	// boundary must never drop rows that a previous scan already paid for.
	pending []*runtimechat.Session

	items    []ui.FullScreenListItem
	sessions []*runtimechat.Session
	// delivered counts window rows already handed to the caller, so a pinned row
	// added by reset() reaches the list exactly once.
	delivered int
}

func newResumePickerSessionLoader(session *ChatSession, filter ChatSessionListFilter, currentID string, current *runtimechat.Session) *resumePickerSessionLoader {
	loader := &resumePickerSessionLoader{
		currentID: strings.TrimSpace(currentID),
		current:   current,
		now:       time.Now(),
	}
	if session != nil {
		loader.manager = session.SessionManager
		loader.userID = session.SessionUserID
	}
	// The interactive picker paginates instead of truncating.
	filter.Limit = 0
	loader.filter = filter
	loader.reset("")
	return loader
}

// LoadPage implements ui.FullScreenListPageLoader. Offset 0 replaces the window
// (the query changed); any other offset appends the next page.
func (l *resumePickerSessionLoader) LoadPage(ctx context.Context, req ui.FullScreenListPageRequest) (ui.FullScreenListPage, error) {
	if l == nil || l.manager == nil {
		return ui.FullScreenListPage{}, fmt.Errorf("会话管理未启用")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	query := strings.TrimSpace(req.Query)
	replaced := query != l.query
	if replaced {
		l.reset(query)
	}
	limit := req.Limit
	if limit <= 0 {
		limit = resumePickerPageSize
	}

	page := ui.FullScreenListPage{Total: -1}
	if l.delivered < len(l.items) {
		// Rows reset() already pinned (the disabled current-session row in the
		// default view) belong to the window being returned now.
		page.Items = append(page.Items, l.items[l.delivered:]...)
	}
	budget := resumePickerScanBudget
	for len(page.Items) < limit && budget > 0 {
		if len(l.pending) == 0 {
			if l.exhausted {
				break
			}
			batch, err := l.manager.ListMetadataPage(ctx, l.userID, resumePickerScanPageSize, l.scanOffset)
			if err != nil {
				return ui.FullScreenListPage{}, err
			}
			if len(batch) == 0 {
				l.exhausted = true
				break
			}
			l.scanOffset += len(batch)
			budget -= len(batch)
			l.pending = batch
			if len(batch) < resumePickerScanPageSize {
				// The catalog is fully scanned, but the pending rows below still
				// have to be offered before the window can be called complete.
				l.exhausted = true
			}
		}
		for len(l.pending) > 0 && len(page.Items) < limit {
			meta := l.pending[0]
			l.pending = l.pending[1:]
			if !l.accept(meta) {
				continue
			}
			item := buildResumePickerMetadataItem(meta, l.now, false)
			l.items = append(l.items, item)
			l.sessions = append(l.sessions, meta)
			page.Items = append(page.Items, item)
		}
	}
	page.HasMore = !l.exhausted || len(l.pending) > 0
	if !page.HasMore {
		page.Total = len(l.items)
	}
	l.delivered = len(l.items)
	return page, nil
}

// CurrentPage implements ui.FullScreenListPageSnapshot so the picker can preload
// the first page (and decide whether anything is selectable) without the list
// repeating the query.
func (l *resumePickerSessionLoader) CurrentPage() ui.FullScreenListPage {
	if l == nil {
		return ui.FullScreenListPage{}
	}
	total := -1
	hasMore := !l.exhausted || len(l.pending) > 0
	if !hasMore {
		total = len(l.items)
	}
	// Adopting the window counts as delivering it: a later append page must only
	// carry rows the caller has not seen.
	l.delivered = len(l.items)
	return ui.FullScreenListPage{
		Items:   append([]ui.FullScreenListItem(nil), l.items...),
		HasMore: hasMore,
		Total:   total,
	}
}

// Items returns the loaded window. The list result index is aligned with it.
func (l *resumePickerSessionLoader) Items() []ui.FullScreenListItem {
	if l == nil {
		return nil
	}
	return append([]ui.FullScreenListItem(nil), l.items...)
}

// SessionAt maps a list result index back to a session. The pinned current
// session row (index 0 of the default view) is not selectable and yields nil.
func (l *resumePickerSessionLoader) SessionAt(index int) *runtimechat.Session {
	if l == nil || index < 0 || index >= len(l.sessions) {
		return nil
	}
	return l.sessions[index]
}

// HasCandidates reports whether any selectable session is loaded. The pinned
// current session row does not count.
func (l *resumePickerSessionLoader) HasCandidates() bool {
	if l == nil {
		return false
	}
	for _, session := range l.sessions {
		if session != nil {
			return true
		}
	}
	return false
}

func (l *resumePickerSessionLoader) reset(query string) {
	l.query = query
	l.scanOffset = 0
	l.exhausted = false
	l.pending = nil
	l.delivered = 0
	// Fresh slices: the caller keeps the previously returned window, so it must
	// never be mutated by the next page append.
	l.items = nil
	l.sessions = nil
	if query == "" && l.current != nil {
		// The live session stays visible (but disabled) in the default view so
		// /rename and /title results can be verified without a restart.
		l.items = append(l.items, buildResumePickerMetadataItem(l.current, l.now, true))
		l.sessions = append(l.sessions, nil)
	}
}

func (l *resumePickerSessionLoader) accept(meta *runtimechat.Session) bool {
	if meta == nil {
		return false
	}
	if l.currentID != "" && strings.EqualFold(strings.TrimSpace(meta.ID), l.currentID) {
		// Already rendered as the pinned current row.
		return false
	}
	if !matchesChatSessionFilter(meta, l.metadataFilter()) {
		return false
	}
	// Metadata-only rows carry the canonical message count; a session without
	// messages is a placeholder and must not be offered as resumable.
	return resumePickerMetadataHasConversation(meta)
}

func (l *resumePickerSessionLoader) metadataFilter() ChatSessionListFilter {
	filter := l.filter
	filter.Limit = 0
	filter.Query = l.query
	return filter
}

func resumePickerMetadataHasConversation(session *runtimechat.Session) bool {
	if session == nil {
		return false
	}
	return session.MessageCount() > 0
}

// buildResumePickerMetadataItem renders one row from metadata alone: the user
// turn count needs history, so the row reports the canonical message count.
// Every row of the paged picker goes through here — including the pinned
// current session — so one list never mixes two count formats.
func buildResumePickerMetadataItem(session *runtimechat.Session, now time.Time, current bool) ui.FullScreenListItem {
	return buildResumeFullScreenItemWithCounts(session, now, current, 0, session.MessageCount(), false)
}

// formatResumePickerPagedSubtitle describes the paged picker without claiming a
// total it cannot know before the catalog is exhausted.
func formatResumePickerPagedSubtitle(includesCurrent bool) string {
	subtitle := "最近更新优先，滚动到底自动加载更多 · 输入关键词搜索"
	if includesCurrent {
		subtitle += " · 当前会话仅展示"
	}
	return subtitle
}

// resumePickerWindow unifies "loaded row -> session" mapping so the full-screen
// result index means the same thing in both picker modes: paged (a loader owns
// the window) and prebuilt (an already materialized session list).
type resumePickerWindow struct {
	loader          *resumePickerSessionLoader
	items           []ui.FullScreenListItem
	selectable      []*runtimechat.Session
	includesCurrent bool
	selectableCount int
}

// newResumePickerWindow preloads the first page when the session has a manager.
// A nil manager (unit tests / no session management) keeps the prebuilt list
// contract, so callers without storage are unaffected.
func newResumePickerWindow(session *ChatSession, filter ChatSessionListFilter, sessions []*runtimechat.Session, current *runtimechat.Session) (*resumePickerWindow, error) {
	if session != nil && session.SessionManager != nil {
		loader := newResumePickerSessionLoader(session, filter, currentRuntimeSessionID(session), current)
		if _, err := loader.LoadPage(context.Background(), ui.FullScreenListPageRequest{Limit: resumePickerPageSize}); err != nil {
			return nil, err
		}
		return &resumePickerWindow{
			loader:          loader,
			items:           loader.Items(),
			includesCurrent: current != nil,
		}, nil
	}
	items, selectable := buildResumeFullScreenItems(sessions, current, time.Now())
	count := 0
	for _, item := range items {
		if !item.Disabled {
			count++
		}
	}
	return &resumePickerWindow{
		items:           items,
		selectable:      selectable,
		includesCurrent: current != nil,
		selectableCount: count,
	}, nil
}

// pageLoader returns the loader as an interface value, or a real nil when the
// window was built from a prebuilt list (a typed nil would be non-nil).
func (w *resumePickerWindow) pageLoader() ui.FullScreenListPageLoader {
	if w == nil || w.loader == nil {
		return nil
	}
	return w.loader
}

func (w *resumePickerWindow) subtitle() string {
	if w == nil {
		return ""
	}
	if w.loader != nil {
		return formatResumePickerPagedSubtitle(w.includesCurrent)
	}
	return formatResumePickerSubtitle(w.selectableCount, w.includesCurrent)
}

// SessionAt maps a list result index back to a session; the pinned/disabled
// current row yields nil.
func (w *resumePickerWindow) SessionAt(index int) *runtimechat.Session {
	if w == nil {
		return nil
	}
	if w.loader != nil {
		return w.loader.SessionAt(index)
	}
	if index < 0 || index >= len(w.selectable) {
		return nil
	}
	return w.selectable[index]
}

// HasCandidates reports whether any selectable session is loaded.
func (w *resumePickerWindow) HasCandidates() bool {
	if w == nil {
		return false
	}
	if w.loader != nil {
		return w.loader.HasCandidates()
	}
	return w.selectableCount > 0
}
