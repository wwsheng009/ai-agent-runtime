package commands

import (
	"strings"
	"testing"
)

// 「文件」/「GIT」页签的编辑器式分栏 + 页签右键菜单的 asset 契约门禁。
//
// 与 web_handlers_mesh_polish_test.go 同套路：前端无构建步骤，真实浏览器行为只能在
// 手工清单里验（docs/aicli/web-testing.md §2.10 / §2.11，行为侧另有 §4 的 Node 沙盒
// scripts/verify-micro-web-pane-split.mjs），这里锁定那些「删掉后页面照常加载、
// 只是悄悄退化」的字符串契约：
//  1. js/splitpane.js 是两个页签**共用**的唯一分栏实现（折叠/拖拽/记忆值/无障碍不许各长一份）；
//  2. 页签右键菜单的五个批量关闭动作与 index.html 的标记一一对应（漏一个就是点了没反应）；
//  3. git 分栏的 DOM 骨架，以及「diff 面板是同一个 DOM」的前提——overlay 一旦被搬出
//     #git-main，大屏右栏就空了（窄屏弹窗却照常工作，最容易被漏掉的那种退化）。
//  4. 「右栏正在看谁」这个状态有两个入口（切换工作区/已暂存、关闭 diff），漏掉任一处
//     左栏就会留着一条对不上的选中标记——右栏看着没问题，只有左栏的 aria-current 说谎。
//  5. git 侧栏的「变更 / 提交记录」子页签：面板显隐对账、roving tabindex、页签上的条数——
//     两条列表同时出现、或两条页签都能被 Tab 到，页面看着都正常，只有键盘/读屏用户受影响。
//
// 只锁结构与接线，不锁文案：改措辞不该惊动测试，改结构必须惊动。

// TestChatWebPaneSplitPaneShared 断言分栏助手只有一份实现，且两个页签都接上了它。
func TestChatWebPaneSplitPaneShared(t *testing.T) {
	split := fetchChatWebAsset(t, "js/splitpane.js")
	want := []string{
		// 导出面：调用方靠它建控制器。
		"export function createSplitPane(opts)",
		// 折叠态用 class 表达（几何在 style.css），宽度写进 CSS 变量。
		"side-collapsed",
		"style.setProperty(widthVar",
		// 记忆值：宽度与折叠态都落 localStorage（隐私模式下静默降级）。
		"localStorage.setItem",
		"localStorage.getItem",
		// 把手：拖拽 + 键盘微调 + 双击复位 + aria 数值 + 拖拽期间锁光标。
		"setPointerCapture",
		"pane-resizing",
		"ArrowLeft",
		"ArrowRight",
		"dblclick",
		"aria-valuenow",
		// 每次对账后回调调用方补齐自己的面板语义。
		"o.onApply(split)",
	}
	for _, token := range want {
		if !strings.Contains(split, token) {
			t.Errorf("js/splitpane.js 缺少 %q（分栏助手契约）", token)
		}
	}

	for _, asset := range []string{"js/files.js", "js/git.js"} {
		body := fetchChatWebAsset(t, asset)
		for _, token := range []string{
			`import { createSplitPane } from "./splitpane.js";`,
			"createSplitPane({",
			".init();", // 首次对账（断点监听 + 记忆值恢复）必须真的被调用
		} {
			if !strings.Contains(body, token) {
				t.Errorf("%s 缺少 %q（未接上 js/splitpane.js）", asset, token)
			}
		}
	}
}

