import { useEffect, useState, useCallback } from 'react'
import { api } from '../api.js'
import { formatDateTime } from '../lib/format.js'

// Действия из бэкенда → по-русски
const actionLabels = {
  login: 'Вход',
  upload: 'Загрузка',
  edit: 'Правка',
  confirm: 'Подтверждение',
  reprocess: 'Повторное распознавание',
  create_user: 'Создание пользователя',
}

const entityLabels = {
  user: 'пользователь',
  document: 'документ',
}

export default function Audit() {
  const [items, setItems] = useState([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const res = await api.listAudit(200)
      setItems(res.items || [])
      setError('')
    } catch (err) {
      setError(err.message)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    load()
  }, [load])

  return (
    <div>
      <div className="toolbar">
        <h1 style={{ margin: 0 }}>Журнал действий</h1>
        <div className="spacer" />
        <button onClick={load} disabled={loading}>
          Обновить
        </button>
      </div>
      {error && <div className="error">{error}</div>}

      <div className="panel" style={{ padding: 0 }}>
        {loading ? (
          <div className="empty">Загрузка…</div>
        ) : items.length === 0 ? (
          <div className="empty">Записей нет.</div>
        ) : (
          <table>
            <thead>
              <tr>
                <th>Время</th>
                <th>Действие</th>
                <th>Объект</th>
                <th>Детали</th>
              </tr>
            </thead>
            <tbody>
              {items.map((a) => (
                <tr key={a.id}>
                  <td className="muted">{formatDateTime(a.created_at)}</td>
                  <td>{actionLabels[a.action] || a.action}</td>
                  <td>
                    {entityLabels[a.entity] || a.entity}
                    <div className="muted small" style={{ wordBreak: 'break-all' }}>
                      {a.entity_id}
                    </div>
                  </td>
                  <td className="muted small">{formatMeta(a.meta)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  )
}

function formatMeta(meta) {
  if (!meta) return ''
  try {
    const obj = typeof meta === 'string' ? JSON.parse(meta) : meta
    return Object.entries(obj)
      .map(([k, v]) => `${k}: ${v}`)
      .join(', ')
  } catch {
    return ''
  }
}
