package supervision

// 指令投递语义 v2（P0-3a/M5）与 trigger_turn 自动消费（P0-3b/M6）的开关解析。
// 规格：docs/plan/supervision-parent-child-control-optimization-plan-20260917.md
// §3.3 / §5 / §7.4。开关字段本体在 config.go；这里只放解析语义，避免宿主各
// 自解释默认值。

// MessageSemanticsV2Enabled reports whether the P0-3a instruction-delivery
// semantics matrix is active. Default false: both hosts must keep the
// 2026-09-17 behavior and return fields byte-for-byte identical.
func (c Config) MessageSemanticsV2Enabled() bool {
	return c.WithDefaults().MessageSemanticsV2
}

// TriggerTurnAutoEnabled reports whether a busy child's followup_task /
// send_input(interrupt=false) delivery may be consumed by the run-end
// trigger_turn drain (P0-3b). Default true; nil means enabled. It only has an
// effect when MessageSemanticsV2Enabled is true.
func (c Config) TriggerTurnAutoEnabled() bool {
	cfg := c.WithDefaults()
	if cfg.TriggerTurnAuto == nil {
		return true
	}
	return *cfg.TriggerTurnAuto
}

// TriggerTurnDrainEnabled reports whether the host should wire the run-end
// drain at all: v2 semantics AND automatic trigger turns.
func (c Config) TriggerTurnDrainEnabled() bool {
	return c.MessageSemanticsV2Enabled() && c.TriggerTurnAutoEnabled()
}
