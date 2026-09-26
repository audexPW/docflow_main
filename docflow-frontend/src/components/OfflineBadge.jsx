import { useEffect, useState, useCallback } from 'react'
import { countOutbox, flushOutbox, onOutboxChange } from '../lib/offlineQueue.js'

// Показывает, сколько документов ждут отправки, и сам досылает их при
// появлении связи. Виден на всех экранах через шапку.
export default function OfflineBadge() {
  const [count, setCount] = useState(0)

  const refresh = useCallback(async () => {
    try {
      setCount(await countOutbox())
    } catch {
      setCount(0)
    }
  }, [])

  useEffect(() => {
    refresh()
    const off = onOutboxChange(refresh)

    async function trySend() {
      await flushOutbox()
      refresh()
    }

    // Досылаем при возврате связи, при возврате в приложение и раз в 30 секунд.
    window.addEventListener('online', trySend)
    document.addEventListener('visibilitychange', trySend)
    const iv = setInterval(trySend, 30000)
    trySend()

    return () => {
      off()
      window.removeEventListener('online', trySend)
      document.removeEventListener('visibilitychange', trySend)
      clearInterval(iv)
    }
  }, [refresh])

  if (count === 0) return null
  return (
    <span className="outbox-badge" title="Документы ждут связи и будут отправлены автоматически">
      Ожидают отправки: {count}
    </span>
  )
}
