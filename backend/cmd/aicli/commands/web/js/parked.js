// 托管挂起/恢复横幅：消费 turn.suspended / turn.resumed（方案 §6.8 / 审计 G3）。
// aicli micro web client 前端模块（拆分自 sse.js，无构建步骤，由 sse.js 导入）。
//
// 契约（与 frontend/src/lib/parked-turn/events.ts 同口径）：
//   - turn.suspended   → 常驻横幅「等待 N 个义务」。挂起期间 turn_end 仍会到达
//     （那只是"本次 run 结束"），没有这一行，用户无法把"等待子任务"与"卡死"区分开；
//   - turn.resumed     → 替换为恢复通知（trigger / pending / terminal），8s 后自动
//     隐藏；恢复后新 run 的 turn_start 不清它（否则用户看不到恢复提示）；
//   - turn_start       → 只清除挂起态横幅（挂起已结束，横幅不再代表当前状态）；
//   - session_switched → 清空（横幅只描述当前会话）。
//
// 边界：micro 客户端没有挂起快照端点，横幅是**纯事件驱动**的；页面刷新/断线重连后
// 若错过边沿，横幅不显示（不伪造状态）。元素缺失时静默降级，页面裁剪结构也不报错。

var bannerEl = null;
var hideTimer = null;
var currentKind = "";

function bannerNode() {
  if (!bannerEl) { bannerEl = document.getElementById("parked-banner"); }
  return bannerEl;
}

function clearHideTimer() {
  if (hideTimer) { clearTimeout(hideTimer); hideTimer = null; }
}

export function hideParkedTurnBanner() {
  clearHideTimer();
  currentKind = "";
  var node = bannerNode();
  if (!node) { return; }
  node.style.display = "none";
  node.textContent = "";
  node.className = "parked-banner";
}

function showParkedTurnBanner(text, kind, autoHideMs) {
  clearHideTimer();
  currentKind = kind;
  var node = bannerNode();
  if (!node) { return; }
  node.textContent = text;
  node.className = "parked-banner parked-" + kind;
  node.style.display = "block";
  if (autoHideMs > 0) {
    hideTimer = setTimeout(hideParkedTurnBanner, autoHideMs);
  }
}

function obligationText(data) {
  var count = data && typeof data.obligation_count === "number" ? data.obligation_count : null;
  var text = count === null ? "托管挂起：等待义务终态" : "托管挂起：等待 " + count + " 个义务";
  if (data && data.batch_id) { text += "（batch " + data.batch_id + "）"; }
  return text;
}

function resumedText(data) {
  var text = "托管恢复：触发 " + ((data && data.trigger) || "other");
  if (data && typeof data.pending_count === "number") { text += "，pending=" + data.pending_count; }
  if (data && data.terminal) { text += "（全部终态）"; }
  return text;
}

export function handleParkedTurnSSEEvent(eventName, data) {
  switch (eventName) {
    case "turn.suspended":
      showParkedTurnBanner(obligationText(data), "suspended", 0);
      break;
    case "turn.resumed":
      showParkedTurnBanner(resumedText(data), "resumed", 8000);
      break;
    case "turn_start":
      if (currentKind === "suspended") { hideParkedTurnBanner(); }
      break;
    case "session_switched":
      hideParkedTurnBanner();
      break;
    default:
      break;
  }
}
