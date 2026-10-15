// Service worker for the aicli-gateway console (docs/gateway/07-console.md).
// Caches the app shell so the console opens instantly; API, tunnel and proxy
// traffic is never cached (data must stay live).
// Cache-busting version: bump together with the ?v= query in index.html and
// the register() URL in app.js on every release (Cloudflare edge-caches
// *.js/*.css for 4h, so a new URL is what makes the update land immediately).
const CACHE = "aicli-gateway-shell-v37";
const SHELL = [
  "./",
  "./index.html",
  "./app.js?v=37",
  "./style.css?v=37",
  "./manifest.webmanifest",
  "./icon.svg",
  "./icon-192.png",
  "./icon-512.png",
];

self.addEventListener("install", (event) => {
  event.waitUntil(
    caches.open(CACHE).then((cache) => cache.addAll(SHELL)).then(() => self.skipWaiting()),
  );
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) => Promise.all(keys.filter((key) => key !== CACHE).map((key) => caches.delete(key))))
      .then(() => self.clients.claim()),
  );
});

self.addEventListener("fetch", (event) => {
  const request = event.request;
  if (request.method !== "GET") {
    return;
  }
  const url = new URL(request.url);
  if (url.origin !== self.location.origin) {
    return;
  }
  // Never cache API / tunnel / reverse-proxy responses.
  if (
    url.pathname.indexOf("/api/") !== -1 ||
    url.pathname.indexOf("/gw/") !== -1 ||
    url.pathname.indexOf("/n/") !== -1
  ) {
    return;
  }
  // Navigations are network-first: the HTML decides which versioned ?v= assets
  // load, so pinning a cached copy would freeze releases behind the CDN cache
  // (the previous cache-first policy needed a manual CF purge after each deploy).
  if (request.mode === "navigate") {
    event.respondWith(
      fetch(request)
        .then((response) => {
          const copy = response.clone();
          caches.open(CACHE).then((cache) => cache.put(request, copy)).catch(() => {});
          return response;
        })
        .catch(() => caches.match(request).then((cached) => cached || caches.match("./"))),
    );
    return;
  }
  event.respondWith(
    caches.match(request).then((cached) => {
      if (cached) {
        return cached;
      }
      return fetch(request)
        .then((response) => {
          const copy = response.clone();
          caches.open(CACHE).then((cache) => cache.put(request, copy)).catch(() => {});
          return response;
        })
        .catch(() => cached);
    }),
  );
});

// Web Push: render an OS notification for attention events pushed by the
// gateway (docs/gateway/08-notifications.md).
self.addEventListener("push", (event) => {
  let data = { title: "aicli-gateway", body: "会话需要处理", url: "./" };
  if (event.data) {
    try {
      data = Object.assign(data, event.data.json());
    } catch (err) {
      data.body = event.data.text();
    }
  }
  event.waitUntil(
    self.registration.showNotification(data.title, {
      body: data.body,
      tag: data.tag,
      data: { url: data.url },
      // PNG, not SVG: iOS and Android system notifications do not render SVG
      // icons (the notification still shows, but with a blank/missing icon).
      icon: "./icon-192.png",
      badge: "./badge-96.png",
    }).then(() => {
      // Tell open console pages that the push actually reached this browser.
      // This separates "push never arrived" from "the OS suppressed the toast"
      // when debugging notification delivery.
      return self.clients.matchAll({ type: "window", includeUncontrolled: true });
    }).then((clients) => {
      for (const client of clients) {
        client.postMessage({ type: "push-received", title: data.title, body: data.body });
      }
    }),
  );
});

function urlBase64ToUint8Array(base64) {
  const padding = "=".repeat((4 - (base64.length % 4)) % 4);
  const normalized = (base64 + padding).replace(/-/g, "+").replace(/_/g, "/");
  const raw = atob(normalized);
  const out = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i++) { out[i] = raw.charCodeAt(i); }
  return out;
}

// Browsers rotate or invalidate push endpoints (subscription expiry, iOS PWA
// reinstall, storage eviction). Without this handler the gateway keeps a dead
// endpoint until it happens to receive a 404/410 — and the device silently
// stops receiving notifications. Re-subscribe and upsert the new endpoint.
self.addEventListener("pushsubscriptionchange", (event) => {
  event.waitUntil((async () => {
    try {
      let sub = event.newSubscription || null;
      if (!sub) {
        const res = await fetch("/api/push/config", { credentials: "same-origin" });
        const cfg = await res.json();
        if (!cfg || !cfg.enabled || !cfg.public_key) { return; }
        sub = await self.registration.pushManager.subscribe({
          userVisibleOnly: true,
          applicationServerKey: urlBase64ToUint8Array(cfg.public_key),
        });
      }
      const json = sub.toJSON();
      await fetch("/api/push/subscriptions", {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          endpoint: json.endpoint,
          keys: json.keys || {},
          label: "service-worker re-subscribe",
        }),
      });
    } catch (err) {
      // The console retries the subscription on its next open.
    }
  })());
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const target = (event.notification.data && event.notification.data.url) || "./";
  event.waitUntil(
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then((clients) => {
      for (const client of clients) {
        if (client.url.indexOf(target) !== -1 && "focus" in client) {
          return client.focus();
        }
      }
      if (self.clients.openWindow) {
        return self.clients.openWindow(target);
      }
    }),
  );
});
