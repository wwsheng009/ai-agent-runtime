//go:build win7compat

package auth

import "fmt"

// defaultOpenBrowser 在 Win7 兼容构建下不自动打开浏览器：
// 始终打印授权 URL 供用户手动复制，避免依赖 Win7 上不可控的 shell 行为。
func defaultOpenBrowser(target string) error {
	return fmt.Errorf("%w：当前构建不自动打开浏览器，请复制链接手动访问", ErrUnsupported)
}
