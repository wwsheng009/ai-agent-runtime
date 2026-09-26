package ui

import "testing"

// §4.8：/mcp 选择器与其它 lease-bound picker 共用同一套屏障语义 ——
// 陈旧租约的 Open 不生效，Close 释放状态，LeaseReleased 兜底清理。
func TestMCPPickerBarrierLifecycle(t *testing.T) {
	state := UIControllerState{}
	state = reduceUIControllerState(state, LeaseAcquired{LeaseID: 7}, 1)
	state = reduceUIControllerState(state, OpenMCPPicker{LeaseID: 7}, 2)
	if !state.AppState.MCPPicker.Active || state.AppState.MCPPicker.LeaseID != 7 {
		t.Fatalf("OpenMCPPicker 未激活: %#v", state.AppState.MCPPicker)
	}

	// 过期/未知租约的 open 不得改写当前所有权。
	state = reduceUIControllerState(state, OpenMCPPicker{LeaseID: 8}, 3)
	if state.AppState.MCPPicker.LeaseID != 7 {
		t.Fatalf("陈旧租约的 Open 不应生效: %#v", state.AppState.MCPPicker)
	}
	// 租约未持有时的 open 不生效。
	state = reduceUIControllerState(state, OpenMCPPicker{LeaseID: 7}, 4)
	if state.AppState.MCPPicker.LeaseID != 7 {
		t.Fatalf("重复 Open 应保持同一租约: %#v", state.AppState.MCPPicker)
	}

	// 不匹配的 Close 不得清理别人的状态。
	state = reduceUIControllerState(state, CloseMCPPicker{LeaseID: 9}, 5)
	if !state.AppState.MCPPicker.Active {
		t.Fatalf("不匹配的 Close 不应清理: %#v", state.AppState.MCPPicker)
	}
	state = reduceUIControllerState(state, CloseMCPPicker{LeaseID: 7}, 6)
	if state.AppState.MCPPicker.Active {
		t.Fatalf("CloseMCPPicker 未清理: %#v", state.AppState.MCPPicker)
	}

	// 租约释放必须兜底清掉选择器状态（异常路径：没有 Close 动作）。
	state = reduceUIControllerState(state, OpenMCPPicker{LeaseID: 7}, 7)
	state = reduceUIControllerState(state, LeaseReleased{LeaseID: 7}, 8)
	if state.AppState.MCPPicker.Active {
		t.Fatalf("LeaseReleased 必须清理 MCP 选择器: %#v", state.AppState.MCPPicker)
	}
}

// 选择器开闭必须是屏障动作：只有屏障语义才能和租约生命周期对齐。
func TestMCPPickerActionClass(t *testing.T) {
	if got := (OpenMCPPicker{LeaseID: 1}).Class(); got != ClassBarrier {
		t.Fatalf("OpenMCPPicker class = %v", got)
	}
	if got := (CloseMCPPicker{LeaseID: 1}).Class(); got != ClassBarrier {
		t.Fatalf("CloseMCPPicker class = %v", got)
	}
	if got := actionClassString(OpenMCPPicker{LeaseID: 1}); got != "OpenMCPPicker" {
		t.Fatalf("OpenMCPPicker action name = %q", got)
	}
	if got := actionClassString(CloseMCPPicker{LeaseID: 1}); got != "CloseMCPPicker" {
		t.Fatalf("CloseMCPPicker action name = %q", got)
	}
}
