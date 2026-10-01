package knowledge

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// Phase 5 交付 1（变更源 2 的第二半）：已索引文件的 stat 校正。
//
// git 校正源（change_git.go）覆盖"git 能看见的变化"，但有一类变化它看不见：
// 工作树相对 HEAD **干净**、内容却与索引记录不一致——最典型的是
// `git checkout -- <file>`（把 agent 改过的文件还原回 HEAD）：HEAD 没动、
// status 干净，而索引里存的是还原前的内容。这类"还原"只有把索引记录与磁盘
// 现状逐文件比对才能发现。
//
// 本文件做的是索引器自身的廉价预筛口径（size + mtime_ns，见 stageFile）：
// 只 stat、不读内容、不遍历目录树（路径来自 store 的已索引文件清单），
// 因此对大型仓库也是 O(已索引文件数) 次 stat。真正的"内容是否相同"由索引器
// 在 stageFile 里用 sha256 复核（mtime 变了但内容没变 → 只刷新元数据）。
//
// 非 git 工作区同样受益：外部编辑器的写盘、脚本写盘都会被 stat 差异发现。

// StatScanReport 是一次 stat 校正的观测结果。
type StatScanReport struct {
	// Scanned 是参与比对（store 中未软删除）的文件数。
	Scanned int
	// Changed 是 size 或 mtime 与索引记录不一致的路径（工作区相对）。
	Changed []string
	// Missing 是索引里有记录、磁盘上已不存在的路径（交给索引器软删除）。
	Missing []string
	// Errors 是 stat 失败（非 NotExist，例如权限）的路径数；不计入变化。
	Errors int
	// Fresh 是"索引记录与磁盘一致（size+mtime 命中）"的路径集合。
	//
	// 它供校正源过滤 git 侧的假阳性：一个持续 modified（相对 HEAD 未提交）的文件
	// 在 git 眼里是"变化"，但索引可能早已跟上——只有当索引真的落后（不在 Fresh
	// 里）才该入队。未跟踪的新文件天然不在 Fresh 里（store 没有记录）✓。
	Fresh map[string]bool
}

// ChangedPaths 返回本次扫描发现的所有需要重索引的路径（排序去重）。
func (r StatScanReport) ChangedPaths() []string {
	if len(r.Changed) == 0 && len(r.Missing) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(r.Changed)+len(r.Missing))
	out := make([]string, 0, len(r.Changed)+len(r.Missing))
	for _, group := range [][]string{r.Changed, r.Missing} {
		for _, p := range group {
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// ScanIndexedFileStats 把 store 的已索引文件与磁盘现状比对（size + mtime_ns）。
//
// 契约：只读、尽力而为——单个文件 stat 失败只计数不中断；store 读失败向上返回
// （调用方按"校正失败不阻断会话"处理）。
func ScanIndexedFileStats(ctx context.Context, store Store, workspace string, workspaceID string) (StatScanReport, error) {
	var report StatScanReport
	if store == nil || workspace == "" || workspaceID == "" {
		return report, nil
	}
	records, err := store.ListActiveFiles(ctx, workspaceID)
	if err != nil {
		return report, err
	}
	root := workspace
	if abs, err := filepath.Abs(workspace); err == nil {
		root = abs
	}
	for _, record := range records {
		rel := filepath.ToSlash(record.Path)
		if rel == "" {
			continue
		}
		report.Scanned++
		abs := filepath.Join(root, filepath.FromSlash(rel))
		info, err := os.Stat(abs)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				report.Missing = append(report.Missing, rel)
				continue
			}
			report.Errors++
			continue
		}
		if info.Size() != record.Size || info.ModTime().UnixNano() != record.MTimeNS {
			report.Changed = append(report.Changed, rel)
			continue
		}
		if report.Fresh == nil {
			report.Fresh = make(map[string]bool, len(records))
		}
		report.Fresh[rel] = true
	}
	sort.Strings(report.Changed)
	sort.Strings(report.Missing)
	return report, nil
}
