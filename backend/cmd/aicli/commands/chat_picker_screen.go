package commands

import (
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// chat_picker_screen.go 把 A 族 picker 的租约生命周期收编到统一副屏框架
// （批次 2；批次 5 起只有 unified 一条路径）：
//
//   - openChatScreen 持有租约、在租约内运行 picker 的既有富交互（即时搜索、
//     实时预览、多阶段、窗口化分页），并统一完成 post Close → release →
//     actor idle 的 close 序列；picker 只保留"结果应用"部分，且仍在租约
//     释放之后执行（I3）。
//   - 退休开关值（如 AICLI_CHAT_SCREEN_FRAMEWORK=legacy）不再回退旧实现：
//     按 unified 运行并记 unknown_env 降级事件（§6.1）。
//
// 对外契约不变：交互体在租约内运行、结果在租约外应用、错误按 open/run/close
// 三阶段映射回各 picker 既有文案。

// errChatPickerScreenUnavailable 表示统一框架在能力不足/租约忙/嵌套时未进入
// 副屏（降级）。调用方按"open 阶段失败"的既有文案处理。
var errChatPickerScreenUnavailable = errors.New("副屏不可用")

// chatPickerScreenPhase 标识失败发生在哪个阶段，调用方据此保留各自文案。
type chatPickerScreenPhase uint8

const (
	chatPickerPhaseOpen chatPickerScreenPhase = iota
	chatPickerPhaseRun
	chatPickerPhaseClose
)

func (p chatPickerScreenPhase) String() string {
	switch p {
	case chatPickerPhaseRun:
		return "run"
	case chatPickerPhaseClose:
		return "close"
	default:
		return "open"
	}
}

// chatPickerScreenResult 是一次 picker 交互的完整结果：
//   - Err 非空时 Phase 指出失败阶段；
//   - Degraded 表示统一框架未进入副屏（此时 Err 为空，调用方静默返回，
//     与批次 2 之前各 opener 的门禁失败行为一致）；
//   - 成功路径的用户结果（选中/取消）由调用方在 Run 闭包中捕获。
type chatPickerScreenResult struct {
	Err           error
	Phase         chatPickerScreenPhase
	Degraded      bool
	DegradeReason string
}

// chatPickerScreen 描述一个 picker 屏的框架身份。
type chatPickerScreen struct {
	// ID 是框架内唯一标识（进 debug 事件与计数器，如 "theme.picker"）。
	ID string
	// Title 是副屏标题（与批次 2 之前 chatPickerOpen 的 title 一致）。
	Title string
	// Hooks 是 picker 的 UI actor 生命周期动作身份。
	Hooks chatPickerLeaseHooks
	// Run 在租约内执行富交互；返回 error 视为 run 阶段失败。
	Run func(*ChatSession, ui.ScreenLease) error
}

// runChatPickerScreen 是 picker 的统一入口：租约、屏障、计数器、降级全部由
// openChatScreen 承担（批次 5 起 legacy 回退分支已删除，§6.1）。
func runChatPickerScreen(session *ChatSession, screen chatPickerScreen) chatPickerScreenResult {
	if screen.Run == nil {
		return chatPickerScreenResult{
			Err:   fmt.Errorf("%w: picker %q has no runner", errChatScreenInvalidSpec, screen.ID),
			Phase: chatPickerPhaseOpen,
		}
	}
	return runChatPickerScreenUnified(session, screen)
}

// runChatPickerScreenUnified 走统一框架：租约、屏障、计数器、降级全部由
// openChatScreen 承担，Run 闭包只在租约存续期内运行交互体。
func runChatPickerScreenUnified(session *ChatSession, screen chatPickerScreen) chatPickerScreenResult {
	var final chatPickerScreenResult
	spec := chatPickerScreenAsSpec(screen, func(_ *ChatSession, res chatPickerScreenResult) {
		final = res
	})
	chatScreenOpenAndApply(session, spec)
	return final
}

// chatPickerScreenAsSpec 把一个 picker 屏描述转换为统一副屏 Spec（批次 5/D-E）：
//   - RunScreen 在框架租约内运行 picker 交互体；
//   - AfterClose 在 close 序列（post Close → release → actor idle）完成后回调
//     apply，回调收到的 chatPickerScreenResult 与批次 5 之前
//     runChatPickerScreen 的返回值逐字段一致，因此各 picker 的
//     「结果应用 / 错误文案」代码可以原样复用且必然发生在租约释放之后（I3）。
func chatPickerScreenAsSpec(screen chatPickerScreen, apply func(*ChatSession, chatPickerScreenResult)) chatScreenSpec {
	runErr := error(nil)
	spec := chatScreenSpec{
		ID:          screen.ID,
		Title:       screen.Title,
		Kind:        chatScreenList,
		Trigger:     "command",
		OpenAction:  screen.Hooks.Open,
		CloseAction: screen.Hooks.Close,
		// picker 的降级不落内联行文本：能力不足时保持批次 2 之前
		// "opener 门禁失败即静默返回"的行为，由调用方决定是否补只读输出。
		SilentDegrade: true,
		RunScreen: func(s *ChatSession, lease ui.ScreenLease, _ chatScreenSpec) chatScreenOutcome {
			if err := screen.Run(s, lease); err != nil {
				runErr = err
				return chatScreenOutcome{Result: chatScreenClosedError, Index: -1, Err: err}
			}
			return chatScreenOutcome{Result: chatScreenClosedConfirm, Index: -1}
		},
	}
	if apply != nil {
		spec.AfterClose = func(s *ChatSession, outcome chatScreenOutcome) {
			switch {
			case outcome.Degraded:
				apply(s, chatPickerScreenResult{Degraded: true, DegradeReason: outcome.DegradeReason})
			case runErr != nil:
				apply(s, chatPickerScreenResult{Err: runErr, Phase: chatPickerPhaseRun})
			case outcome.Err != nil:
				apply(s, chatPickerScreenResult{Err: chatPickerMapFrameworkErr(outcome.Err), Phase: chatPickerPhaseClose})
			default:
				apply(s, chatPickerScreenResult{})
			}
		}
	}
	return spec
}

// chatPickerMapFrameworkErr 把框架哨兵映射回 picker 既有哨兵（批次 2 之前
// 的调用方 errors.Is 判定保持不变），未知错误原样透传。
func chatPickerMapFrameworkErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errChatScreenStateUncommitted):
		return fmt.Errorf("%w (%v)", errChatPickerStateUncommitted, err)
	case errors.Is(err, errChatScreenRenderNotReady):
		return fmt.Errorf("%w (%v)", errChatPickerRenderNotReady, err)
	case errors.Is(err, errChatScreenActorNotIdle):
		return fmt.Errorf("%w (%v)", errChatPickerActorNotIdle, err)
	default:
		return err
	}
}

