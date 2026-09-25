//go:build win7compat

package chat

// win7compatBuild 标记当前测试二进制由 Win7 兼容构建（Go 1.21.4 + go.win7.mod）
// 编译：该依赖图把 github.com/ncruces/go-sqlite3 钉在 v0.22.0（主线 v0.32.0），
// 部分 WAL / VACUUM INTO 行为与主线依赖不同（实测：长读期间写失败计数非零、
// 快照的 VACUUM INTO 报 "cannot VACUUM - SQL statements in progress"）。
//
// 依赖这些行为的测试用本常量显式跳过，让 win7 测试阶段只跑与依赖版本无关的断言。
const win7compatBuild = true
