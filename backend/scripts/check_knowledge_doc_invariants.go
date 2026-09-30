//go:build ignore

// check_knowledge_doc_invariants.go — ADR-0007 §4.4 文档不变量（I1–I5）机械检查。
//
// 只依赖标准库；默认从 backend/ 目录运行（-docs 相对 cwd 解析）：
//
//	go run scripts/check_knowledge_doc_invariants.go
//	go run scripts/check_knowledge_doc_invariants.go -docs ../docs/knowledge_Layer
//
// 退出码：0 = 全部 PASS；1 = 存在 FAIL；2 = 运行错误（如目录不存在）。
//
// 语义边界（与 ADR-0007 §4.4/§8 对齐）：
//   - I1 优先比较 `02` 的【v1 core】分组；`02` §8 尚未三分组时回退为
//     "整个总览块 vs 02 DDL 集合"，并在输出中显式标注回退。
//   - I2 只把"DDL 语句"（行首 CREATE TABLE / CREATE VIRTUAL TABLE）计为违规；
//     散文/行内 `CREATE TABLE` 提及仅作为 INFO 输出。adr/* 按 ADR-0007 §8
//     "仅允许出现在引用块中"处理：围栏代码块与引用行（>）内的语句豁免。
//   - I3 扫描范围 = 规范文档（01–06、README、GLOSSARY、adr/*、supplement/*），
//     不含 CHANGELOG / reports / archive（历史与报告含已废弃名字）。
//     漂移规则 = {X, X_edges} / {X, X_versions}（复数不敏感）+ 显式 allowlist。
//   - I4 期望三分组【v1 core】/【extension】/【deferred（或 已推迟）】；
//     其他分组名仍参与"属于且仅属于一个分组"的交集/并集检查。
//   - I5 把 `04` §4.3 的声明视为 "v1 上限（≤ N）"，实际数量取 `02` 的 v1 core
//     （无分组时回退为 02 的 DDL 集合，含虚拟表；两种情况都输出构成明细）。
package main

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ---------- 数据模型 ----------

type doc struct {
	rel   string // 相对 docs 根目录，slash 分隔
	lines []string
}

type docSet struct {
	root  string
	files map[string]*doc
	rels  []string // 排序后的相对路径
}

type group struct {
	raw   string // 文档中的原始分组名
	canon string // 规范化分组种类（v1 core / extension / deferred / ""）
	names []string
}

type overview struct {
	found   bool
	block   []string
	groups  []group
	orphans []string // 出现在任何分组头之前的名字
}

type result struct {
	id         string
	title      string
	pass       bool
	violations []string
	notes      []string
}

func main() {
	docsFlag := flag.String("docs", "../docs/knowledge_Layer", "docs/knowledge_Layer 目录（相对 cwd 解析）")
	flag.Parse()

	root, err := filepath.Abs(*docsFlag)
	if err != nil {
		fatal(err)
	}
	ds, err := loadDocs(root)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("docs: %s（%d 个 markdown 文件）\n\n", root, len(ds.rels))

	checks := []result{checkI1(ds), checkI2(ds), checkI3(ds), checkI4(ds), checkI5(ds)}
	failed := 0
	for _, r := range checks {
		status := "PASS"
		if !r.pass {
			status = "FAIL"
			failed++
		}
		fmt.Printf("[%s] %s %s\n", status, r.id, r.title)
		for _, n := range r.notes {
			fmt.Printf("       · %s\n", n)
		}
		for _, v := range r.violations {
			fmt.Printf("       违规: %s\n", v)
		}
		fmt.Println()
	}
	fmt.Printf("summary: %d/%d passed, %d failed\n", len(checks)-failed, len(checks), failed)
	if failed > 0 {
		os.Exit(1)
	}
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "check_knowledge_doc_invariants: %v\n", err)
	os.Exit(2)
}

// ---------- 文档加载 ----------

