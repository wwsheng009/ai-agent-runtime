// aicli micro web client 前端入口。
// 原为单文件 IIFE(3297 行),现按功能域拆分为 js/ 下的 ES 模块(无构建步骤,
// <script type="module"> 直接加载本入口);各模块导出 initXxx() 供此处按
// 原初始化顺序统一调用。测试方法见 docs/aicli/web-testing.md。
import { initChat, renderButton, refreshScreen } from "./js/chat.js";
import { initConfigAdmin } from "./js/config-admin.js";
import { initProviderEditor } from "./js/provider-editor.js";
import { initProviderImport } from "./js/provider-import.js";
import { initApprovals } from "./js/approvals.js";
import { initRuntimeBar, loadRuntimeMeta } from "./js/runtime.js";
import { initSessions, loadSessions } from "./js/sessions.js";
import { initSSE } from "./js/sse.js";
import { initStream } from "./js/stream.js";
import { initFooter, initShortcutHelp, initTabs, initTheme } from "./js/ui.js";

// ---- 事件绑定(原 IIFE 尾部,按组件归属拆分到各模块) ----
initTabs();
initTheme();
initShortcutHelp();
initFooter();
initStream();
initChat();
initRuntimeBar();
initSessions();
initApprovals();
initConfigAdmin();
initProviderEditor();
initProviderImport();

// ---- 启动序列(原文件尾部) ----
loadRuntimeMeta(); // 权威 provider/model/reasoning 值同步到底部选择器
initSSE();         // EventSource 连接 + 动态状态栏时钟
refreshScreen();
loadSessions();
renderButton(); // 初始按钮状态(idle:输入为空时禁用)
