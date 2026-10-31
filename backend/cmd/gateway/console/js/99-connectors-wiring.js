// 99-connectors-wiring.js
// gateway 控制台功能模块（gateway 控制台模块化，共享面见 console/js/runtime.js）。
// 共享面：S = 可变状态（原单文件闭包里的顶层 var），G = 函数注册表（原顶层 function）。
// 本模块只依赖 ./runtime.js，不 import 其它功能模块——模块求值顺序 = 入口 import 顺序，
// 与原单文件的顶层语句顺序一致。
import { S, G } from "./runtime.js?v=95";

Object.assign(G, {
  wireConnectorScrim: wireConnectorScrim,
});

  /* ─────────────── connectors: management wiring ─────────────── */

  S.connectorsCreateBtn = S.el("connectors-create");
  if (S.connectorsCreateBtn) {
    S.connectorsCreateBtn.addEventListener("click", function () { G.openConnectorEditor(null); });
  }

  if (S.el("connector-editor-close")) {
    S.el("connector-editor-close").addEventListener("click", G.closeConnectorEditor);
  }
  if (S.el("connector-editor-cancel")) {
    S.el("connector-editor-cancel").addEventListener("click", G.closeConnectorEditor);
  }
  if (S.el("connector-editor-save")) {
    S.el("connector-editor-save").addEventListener("click", G.saveConnectorEditor);
  }

  S.connectorTestClose = function () { G.closeModal("connector-test-modal"); };
  if (S.el("connector-test-close")) { S.el("connector-test-close").addEventListener("click", S.connectorTestClose); }
  if (S.el("connector-test-cancel")) { S.el("connector-test-cancel").addEventListener("click", S.connectorTestClose); }
  if (S.el("connector-test-send")) { S.el("connector-test-send").addEventListener("click", G.sendConnectorTest); }
  if (S.el("connector-test-chat")) {
    S.el("connector-test-chat").addEventListener("keydown", function (e) {
      if (e.key === "Enter") { e.preventDefault(); G.sendConnectorTest(); }
    });
  }

  S.connectorLogsClose = function () { G.closeModal("connector-logs-modal"); };
  if (S.el("connector-logs-close")) { S.el("connector-logs-close").addEventListener("click", S.connectorLogsClose); }
  if (S.el("connector-logs-refresh")) {
    S.el("connector-logs-refresh").addEventListener("click", function () {
      if (S.connectorLogsState.id) { G.loadConnectorLogs(S.connectorLogsState.id); }
    });
  }

  S.connectorActionClose = function () { G.closeModal("connector-action-modal"); };
  if (S.el("connector-action-close")) { S.el("connector-action-close").addEventListener("click", S.connectorActionClose); }
  if (S.el("connector-action-done")) { S.el("connector-action-done").addEventListener("click", S.connectorActionClose); }

  S.connectorQRClose = function () { G.closeConnectorQRDialog(); };
  if (S.el("connector-qr-close")) { S.el("connector-qr-close").addEventListener("click", S.connectorQRClose); }
  if (S.el("connector-qr-done")) { S.el("connector-qr-done").addEventListener("click", S.connectorQRClose); }
  if (S.el("connector-qr-restart")) { S.el("connector-qr-restart").addEventListener("click", G.startConnectorQRLogin); }
  if (S.el("connector-qr-copy")) {
    S.el("connector-qr-copy").addEventListener("click", function () {
      G.copyText(S.connectorQRState.content || "", S.el("connector-qr-copy"));
    });
  }

  function wireConnectorScrim(id, closeFn) {
    var modal = S.el(id);
    if (!modal) { return; }
    modal.addEventListener("click", function (e) { if (e.target === modal) { closeFn(); } });
    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape" && !modal.hidden) { closeFn(); }
    });
  }
  wireConnectorScrim("connector-editor-modal", G.closeConnectorEditor);
  wireConnectorScrim("connector-test-modal", S.connectorTestClose);
  wireConnectorScrim("connector-logs-modal", S.connectorLogsClose);
  wireConnectorScrim("connector-action-modal", S.connectorActionClose);
  wireConnectorScrim("connector-qr-modal", S.connectorQRClose);
