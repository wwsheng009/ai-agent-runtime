package commands

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// chat_picker_common.go 是 /model 切换/搜索交互的通用化组件。
//
// /model 的 provider→model→reasoning 全屏选择器（即时搜索 + 上下键导航 +
// 页面内编号选择）原先散落在 chat_model_picker.go 中。这里把可复用的部分
// 抽取出来，供 /login 等其它交互式命令共用：
//   - chatPickerSurfaceReady       全屏选择器可用性前置检查
//   - normalizeChatPickerOptions   选项去重 + 大小写不敏感稳定排序
//   - buildChatPickerItems         选项 → 可搜索列表行（(当前) 标记 + SearchText）
//   - chatPickerStage              在已持有 lease 上运行单个搜索选择阶段
//   - chatPickerOpen / chatPickerClose  备屏 lease 生命周期（UI-actor 屏障）
//
// 各命令仍保留自己的语义（例如 /login 的"新建 provider"项、/model 的阶段
// 组合与最终应用），但共享同一套全屏搜索/导航交互。

var (
	// errChatPickerStateUncommitted reports that the UI-actor barrier refused
	// the picker open action (a competing modal owns the surface).
	errChatPickerStateUncommitted = errors.New("选择器状态未提交")
	// errChatPickerRenderNotReady reports that the actor did not reach an idle
	// frame after the open barrier.
	errChatPickerRenderNotReady = errors.New("选择器渲染未就绪")
	// errChatPickerActorNotIdle reports that the actor did not observe the
	// close barrier before the caller resumed primary rendering.
	errChatPickerActorNotIdle = errors.New("选择器关闭未就绪")
)

// chatPickerSurfaceReady reports whether the session can host a full-screen
// searchable picker: unified primary presenter idle, viewport owned, no
// competing popup/lease, and an ANSI TTY tall enough for the list.
func chatPickerSurfaceReady(session *ChatSession) bool {
	if session == nil || session.NoInteractive || session.JSONOutput ||
		session.Interaction == nil || session.Surface == nil {
		return false
	}
	if !session.Surface.Enabled() || !session.Surface.OwnedViewport() ||
		session.Surface.LeaseActive() || session.Surface.HasActivePopup() {
		return false
	}
	if session.RuntimeEventBridge != nil && session.RuntimeEventBridge.isRunActive() {
		return false
	}
	return ui.CanUseFullScreenList(resumeFullScreenTerminal(session))
}

// normalizeChatPickerOptions dedupes case-insensitively and sorts stably
// (case-insensitive primary, raw trimmed tie-break). Every searchable picker
// option builder shares this ordering so providers/models appear in the same
// order across /model and /login.
func normalizeChatPickerOptions(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	options := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		options = append(options, value)
	}
	sort.SliceStable(options, func(i, j int) bool {
		left := strings.ToLower(strings.TrimSpace(options[i]))
		right := strings.ToLower(strings.TrimSpace(options[j]))
		if left == right {
			return strings.TrimSpace(options[i]) < strings.TrimSpace(options[j])
		}
		return left < right
	})
	return options
}

// buildChatPickerItems projects a normalized option list into searchable
// full-screen rows, marking the current value and tagging SearchText with a
// stable kind prefix so one search box can distinguish stages.
func buildChatPickerItems(options []string, current, detail, searchPrefix string) []ui.FullScreenListItem {
	current = strings.TrimSpace(current)
	items := make([]ui.FullScreenListItem, 0, len(options))
	for _, option := range options {
		title := option
		if current != "" && strings.EqualFold(strings.TrimSpace(option), current) {
			title += "  (当前)"
		}
		searchText := option
		if searchPrefix != "" {
			searchText = searchPrefix + " " + option
		}
		items = append(items, ui.FullScreenListItem{
			Title:      title,
			Detail:     detail,
			SearchText: searchText,
		})
	}
	return items
}

// chatPickerStage runs one searchable full-screen stage on an already-acquired
// lease. It returns the original item index, whether the user cancelled, or a
// stage error. An empty item list is an error so the caller decides whether an
// empty catalog is fatal or deserves a fallback.
func chatPickerStage(ctx context.Context, session *ChatSession, lease ui.ScreenLease, options ui.FullScreenListOptions) (int, bool, error) {
	if len(options.Items) == 0 {
		return -1, false, fmt.Errorf("没有可选项")
	}
	picked, err := ui.SelectFullScreenListWithLease(ctx, resumeFullScreenTerminal(session), options, lease)
	if err != nil {
		return -1, false, err
	}
	if picked.Cancelled || picked.Index < 0 || picked.Index >= len(options.Items) {
		return -1, true, nil
	}
	return picked.Index, false, nil
}

// chatPickerStageResult is chatPickerStage for stages that need the full list
// result, in particular DeleteRequested (model removal from /model). Callers
// own the confirm-and-persist cycle and reopen the stage with refreshed items.
func chatPickerStageResult(ctx context.Context, session *ChatSession, lease ui.ScreenLease, options ui.FullScreenListOptions) (ui.FullScreenListResult, error) {
	if len(options.Items) == 0 {
		return ui.FullScreenListResult{}, fmt.Errorf("没有可选项")
	}
	picked, err := ui.SelectFullScreenListWithLease(ctx, resumeFullScreenTerminal(session), options, lease)
	if err != nil {
		return ui.FullScreenListResult{}, err
	}
	return picked, nil
}

// chatPickerLeaseHooks binds one picker kind to its UI-actor barrier actions so
// the lease lifecycle stays generic while each picker keeps its own action
// identity in controller state.
type chatPickerLeaseHooks struct {
	Open  func(leaseID uint64) ui.UIAction
	Close func(leaseID uint64) ui.UIAction
}

// chatPickerOpen borrows the alternate screen for a searchable picker and posts
// the matching UI-actor barrier so the first list frame cannot race the primary
// presenter. The caller owns the returned lease and must release it through
// chatPickerClose.
func chatPickerOpen(session *ChatSession, title string, hooks chatPickerLeaseHooks) (ui.ScreenLease, error) {
	lease, err := session.Surface.AcquireAlternateScreen(context.Background(), ui.FullscreenRequest{
		Title: title,
	})
	if err != nil {
		return nil, err
	}
	if !session.Interaction.postUIAction(hooks.Open(lease.ID())) {
		_ = lease.Release(context.Background())
		return nil, errChatPickerStateUncommitted
	}
	if !session.Interaction.waitUIActorIdleBounded("open chat picker") {
		_ = lease.Release(context.Background())
		return nil, errChatPickerRenderNotReady
	}
	return lease, nil
}

// chatPickerClose posts the close barrier, releases the lease, and waits for
// the actor to observe it (the primary recovery boundary). It tolerates a nil
// session/lease for callers that validated readiness first.
func chatPickerClose(session *ChatSession, lease ui.ScreenLease, hooks chatPickerLeaseHooks) error {
	if session == nil || session.Interaction == nil || lease == nil {
		return nil
	}
	_ = session.Interaction.postUIAction(hooks.Close(lease.ID()))
	releaseErr := lease.Release(context.Background())
	if !session.Interaction.waitUIActorIdleBounded("close chat picker") {
		return errors.Join(errChatPickerActorNotIdle, releaseErr)
	}
	return releaseErr
}
