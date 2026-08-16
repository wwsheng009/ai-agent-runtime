import { expect, type Page, test } from "@playwright/test";

// e2e acceptance coverage for the workspace streaming chat surface
// (Phase 1: G1 reasoning-first, G2 tool card lifecycle, G5 scroll-follow,
// G6 phase status strip, G8 stopped markers / interruption).
//
// The backend is the scripted mock server in ./mock-server.mjs; scripts are
// selected by keywords in the prompt text.

const composer = (page: Page) => page.locator(".app-chat-input");

async function sendPrompt(page: Page, text: string) {
  await composer(page).fill(text);
  await composer(page).press("Control+Enter");
}

async function waitForPromptVisible(page: Page) {
  await expect(composer(page)).toBeVisible({ timeout: 30_000 });
}

async function scrollMetrics(page: Page) {
  return page.evaluate(() => {
    const log = document.querySelector('[role="log"]');
    const candidates = [
      ...(log?.parentElement ? [log.parentElement] : []),
      ...Array.from(
        document.querySelectorAll(
          "main, [class*='scroll'], [class*='list'], [class*='overflow']",
        ),
      ),
      document.scrollingElement,
    ];
    for (const el of candidates) {
      if (!el) continue;
      const style = getComputedStyle(el);
      if (style.overflowY === "hidden" || style.overflow === "hidden") {
        continue;
      }
      if (el.scrollHeight > el.clientHeight + 120) {
        return {
          top: Math.round(el.scrollTop),
          max: Math.round(el.scrollHeight - el.clientHeight),
          height: el.scrollHeight,
          client: el.clientHeight,
        };
      }
    }
    return { top: 0, max: 0, height: 0, client: 0 };
  });
}

test.beforeEach(async ({ page }) => {
  await page.goto("/workspace");
  await waitForPromptVisible(page);
});

test("G1: reasoning renders live before the answer chunk, then completes", async ({
  page,
}) => {
  await sendPrompt(page, "capital of france (reasoning)");

  // reasoning row appears while the answer is still pending
  const reasoningButton = page.getByRole("button", { name: /Reasoning/ });
  await expect(reasoningButton).toBeVisible({ timeout: 15_000 });
  await expect(
    page.getByText("Checking whether the user request needs a tool").first(),
  ).toBeVisible();

  // reasoning is still not followed by the answer yet
  await expect(page.getByText("The capital of France is Paris.")).not.toBeVisible();

  // expand reasoning to reveal the full transcript
  await reasoningButton.click();
  const reasoningTranscript = page.locator("pre");
  await expect(reasoningTranscript).toContainText("No tool needed, drafting the answer");
  await expect(reasoningTranscript).toContainText("Writing the final answer now");

  // answer chunk lands afterwards
  await expect(page.getByText("The capital of France is Paris.")).toBeVisible({
    timeout: 15_000,
  });
});

test("G2: tool card walks Started -> Running -> Finished with visible result", async ({
  page,
}) => {
  await sendPrompt(page, "use the tool to look it up");

  // phase strip reports the tool phase
  await expect(page.getByText("Calling tools…")).toBeVisible({ timeout: 15_000 });

  // tool identity is shown
  await expect(page.getByText("web_search")).toBeVisible();

  // badge lifecycle
  const startedBadge = page.getByText("Started", { exact: true }).first();
  await expect(startedBadge).toBeVisible({ timeout: 10_000 });
  const runningBadge = page.getByText("Running", { exact: true }).first();
  await expect(runningBadge).toBeVisible({ timeout: 10_000 });
  await expect(page.getByText("Finished", { exact: true }).first()).toBeVisible({
    timeout: 10_000,
  });

  // tool args are displayed (collapsible section) and the result shows
  await expect(page.getByText(/capital of France/).first()).toBeVisible();
  await expect(page.getByText("Paris", { exact: true }).first()).toBeVisible();

  // final assistant text arrives
  await expect(page.getByText("Paris is the capital of France.")).toBeVisible({
    timeout: 15_000,
  });
});

test("G5: list auto-follows the stream, pauses while scrolled up, resumes at bottom", async ({
  page,
}) => {
  await sendPrompt(page, "scroll long answer");

  // stream starts; the list should follow so the newest chunk stays near the bottom
  await expect(page.getByText(/part 1/)).toBeVisible({ timeout: 15_000 });
  await expect(page.getByText(/part 8/)).toBeVisible({ timeout: 15_000 });

  let metrics = await scrollMetrics(page);
  expect(metrics.max).toBeGreaterThan(200); // content overflowed the viewport
  expect(Math.abs(metrics.max - metrics.top)).toBeLessThanOrEqual(120); // following

  // user scrolls up: follow must pause
  const list = page.locator('[role="log"]').locator("..");
  await list.hover();
  await page.mouse.wheel(0, -1600);
  await page.waitForTimeout(300);
  const scrolledTop = (await scrollMetrics(page)).top;
  expect(scrolledTop).toBeLessThan((await scrollMetrics(page)).max - 150);

  await expect(page.getByText(/part 16/)).toBeVisible({ timeout: 15_000 });
  const paused = await scrollMetrics(page);
  expect(Math.abs(paused.top - scrolledTop)).toBeLessThanOrEqual(60); // did not follow

  // user returns to the bottom: follow resumes
  await page.mouse.wheel(0, 4000);
  await page.waitForTimeout(300);
  await expect(page.getByText(/part 24/)).toBeVisible({ timeout: 15_000 });
  metrics = await scrollMetrics(page);
  expect(Math.abs(metrics.max - metrics.top)).toBeLessThanOrEqual(120); // following again
});

test("G6: phase strip reflects the stream lifecycle", async ({ page }) => {
  await sendPrompt(page, "capital of france (phase strip)");

  // an explicit phase label appears while the turn is active
  await expect(page.getByText(/Waiting for first output|Streaming output|Calling tools|Finalizing turn|Connecting to runtime/)).toBeVisible({
    timeout: 15_000,
  });

  // the strip disappears once the turn completes
  await expect(page.getByText("The capital of France is Paris.")).toBeVisible({
    timeout: 15_000,
  });
  await expect(page.getByText("Streaming output…")).not.toBeVisible({ timeout: 15_000 });
});

test("G8a: server-side interruption surfaces a stopped marker", async ({ page }) => {
  await sendPrompt(page, "trigger the error case");

  // partial text arrives before the interruption
  await expect(page.getByText("The capital of France is ")).toBeVisible({
    timeout: 15_000,
  });

  // the stream error is surfaced in the message surface
  await expect(page.getByText("stream interrupted by test")).toBeVisible({
    timeout: 15_000,
  });
});

test("G8b: the Stop shortcut aborts a live stream", async ({ page }) => {
  await sendPrompt(page, "interrupt this stream");

  // chunks are flowing
  await expect(page.getByText(/Interruptible chunk 1/)).toBeVisible({ timeout: 15_000 });
  await expect(page.getByText(/Interruptible chunk 3/)).toBeVisible({ timeout: 15_000 });

  // Ctrl+Enter while responding stops the turn (same shortcut as submit)
  await composer(page).press("Control+Enter");

  // the assistant message is marked stopped instead of completed
  await expect(page.getByText("Stopped", { exact: true }).first()).toBeVisible();
});