func loadDocs(root string) (*docSet, error) {
	ds := &docSet{root: root, files: map[string]*doc{}}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		text := strings.ReplaceAll(string(raw), "\r\n", "\n")
		text = strings.ReplaceAll(text, "\r", "\n")
		rel = filepath.ToSlash(rel)
		ds.files[rel] = &doc{rel: rel, lines: strings.Split(text, "\n")}
		ds.rels = append(ds.rels, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(ds.rels)
	return ds, nil
}

// top 返回顶层（非子目录）且文件名以 prefix 开头的文档，如 "02"、"04"。
func (ds *docSet) top(prefix string) *doc {
	for _, rel := range ds.rels {
		if strings.Contains(rel, "/") {
			continue
		}
		if strings.HasPrefix(rel, prefix) {
			return ds.files[rel]
		}
	}
	return nil
}

// ---------- 通用解析工具 ----------

var (
	// 行首（允许引用行前缀 ">"）的 DDL 语句；group 1 = VIRTUAL 标记，group 2 = 表名。
	ddlNameRe = regexp.MustCompile("(?i)^\\s*(?:>\\s*)*CREATE\\s+(VIRTUAL\\s+)?TABLE\\s+(?:IF\\s+NOT\\s+EXISTS\\s+)?([A-Za-z_][A-Za-z0-9_]*)")
	// 任意位置的 CREATE TABLE / CREATE VIRTUAL TABLE 字符串（用于区分"提及"与"DDL 语句"）。
	anyDDLRe = regexp.MustCompile("(?i)CREATE\\s+(VIRTUAL\\s+)?TABLE\\b")
	groupRe  = regexp.MustCompile("【([^】]+)】")
	identRe  = regexp.MustCompile("^[A-Za-z_][A-Za-z0-9_]*$")
)

type ddlHit struct {
	file    string
	line    int
	name    string
	virtual bool
}

func scanDDL(d *doc) []ddlHit {
	if d == nil {
		return nil
	}
	var hits []ddlHit
	for i, ln := range d.lines {
		m := ddlNameRe.FindStringSubmatch(ln)
		if m == nil {
			continue
		}
		hits = append(hits, ddlHit{file: d.rel, line: i + 1, name: m[2], virtual: m[1] != ""})
	}
	return hits
}

// fenceFlags 标记每一行是否位于围栏代码块（``` / ~~~）内。
func fenceFlags(lines []string) []bool {
	flags := make([]bool, len(lines))
	in := false
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			in = !in
			flags[i] = true
			continue
		}
		flags[i] = in
	}
	return flags
}

func isBlockquote(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), ">")
}

// ---------- 02 §8 总览块解析 ----------

func parseOverview(d *doc) overview {
	var ov overview
	if d == nil {
		return ov
	}
	start := -1
	for i, ln := range d.lines {
		if strings.Contains(ln, "数据模型总览") {
			start = i
			break
		}
	}
	if start < 0 {
		return ov
	}
	i := start + 1
	for ; i < len(d.lines); i++ {
		t := strings.TrimSpace(d.lines[i])
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			break
		}
	}
	if i >= len(d.lines) {
		return ov
	}
	marker := "```"
	if strings.HasPrefix(strings.TrimSpace(d.lines[i]), "~~~") {
		marker = "~~~"
	}
	i++
	for ; i < len(d.lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(d.lines[i]), marker) {
			ov.found = true
			break
		}
		ov.block = append(ov.block, d.lines[i])
	}
	if !ov.found {
		return ov
	}
	cur := -1
	for _, raw := range ov.block {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if m := groupRe.FindStringSubmatch(line); m != nil {
			ov.groups = append(ov.groups, group{raw: strings.TrimSpace(m[1]), canon: canonGroup(m[1])})
			cur = len(ov.groups) - 1
			continue
		}
		for _, name := range splitNames(line) {
			if cur >= 0 {
				ov.groups[cur].names = append(ov.groups[cur].names, name)
			} else {
				ov.orphans = append(ov.orphans, name)
			}
		}
	}
	return ov
}