// TestChatWebFilesTabMenuWiring 断言页签右键菜单：五个动作、键盘入口、三种收起时机。
func TestChatWebFilesTabMenuWiring(t *testing.T) {
	page := fetchChatWebAsset(t, "")
	for _, token := range []string{
		`id="files-tab-menu"`,
		`role="menu"`,
		`role="menuitem"`,
		// 五个动作：关闭当前 / 其它 / 左侧 / 右侧 / 全部。
		`data-menu-action="close"`,
		`data-menu-action="others"`,
		`data-menu-action="left"`,
		`data-menu-action="right"`,
		`data-menu-action="all"`,
	} {
		if !strings.Contains(page, token) {
			t.Errorf("index.html 缺少 %q（页签菜单标记）", token)
		}
	}

	files := fetchChatWebAsset(t, "js/files.js")
	for _, token := range []string{
		// 批量关闭 + 禁用判定（「目录浏览」不可关，也不当别人的左侧页签）。
		"function closeFileTabsAround(mode, targetKey, focus)",
		"function filesTabMenuFlags(key)",
		// 打开 / 收起 / 焦点（↑↓ 跳过禁用项）。
		"function openFilesTabMenu(button, x, y)",
		"function closeFilesTabMenu(restoreFocus)",
		"function moveFilesTabMenuFocus(mode)",
		"function runFilesTabMenuAction(action)",
		// 指针入口（右键）与键盘入口（菜单键 / Shift+F10）。
		`addEventListener("contextmenu"`,
		`event.key === "ContextMenu"`,
		`event.key === "F10"`,
		// 收起时机：Esc、点到菜单外、滚动、窗口尺寸变化。
		`event.key === "Escape"`,
		`addEventListener("pointerdown"`,
		`addEventListener("resize"`,
		`addEventListener("scroll"`,
	} {
		if !strings.Contains(files, token) {
			t.Errorf("js/files.js 缺少 %q（页签菜单接线）", token)
		}
	}
}

// TestChatWebGitSplitLayoutWiring 断言 git 页签的分栏骨架与 diff 面板「弹窗↔右栏」同 DOM。
func TestChatWebGitSplitLayoutWiring(t *testing.T) {
	page := fetchChatWebAsset(t, "")
	for _, token := range []string{
		`id="git-layout"`,
		`id="git-side"`,
		`id="git-side-toggle"`,
		`id="git-side-splitter"`,
		`id="git-main"`,
		`id="git-detail-empty"`,
		`id="git-diff-overlay"`,
	} {
		if !strings.Contains(page, token) {
			t.Errorf("index.html 缺少 %q（git 分栏骨架）", token)
		}
	}

	git := fetchChatWebAsset(t, "js/git.js")
	for _, token := range []string{
		// 右栏空态 + 面板语义跟着断点走：大屏 region / 窄屏 dialog。
		"function syncGitLayout(nowSplit)",
		`modal.setAttribute("role", "region")`,
		`modal.setAttribute("role", "dialog")`,
		"empty.hidden = !(nowSplit && !open)",
		// 左栏标出右栏正在看的那条（同文件可能在 staged/unstaged 两组里，按 data-group 对齐）。
		"function syncGitActiveRow()",
		`row.getAttribute("data-group")`,
		`row.classList.toggle("active", on)`,
		// 打开/关闭 diff 都要重新对账（空态与选中行）。
		"gitPane.apply();",
	} {
		if !strings.Contains(git, token) {
			t.Errorf("js/git.js 缺少 %q（git 分栏接线）", token)
		}
	}

	// 回归门禁：overlay 必须留在 #git-main 里（大屏当右栏）。搬到 body 下窄屏照常弹窗，
	// 只有大屏会悄悄空掉——最不该靠肉眼发现的那种退化。
	if strings.Contains(git, "document.body.appendChild") {
		t.Error("js/git.js 不应把 #git-diff-overlay 搬到 body 下（大屏右栏会空掉）")
	}
}

