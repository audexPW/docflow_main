import { useEffect, useState, useCallback } from 'react'
import { Link } from 'react-router-dom'
import { api } from '../api.js'
import { docTypeLabel, onecStatusLabel } from '../lib/labels.js'
import { formatDateTime, formatSize } from '../lib/format.js'
import { useAuth } from '../auth.jsx'

// Архив — документы, по которым 1С отчиталась о проведении. Работа над ними
// закончена, поэтому в рабочем списке их больше нет: он рос бесконечно, и
// бухгалтеру приходилось глазами отделять «уже в учёте» от «в работе».
//
// Хранятся они ограниченный срок (ARCHIVE_KEEP_DAYS), после чего удаляются
// вместе с файлом и своей выгрузкой в папке компании. Срок виден в списке —
// иначе удаление выглядело бы как пропажа документа.

const PAGE_SIZE = 100

function daysLeft(archivedAt, keepDays) {
  if (!archivedAt || !keepDays) return null
  const archived = new Date(archivedAt).getTime()
  if (isNaN(archived)) return null
  const deadline = archived + keepDays * 24 * 60 * 60 * 1000
  return Math.ceil((deadline - Date.now()) / (24 * 60 * 60 * 1000))
}

function RetentionCell({ archivedAt, keepDays }) {
  const left = daysLeft(archivedAt, keepDays)
  if (left === null) return <span className="muted">бессрочно</span>
  if (left <= 0) return <span className="muted">удаляется</span>
  if (left <= 3) return <strong>{left} дн.</strong>
  return <span className="muted">{left} дн.</span>
}

export default function Archive() {
  const { role } = useAuth()
  const canRestore = role === 'accountant' || role === 'admin'

  const [companyId, setCompanyId] = useState('')
  const [companies, setCompanies] = useState([])
  const [items, setItems] = useState([])
  const [total, setTotal] = useState(0)
  const [keepDays, setKeepDays] = useState(0)
  const [loading, setLoading] = useState(true)
  const [loadingMore, setLoadingMore] = useState(false)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const res = await api.listDocuments({
        archive: 'only',
        companyId: companyId || undefined,
        limit: PAGE_SIZE,
        offset: 0,
      })
      setItems(res.items || [])
      setTotal(res.total ?? (res.items || []).length)
      setKeepDays(res.archive_keep_days || 0)
    } catch (err) {
      setError(err.message)
    } finally {
      setLoading(false)
    }
  }, [companyId])

  const loadMore = useCallback(async () => {
    setLoadingMore(true)
    try {
      const res = await api.listDocuments({
        archive: 'only',
        companyId: companyId || undefined,
        limit: PAGE_SIZE,
        offset: items.length,
      })
      const next = res.items || []
      setItems((prev) => {
        const seen = new Set(prev.map((d) => d.id))
        return prev.concat(next.filter((d) => !seen.has(d.id)))
      })
      setTotal(res.total ?? items.length + next.length)
    } catch (err) {
      setError(err.message)
    } finally {
      setLoadingMore(false)
    }
  }, [companyId, items.length])

  useEffect(() => {
    let cancelled = false
    api
      .listCompanies()
      .then((res) => {
        if (!cancelled) setCompanies(res.items || [])
      })
      .catch(() => {})
    return () => {
      cancelled = true
    }
  }, [])

  useEffect(() => {
    load()
  }, [load])

  // Возврат в работу: 1С отчиталась о проведении ошибочно или документ
  // переоткрыли. Отсчёт срока хранения при этом обнуляется.
  async function restore(doc) {
    if (!window.confirm('Вернуть «' + doc.original_name + '» из архива в работу?')) return
    setBusy(doc.id)
    setError('')
    try {
      await api.unarchiveDocument(doc.id)
      setItems((prev) => prev.filter((d) => d.id !== doc.id))
      setTotal((n) => Math.max(0, n - 1))
    } catch (err) {
      setError(err.message)
    } finally {
      setBusy('')
    }
  }

  return (
    <div>
      <div className="toolbar">
        <h1 style={{ margin: 0 }}>Архив</h1>
        <div className="spacer" />
        {companies.length > 1 && (
          <select
            value={companyId}
            onChange={(e) => setCompanyId(e.target.value)}
            style={{ width: 'auto' }}
            aria-label="Компания"
          >
            <option value="">Все компании</option>
            {companies.map((c) => (
              <option key={c.id} value={c.id}>
                {c.name}
              </option>
            ))}
          </select>
        )}
        <button onClick={load} disabled={loading}>
          Обновить
        </button>
      </div>

      <p className="small muted" style={{ marginTop: 0 }}>
        Документы, проведённые в 1С.{' '}
        {keepDays > 0
          ? 'Хранятся ' + keepDays + ' дн. с момента проведения, затем удаляются вместе с файлом.'
          : 'Срок хранения не задан — документы не удаляются.'}
      </p>

      {error && <div className="error">{error}</div>}

      <div className="panel" style={{ padding: 0 }}>
        {loading ? (
          <div className="empty">Загрузка…</div>
        ) : items.length === 0 ? (
          <div className="empty">
            Архив пуст. Документы попадают сюда, когда 1С сообщает о проведении.
          </div>
        ) : (
          <table>
            <thead>
              <tr>
                <th>Файл</th>
                <th>Компания</th>
                <th>Тип</th>
                <th>Размер</th>
                <th>Ответ 1С</th>
                <th>Проведён</th>
                <th>Удаление через</th>
                {canRestore && <th style={{ width: 120 }} />}
              </tr>
            </thead>
            <tbody>
              {items.map((d) => (
                <tr key={d.id}>
                  <td>
                    <Link to={'/documents/' + d.id}>{d.original_name}</Link>
                  </td>
                  <td className="muted">{d.company_name || '—'}</td>
                  <td>{d.recognition ? docTypeLabel(d.recognition.doc_type) : '—'}</td>
                  <td>{formatSize(d.size_bytes)}</td>
                  <td className="muted">
                    {onecStatusLabel(d.onec_status)}
                    {d.onec_ref ? ' · ' + d.onec_ref : ''}
                  </td>
                  <td className="muted">{formatDateTime(d.archived_at)}</td>
                  <td>
                    <RetentionCell archivedAt={d.archived_at} keepDays={keepDays} />
                  </td>
                  {canRestore && (
                    <td>
                      <button onClick={() => restore(d)} disabled={busy === d.id}>
                        В работу
                      </button>
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {!loading && items.length > 0 && (
        <div className="toolbar" style={{ marginTop: 10 }}>
          <span className="small muted">
            Показано {items.length} из {total}
          </span>
          <div className="spacer" />
          {items.length < total && (
            <button onClick={loadMore} disabled={loadingMore}>
              {loadingMore ? 'Загружаю…' : 'Показать ещё'}
            </button>
          )}
        </div>
      )}
    </div>
  )
}
