// 模型编辑器布局探针（Playwright + stub API）：在真实 Chromium 里量测模型
// 编辑器几何。布局靠肉眼判断很容易漏掉「flex item 顶破容器」「标签被省略号
// 截断」这类问题，这里把它们变成可断言的数字。
//
// 运行：
//   node backend/scripts/stub-micro-web.mjs 8792                  # 终端 A
//   node backend/scripts/probe-model-editor-layout.mjs            # 宽屏 1280x900
//   node backend/scripts/probe-model-editor-layout.mjs --narrow    # 窄屏 420x820
//   AICLI_SHOT=out.png node backend/scripts/probe-model-editor-layout.mjs
//
// 关注指标：
//   body.scrollRatio  面板内容高 / 视口高。越大越难用（改版前是 2.86）。
//   anyOverflowX      横向溢出（flex item 顶破容器的典型症状）。
//   clippedLabels     被省略号截断的中文名 / config key。
import { chromium } from "../../frontend/node_modules/@playwright/test/index.mjs";

const BASE = process.env.AICLI_STUB || "http://127.0.0.1:8792/";
const NARROW = process.argv.includes("--narrow");
const viewport = NARROW ? { width: 420, height: 820 } : { width: 1280, height: 900 };
const MODELS = ["gpt-5-high", "gpt-5-mini", "gpt-4.1", "gpt-4o-mini",
  "o3-mini-high", "o3-mini", "text-embedding-3-large", "dall-e-3"];

const browser = await chromium.launch();
const page = await browser.newPage({ viewport });
page.on("pageerror", (e) => console.log("PAGE ERROR:", e.message));
await page.goto(BASE, { waitUntil: "networkidle" });

await page.evaluate(async (models) => {
  const ca = await import("./js/config-admin.js");
  const pe = await import("./js/provider-editor.js");
  document.getElementById("tab-config-btn").click();
  ca.loadConfigAdmin();
  for (let i = 0; i < 40 && !ca.configData; i++) { await new Promise((r) => setTimeout(r, 100)); }
  pe.openProviderEditor("alpha");
  await new Promise((r) => setTimeout(r, 150));
  const ta = document.getElementById("cfg-provider-models");
  ta.value = models.join("\n");
  ta.dispatchEvent(new Event("input", { bubbles: true }));
  await new Promise((r) => setTimeout(r, 100));
  document.querySelector('#cfg-model-list [data-model="gpt-5-high"]').click();
  await new Promise((r) => setTimeout(r, 200));
}, MODELS);

const m = await page.evaluate(() => {
  const wrap = document.querySelector(".cfg-model-editor");
  const panel = document.getElementById("cfg-model-editor");
  const body = panel.querySelector(".cfg-model-editor-body");
  const list = document.getElementById("cfg-model-list");
  const pr = panel.getBoundingClientRect();
  const lr = list.getBoundingClientRect();
  const w = (e) => Math.round(e.getBoundingClientRect().width);
  const h = (e) => Math.round(e.getBoundingClientRect().height);
  const clipped = (sel) => Array.from(panel.querySelectorAll(sel))
    .filter((e) => e.scrollWidth > e.clientWidth + 1)
    .map((e) => e.textContent.trim());
  return {
    viewport: { w: window.innerWidth, h: window.innerHeight },
    list: { w: w(list), h: h(list) },
    panel: { w: w(panel), h: h(panel) },
    equalHeight: Math.abs(lr.height - pr.height) < 1,
    stacked: lr.bottom <= pr.top + 1, // 窄屏断点下应上下堆叠
    gridCols: getComputedStyle(wrap).gridTemplateColumns.split(" ").length,
    body: {
      clientH: body.clientHeight,
      scrollH: body.scrollHeight,
      scrollRatio: +(body.scrollHeight / Math.max(1, body.clientHeight)).toFixed(2),
    },
    sections: Array.from(panel.querySelectorAll(".cfg-model-section")).map((s) => ({
      title: s.querySelector(".cfg-model-section-title").textContent.trim(),
      h: h(s),
      cols: getComputedStyle(s.querySelector(".cfg-model-grid")).gridTemplateColumns.split(" ").length,
    })),
    fields: panel.querySelectorAll("[data-field]").length,
    switches: panel.querySelectorAll(".cfg-switch").length,
    sub: (panel.querySelector(".cfg-model-editor-sub") || {}).textContent || null,
    anyOverflowX: [wrap, panel, body, list].some((e) => e.scrollWidth > e.clientWidth + 1),
    docOverflowX: document.documentElement.scrollWidth > window.innerWidth + 1,
    clippedLabels: clipped(".cfg-label"),
    clippedKeys: clipped(".cfg-key"),
    // 触摸目标高度（窄屏可点按区域）
    switchH: Array.from(panel.querySelectorAll(".cfg-switch")).map(h),
    // 输入模态 chip 组：全部渲染、已保存值处于勾选态、不溢出、触摸高度够
    modalities: {
      rendered: Array.from(panel.querySelectorAll("[data-modality]"))
        .map((b) => (b.checked ? b.getAttribute("data-modality") : null)).filter(Boolean),
      vocabCount: panel.querySelectorAll("[data-modality]").length,
      hidden: (panel.querySelector('[data-field="input_modalities"]') || {}).value,
      note: (panel.querySelector("[data-modality-note]") || {}).textContent || null,
      chipH: Array.from(panel.querySelectorAll(".cfg-mod")).map(h),
      overflowX: (() => {
        const g = panel.querySelector(".cfg-modalities");
        return g ? g.scrollWidth > g.clientWidth + 1 : null;
      })(),
    },
  };
});

console.log(JSON.stringify(m, null, 2));

// 汇总成一眼能看的结论
const problems = [];
if (m.anyOverflowX) { problems.push("横向溢出"); }
if (m.docOverflowX) { problems.push("文档横向溢出"); }
if (m.clippedLabels.length) { problems.push("中文名被截断: " + m.clippedLabels.join(", ")); }
if (m.clippedKeys.length) { problems.push("config key 被截断: " + m.clippedKeys.join(", ")); }
if (NARROW && m.switchH.some((v) => v < 28)) { problems.push("窄屏开关触摸目标偏小: " + m.switchH.join(",")); }
if (m.modalities.vocabCount !== 5) { problems.push("模态 chip 应渲染 5 项，实为 " + m.modalities.vocabCount); }
if (m.modalities.overflowX) { problems.push("模态 chip 组横向溢出"); }
if (m.modalities.rendered.join(",") !== "text,image") {
  problems.push("模态勾选态与已保存值不符: " + JSON.stringify(m.modalities.rendered));
}
if (m.modalities.hidden !== "text, image") {
  problems.push("模态隐藏输入未同步: " + JSON.stringify(m.modalities.hidden));
}
console.log(problems.length ? "PROBLEMS: " + problems.join(" | ") : "OK: no layout problems detected");

const out = process.env.AICLI_SHOT;
if (out) {
  // 模型编辑器在弹窗正文靠下，且输入模态在最后一个分区：先滚到 chip 组再截，
  // 否则截到的是上方令牌预算区，看不到本次改动的目标。
  await page.locator(".cfg-modalities").scrollIntoViewIfNeeded();
  await page.waitForTimeout(150);
  await page.locator(".cfg-model-editor").screenshot({ path: out });
  console.log("screenshot -> " + out);
}
await browser.close();
