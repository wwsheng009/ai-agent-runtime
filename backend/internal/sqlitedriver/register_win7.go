//go:build win7compat

package sqlitedriver

// Windows 7 兼容构建：工具链被钉在 Go 1.21.4 + go.win7.mod（ncruces/go-sqlite3
// v0.22.x），该版本的 SQLite 引擎是 Wasm，必须由 `embed` 包提供二进制并在
// 进程启动时初始化（v0.33+ 才移除该步骤）。这里的 import 不能删，否则 win7
// 构建会在首次打开数据库时报 “sqlite3: not initialized”。
import (
	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	_ "github.com/ncruces/go-sqlite3/vfs/memdb"
)
