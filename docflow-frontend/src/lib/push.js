// Регистрация устройства для серверных push-уведомлений (FCM/APNs) через плагин
// @capacitor/push-notifications. Работает только в нативном приложении; в браузере
// молча ничего не делает (там действует опрос статусов + Web Notifications).
//
// Полученный токен привязывается к пользователю на бэкенде (POST /api/devices),
// откуда сервер и рассылает уведомления при смене статуса документа.

import { api } from '../api.js'

function plugin() {
  return (window.Capacitor && window.Capacitor.Plugins && window.Capacitor.Plugins.PushNotifications) || null
}

function platform() {
  const c = window.Capacitor
  if (c && typeof c.getPlatform === 'function') return c.getPlatform() // 'ios' | 'android' | 'web'
  return 'web'
}

let registered = false
let currentToken = ''

// registerForPush запрашивает разрешение, регистрируется в APNs/FCM и отправляет
// токен на сервер. Повторные вызовы безопасны.
export async function registerForPush() {
  const pn = plugin()
  if (!pn || registered) return
  const plat = platform()
  if (plat !== 'ios' && plat !== 'android') return

  try {
    let perm = await pn.checkPermissions()
    if (perm.receive !== 'granted') perm = await pn.requestPermissions()
    if (perm.receive !== 'granted') return

    // Токен приходит асинхронно в событии 'registration'.
    pn.addListener('registration', async (t) => {
      const token = t && t.value
      if (!token) return
      currentToken = token
      try {
        await api.registerDevice({ platform: plat, token })
      } catch {
        /* нет связи — повторится при следующем входе */
      }
    })
    pn.addListener('registrationError', () => {
      /* платформенная ошибка регистрации — не критично */
    })

    await pn.register()
    registered = true
  } catch {
    /* плагин недоступен или пользователь отказал — тихо выходим */
  }
}

// unregisterForPush отвязывает токен от аккаунта (например, при выходе).
export async function unregisterForPush() {
  if (!currentToken) return
  try {
    await api.unregisterDevice(currentToken)
  } catch {
    /* не критично */
  }
  currentToken = ''
  registered = false
}
