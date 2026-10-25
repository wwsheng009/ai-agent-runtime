// Dev-only: click the console's notification toggle and report subscription state.
// Usage: node scripts/cdp-push-subscribe.mjs [port]
import { connectPage } from "./cdp-client.mjs";

const port = process.argv[2] || "9222";
const { socket, call } = await connectPage(port);
const result = await call("Runtime.evaluate", {
  expression: `(async () => {
    const button = document.getElementById("push-toggle");
    if (!button) return "push toggle not found";
    button.click();
    await new Promise((resolve) => setTimeout(resolve, 1000));
    const earlyToast = document.getElementById("toast-host")
      ? document.getElementById("toast-host").innerText
      : "";
    const registration = await navigator.serviceWorker.ready;
    let subscription = await registration.pushManager.getSubscription();
    let subscribeError = "";
    if (!subscription) {
      try {
        const cfg = await (await fetch("/api/push/config", { credentials: "same-origin" })).json();
        if (!cfg || !cfg.enabled || !cfg.public_key) {
          return JSON.stringify({ earlyToast, error: "gateway push config disabled" });
        }
        const padding = "=".repeat((4 - (cfg.public_key.length % 4)) % 4);
        const normalized = (cfg.public_key + padding).replace(/-/g, "+").replace(/_/g, "/");
        const raw = atob(normalized);
        const key = new Uint8Array(raw.length);
        for (let i = 0; i < raw.length; i++) key[i] = raw.charCodeAt(i);
        subscription = await registration.pushManager.subscribe({
          userVisibleOnly: true,
          applicationServerKey: key
        });
        const json = subscription.toJSON();
        const saved = await fetch("/api/push/subscriptions", {
          method: "POST",
          credentials: "same-origin",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            endpoint: json.endpoint,
            keys: json.keys || {},
            label: "tauri-webview-probe"
          })
        });
        if (!saved.ok) subscribeError = "server rejected subscription: HTTP " + saved.status;
      } catch (error) {
        subscribeError = error && error.name ? error.name + ": " + error.message : String(error);
      }
    }
    const label = document.getElementById("push-toggle-label");
    return JSON.stringify({
      label: label ? label.textContent : "",
      permission: Notification.permission,
      subscribed: !!subscription,
      endpoint: subscription ? subscription.endpoint.slice(0, 48) : "",
      earlyToast,
      subscribeError
    });
  })()`,
  awaitPromise: true,
  returnByValue: true,
});

console.log(result.result.value);
socket.close();