// splitNames 从一行中提取表名：逗号/逐行分隔，去掉行尾说明与 (virtual) 等括号注记。
func splitNames(line string) []string {
	for _, sep := range []string{"——", " -- ", "--"} {
		if i := strings.Index(line, sep); i >= 0 {
			line = line[:i]
		}
	}
	var out []string
	for _, part := range strings.Split(line, ",") {
		t := strings.Trim(strings.TrimSpace(part), "`")
		if i := strings.IndexAny(t, "(（"); i >= 0 {
			t = strings.TrimSpace(t[:i])
		}
		if identRe.MatchString(t) {
			out = append(out, t)
		}
	}
	return out
}

func canonGroup(label string) string {
	n := strings.ToLower(strings.TrimSpace(label))
	n = strings.NewReplacer(" ", "", "-", "", "_", "").Replace(n)
	switch {
	case strings.Contains(n, "v1") && strings.Contains(n, "core"):
		return "v1 core"
	case strings.Contains(n, "core"):
		return "v1 core"
	case strings.Contains(n, "extension") || strings.Contains(n, "扩展"):
		return "extension"
	case strings.Contains(n, "deferred") || strings.Contains(n, "推迟") || strings.Contains(n, "defer"):
		return "deferred"
	default:
		return ""
	}
}

func (o *overview) nameSet() map[string]bool {
	set := map[string]bool{}
	for _, g := range o.groups {
		for _, n := range g.names {
			set[n] = true
		}
	}
	for _, n := range o.orphans {
		set[n] = true
	}
	return set
}

