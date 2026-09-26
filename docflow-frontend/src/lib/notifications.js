// Уведомления о смене статуса документа.
// В приложении — нативный плагин LocalNotifications (если установлен),
// в браузере — Web Notifications API. Если ничего недоступно или доступ не дан,
// молча ничего не делаем — приложение работает как обычно.

function nativeLocal() {
  return (window.Capacitor && window.Capacitor.Plugins && window.Capacitor.Plugins.LocalNotifications) || null
}

export async function ensureNotificationPermission() {
  const ln = nativeLocal()
  if (ln) {
    try {
      let res = await ln.checkPermissions()
      if (res.display !== 'granted') res = await ln.requestPermissions()
      return res.display === 'granted'
    } catch {
      return false
    }
  }
  if (typeof Notification === 'undefined') return false
  if (Notification.permission === 'granted') return true
  if (Notification.permission === 'denied') return false
  try {
    return (await Notification.requestPermission()) === 'granted'
  } catch {
    return false
  }
}

export function notificationsBlocked() {
  if (nativeLocal()) return false
  return typeof Notification !== 'undefined' && Notification.permission === 'denied'
}

export async function notify(title, body) {
  const ln = nativeLocal()
  if (ln) {
    try {
      await ln.schedule({
        notifications: [{ id: Date.now() % 2000000000, title, body, schedule: { at: new Date(Date.now() + 100) } }],
      })
    } catch {
      /* игнорируем — не критично */
    }
    return
  }
  if (typeof Notification !== 'undefined' && Notification.permission === 'granted') {
    try {
      new Notification(title, { body })
    } catch {
      /* Safari требует ServiceWorker для веб-пушей — тихо пропускаем */
    }
  }
}
