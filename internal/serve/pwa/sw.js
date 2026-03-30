// Service Worker for C-Suite PWA
// Caches the app shell for fast startup and offline shell display.
// Message data is never cached — it must always come from the server.

const CACHE_NAME = 'csuite-v2';
const APP_SHELL = [
  '/',
  '/style.css',
  '/app.js',
  '/voice.js',
  '/manifest.json',
  '/icons/icon-192.svg',
  '/icons/icon-512.svg',
];

// Install — pre-cache the app shell assets.
self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(CACHE_NAME).then((cache) => cache.addAll(APP_SHELL))
  );
  // Activate immediately without waiting for old clients to close.
  self.skipWaiting();
});

// Activate — remove stale caches from previous versions.
self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys().then((names) =>
      Promise.all(
        names
          .filter((name) => name !== CACHE_NAME)
          .map((name) => caches.delete(name))
      )
    )
  );
  // Claim all open clients so the new SW takes effect immediately.
  self.clients.claim();
});

// Fetch — serve app shell from cache (network-first for API requests).
self.addEventListener('fetch', (event) => {
  const url = new URL(event.request.url);

  // API requests and WebSocket upgrades always go to the network.
  if (url.pathname.startsWith('/api/')) {
    return;
  }

  // App shell assets: cache-first, fall back to network.
  event.respondWith(
    caches.match(event.request).then((cached) => {
      if (cached) {
        return cached;
      }
      return fetch(event.request).then((response) => {
        // Cache successful GET responses for future use.
        if (response.ok && event.request.method === 'GET') {
          const clone = response.clone();
          caches.open(CACHE_NAME).then((cache) => cache.put(event.request, clone));
        }
        return response;
      });
    })
  );
});
