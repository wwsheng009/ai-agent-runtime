package skill

import (
	"testing"

	"github.com/stretchr/testify/require"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
)

// 锁定非标准 skill 文件的错误分类：只有「文件本身不规范」这一类错误
// （CONFIG_INVALID / INVALID_MANIFEST / VALIDATION_FAILED，含任意层包装）
// 才降级为告警跳过；IO/不存在等错误仍按真实错误上报。
func TestIsNonStandardSkillFileError(t *testing.T) {
	cause := runtimeerrors.New(runtimeerrors.ErrConfigInvalid, "missing YAML frontmatter")
	// 与 Loader.DiscoverFile 相同的包装链：SKILL_LOAD_FAILED 在外，CONFIG_INVALID 在内。
	wrapped := runtimeerrors.Wrap(runtimeerrors.ErrSkillLoadFailed, "failed to discover skill file: x", cause)
	require.True(t, isNonStandardSkillFileError(wrapped))

	plainIO := runtimeerrors.Wrap(runtimeerrors.ErrSkillLoadFailed,
		"failed to discover skill file: x",
		runtimeerrors.New(runtimeerrors.ErrConfigNotFound, "read failed"))
	require.False(t, isNonStandardSkillFileError(plainIO))
	require.False(t, isNonStandardSkillFileError(nil))
}
