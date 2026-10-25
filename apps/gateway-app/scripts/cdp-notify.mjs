// Dev-only native notification smoke test for a running Tauri WebView2.
// Usage: node scripts/cdp-notify.mjs [port]
import { connectPage } from "./cdp-client.mjs";

const port = process.argv[2] || "9222";
const { socket, call } = await connectPage(port);
const result = await call("Runtime.evaluate", {
  expression: `(async () => {
    const api = window.__TAURI__ && window.__TAURI__.notification;
    if (!api) return "notification API unavailable";
    const granted = await api.isPermissionGranted();
    const permission = granted ? "granted" : await api.requestPermission();
    if (permission !== "granted") return "permission=" + permission;
    await api.sendNotification({
      title: "aicli-gateway",
      body: "Native notification bridge is ready"
    });
    return "sent";
  })()`,
  awaitPromise: true,
  returnByValue: true,
});

console.log(result.result.value);
socket.close();
