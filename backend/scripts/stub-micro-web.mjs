// Provider 编辑弹窗的布局调试服务器（方式 B：静态伺服 + stub API）。
// 用途：本地看模型编辑器真实布局，配套 chrome-devtools 截图迭代。
// 运行：node backend/scripts/stub-micro-web.mjs [port]
import http from "node:http";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { dirname, resolve, normalize } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const webDir = resolve(here, "..", "cmd", "aicli", "commands", "web");
const port = Number(process.argv[2] || 8791);

const cap = (o) => o;
const providers = [
  {
    name: "alpha", protocol: "openai", enabled: true,
    base_url: "https://api.openai.com", api_path: "/v1/chat/completions",
    forward_url: "", default_model: "gpt-5-high",
    api_key_set: true, api_key_source: "key_store", api_key_masked: "sk-...9f2a",
    headers: {},
    models: [
      { name: "gpt-5-high", reasoning_model: true, reasoning_efforts: ["none", "low", "medium", "high"],
        reasoning_effort_budgets: { high: 64000, medium: 16000, low: 4000 },
        default_reasoning_effort: "medium", compact_reasoning_effort: "low",
        max_context_tokens: 400000, max_tokens: 128000, auto_compact_ratio: 0.85,
        auto_compact_token_limit: 320000, auto_compact_mode: "aggressive",
        supports_remote_compact: true, replay_reasoning_content: true,
        input_modalities: ["text", "image"], native_tools: { image_generation: true, images_generations_api: false } },
      { name: "gpt-5-mini", reasoning_model: true, reasoning_efforts: ["low", "medium", "high"],
        default_reasoning_effort: "low", max_context_tokens: 400000, max_tokens: 128000,
        auto_compact_ratio: 0.8, input_modalities: ["text", "image"], native_tools: { image_generation: true, images_generations_api: false } },
      { name: "gpt-4.1", max_context_tokens: 1047576, max_tokens: 32768, input_modalities: ["text", "image"] },
      { name: "gpt-4o-mini", max_context_tokens: 128000, max_tokens: 16384 },
      { name: "o3-mini-high", reasoning_model: true, reasoning_efforts: ["low", "medium", "high"],
        default_reasoning_effort: "medium", max_context_tokens: 200000, max_tokens: 100000,
        replay_reasoning_content: false, input_modalities: ["text"] },
      { name: "o3-mini", reasoning_model: true, reasoning_efforts: ["low", "medium", "high"] },
      { name: "text-embedding-3-large" },
      { name: "dall-e-3", native_tools: { image_generation: true, images_generations_api: true } },
    ],
  },
  { name: "beta", protocol: "anthropic", enabled: false, base_url: "https://api.anthropic.com",
    api_path: "/v1/messages", default_model: "claude-sonnet-4-5", api_key_set: false,
    models: [{ name: "claude-sonnet-4-5", max_context_tokens: 200000, max_tokens: 64000,
      input_modalities: ["text", "image"], replay_reasoning_content: true }] },
];

const config = {
  config_path: "/home/u/.aicli/config.yaml",
  default_provider: "alpha",
  chat: { default_provider: "alpha", default_model: "gpt-5-high", reasoning_effort: "medium" },
  providers,
};

const runtime = {
  current: { provider: "alpha", model: "gpt-5-high", reasoning_effort: "medium" },
  providers: providers.map((p) => ({ name: p.name, models: p.models.map((m) => m.name) })),
};

const MIME = { ".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8", ".json": "application/json", ".svg": "image/svg+xml" };

const json = (res, body) => {
  res.writeHead(200, { "Content-Type": "application/json; charset=utf-8" });
  res.end(JSON.stringify(body));
};

http.createServer(async (req, res) => {
  const url = new URL(req.url, "http://127.0.0.1");
  const p = url.pathname;
  if (p === "/web/api/config") { return json(res, config); }
  if (p === "/web/api/runtime") { return json(res, runtime); }
  if (p === "/web/api/sessions") { return json(res, { sessions: [] }); }
  if (p === "/web/api/skills") { return json(res, { count: 0, skills: [] }); }
  if (p === "/web/api/status") { res.writeHead(200, { "Content-Type": "text/plain" }); return res.end("stub"); }
  if (p.startsWith("/web/api/")) { return json(res, { status: "ok" }); }
  if (p === "/healthz") { return json(res, { ok: true }); }

  // Windows 下 normalize("/x") 会产出 "\x"（带反斜杠），必须剥掉前导分隔符，
  // 否则 resolve() 会把 "\x" 当成盘符绝对路径。
  const rel = normalize(p === "/" ? "/index.html" : p)
    .replace(/^[\\/]+/, "")
    .replace(/^(\.\.[\\/])+/, "");
  try {
    const buf = await readFile(resolve(webDir, rel));
    const ext = rel.slice(rel.lastIndexOf("."));
    res.writeHead(200, { "Content-Type": MIME[ext] || "application/octet-stream" });
    res.end(buf);
  } catch {
    res.writeHead(404, { "Content-Type": "text/plain" });
    res.end("not found: " + rel);
  }
}).listen(port, "127.0.0.1", () => console.log("stub server http://127.0.0.1:" + port + "/"));
