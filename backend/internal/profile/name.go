package profile

import "fmt"

// ValidateProfileName 校验 profile 名称：它同时是目录名与 profile.name，因此
// 只允许字母/数字/-/_/.，且不能是 "." / ".."。
//
// 单一权威实现：CLI `profile create`（cmd/aicli/commands/profile_create.go 的
// validateProfileCreateName 委托到这里）与 profiles API 的 create/duplicate/
// rename/move 共用同一份规则，避免出现"API 能建、CLI 建不了"的第二套方言。
func ValidateProfileName(name string) error {
	if name == "" {
		return fmt.Errorf("profile 名称不能为空")
	}
	if len(name) > 64 {
		return fmt.Errorf("profile 名称过长（最多 64 字符）：%s", name)
	}
	if name == "." || name == ".." {
		return fmt.Errorf("profile 名称非法：%s", name)
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			return fmt.Errorf("profile 名称只允许字母/数字/-/_/.：%s", name)
		}
	}
	return nil
}