// coreNames 返回【v1 core】分组名字（排序、去重）；无该分组时返回 nil。
func (o *overview) coreNames() []string {
	for _, g := range o.groups {
		if g.canon == "v1 core" {
			set := map[string]bool{}
			for _, n := range g.names {
				set[n] = true
			}
			return sortedKeys(set)
		}
	}
	return nil
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func diffSet(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// previewList 截断过长列表，保留总数标注。
func previewList(items []string, max int) string {
	if len(items) <= max {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:max], ", ") + fmt.Sprintf(" …（共 %d 处）", len(items))
}

// ---------- I1：02 §8 的 v1 core 名单 == 02 的 DDL 名单（含虚拟表） ----------

func checkI1(ds *docSet) result {
	r := result{
		id:    "I1",
		title: "`02` §8 总览的【v1 core】名单 == `02` 的 CREATE TABLE 名单（含虚拟表）",
	}
	d02 := ds.top("02")
	if d02 == nil {
		r.violations = append(r.violations, "未找到 02_*.md")
		return r
	}
	ov := parseOverview(d02)
	if !ov.found {
		r.violations = append(r.violations, "02 §8 未解析到总览围栏块（关键词：数据模型总览）")
		return r
	}

	ddlSet := map[string]bool{}
	virtualByName := map[string]bool{}
	nVirtual := 0
	for _, h := range scanDDL(d02) {
		ddlSet[h.name] = true
		virtualByName[h.name] = h.virtual
		if h.virtual {
			nVirtual++
		}
	}

	core := ov.coreNames()
	scope := "【v1 core】分组"
	var coreSet map[string]bool
	if core == nil {
		coreSet = ov.nameSet()
		scope = "回退：整个总览块（未找到【v1 core】分组）"
		r.notes = append(r.notes, "未找到【v1 core】分组；按 ADR-0007 §1.1 证据口径回退比较整个总览块与 02 DDL 集合")
	} else {
		coreSet = map[string]bool{}
		for _, n := range core {
			coreSet[n] = true
		}
	}

	missing := diffSet(coreSet, ddlSet) // 总览列了但 02 无 DDL
	extra := diffSet(ddlSet, coreSet)   // 02 有 DDL 但总览未列
	if len(missing) > 0 {
		r.violations = append(r.violations,
			fmt.Sprintf("总览列名但 02 无 DDL（%d）: %s", len(missing), strings.Join(missing, ", ")))
	}
	if len(extra) > 0 {
		annotated := make([]string, 0, len(extra))
		for _, n := range extra {
			if virtualByName[n] {
				annotated = append(annotated, n+" (virtual)")
			} else {
				annotated = append(annotated, n)
			}
		}
		r.violations = append(r.violations,
			fmt.Sprintf("02 有 DDL 但总览未列（%d）: %s", len(extra), strings.Join(annotated, ", ")))
	}

	r.notes = append(r.notes, fmt.Sprintf("比较范围: %s；总览 %d 个名字，02 DDL %d 个（%d 表 + %d 虚表）",
		scope, len(coreSet), len(ddlSet), len(ddlSet)-nVirtual, nVirtual))
	r.pass = len(r.violations) == 0
	return r
}

// ---------- I2：DDL 只允许出现在 02 与 03/supplement/* ----------

func isI2Target(rel string) bool {
	if strings.HasPrefix(rel, "adr/") {
		return strings.HasSuffix(rel, ".md")
	}
	if strings.Contains(rel, "/") {
		return false
	}
	return rel == "README.md" || strings.HasPrefix(rel, "01") || strings.HasPrefix(rel, "04")
}

func checkI2(ds *docSet) result {
	r := result{
		id:    "I2",
		title: "DDL 只出现在 `02` 与 `03`/`supplement/*`（`04`/`01`/`README.md`/`adr/*` 无越位 DDL）",
	}
	var mentions []string
	exempt := 0
	for _, rel := range ds.rels {
		if !isI2Target(rel) {
			continue
		}
		d := ds.files[rel]
		flags := fenceFlags(d.lines)
		for i, ln := range d.lines {
			m := ddlNameRe.FindStringSubmatch(ln)
			if m == nil {
				if anyDDLRe.MatchString(ln) {
					mentions = append(mentions, fmt.Sprintf("%s:%d", rel, i+1))
				}
				continue
			}
			kind := "table"
			if m[1] != "" {
				kind = "virtual table"
			}
			if strings.HasPrefix(rel, "adr/") && (flags[i] || isBlockquote(ln)) {
				exempt++
				continue
			}
			r.violations = append(r.violations, fmt.Sprintf("%s:%d `%s` (%s)", rel, i+1, m[2], kind))
		}
	}
	if len(mentions) > 0 {
		r.notes = append(r.notes,
			fmt.Sprintf("忽略的散文/行内提及（非 DDL 语句，%d 处）: %s", len(mentions), previewList(mentions, 8)))
	}
	if exempt > 0 {
		r.notes = append(r.notes,
			fmt.Sprintf("adr/* 引用块（围栏代码块或引用行）内 DDL 语句 %d 处，按 ADR-0007 §8 豁免", exempt))
	}
	r.pass = len(r.violations) == 0
	return r
}

// ---------- I3：命名漂移（{X, X_edges} / {X, X_versions}） ----------

var driftSuffixes = []string{"_edges", "_versions"}

// driftAllowlist：已知合理对（同表族 / FTS 伴生），显式豁免；键为排序后的名字对。
var driftAllowlist = [][2]string{
	{"files", "file_versions"},
	{"symbols", "symbol_versions"},
}

func isNormativeDoc(rel string) bool {
	if strings.HasPrefix(rel, "adr/") || strings.HasPrefix(rel, "supplement/") {
		return true
	}
	if strings.Contains(rel, "/") {
		return false
	}
	if rel == "README.md" || rel == "GLOSSARY.md" {
		return true
	}
	for _, p := range []string{"01", "02", "03", "04", "05", "06"} {
		if strings.HasPrefix(rel, p) {
			return true
		}
	}
	return false
}

// singularize 做最小复数归一，用于 {dependencies, dependency_versions} 这类对。
func singularize(s string) string {
	switch {
	case strings.HasSuffix(s, "ies") && len(s) > 3:
		return s[:len(s)-3] + "y"
	case strings.HasSuffix(s, "ses"), strings.HasSuffix(s, "xes"),
		strings.HasSuffix(s, "zes"), strings.HasSuffix(s, "ches"), strings.HasSuffix(s, "shes"):
		return s[:len(s)-2]
	case strings.HasSuffix(s, "s") && !strings.HasSuffix(s, "ss") && len(s) > 1:
		return s[:len(s)-1]
	default:
		return s
	}
}

func pairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "|" + b
}

