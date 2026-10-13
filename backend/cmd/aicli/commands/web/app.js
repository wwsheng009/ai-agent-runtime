// aicli micro web client 前端入口。
// 原为单文件 IIFE(3297 行),现按功能域拆分为 js/ 下的 ES 模块(无构建步骤,
// <script type="module"> 直接加载本入口);各模块导出 initXxx() 供此处按
// 原初始化顺序统一调用。测试方法见 docs/aicli/web-testing.md。
import { initChat, renderButton, refreshScreen } from "./js/chat.js";
import { initComposerPanel } from "./js/composer.js";
import { initComposerAttachments } from "./js/attachments.js";
import { initAnalysis } from "./js/analysis.js";
import { initLSP } from "./js/lsp.js";
import { initConfigAdmin } from "./js/config-admin.js";
import { initConnectors } from "./js/connectors.js";
import { initProviderEditor } from "./js/provider-editor.js";
import { initProviderImport } from "./js/provider-import.js";
import { initApprovals } from "./js/approvals.js";
import { initRuntimeBar, loadRuntimeMeta } from "./js/runtime.js";
import { holdSessionsLoad, initSessions, releaseSessionsLoad } from "./js/sessions.js";
import { initSchedule } from "./js/schedule.js";
import { initSkills } from "./js/skills.js";
import { initFiles } from "./js/files.js";
import { initGit } from "./js/git.js";
import { initMCP } from "./js/mcp.js";
import { initMenu } from "./js/menu.js";
import { initMsgFilter } from "./js/msg-filter.js";
import { initSSE } from "./js/sse.js";
import { initStream } from "./js/stream.js";
import { initTodoPanel } from "./js/todos.js";
import { initAboutSessionCopy, initAboutToken, initShortcutHelp, initTabs, initTheme } from "./js/ui.js";
import { initStatusBar } from "./js/statusbar.js";

// ---- 事件绑定(原 IIFE 尾部,按组件归属拆分到各模块) ----
initTabs();
initTheme();
initAboutToken(); // 关于页签的写令牌显示（读页面注入 meta，回退 /web/api/token）
initAboutSessionCopy(); // 关于页签的当前会话 ID 复制（值由 sessions.js 同步写入）
initShortcutHelp();
initMenu(); // 顶部菜单栏（文件/视图/帮助 下拉 + 会话导出下载，见 js/menu.js）
initStatusBar();
initStream();
initChat();
initComposerPanel(); // composer 面板（首行动态状态条；正文 = 输入 + provider/model/reasoning 选择器）
initTodoPanel(); // 任务列表浮动面板（贴在 composer 上沿；SSE tool_end 实时 + screen 回放）
initMsgFilter(); // 对话区消息过滤面板（角色多选 + 正文搜索，服务端过滤）
// 图片附件（📎 / 粘贴 / 面板内拖放 → 上传 → 附件轨；发送带 image_paths）。
// 不挪到 initComposerPanel() 旁边：verify-micro-web-msg-filter.mjs 要求
// initChat()→initMsgFilter() 之间的文本不超过 200 字符，插进那个窗口会挤爆它。
initComposerAttachments();
initRuntimeBar();
initSessions();
initApprovals();
initConfigAdmin();
initConnectors(); // 「机器人」页签：IM 连接器（列表/表单/扫码登录/日志，数据源 /api/connectors）
initProviderEditor();
initProviderImport();
initSkills();
initFiles(); // 「文件」页签：文件管理器（预览弹窗 reparent + 工具栏事件绑定）
initGit();   // 「GIT」页签：git 管理器（diff 弹窗 reparent + 工具栏事件绑定）
initMCP();
initSchedule(); // 「任务」页签：自动化任务目录（删除两段式确认 + 刷新）
initAnalysis();
initLSP();

// ---- 启动序列(原文件尾部) ----
// 数据加载次序（性能约定）：
//   ① 当前会话信息优先——屏幕快照（refreshScreen → /web/api/screen）先发起；
//   ② 会话列表其次——首屏请求落地前由 holdSessionsLoad() 挂起，落地（或 1.5s
//      兜底超时，防快照异常缓慢时侧栏无限空等）后 releaseSessionsLoad() 异步放行；
//   ③ 其它页签的数据源不在启动期预热——ui.js::activateTab 在用户点击页签时才加载。
var SESSION_LIST_FALLBACK_DELAY_MS = 1500;

function releaseSessionsAfterScreen(screenPromise) {
  var released = false;
  function release() {
    if (released) { return; }
    released = true;
    releaseSessionsLoad();
  }
  if (screenPromise && typeof screenPromise.then === "function") {
    screenPromise.then(release, release);
  }
  setTimeout(release, SESSION_LIST_FALLBACK_DELAY_MS);
}

loadRuntimeMeta(); // 权威 provider/model/reasoning 值同步到底部选择器
initSSE();         // EventSource 连接 + 动态状态栏时钟
holdSessionsLoad(); // ① 会话列表先挂起，确保屏幕快照先落地
releaseSessionsAfterScreen(refreshScreen()); // ② 首屏落地后异步放行列表
renderButton(); // 初始按钮状态(idle:输入为空时禁用)
// 底部状态栏的首次拉取已由 initStatusBar()（上方绑定区）完成，此处不再重复调用。
