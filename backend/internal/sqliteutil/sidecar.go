package sqliteutil

import (
	"encoding/binary"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// SQLite 附属文件与 WAL 帧布局常量（见 SQLite 源码 wal.c / pager.c）。
const (
	// walHeaderSize 是 -wal 文件头长度；每帧固定 24 字节帧头 + 一个页大小的帧体。
	walHeaderSize   = 32
	walFrameHdrSize = 24
	// walIndexHdrSize 是 -shm 文件首部 WalIndexHdr 的长度。
	walIndexHdrSize = 48
	// SQLite 合法页大小区间。
	sqliteMinPageSize = 512
	sqliteMaxPageSize = 65536
)

// FilePathFromDSN 从文件型 SQLite DSN 解析出数据库主文件路径。
//
// 内存库、空 DSN，以及无法在 Windows 上安全还原的 authority 形式
// （file://host/path）返回 ok=false，调用方应跳过一切基于路径的处理。
func FilePathFromDSN(dsn string) (string, bool) {
	raw := strings.TrimSpace(dsn)
	if raw == "" || strings.EqualFold(raw, ":memory:") {
		return "", false
	}
	pathPart := raw
	if len(raw) >= 5 && strings.EqualFold(raw[:5], "file:") {
		pathPart = raw[5:]
		query := ""
		if idx := strings.IndexByte(pathPart, '?'); idx >= 0 {
			query = pathPart[idx+1:]
			pathPart = pathPart[:idx]
		}
		if strings.Contains(strings.ToLower(query), "mode=memory") {
			return "", false
		}
		// authority 形式（file://host/path）的语义依赖主机名，不做猜测。
		if strings.HasPrefix(pathPart, "//") {
			return "", false
		}
	} else if idx := strings.IndexByte(pathPart, '?'); idx >= 0 {
		pathPart = pathPart[:idx]
	}
	pathPart = strings.TrimSpace(pathPart)
	if pathPart == "" || strings.EqualFold(pathPart, ":memory:") {
		return "", false
	}
	if decoded, err := url.PathUnescape(pathPart); err == nil {
		pathPart = decoded
	}
	return filepath.FromSlash(pathPart), true
}

// walIndexHeader 是 -shm 首部 WalIndexHdr 的只读视图。
type walIndexHeader struct {
	isInit  bool
	pageSz  int
	mxFrame uint32
}

// readWALIndexHeader 读取 -shm 首部。ok=false 表示文件不可读，或内容无法解释为
// 合法的 wal-index 头（例如平台字节序不同导致 szPage 落在非法区间）；调用方必须
// 保守跳过，而不是据此删除文件。
func readWALIndexHeader(shmPath string) (walIndexHeader, bool) {
	file, err := os.Open(shmPath)
	if err != nil {
		return walIndexHeader{}, false
	}
	defer func() { _ = file.Close() }()
	var buf [walIndexHdrSize]byte
	if _, err := io.ReadFull(file, buf[:]); err != nil {
		return walIndexHeader{}, false
	}
	// WalIndexHdr 由 walIndexWriteHdr 以本机字节序直接落盘，小端平台按
	// LittleEndian 解读；字节序不同的平台上 szPage 会落到非法区间，被下面的
	// 合法性校验判为不可用。
	pageSz := int(binary.LittleEndian.Uint16(buf[14:16]))
	if pageSz == 1 {
		// SQLite 用 1 表示 64KiB（u16 装不下 65536）。
		pageSz = sqliteMaxPageSize
	}
	if pageSz < sqliteMinPageSize || pageSz > sqliteMaxPageSize {
		return walIndexHeader{}, false
	}
	return walIndexHeader{
		isInit:  buf[12] == 1,
		pageSz:  pageSz,
		mxFrame: binary.LittleEndian.Uint32(buf[16:20]),
	}, true
}

// ReconcileOrphanedSidecars 在打开文件型 SQLite 之前校验 -wal/-shm 附属文件是否
// 自洽，并在 -shm（wal-index）属于上一代残留时删除它。
//
// 为什么必须做：-shm 只是索引缓存，SQLite 仅在“进程崩溃”假设下信任它——崩溃时
// -shm 与 -wal 必然同代。一旦主库或 -wal 被带外替换/截断（备份还原、手工替换 DB
// 文件、WAL 被截断），残留的 -shm 仍带着旧一代的 mxFrame/salt，SQLite 会直接采信
// 这份陈旧索引，按并不存在的帧数定位页面，首次写入即撕裂 B 树。删除陈旧 -shm 不
// 丢数据：wal-index 完全可由 -wal 重建，真正承载数据的是主库与 -wal。
//
// 判定规则（宁可漏报，不可误删）：见 shmLooksStale。关键约束是判定必须能在
// 多进程并发下保持正确——判定与删除之间不可能与 SQLite 的 checkpoint 原子化，
// 因此这里只采信“健康并发会话不可能出现”的形态，并用 seqlock 式二次读取排除
// “读到一半被 checkpoint 截断 WAL”的竞态。
//
// 删除是 best-effort：Windows 上仍被其他进程映射时删除会失败，而这个失败本身
// 就说明该 sidecar 属于活跃会话，因此绝不能让它导致打开失败。
//
// 返回 removed 报告是否确实删除了文件。
func ReconcileOrphanedSidecars(dbPath string) (bool, error) {
	path := strings.TrimSpace(dbPath)
	if path == "" {
		return false, nil
	}
	shmPath := path + "-shm"
	info, err := os.Stat(shmPath)
	if err != nil || info.IsDir() || info.Size() == 0 {
		return false, nil
	}
	if !shmLooksStale(path, shmPath) {
		return false, nil
	}
	if err := os.Remove(shmPath); err != nil {
		// 另一个进程仍持有该 sidecar：交还给它，不要打断一个活跃会话。
		return false, nil
	}
	return true, nil
}

// shmLooksStale 报告 -shm 是否可证明属于上一代数据库（而不是某个活跃会话）。
//
// 只在两种形态下返回 true：
//
//   - -wal 不存在或没有有效 WAL 头，而 -shm 声称 mxFrame>0。没有帧可恢复时，
//     活跃会话的 mxFrame 必为 0（checkpoint 归零 WAL 的同时也会归零 mxFrame）。
//   - -shm 声称的帧数在 -wal 中物理放不下。帧总是先落盘、再更新索引，因此
//     健康状态下 walLen >= 32 + mxFrame*(24+pageSize)。
//
// 判定成立后再做一次 seqlock 式复核：活跃会话在两次读取之间一定会推进或归零
// mxFrame，因此“首部稳定不变”是“没有并发会话正在使用它”的有力证据。
func shmLooksStale(dbPath, shmPath string) bool {
	first, ok := readWALIndexHeader(shmPath)
	if !ok || !first.isInit || first.mxFrame == 0 {
		return false
	}
	walSize, walPresent := sidecarSize(dbPath + "-wal")
	if !walPresent || walSize < int64(walHeaderSize) {
		return rereadSHMHeaderMatches(shmPath, first)
	}
	required := int64(walHeaderSize) +
		int64(first.mxFrame)*int64(walFrameHdrSize+first.pageSz)
	if walSize >= required {
		return false
	}
	return rereadSHMHeaderMatches(shmPath, first)
}

// rereadSHMHeaderMatches 复核 -shm 首部与首次读取是否完全一致。
func rereadSHMHeaderMatches(shmPath string, first walIndexHeader) bool {
	second, ok := readWALIndexHeader(shmPath)
	if !ok {
		return false
	}
	return second.isInit == first.isInit &&
		second.pageSz == first.pageSz &&
		second.mxFrame == first.mxFrame
}

func sidecarSize(path string) (int64, bool) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return 0, false
	}
	return info.Size(), true
}

// ReconcileOrphanedSidecarsDSN 是 ReconcileOrphanedSidecars 的 DSN 形式；内存库或
// 无法解析出路径的 DSN 直接返回 (false, nil)。
func ReconcileOrphanedSidecarsDSN(dsn string) (bool, error) {
	path, ok := FilePathFromDSN(dsn)
	if !ok {
		return false, nil
	}
	return ReconcileOrphanedSidecars(path)
}