func checkI3(ds *docSet) result {
	r := result{
		id:    "I3",
		title: "无命名漂移（{X, X_edges} / {X, X_versions}，allowlist 显式豁免）",
	}

	sources := map[string][]string{} // 名字 → 出现位置
	add := func(name, src string) {
		if !containsStr(sources[name], src) {
			sources[name] = append(sources[name], src)
		}
	}
	for _, rel := range ds.rels {
		if !isNormativeDoc(rel) {
			continue
		}
		for _, h := range scanDDL(ds.files[rel]) {
			add(h.name, rel)
		}
	}
	if d02 := ds.top("02"); d02 != nil {
		ov := parseOverview(d02)
		for name := range ov.nameSet() {
			add(name, "02 §8 总览")
		}
	}

	names := make([]string, 0, len(sources))
	for n := range sources {
		names = append(names, n)
	}
	sort.Strings(names)

	allow := map[string]bool{}
	for _, p := range driftAllowlist {
		allow[pairKey(p[0], p[1])] = true
	}
	srcOf := func(n string) string {
		s := append([]string(nil), sources[n]...)
		sort.Strings(s)
		return strings.Join(s, "; ")
	}

	seen := map[string]bool{}
	var drift, allowed []string
	for _, t := range names {
		for _, suf := range driftSuffixes {
			if !strings.HasSuffix(t, suf) {
				continue
			}
			base := strings.TrimSuffix(t, suf)
			stem := singularize(base)
			for _, n := range names {
				if n == t || singularize(n) != stem {
					continue
				}
				key := pairKey(n, t)
				if seen[key] {
					continue
				}
				seen[key] = true
				msg := fmt.Sprintf("{%s, %s}：%s（%s） vs %s（%s）", n, t, n, srcOf(n), t, srcOf(t))
				if allow[key] {
					allowed = append(allowed, msg)
				} else {
					drift = append(drift, msg)
				}
			}
		}
	}
	sort.Strings(drift)
	sort.Strings(allowed)
	r.violations = drift
	if len(allowed) > 0 {
		r.notes = append(r.notes, fmt.Sprintf("allowlist 豁免（%d 对）: %s", len(allowed), strings.Join(allowed, "；")))
	}
	r.notes = append(r.notes,
		fmt.Sprintf("扫描范围: 规范文档（01–06/README/GLOSSARY/adr/*/supplement/*）+ 02 §8 总览名字，共 %d 个表名", len(names)))
	r.pass = len(r.violations) == 0
	return r
}

// ---------- I4：总览名字属于且仅属于一个分组 ----------

func checkI4(ds *docSet) result {
	r := result{
		id:    "I4",
		title: "总览中每个名字属于且仅属于一个分组（【v1 core】/【extension】/【deferred】）",
	}
	d02 := ds.top("02")
	if d02 == nil {
		r.violations = append(r.violations, "未找到 02_*.md")
		return r
	}
	ov := parseOverview(d02)
	if !ov.found {
		r.violations = append(r.violations, "02 §8 未解析到总览围栏块（关键词：数据模型总览）")
		return r
	}

	counts := map[string][]string{}
	for _, g := range ov.groups {
		for _, n := range g.names {
			if !containsStr(counts[n], g.raw) {
				counts[n] = append(counts[n], g.raw)
			}
		}
	}
	for _, n := range sortedNameCountKeys(counts) {
		if len(counts[n]) > 1 {
			r.violations = append(r.violations,
				fmt.Sprintf("名字 %s 同时出现在多个分组: %s", n, strings.Join(counts[n], ", ")))
		}
	}

	if len(ov.groups) == 0 {
		r.violations = append(r.violations,
			"总览块没有任何【分组】头（未找到【v1 core】/【extension】/【deferred】），全部名字处于无序单列")
		if len(ov.orphans) > 0 {
			r.violations = append(r.violations,
				fmt.Sprintf("未分组名字（%d）: %s", len(ov.orphans), strings.Join(ov.orphans, ", ")))
		}
	} else {
		if len(ov.orphans) > 0 {
			r.violations = append(r.violations,
				fmt.Sprintf("分组外名字（%d）: %s", len(ov.orphans), strings.Join(ov.orphans, ", ")))
		}
		seen := map[string]bool{}
		for _, g := range ov.groups {
			seen[g.canon] = true
		}
		for _, want := range []string{"v1 core", "extension", "deferred"} {
			if !seen[want] {
				r.violations = append(r.violations, fmt.Sprintf("缺少分组: 【%s】", want))
			}
		}
	}

	var summary []string
	for _, g := range ov.groups {
		summary = append(summary, fmt.Sprintf("【%s】(canon=%q, %d 个名字)", g.raw, g.canon, len(g.names)))
	}
	if len(summary) > 0 {
		r.notes = append(r.notes, "解析到分组: "+strings.Join(summary, "; "))
	}
	r.notes = append(r.notes, fmt.Sprintf("总览名字 %d 个（去重后 %d）",
		len(ov.orphans)+countGroupNames(&ov), len(ov.nameSet())))
	r.pass = len(r.violations) == 0
	return r
}

