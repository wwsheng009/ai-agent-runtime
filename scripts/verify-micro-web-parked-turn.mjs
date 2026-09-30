// 托管挂起/恢复横幅（backend/cmd/aicli/commands/web/js/parked.js）行为回归。
// 运行：node scripts/verify-micro-web-parked-turn.mjs（仓库根目录）
// 无需浏览器：迷你 DOM stub 只实现模块用到的 getElementById / style / textContent / className。

import path from "node:path";
import { pathToFileURL } from "node:url";

let failures = 0;
function check(name, cond, detail) {
  if (cond) {
    console.log("  ok  " + name);
    return;
  }
  failures += 1;
  console.error("  FAIL " + name + (detail ? " — " + detail : ""));
}

const banner = { style: {}, textContent: "", className: "parked-banner" };
let nodeMissing = false;
globalThis.document = {
  getElementById(id) {
    if (nodeMissing) { return null; }
    return id === "parked-banner" ? banner : null;
  },
};

const moduleURL = pathToFileURL(
  path.resolve(process.cwd(), "backend/cmd/aicli/commands/web/js/parked.js"),
).href;
const parked = await import(moduleURL);

// 1. turn.suspended：常驻横幅，文案含义务数与 batch 身份。
parked.handleParkedTurnSSEEvent("turn.suspended", { obligation_count: 2, batch_id: "b1" });
check("suspended 显示横幅", banner.style.display === "block");
check(
  "suspended 文案含义务数与 batch",
  banner.textContent.includes("2 个义务") && banner.textContent.includes("b1"),
  banner.textContent,
);
check("suspended 样式类", banner.className.includes("parked-suspended"), banner.className);

// 2. turn.suspended 无计数（轻量子会话宿主侧挂起没有 batch_id）也要可读。
parked.handleParkedTurnSSEEvent("turn.suspended", {});
check("suspended 无计数回退文案", banner.textContent.includes("等待义务终态"), banner.textContent);

// 3. turn.resumed：替换为恢复通知；恢复后新 run 的 turn_start 不清它。
parked.handleParkedTurnSSEEvent("turn.resumed", { trigger: "terminal", pending_count: 0, terminal: true });
check(
  "resumed 文案含 trigger/pending/终态",
  banner.textContent.includes("terminal") && banner.textContent.includes("pending=0") && banner.textContent.includes("全部终态"),
  banner.textContent,
);
check("resumed 样式类", banner.className.includes("parked-resumed"), banner.className);
parked.handleParkedTurnSSEEvent("turn_start", {});
check("resumed 通知在 turn_start 后仍可见", banner.style.display === "block");

// 4. 挂起态在 turn_start / session_switched 时清除。
parked.handleParkedTurnSSEEvent("turn.suspended", { obligation_count: 1 });
parked.handleParkedTurnSSEEvent("turn_start", {});
check("挂起态在 turn_start 后清除", banner.style.display === "none" && banner.textContent === "");

parked.handleParkedTurnSSEEvent("turn.suspended", { obligation_count: 1 });
parked.handleParkedTurnSSEEvent("session_switched", {});
check("会话切换清空横幅", banner.style.display === "none" && banner.textContent === "");

// 5. 元素缺失静默降级（页面裁剪结构不报错）。
nodeMissing = true;
let degraded = true;
try {
  parked.handleParkedTurnSSEEvent("turn.suspended", { obligation_count: 1 });
  parked.handleParkedTurnSSEEvent("turn.resumed", { trigger: "terminal" });
} catch (err) {
  degraded = false;
  console.error(String(err));
}
check("元素缺失静默降级", degraded);

parked.hideParkedTurnBanner(); // 清掉 resumed 的 8s 定时器，避免进程挂起

if (failures > 0) {
  console.error("verify-micro-web-parked-turn: " + failures + " 项失败");
  process.exit(1);
}
console.log("verify-micro-web-parked-turn: 全部断言通过（挂起常驻 / 恢复通知 / 清除时机 / 降级）");
