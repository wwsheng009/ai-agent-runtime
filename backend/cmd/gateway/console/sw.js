// Service worker for the aicli-gateway console (docs/gateway/07-console.md).
// Caches the app shell so the console opens instantly; API, tunnel and proxy
// traffic is never cached (data must stay live).
const CACHE = "aicli-gateway-shell-v1";
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
