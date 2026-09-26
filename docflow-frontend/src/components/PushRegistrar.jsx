import { useEffect } from 'react'
import { useAuth } from '../auth.jsx'
import { registerForPush } from '../lib/push.js'

// Невидимый компонент: в мобильном приложении регистрирует устройство для
// серверных push-уведомлений, как только пользователь вошёл. В браузере ничего
// не делает (там работает опрос статусов + Web Notifications).
export default function PushRegistrar() {
  const { isAuthenticated } = useAuth()

  useEffect(() => {
    if (!isAuthenticated) return
    registerForPush()
  }, [isAuthenticated])

  return null
}
