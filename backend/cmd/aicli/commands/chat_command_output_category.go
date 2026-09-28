package commands

// 批次 5（方案 §5.2(4)）：统一输出类别声明——描述「命令的结果落在哪个渲染面」。
//
// 类别与允许的效果字段一一对应（T11 守卫规则 2）：
//
//	inline             主屏内联单元格（Blocks）
//	screen-document    副屏只读页（Screen{Kind: ScreenDocument}）
//	screen-interactive 副屏交互页（Screen{Kind: ScreenList / ScreenStages}）
//	side-effect        主屏执行 + 结果单元格（无 Screen 效果）
//
// 事实源合并：catalog 不再声明 BusyPolicy / 输出类别；类别由运行时注册表显式
// 声明（rtOutput）。未显式声明时按 Mode/Confirm 派生默认值；screen 档必须显式
// 声明，否则 T11 失败——防止新增副屏命令漏标类别（防漂移）。

type chatCommandOutputCategory uint8

const (
	chatOutputUnspecified chatCommandOutputCategory = iota
	chatOutputInline
	chatOutputScreenDocument
	chatOutputScreenInteractive
	chatOutputSideEffect
)

func (c chatCommandOutputCategory) String() string {
	switch c {
	case chatOutputInline:
		return "inline"
	case chatOutputScreenDocument:
		return "screen-document"
	case chatOutputScreenInteractive:
		return "screen-interactive"
	case chatOutputSideEffect:
		return "side-effect"
	default:
		return "unspecified"
	}
}

func (c chatCommandOutputCategory) declared() bool { return c >= chatOutputInline }

// chatCommandOutputCategoryFor 解析注册表声明（显式优先），未声明时按交互语义
// 派生：screen+confirm→交互页，screen→只读页，inline→内联，其余→副作用。
func chatCommandOutputCategoryFor(spec runtimeCommandSpec) chatCommandOutputCategory {
	if spec.Output.declared() {
		return spec.Output
	}
	switch spec.Mode {
	case runtimeModeScreen:
		if spec.Confirm {
			return chatOutputScreenInteractive
		}
		return chatOutputScreenDocument
	case runtimeModeInline:
		return chatOutputInline
	default:
		return chatOutputSideEffect
	}
}