// TestChatWebGitSideSubTabsWiring 断言 git 侧栏的「变更 / 提交记录」子页签：标记、接线、面板对账。
//
// 两条列表原先上下堆叠（小节标题 + 列表 + 小节标题 + 列表），改成子页签后一次只看一条，
// 于是多了三处「删掉也不报错、只是悄悄退化」的契约：面板 hidden 的显隐对账、roving tabindex
// （两条页签都能被 Tab 到 = 键盘要按两次）、以及另一条页签看不见时页签上的条数。
func TestChatWebGitSideSubTabsWiring(t *testing.T) {
	page := fetchChatWebAsset(t, "")
	for _, token := range []string{
		`id="git-subtabs"`,
		`role="tablist"`,
		`data-git-subtab="changes"`,
		`data-git-subtab="commits"`,
		// 两个面板：role=tabpanel 与页签互指（aria-controls / aria-labelledby 两头都要有）。
		`id="git-panel-changes"`,
		`id="git-panel-commits"`,
		`role="tabpanel"`,
		`aria-controls="git-panel-changes"`,
		`aria-labelledby="git-subtab-commits"`,
		// 页签上的条数徽标。
		`id="git-changes-count"`,
		`id="git-commits-count"`,
	} {
		if !strings.Contains(page, token) {
			t.Errorf("index.html 缺少 %q（git 侧栏子页签标记）", token)
		}
	}
	// 子串都在文件里 ≠ 结构对：页签栏必须嵌在 .git-side-bar 内部（大屏下整块 sticky 贴顶，
	// 列表滚到哪都能换页签）。挪到块外不会报错，只是左栏一滚页签就跟着滚走了。
	if !nestedInClass(page, `class="git-side-bar"`, `id="git-subtabs"`) {
		t.Error("index.html 的 #git-subtabs 必须嵌在 .git-side-bar 内部（sticky 块的最后一行）")
	}

	git := fetchChatWebAsset(t, "js/git.js")
	for _, token := range []string{
		"function selectGitSubTab(key, focus)",
		"function moveGitSubTabFocus(key)",
		`button[data-git-subtab]`,
		// roving tabindex + aria-selected：只有当前页签能被 Tab 到。
		`btn.setAttribute("tabindex", on ? "0" : "-1")`,
		`btn.setAttribute("aria-selected", on ? "true" : "false")`,
		// 面板显隐对账（另一个面板必须收起，否则两条列表又并排出现）。
		"panel.hidden = panels[j][1] !== want",
		// 键盘入口：←/→ 环绕 + Home/End（与文件页签同一套）。
		`event.key !== "ArrowRight"`,
		`event.key !== "Home"`,
		// 条数：加载中先置空（不留上一轮的旧数字），渲染后再写。
		`setGitSubTabCount("git-changes-count", "")`,
		`setGitSubTabCount("git-commits-count", "")`,
	} {
		if !strings.Contains(git, token) {
			t.Errorf("js/git.js 缺少 %q（git 侧栏子页签接线）", token)
		}
	}

	css := fetchChatWebAsset(t, "style.css")
	for _, token := range []string{
		// 页签条外观与文件页签共用同一组规则（不是各写一份）。
		".files-tabs, .git-subtabs {",
		"#git-subtabs .git-subtab {",
		".git-subtab-count {",
		// 面板 hidden 显式生效；折叠成窄轨时整条页签栏跟着收起。
		".git-panel[hidden] { display: none; }",
		// 页签栏是 sticky 块的最后一行：下沿即分隔线，间距只留 .git-side-bar 那一份。
		".git-side-bar .git-subtabs { margin-bottom: 0; }",
		".git-layout.side-collapsed .git-subtabs,",
	} {
		if !strings.Contains(css, token) {
			t.Errorf("style.css 缺少 %q（git 侧栏子页签样式）", token)
		}
	}
	// 回归门禁：改成子页签后不该再有「小节标题」那套上下堆叠的样式（留着说明有人把 h3 写回来了）。
	if strings.Contains(css, ".git-section-title") {
		t.Error("style.css 不应再有 .git-section-title（侧栏已改成子页签，不再上下堆叠）")
	}
}

