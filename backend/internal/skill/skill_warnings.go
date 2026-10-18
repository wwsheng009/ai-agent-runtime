package skill

import (
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
)

// isNonStandardSkillFileError 判定错误属于「skill 文件本身不规范」这一类：
// 缺 YAML frontmatter、frontmatter 无法解析、清单字段校验失败等。这类错误
// 在批量扫描中必须降级为告警并跳过单个文件，不能中断整次加载或能力面初始化
// （2026-10-18 deskctl 实例：skills/desktop-* 缺 frontmatter 导致
// “能力面初始化失败: [SKILL_LOAD_FAILED] failed to discover skill file”）。
//
// runtimeerrors.Is 只看错误链最外层的 code（SKILL_LOAD_FAILED），这里必须
// 逐层 Unwrap 才能命中内层的 CONFIG_INVALID / INVALID_MANIFEST / VALIDATION_FAILED。
func isNonStandardSkillFileError(err error) bool {
	codes := [...]runtimeerrors.ErrorCode{
		runtimeerrors.ErrConfigInvalid,
		runtimeerrors.ErrInvalidManifest,
		runtimeerrors.ErrValidationFailed,
	}
	for err != nil {
		for _, code := range codes {
			if runtimeerrors.Is(err, code) {
				return true
			}
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			break
		}
		err = unwrapper.Unwrap()
	}
	return false
}
