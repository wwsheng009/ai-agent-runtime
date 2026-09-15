package config

import (
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestNormalizeAgentsConfigFillsEveryUnsetField 是「一个字段一条用例」的穷尽式守卫：
// 通过反射遍历 AgentsConfig 的全部字段，每次只给一个字段赋非默认值，断言归一化后其余
// 字段全部等于默认值。新增字段会自动纳入覆盖——这正是过去两份「全零才回退」清单漏改
// 的根因。
func TestNormalizeAgentsConfigFillsEveryUnsetField(t *testing.T) {
	defaults := DefaultAgentsConfig()
	require.Equal(t, defaults, NormalizeAgentsConfig(AgentsConfig{}), "zero value must resolve to the defaults")

	typ := reflect.TypeOf(AgentsConfig{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		t.Run(field.Name, func(t *testing.T) {
			probe := agentsProbeValue(t, field.Type, reflect.ValueOf(defaults).Field(i))

			partial := AgentsConfig{}
			reflect.ValueOf(&partial).Elem().Field(i).Set(probe)

			got := NormalizeAgentsConfig(partial)

			want := defaults
			reflect.ValueOf(&want).Elem().Field(i).Set(probe)
			require.Equal(t, want, got,
				"setting only %s must leave every other field on its default", field.Name)
		})
	}
}

// TestNormalizeAgentsConfigPreservesExplicitOptOuts 固定那些「非零但非默认」的显式选择：
// 归一化只能填未设置值，不能改写操作者的明确意图。
func TestNormalizeAgentsConfigPreservesExplicitOptOuts(t *testing.T) {
	cfg := AgentsConfig{
		MaxThreads:                -1, // 显式不限
		MaxDepth:                  3,
		RegistryTerminalRetention: -time.Hour, // 永不清除
		ReclaimIdleMs:             5000,
	}
	got := NormalizeAgentsConfig(cfg)
	require.Equal(t, -1, got.MaxThreads)
	require.Equal(t, 3, got.MaxDepth)
	require.Equal(t, -time.Hour, got.RegistryTerminalRetention)
	require.Equal(t, 5000, got.ReclaimIdleMs)

	// 未设置项仍然被填：显式选择不会关掉「未设置即默认」。
	require.Equal(t, DefaultAgentsConfig().RegistryReconcileInterval, got.RegistryReconcileInterval)
	require.Equal(t, DefaultAgentsConfig().RegistryReconcileMode, got.RegistryReconcileMode)
}

// TestNormalizeAgentsConfigStaysConservativeForReclaim 单独钉住 P2-8 的保守回收口径：
// ReclaimIdleMs 的默认值就是 0（只回收可证明已死/终态的子会话），归一化不得把它变成
// 「按 TTL 驱逐闲置子会话」。
func TestNormalizeAgentsConfigStaysConservativeForReclaim(t *testing.T) {
	require.Zero(t, DefaultAgentsConfig().ReclaimIdleMs, "the built-in default must stay conservative")
	require.Zero(t, NormalizeAgentsConfig(AgentsConfig{MaxDepth: 2}).ReclaimIdleMs)
}

func TestNormalizeAgentsConfigIsIdempotent(t *testing.T) {
	partial := AgentsConfig{MaxThreads: 4, WaitTimeoutMode: "error"}
	once := NormalizeAgentsConfig(partial)
	require.Equal(t, once, NormalizeAgentsConfig(once))
}

// agentsProbeValue 为每个字段构造一个「非零且不等于默认值」的探针值。
func agentsProbeValue(t *testing.T, fieldType reflect.Type, defaultValue reflect.Value) reflect.Value {
	t.Helper()
	switch fieldType.Kind() {
	case reflect.Int:
		if defaultValue.Int() == 0 {
			return reflect.ValueOf(7).Convert(fieldType)
		}
		return reflect.ValueOf(defaultValue.Int() + 1).Convert(fieldType)
	case reflect.Int64: // time.Duration
		if defaultValue.Int() == 0 {
			return reflect.ValueOf(15 * time.Minute).Convert(fieldType)
		}
		return reflect.ValueOf(time.Duration(defaultValue.Int()) + time.Minute).Convert(fieldType)
	case reflect.String:
		return reflect.ValueOf("probe-value").Convert(fieldType)
	default:
		t.Fatalf("unhandled agents config field kind %s; extend agentsProbeValue", fieldType.Kind())
		return reflect.Value{}
	}
}
