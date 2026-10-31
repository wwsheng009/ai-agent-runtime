  /* ─────────────── connectors: management wiring ─────────────── */

  var connectorsCreateBtn = el("connectors-create");
  if (connectorsCreateBtn) {
    connectorsCreateBtn.addEventListener("click", function () { openConnectorEditor(null); });
  }

  if (el("connector-editor-close")) {
    el("connector-editor-close").addEventListener("click", closeConnectorEditor);
  }
  if (el("connector-editor-cancel")) {
    el("connector-editor-cancel").addEventListener("click", closeConnectorEditor);
  }
  if (el("connector-editor-save")) {
    el("connector-editor-save").addEventListener("click", saveConnectorEditor);
  }

  var connectorTestClose = function () { closeModal("connector-test-modal"); };
  if (el("connector-test-close")) { el("connector-test-close").addEventListener("click", connectorTestClose); }
  if (el("connector-test-cancel")) { el("connector-test-cancel").addEventListener("click", connectorTestClose); }
  if (el("connector-test-send")) { el("connector-test-send").addEventListener("click", sendConnectorTest); }
  if (el("connector-test-chat")) {
    el("connector-test-chat").addEventListener("keydown", function (e) {
      if (e.key === "Enter") { e.preventDefault(); sendConnectorTest(); }
    });
  }

  var connectorLogsClose = function () { closeModal("connector-logs-modal"); };
  if (el("connector-logs-close")) { el("connector-logs-close").addEventListener("click", connectorLogsClose); }
  if (el("connector-logs-refresh")) {
    el("connector-logs-refresh").addEventListener("click", function () {
      if (connectorLogsState.id) { loadConnectorLogs(connectorLogsState.id); }
    });
  }

  var connectorActionClose = function () { closeModal("connector-action-modal"); };
  if (el("connector-action-close")) { el("connector-action-close").addEventListener("click", connectorActionClose); }
  if (el("connector-action-done")) { el("connector-action-done").addEventListener("click", connectorActionClose); }

  var connectorQRClose = function () { closeConnectorQRDialog(); };
  if (el("connector-qr-close")) { el("connector-qr-close").addEventListener("click", connectorQRClose); }
  if (el("connector-qr-done")) { el("connector-qr-done").addEventListener("click", connectorQRClose); }
  if (el("connector-qr-restart")) { el("connector-qr-restart").addEventListener("click", startConnectorQRLogin); }
  if (el("connector-qr-copy")) {
    el("connector-qr-copy").addEventListener("click", function () {
      copyText(connectorQRState.content || "", el("connector-qr-copy"));
    });
  }

  function wireConnectorScrim(id, closeFn) {
    var modal = el(id);
    if (!modal) { return; }
    modal.addEventListener("click", function (e) { if (e.target === modal) { closeFn(); } });
    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape" && !modal.hidden) { closeFn(); }
    });
  }
  wireConnectorScrim("connector-editor-modal", closeConnectorEditor);
  wireConnectorScrim("connector-test-modal", connectorTestClose);
  wireConnectorScrim("connector-logs-modal", connectorLogsClose);
  wireConnectorScrim("connector-action-modal", connectorActionClose);
  wireConnectorScrim("connector-qr-modal", connectorQRClose);

