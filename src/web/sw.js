// POSTERM service worker: caches nothing (an offline wall is useless), shows
// pushes and answers a lab from the notification. Its words come in the payload.

self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', e => e.waitUntil(self.clients.claim()));

self.addEventListener('push', e => {
  const d = e.data ? e.data.json() : {};
  const jobs = [self.registration.showNotification(d.title || '', {
    body: d.body || '',
    tag: d.tag,
    renotify: true,
    icon: '/icon-192.png',
    badge: '/icon-192.png',
    data: d,
    actions: d.reply && d.replyLabel ? [{ action: 'reply', type: 'text', title: d.replyLabel, placeholder: d.replyHint || '' }] : [],
  })];
  if (navigator.setAppBadge && d.n != null) jobs.push(d.n ? navigator.setAppBadge(d.n) : navigator.clearAppBadge());
  e.waitUntil(Promise.all(jobs));
});

self.addEventListener('notificationclick', e => {
  const d = e.notification.data || {};
  e.notification.close();
  if (e.action === 'reply' && e.reply && d.m && d.id) {
    e.waitUntil(fetch(`/m/${encodeURIComponent(d.m)}/api/labs/${encodeURIComponent(d.id)}/keys`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ text: e.reply, enter: true }),
    }));
    return;
  }
  e.waitUntil(self.clients.matchAll({ type: 'window' }).then(list => {
    const open = list[0];
    return open ? open.navigate(d.url || '/').then(c => c && c.focus()) : self.clients.openWindow(d.url || '/');
  }));
});
