// Service worker for the aicli-gateway console (docs/gateway/07-console.md).
// Caches the app shell so the console opens instantly; API, tunnel and proxy
// traffic is never cached (data must stay live).
const CACHE = "aicli-gateway-shell-v9";
const SHELL = ["./", "./index.html", "./app.js", "./style.css", "./manifest.webmanifest", "./icon.svg"];

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
      icon: "./icon.svg",
      badge: "./icon.svg",
    }),
  );
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
