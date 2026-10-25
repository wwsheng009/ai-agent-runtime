package mesh

import (
	"strings"
	"sync"
	"time"
)

// liveness_cache.go —— 节点 PID 判活的进程内缓存。
//
// BuildView 会在每次扫描时为每个节点调用 processAlive；生产目录有近两千个
// 历史节点时，这会变成每 5 秒上千次 OpenProcess。进程一旦退出，以其
// NodeID 命名的档案不会再“复活”（重启会生成新的 NodeID），因此 dead 结果
// 可以缓存较久；alive 结果只缓存很短时间，保证巡检能及时发现退出。
const (
	nodeAliveCacheTTL = 2 * time.Second
	nodeDeadCacheTTL  = 10 * time.Minute
)

type nodeLivenessEntry struct {
	pid       int
	alive     bool
	checkedAt time.Time
}

var (
	nodeLivenessCache sync.Map // nodeID -> nodeLivenessEntry
	// processAliveProbe 是测试注入点；生产保持指向 processAlive。
	processAliveProbe = processAlive
)

func nodeProcessAlive(nodeID string, pid int) bool {
	if pid <= 0 {
		return false
	}
	key := strings.TrimSpace(nodeID)
	if key == "" {
		// 没有稳定 NodeID 时不做缓存，避免 PID 复用把新进程误判为旧进程。
		return processAliveProbe(pid)
	}
	if cached, ok := nodeLivenessCache.Load(key); ok {
		entry := cached.(nodeLivenessEntry)
		if entry.pid == pid {
			ttl := nodeAliveCacheTTL
			if !entry.alive {
				ttl = nodeDeadCacheTTL
			}
			if time.Since(entry.checkedAt) < ttl {
				return entry.alive
			}
		}
	}
	alive := processAliveProbe(pid)
	nodeLivenessCache.Store(key, nodeLivenessEntry{
		pid:       pid,
		alive:     alive,
		checkedAt: time.Now(),
	})
	return alive
}

func invalidateNodeProcessLiveness(nodeID string) {
	key := strings.TrimSpace(nodeID)
	if key != "" {
		nodeLivenessCache.Delete(key)
	}
}
