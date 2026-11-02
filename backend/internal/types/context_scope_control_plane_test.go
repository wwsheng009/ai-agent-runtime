package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The digest and wake strings below are copied verbatim from stored production
// rows that the fact ledger had to reject; the predicate is the only thing that
// keeps runtime bookkeeping from being filed as a user-claimed fact.
func TestIsControlPlanePromptText(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		wanted bool
	}{
		{
			name:   "supervision progress digest",
			text:   "[子任务与后台作业进展] 有 20 项待处理。 - 子任务会话 session_20261027073826_2rWb8QSo：已结束。child session failed 结果状态：failed",
			wanted: true,
		},
		{
			name:   "supervision auto wake",
			text:   "[supervision] 存在待处理的子 Agent / Team 关键生命周期事件，请检查生命周期摘要（lifecycle digest）并继续。",
			wanted: true,
		},
		{
			name:   "supervision resume",
			text:   "[supervision] resume：子任务生命周期事件触发**同一 turn 续跑**。以下 rollup 由 runtime 组装，无需回溯历史。",
			wanted: true,
		},
		{
			name:   "real user assertion",
			text:   "字段类的属性块必须在几何之前，不要用旧接口",
			wanted: false,
		},
		{
			name:   "tag quoted mid-sentence is still user speech",
			text:   "the doc says [supervision] output is noisy, ignore it",
			wanted: false,
		},
		{name: "blank", text: "   ", wanted: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			require.Equal(t, testCase.wanted, IsControlPlanePromptText(testCase.text))
		})
	}
}

// The markers are consumed by supervision to build the prompts and by the fact
// ledger to reject them; a rename that changes only one side breaks both.
func TestControlPlaneMarkerShape(t *testing.T) {
	require.Equal(t, "[子任务与后台作业进展]", SupervisionDigestPromptHeader)
	require.Equal(t, "[supervision]", SupervisionPromptTag)
	require.True(t, IsControlPlanePromptText(SupervisionDigestPromptHeader))
	require.True(t, IsControlPlanePromptText(SupervisionPromptTag))
}