func countGroupNames(ov *overview) int {
	n := 0
	for _, g := range ov.groups {
		n += len(g.names)
	}
	return n
}

func sortedNameCountKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------- I5：表数声明自洽 ----------

var (
	v1LimitReUpper = regexp.MustCompile(`v1\s*表集\s*(?:≤|<=|不超过)?\s*([0-9]+)\s*张`)
	v1LimitReTitle = regexp.MustCompile(`v1\s*最小数据模型\s*[（(]\s*([0-9]+)\s*张`)
)

func checkI5(ds *docSet) result {
	r := result{
		id:    "I5",
		title: "表数声明自洽（`04` §4.3 的 v1 上限 vs `02` 的 v1 core 实际数量）",
	}
	d04 := ds.top("04")
	d02 := ds.top("02")
	if d04 == nil || d02 == nil {
		r.violations = append(r.violations, "未找到 04_*.md 或 02_*.md")
		return r
	}

	declared, declaredSrc := 0, ""
	text := strings.Join(d04.lines, "\n")
	if m := v1LimitReTitle.FindStringSubmatch(text); m != nil {
		declared, _ = strconv.Atoi(m[1])
		declaredSrc = "04 §4.3 标题"
	}
	if m := v1LimitReUpper.FindStringSubmatch(text); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			if declared == 0 {
				declared, declaredSrc = n, "04 §0.3 声明"
			} else if n != declared {
				r.notes = append(r.notes,
					fmt.Sprintf("04 内两处声明不一致: §4.3 标题=%d，§0.3 v1 表集上限=%d", declared, n))
			} else {
				r.notes = append(r.notes, fmt.Sprintf("04 内两处声明一致: v1 上限 = %d 张", declared))
			}
		}
	}
	if declared == 0 {
		r.violations = append(r.violations, "未能从 04 解析到 v1 表数声明（期望形如“v1 表集 ≤ 16 张”或“v1 最小数据模型（16 张）”）")
		return r
	}

	ov := parseOverview(d02)
	hits := scanDDL(d02)
	virtual := 0
	for _, h := range hits {
		if h.virtual {
			virtual++
		}
	}
	actual := len(hits)
	scope := "回退：02 DDL 集合（未找到【v1 core】分组）"
	if core := ov.coreNames(); core != nil {
		actual = len(core)
		scope = "【v1 core】分组"
		virtual = 0
		for _, n := range core {
			for _, h := range hits {
				if h.name == n && h.virtual {
					virtual++
				}
			}
		}
	}
	if actual > declared {
		r.violations = append(r.violations,
			fmt.Sprintf("04 声明 v1 上限 %d 张（%s） < 02 实际 %d 张（%s；%d 表 + %d 虚表）",
				declared, declaredSrc, actual, scope, actual-virtual, virtual))
	}
	r.notes = append(r.notes,
		fmt.Sprintf("声明数字 = ≤ %d（%s）；实际数字 = %d（%s；%d 表 + %d 虚表）",
			declared, declaredSrc, actual, scope, actual-virtual, virtual))
	r.pass = len(r.violations) == 0
	return r
}
