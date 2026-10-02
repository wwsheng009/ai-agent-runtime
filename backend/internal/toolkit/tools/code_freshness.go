package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// codeFreshnessMaxReads 是单次查询内允许的"读盘算哈希"次数上限。
//
// 判定顺序把大部分文件挡在 stat 之外（mtime 一致即判新鲜），只有 mtime 变过
// 的文件才需要读盘。git checkout 这类操作会把工作区所有文件的 mtime 一起刷新
// 而内容不变——没有上限时，一次跨 200 个文件的引用查询会退化成读 200 个文件。
// 超过预算的文件**计入未校验**而不是默认新鲜：报告口径必须如实。
const codeFreshnessMaxReads = 32

// FileStamp 是索引侧记录的文件指纹（files 表的 mtime/size/content_hash 三元组）。
//
// 关系类查询（code_references / code_callers / code_navigate refs）的正确性
// 依赖"本次涉及的每个文件都已被索引到当前内容"：只要有一个文件在索引之后
// 被改写，它的引用集合就可能漏掉磁盘上**已经存在**的调用点，而这类漏报是
// 静默的——返回的那几条引用看起来完全正常。
//
// 定义类查询（code_inspect / code_navigate definition|members）依赖的是同一
// 个前提的另一面：符号的行号范围来自索引快照，文件改写后这个范围可能指向
// 磁盘上完全不同的代码，返回的正文看起来合理却是错的。
//
// 全局 tier（CodeTierForSnapshot）两者都挡不住：staleness_seconds 取自
// "最近一次成功写事务"，索引持续给别的文件写入时它会一直停在新鲜档，而本
// 次查询的文件可能几小时没被重新索引过。ADR-0004 §4.1/§5 D1 要求不得返回
// 可能静默错误的结果，因此守卫必须落到文件粒度。
type FileStamp struct {
	// MTimeNS / Size 是廉价预筛字段（与 knowledge.FileRecord 同源）。
	MTimeNS int64
	Size    int64
	// ContentHash 是判定依据：hex(sha256(文件字节))，与索引器
	// knowledge/indexer.go 的 content_hash 口径完全一致。
	ContentHash string
}

// fileVerdict 是单个文件的新鲜度判定结果。
type fileVerdict int

const (
	// verdictFresh：索引视图与磁盘一致。
	verdictFresh fileVerdict = iota
	// verdictStale：文件已被改写、删除或不可读，索引视图不可信。
	verdictStale
	// verdictUnknown：无法判定（无指纹 / 无根目录 / 超出读盘预算）。
	// 这是"不可信但没证据"，不得当作新鲜。
	verdictUnknown
)

// freshnessBudget 是一次查询内共享的读盘预算（stat 不计费）。
type freshnessBudget struct{ readsLeft int }

func newFreshnessBudget() *freshnessBudget {
	return &freshnessBudget{readsLeft: codeFreshnessMaxReads}
}

