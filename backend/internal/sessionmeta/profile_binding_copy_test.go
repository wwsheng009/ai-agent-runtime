package sessionmeta

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCopyProfileBinding_SnapshotsCanonicalAndLegacyKeys 覆盖 FR-9/D8 的快照语义：
// canonical 键与其 legacy 别名都要复制，且 src 未声明的键不得凭空出现。
func TestCopyProfileBinding_SnapshotsCanonicalAndLegacyKeys(t *testing.T) {
	src := map[string]interface{}{}
	Set(src, ProfileRef, "/profiles/coding", LegacyAPIProfileReference)
	Set(src, ProfileName, "coding")
	Set(src, ProfileAgent, "explore")

	var dst map[string]interface{}
	changed := CopyProfileBinding(&dst, src)
	require.True(t, changed)
	require.Equal(t, "/profiles/coding", String(dst, ProfileRef))
	require.Equal(t, "/profiles/coding", dst[LegacyAPIProfileReference])
	require.Equal(t, "coding", String(dst, ProfileName))
	require.Equal(t, "explore", String(dst, ProfileAgent))
	_, hasRoot := dst[ProfileRoot]
	require.False(t, hasRoot, "src 未声明的键不得凭空出现")
}

// TestCopyProfileBinding_NoSourceBindingIsNoOp 守护 NFR-1：父会话未绑定 profile 时，
// 复制必须完全不写键，未引入 profile 的路径行为逐字节不变。
func TestCopyProfileBinding_NoSourceBindingIsNoOp(t *testing.T) {
	dst := map[string]interface{}{"keep": "me"}
	changed := CopyProfileBinding(&dst, map[string]interface{}{Model: "test-model"})
	require.False(t, changed)
	require.Equal(t, map[string]interface{}{"keep": "me"}, dst)
}

func TestCopyProfileBinding_NilInputsAreSafe(t *testing.T) {
	require.False(t, CopyProfileBinding(nil, nil))

	dst := map[string]interface{}{}
	require.False(t, CopyProfileBinding(&dst, nil))
	require.Empty(t, dst)

	src := map[string]interface{}{}
	Set(src, ProfileRef, "/p", LegacyAPIProfileReference)
	require.False(t, CopyProfileBinding(nil, src))
}

func TestCopyProfileBinding_Idempotent(t *testing.T) {
	src := map[string]interface{}{}
	Set(src, ProfileRef, "/p", LegacyAPIProfileReference)

	dst := map[string]interface{}{}
	require.True(t, CopyProfileBinding(&dst, src))
	require.False(t, CopyProfileBinding(&dst, src), "同值重复复制不得再报变更")
	require.Equal(t, "/p", String(dst, ProfileRef))
}
