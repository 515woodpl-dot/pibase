// pibase service worker. It makes the dashboard installable as a phone or
// desktop app. It stores nothing: every request goes to the server as usual,
// so no project data or keys are ever kept on the device. Only when the
// device is offline does a page load get a short "You're offline" page.
self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', event => event.waitUntil(self.clients.claim()));
self.addEventListener('fetch', event => {
  if (event.request.mode !== 'navigate') return;
  event.respondWith(fetch(event.request).catch(() => new Response(
    '<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>pibase</title>' +
    '<body style="margin:0;display:grid;place-items:center;min-height:100vh;font:16px system-ui,sans-serif;background:#f6f9ff;color:#17203a;text-align:center;padding:24px">' +
    '<div><h1 style="font-size:20px">You\'re offline</h1><p>pibase needs a connection. Check your internet, then reload.</p>' +
    '<p><a href="/app" style="color:#2557d6">Try again</a></p></div>',
    { status: 503, headers: { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store' } })));
});
