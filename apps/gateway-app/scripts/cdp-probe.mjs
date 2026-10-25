// Dev-only probe for a running Tauri WebView2 with remote debugging enabled.
// Usage: node scripts/cdp-probe.mjs [port]
import { connectPage } from "./cdp-client.mjs";

const port = process.argv[2] || "9222";
const { socket, call } = await connectPage(port);
const result = await call("Runtime.evaluate", {
  expression: `JSON.stringify({
    href: location.href,
    title: document.title,
    tauriGlobal: !!window.__TAURI__,
    tauriKeys: window.__TAURI__ ? Object.keys(window.__TAURI__).sort() : [],
    notificationApi: !!(window.__TAURI__ && window.__TAURI__.notification),
    webPushManager: "PushManager" in window,
    webNotificationPermission: typeof Notification === "undefined" ? "unsupported" : Notification.permission,
    serviceWorker: !!navigator.serviceWorker,
    toastText: document.getElementById("toast-host") ? document.getElementById("toast-host").innerText : "",
    bodyText: document.body ? document.body.innerText.slice(0, 300) : ""
  })`,
  returnByValue: true,
});

console.log(result.result.value);
socket.close();
