package supervision

import "github.com/wwsheng009/ai-agent-runtime/internal/pkg/uniqid"

// Identifiers in this package are primary keys, so they must stay unique even
// when the host clock is coarse. Go reads time.Now() from the operating system
// clock, and on some hosts (notably Windows guests) many consecutive calls
// return the exact same nanosecond value. Identifiers built from UnixNano
// alone then collide, and an INSERT ... OR IGNORE drops the row silently: a
// scheduled wake (including a blocking approval) disappears without any error,
// so the parent is never woken.
//
// uniqid（internal/pkg/uniqid）在时间戳后追加进程内序号与进程随机后缀，
// 使 id 在同一时钟 tick 内、以及共享同一数据库的多个进程之间都保持唯一。
func uniqueSupervisionID(prefix string) string {
	return uniqid.New(prefix)
}
