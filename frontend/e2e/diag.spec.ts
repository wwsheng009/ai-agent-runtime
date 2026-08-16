import { test } from "@playwright/test";

test("diag2: submit flow", async ({ page }) => {
  page.on("console", (m) => {
    if (m.type() === "error") console.log("[console]", m.text().slice(0, 300));
  });
  page.on("pageerror", (e) => console.log("[pageerror]", (e.stack ?? String(e)).slice(0, 1500)));
  page.on("request", (r) => {
    if (r.url().includes("/api/")) console.log("[req]", r.method(), r.url().replace("http://127.0.0.1:5193", ""));
  });
  page.on("response", (r) => {
    if (r.url().includes("/api/") && r.request().method() === "POST") {
      console.log("[resp]", r.status(), r.url().replace("http://127.0.0.1:5193", ""));
    }
  });

  await page.goto("/workspace", { timeout: 60_000 });
  await page.waitForSelector(".app-chat-input", { timeout: 60_000 });
  console.log("[nav] url =", page.url());

  await page.locator(".app-chat-input").fill("capital of france (reasoning)");
  await page.locator(".app-chat-input").press("Control+Enter");
  console.log("[submit] pressed");

  for (let i = 0; i < 12; i += 1) {
    await page.waitForTimeout(2000);
    const state = await page.evaluate(() => ({
      body: document.body.innerText.slice(0, 300).replace(/\n/g, " | "),
      textareas: document.querySelectorAll("textarea").length,
    }));
    console.log(`[t+${(i + 1) * 2}s]`, JSON.stringify(state));
  }
});