// nestedInClass 判断 page 里以 openToken 开头的那个元素内部是否包含 targetToken。
//
// 这里用最朴素的 div 深度扫描：strings.Contains 只能证明「两者都在文件里」，证明不了
// 「一个在另一个里面」——而 git 侧栏子页签的 sticky 行为恰恰只取决于嵌套关系。
func nestedInClass(page, openToken, targetToken string) bool {
	start := strings.Index(page, openToken)
	target := strings.Index(page, targetToken)
	if start < 0 || target < start {
		return false
	}
	depth := 1 // 已经站在该元素的起始标签里（openToken 指向标签内部）
	for i := start; i < len(page); {
		open := strings.Index(page[i:], "<div")
		closeIdx := strings.Index(page[i:], "</div>")
		if open < 0 && closeIdx < 0 {
			return false
		}
		pos, isOpen := 0, false
		switch {
		case open < 0:
			pos, isOpen = closeIdx, false
		case closeIdx < 0:
			pos, isOpen = open, true
		case open < closeIdx:
			pos, isOpen = open, true
		default:
			pos, isOpen = closeIdx, false
		}
		at := i + pos
		if at >= target {
			return depth > 0
		}
		if isOpen {
			depth++
		} else if depth--; depth <= 0 {
			return false // 目标出现前这个元素就闭合了 = 目标在它外面
		}
		i = at + 1
	}
	return false
}

// TestChatWebGitDiffTargetKeepsRowInSync 断言「右栏正在看谁」的两个入口都会重新对账左栏。
//
// 同一个文件可以同时在「已暂存」与「未暂存」两组里（xy=MM 之类），所以：
//   - 切「工作区/已暂存」时，选中标记要跟着挪到对应分组那一条；
//   - 关闭 diff 后右栏是空态，左栏就不该再留着任何选中标记（aria-current 不能说谎）。
//
// 两处都只在真实浏览器里看得见，Node 沙盒只覆盖 js/splitpane.js 的几何，故在这里锁结构。
func TestChatWebGitDiffTargetKeepsRowInSync(t *testing.T) {
	git := fetchChatWebAsset(t, "js/git.js")
	for _, token := range []string{
		// 切换目标是单一入口：内部负责重标左栏（syncGitActiveRow）+ 重拉 diff。
		"function switchDiffTarget(target)",
		`switchDiffTarget("working")`,
		`switchDiffTarget("staged")`,
		// 关闭 diff 要清掉「右栏正在看谁」，否则左栏会留着一条已不存在的选中标记。
		"diffFile = null;",
	} {
		if !strings.Contains(git, token) {
			t.Errorf("js/git.js 缺少 %q（diff 目标 / 选中行同步契约）", token)
		}
	}

	// 回归门禁：切换目标不许再内联改状态——那样右栏照常换 diff，只有左栏的选中行会留在原地。
	if strings.Contains(git, `diffTarget = "staged"; syncDiffTargetButtons(); loadDiff();`) {
		t.Error("切换 diff 目标不应内联改状态（会漏掉 syncGitActiveRow 对账）")
	}
}

// TestChatWebPaneLayoutStyles 断言样式侧真的画了分栏与菜单（几何可以调，结构不能丢）。
func TestChatWebPaneLayoutStyles(t *testing.T) {
	css := fetchChatWebAsset(t, "style.css")
	for _, token := range []string{
		// 页签菜单浮层（hidden 属性控制显隐，禁用项有明确视觉）。
		".tab-menu {",
		".tab-menu[hidden]",
		".tab-menu-item:disabled",
		// 两个页签的把手与折叠态。
		".git-splitter {",
		".git-layout.side-collapsed .git-side {",
		// diff 面板在大屏从「固定遮罩」退回右栏常驻块：定位与遮罩底色都得撤掉。
		"#git-diff-overlay { position: static;",
		"#git-diff-modal { width: 100%;",
	} {
		if !strings.Contains(css, token) {
			t.Errorf("style.css 缺少 %q（分栏/菜单样式）", token)
		}
	}
	// 文件页签与 git 页签各自一份 900px 分栏几何（共用助手，几何各自声明）。
	if n := strings.Count(css, "@media (min-width: 900px) {"); n < 2 {
		t.Errorf("style.css 的 900px 分栏媒体查询有 %d 处，期望 ≥2（文件 + GIT）", n)
	}
}
