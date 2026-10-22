package mesh

import (
	"os"
	"sync"
	"time"
)

// fileStatKey 是判定「文件内容是否可能变化」的最小指纹。
//
// 为什么用 mtime+size 而不是内容哈希：每次 BuildView 都会扫描全部节点与
// binding 文件，内容哈希需要先读完整文件，等于没有省掉 I/O。mtime+size 只需
// 一次 Stat；而 mesh 的所有写入都走 writeFileAtomic（临时文件 + rename），
// rename 必然更新 mtime，因此不会漏判（NTFS 的 mtime 精度为 100ns）。
//
// 生产实测（2026-10-22）：6.7 小时内 BuildView 累计分配 49.6 GB，其中
// ListNodeFiles 32.1 GB（os.ReadFile + json.Unmarshal 每个节点文件）、
// buildNodeView → LoadBinding 14.3 GB。三个独立循环（peer sync 每 5s、
// schedulehost 让位判定、attention 转发）各自全量重读，且文件内容几乎不变。
type fileStatKey struct {
	modTime time.Time
	size    int64
}

type nodeFileCacheEntry struct {
	key  fileStatKey
	file NodeFile
}

type bindingCacheEntry struct {
	key     fileStatKey
	binding SessionBinding
	ok      bool
}

var (
	// nodeFileCache / bindingFileCache 以绝对路径为键。条目数量由 mesh 节点数
	// 与会话数决定（都是小规模目录），解析后的结构体很小，不做容量淘汰。
	nodeFileCache    sync.Map // string -> nodeFileCacheEntry
	bindingFileCache sync.Map // string -> bindingCacheEntry
)

// statKeyFor 返回文件当前 stat；失败时 ok=false，调用方应走未缓存的原始路径
// 以保留原有的错误语义（文件不存在 / 不可读）。
func statKeyFor(path string) (fileStatKey, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return fileStatKey{}, false
	}
	return fileStatKey{modTime: info.ModTime(), size: info.Size()}, true
}
