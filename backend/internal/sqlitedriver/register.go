//go:build !win7compat

package sqlitedriver

// 主线构建：ncruces/go-sqlite3 v0.33+ 已把 SQLite 引擎改成纯 Go 翻译实现
// （go-sqlite3-wasm/vN），不再需要 `embed` 包；该包自 v0.35 起已废弃并且
// import 它会在启动时打印噪音。win7compat 构建仍需要它，见 register_win7.go。
import (
	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/vfs/memdb"
)