// chatPickerScreenErrorText 把三阶段失败映射回各 picker 既有错误文案
// （label 形如 "主题选择器"、"会话选择器"）。
func chatPickerScreenErrorText(label string, res chatPickerScreenResult) error {
	switch res.Phase {
	case chatPickerPhaseRun:
		return fmt.Errorf("%s失败: %w", label, res.Err)
	case chatPickerPhaseClose:
		if errors.Is(res.Err, errChatPickerActorNotIdle) {
			return fmt.Errorf("%s关闭未就绪", label)
		}
		gap := chatPickerScreenLabelGap(label)
		return fmt.Errorf("关闭%s%s失败: %w", gap, label, res.Err)
	default:
		switch {
		case errors.Is(res.Err, errChatPickerStateUncommitted):
			return fmt.Errorf("%s状态未提交", label)
		case errors.Is(res.Err, errChatPickerRenderNotReady):
			return fmt.Errorf("%s渲染未就绪", label)
		default:
			gap := chatPickerScreenLabelGap(label)
			return fmt.Errorf("打开%s%s失败: %w", gap, label, res.Err)
		}
	}
}

// chatPickerScreenLabelGap 复现历史文案的空格约定：label 以拉丁字符开头时
// "打开/关闭 + label + 失败" 之间保留一个空格（如「打开 MCP 选择器失败」），
// 纯中文 label 不加空格（如「打开主题选择器失败」）。仅用于动词前缀句。
func chatPickerScreenLabelGap(label string) string {
	if label == "" {
		return ""
	}
	if b := label[0]; b < utf8.RuneSelf && b != ' ' {
		return " "
	}
	return ""
}