// verdictOf 判定一个文件的新鲜度，并返回它的 workspace 相对路径
// （file_id → path 的映射缺失时返回空串）。
//
// 判定按代价从低到高，尽量不读盘：
//  1. 无指纹 / 无根目录 → verdictUnknown（不 stat，避免把相对路径当绝对路径误判）；
//  2. size 不符 → verdictStale（大小都变了，哈希必然不同，没必要读）；
//  3. mtime 与记录一致 → verdictFresh（未改动的常见路径，零 IO）；
//  4. 预算用尽 → verdictUnknown（不猜）；
//  5. 读盘算 sha256 —— checkout / touch 这类内容未变的改写因此不会被误判为陈旧
//     （与索引器 indexer.go 的 "mtime 变了但内容没变" 判定同构）。
func verdictOf(handle *CodeIndexHandle, fileID string, budget *freshnessBudget) (fileVerdict, string) {
	if handle == nil || strings.TrimSpace(fileID) == "" {
		return verdictUnknown, ""
	}
	stamp, ok := handle.FileStamps[fileID]
	if !ok {
		return verdictUnknown, ""
	}
	rel := handle.PathForFile(fileID)
	root := strings.TrimSpace(handle.Root)
	if rel == "" || root == "" {
		// 路径不可锚定：判陈旧是过度反应（生产句柄不会走到这里），判新鲜是撒谎。
		return verdictUnknown, ""
	}
	abs := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Stat(abs)
	if err != nil {
		// 文件已从磁盘消失或不可读：索引里的记录是残留，视为陈旧。
		return verdictStale, rel
	}
	if info.Size() != stamp.Size {
		return verdictStale, rel
	}
	if stamp.MTimeNS != 0 && info.ModTime().UnixNano() == stamp.MTimeNS {
		return verdictFresh, rel
	}
	if budget == nil || budget.readsLeft <= 0 {
		return verdictUnknown, rel
	}
	budget.readsLeft--
	content, err := os.ReadFile(abs)
	if err != nil {
		return verdictStale, rel
	}
	sum := sha256.Sum256(content)
	if hex.EncodeToString(sum[:]) != stamp.ContentHash {
		return verdictStale, rel
	}
	return verdictFresh, rel
}

// fileFresh 是单文件口径（每次调用自带读盘预算）。known=false 表示无法判定。
func fileFresh(handle *CodeIndexHandle, fileID string) (known, fresh bool) {
	verdict, _ := verdictOf(handle, fileID, newFreshnessBudget())
	switch verdict {
	case verdictFresh:
		return true, true
	case verdictStale:
		return true, false
	default:
		return false, false
	}
}

// staleRelFiles 判定一批文件，返回其中索引视图已落后于磁盘的路径（去重），
// 以及**无法判定**的文件数。
//
// 调用方拿到 unverified > 0 时必须在解释里说明"这些文件未参与校验"——
// 把它当成已校验就是本次修复要消灭的那类静默错误。
func staleRelFiles(handle *CodeIndexHandle, fileIDs []string) (stale []string, unverified int) {
	if handle == nil {
		return nil, 0
	}
	budget := newFreshnessBudget()
	seen := make(map[string]bool, len(fileIDs))
	for _, fileID := range fileIDs {
		id := strings.TrimSpace(fileID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		verdict, rel := verdictOf(handle, id, budget)
		switch verdict {
		case verdictStale:
			if rel == "" {
				rel = id
			}
			stale = append(stale, rel)
		case verdictUnknown:
			unverified++
		}
	}
	return stale, unverified
}

// freshnessNote 构造结果路径上关于新鲜度的说明（ADR-0004 §4.2 的可观测性补齐）。
//
// 旧实现只在降级路径解释陈旧度，索引命中路径只说"命中 N 条"——消费方无从
// 判断这条结果是否做过新鲜度校验，于是把可能漏报的结果当成了完整集合。
// unverified 是本次查询中无法判定的文件数（无指纹 / 超出读盘预算）。
func freshnessNote(handle *CodeIndexHandle, unverified int) string {
	if handle == nil {
		return ""
	}
	if len(handle.FileStamps) == 0 {
		return "本句柄未提供文件指纹，未做文件级新鲜度校验（结果可能漏掉索引后新增的调用点，关键结论请用 grep 复核）。"
	}
	note := fmt.Sprintf("文件级新鲜度：已校验，一致；全局快照陈旧度约 %d 秒。", maxInt64(handle.StalenessSeconds, 0))
	if unverified > 0 {
		note += fmt.Sprintf("另有 %d 个文件无索引指纹或超出读盘预算，未参与校验（其结果可能陈旧）。", unverified)
	}
	return note
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// truncateRelFiles 折叠过长的文件列表，保证 explanation 可读。
func truncateRelFiles(paths []string, max int) string {
	if max <= 0 || len(paths) <= max {
		return strings.Join(paths, "、")
	}
	return strings.Join(paths[:max], "、") + fmt.Sprintf(" 等 %d 个", len(paths))
}