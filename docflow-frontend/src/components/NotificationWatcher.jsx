import { useEffect } from 'react'
import { useAuth } from '../auth.jsx'
import { api } from '../api.js'
import { ensureNotificationPermission, notify } from '../lib/notifications.js'
import { statusLabel } from '../lib/labels.js'

const POLL_MS = 20000
const SEEN_KEY = 'docflow_seen_status'

// Статусы, о которых имеет смысл сообщать пользователю.
const NOTIFY_ON = {
  needs_review: true, // распознан, ждёт проверки
  needs_input: true, // распознан частично, нужно уточнить поля
  exported: true, // ушёл в 1С
  failed: true, // ошибка
}

function loadSeen() {
  try {
    return JSON.parse(localStorage.getItem(SEEN_KEY) || '{}')
  } catch {
    return {}
  }
}

function saveSeen(map) {
  try {
    localStorage.setItem(SEEN_KEY, JSON.stringify(map))
  } catch {
    /* приватный режим и т.п. — не критично */
  }
}

// Невидимый компонент: пока пользователь в системе, периодически проверяет
// свои документы и уведомляет, когда статус изменился на значимый.
export default function NotificationWatcher() {
  const { isAuthenticated } = useAuth()

  useEffect(() => {
    if (!isAuthenticated) return
    let timer
    let stopped = false

    ensureNotificationPermission()

    async function tick() {
      try {
        const { items = [] } = await api.listDocuments({ limit: 100 })
        const seen = loadSeen()
        const firstRun = Object.keys(seen).length === 0
        for (const d of items) {
          const prev = seen[d.id]
          // На первом проходе только запоминаем, чтобы не сыпать
          // уведомлениями по уже загруженным документам.
          if (!firstRun && prev && prev !== d.status && NOTIFY_ON[d.status]) {
            notify('ПартнерБухгалтер', `${d.original_name}: ${statusLabel(d.status)}`)
          }
          seen[d.id] = d.status
        }
        saveSeen(seen)
      } catch {
        /* нет связи — просто ждём следующего тика */
      }
      if (!stopped) timer = setTimeout(tick, POLL_MS)
    }

    tick()
    return () => {
      stopped = true
      clearTimeout(timer)
    }
  }, [isAuthenticated])

  return null
}
