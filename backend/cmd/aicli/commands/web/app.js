// aicli micro web client 前端入口。
// 原为单文件 IIFE(3297 行),现按功能域拆分为 js/ 下的 ES 模块(无构建步骤,
// <script type="module"> 直接加载本入口);各模块导出 initXxx() 供此处按
// 原初始化顺序统一调用。测试方法见 docs/aicli/web-testing.md。
import { initChat, renderButton, refreshScreen } from "./js/chat.js";
import { initAnalysis } from "./js/analysis.js";
import { initConfigAdmin } from "./js/config-admin.js";
import { initProviderEditor } from "./js/provider-editor.js";
import { initProviderImport } from "./js/provider-import.js";
import { initApprovals } from "./js/approvals.js";
import { initRuntimeBar, loadRuntimeMeta } from "./js/runtime.js";
import { initSessions, loadSessions } from "./js/sessions.js";
import { initSkills } from "./js/skills.js";
import { initMCP } from "./js/mcp.js";
import { initMenu } from "./js/menu.js";
import { initMsgFilter } from "./js/msg-filter.js";
import { initSSE } from "./js/sse.js";
import { initStream } from "./js/stream.js";
import { initAboutSessionCopy, initAboutToken, initShortcutHelp, initTabs, initTheme } from "./js/ui.js";
import { initStatusBar, loadStatusBar } from "./js/statusbar.js";

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
initMsgFilter(); // 对话区消息过滤面板（角色多选 + 正文搜索，服务端过滤）
initRuntimeBar();
initSessions();
initApprovals();
initConfigAdmin();
initProviderEditor();
initProviderImport();
initSkills();
initMCP();
initAnalysis();

// ---- 启动序列(原文件尾部) ----
loadRuntimeMeta(); // 权威 provider/model/reasoning 值同步到底部选择器
loadStatusBar();    // 底部状态栏: balance / context / directory / git branch / window
initSSE();         // EventSource 连接 + 动态状态栏时钟
refreshScreen();
loadSessions();
renderButton(); // 初始按钮状态(idle:输入为空时禁用)
