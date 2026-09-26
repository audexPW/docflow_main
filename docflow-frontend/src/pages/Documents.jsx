import { useEffect, useState, useCallback, useMemo } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { api } from '../api.js'
import StatusBadge from '../components/StatusBadge.jsx'
import { docTypeLabel, docTypeInfo, statusLabels } from '../lib/labels.js'
import { formatDateTime, formatSize } from '../lib/format.js'
import { useAuth } from '../auth.jsx'

// Размер порции. Список догружается кнопкой «Показать ещё»: раньше страница
// показывала первые 50 документов и на этом заканчивалась — добраться до
// остальных было нечем.
const PAGE_SIZE = 100

const statusFilters = [
  ['', 'Все'],
  ['received', statusLabels.received],
  ['processing', statusLabels.processing],
  ['needs_review', statusLabels.needs_review],
  ['needs_input', statusLabels.needs_input],
  ['needs_approval', statusLabels.needs_approval],
  ['rejected', statusLabels.rejected],
  ['confirmed', statusLabels.confirmed],
  ['exported', statusLabels.exported],
  ['failed', statusLabels.failed],
]

// Почему документ нельзя отправить в 1С. Возвращает пустую строку, если можно.
// Правила те же, что и на карточке документа: без распознавания отправлять
// нечего, выгруженное второй раз не отправляем, а тип без объекта-приёмника
// в 1С попросту некуда класть.
function blockReason(d, canApprove) {
  if (d.status === 'exported') return 'Уже выгружен в 1С'
  // Для главбуха и админа документ, ждущий согласования, не помеха: массовая
  // отправка для них и есть согласование. Остальным он недоступен.
  if (d.status === 'needs_approval' && !canApprove) return 'Ждёт согласования главбухом'
  if (d.status === 'rejected' && !canApprove) return 'Возвращён главбухом'
  if (!d.recognition) return 'Ещё не распознан'
  const info = docTypeInfo(d.recognition.doc_type)
  if (info && !info.routable) {
    return 'Для типа «' + docTypeLabel(d.recognition.doc_type) + '» не задан объект-приёмник в 1С'
  }
  return ''
}

export default function Documents() {
  const navigate = useNavigate()
  const { role } = useAuth()
  const canApprove = role === 'accountant' || role === 'admin'
  const [status, setStatus] = useState('')
  const [companyId, setCompanyId] = useState('')
  const [companies, setCompanies] = useState([])
  const [items, setItems] = useState([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(true)
  const [loadingMore, setLoadingMore] = useState(false)
  const [error, setError] = useState('')

  const [selected, setSelected] = useState(() => new Set())
  const [sending, setSending] = useState(null) // { done, total }
  const [report, setReport] = useState(null) // { ok, failed: [{ name, message }] }

  // Причина, по которой галочка не ставится. disabled-инпут не отдаёт браузеру
  // ни клика, ни подсказки title, поэтому неактивный флажок рисуется обычным
  // (только для чтения) внутри span, а причина показывается всплывающей строкой.
  const [hint, setHint] = useState(null) // { text }
  useEffect(() => {
    if (!hint) return
    const t = setTimeout(() => setHint(null), 5000)
    return () => clearTimeout(t)
  }, [hint])

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const res = await api.listDocuments({
        status: status || undefined,
        companyId: companyId || undefined,
        limit: PAGE_SIZE,
        offset: 0,
      })
      setItems(res.items || [])
      setTotal(res.total ?? (res.items || []).length)
      setSelected(new Set())
    } catch (err) {
      setError(err.message)
    } finally {
      setLoading(false)
    }
  }, [status, companyId])

  // Догрузка следующей порции. Уже выбранные документы не сбрасываем: человек
  // мог отметить половину списка и только потом решить посмотреть остальное.
  const loadMore = useCallback(async () => {
    setLoadingMore(true)
    setError('')
    try {
      const res = await api.listDocuments({
        status: status || undefined,
        companyId: companyId || undefined,
        limit: PAGE_SIZE,
        offset: items.length,
      })
      const next = res.items || []
      setItems((prev) => {
        // Пока грузилась страница, список мог измениться — защищаемся от
        // дублей по id, иначе React отрисует две одинаковые строки.
        const seen = new Set(prev.map((d) => d.id))
        return prev.concat(next.filter((d) => !seen.has(d.id)))
      })
      setTotal(res.total ?? items.length + next.length)
    } catch (err) {
      setError(err.message)
    } finally {
      setLoadingMore(false)
    }
  }, [status, companyId, items.length])

  // Список компаний для фильтра. Каждому приходят только свои: главбуху — те,
  // за которыми его закрепили, админу — все.
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

  const selectable = useMemo(
    () => items.filter((d) => !blockReason(d, canApprove)),
    [items, canApprove]
  )
  const allSelected = selectable.length > 0 && selected.size === selectable.length

  function toggleOne(id) {
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  function toggleAll() {
    setSelected(allSelected ? new Set() : new Set(selectable.map((d) => d.id)))
  }

  // Отправляем по одному: так каждый документ проходит те же проверки, что и
  // при подтверждении из карточки, а по сбоям видно, какой именно не прошёл.
  async function exportSelected() {
    const queue = items.filter((d) => selected.has(d.id))
    if (queue.length === 0) return
    const company = companies.find((c) => c.id === companyId)
    const scopeText = company ? ' (компания: ' + company.name + ')' : ''
    if (!window.confirm('Отправить в 1С документов: ' + queue.length + scopeText + '?')) return

    setReport(null)
    setSending({ done: 0, total: queue.length })

    let ok = 0
    const failed = []
    for (let i = 0; i < queue.length; i++) {
      const d = queue[i]
      try {
        // Документ, ждущий решения главбуха, отправляется через согласование:
        // это одно действие — «согласовано и в 1С».
        if (canApprove && (d.status === 'needs_approval' || d.status === 'rejected')) {
          await api.approveDocument(d.id, '')
        } else {
          await api.confirmDocument(d.id)
        }
        ok++
      } catch (err) {
        failed.push({ name: d.original_name, message: err.message })
      }
      setSending({ done: i + 1, total: queue.length })
    }

    setSending(null)
    setReport({ ok, failed })
    await load()
  }

  const blocked = items.length - selectable.length

  return (
    <div>
      <div className="toolbar">
        <h1 style={{ margin: 0 }}>Документы</h1>
        <div className="spacer" />
        {companies.length > 1 && (
          <select
            value={companyId}
            onChange={(e) => setCompanyId(e.target.value)}
            style={{ width: 'auto' }}
            disabled={Boolean(sending)}
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
        <select
          value={status}
          onChange={(e) => setStatus(e.target.value)}
          style={{ width: 'auto' }}
          disabled={Boolean(sending)}
        >
          {statusFilters.map(([v, l]) => (
            <option key={v} value={v}>
              {l}
            </option>
          ))}
        </select>
        <button onClick={load} disabled={loading || Boolean(sending)}>
          Обновить
        </button>
        <button
          className="btn-primary"
          onClick={() => navigate('/upload')}
          disabled={Boolean(sending)}
        >
          Загрузить
        </button>
      </div>

      {error && <div className="error">{error}</div>}

      {(selected.size > 0 || sending) && (
        <div className="panel" style={{ marginBottom: 12 }}>
          <div className="toolbar" style={{ marginBottom: 0 }}>
            <strong>
              {sending
                ? 'Отправляю в 1С: ' + sending.done + ' из ' + sending.total + '…'
                : 'Выбрано документов: ' + selected.size}
            </strong>
            <div className="spacer" />
            <button onClick={() => setSelected(new Set())} disabled={Boolean(sending)}>
              Снять выделение
            </button>
            <button className="btn-primary" onClick={exportSelected} disabled={Boolean(sending)}>
              Выгрузить в 1С
            </button>
          </div>
        </div>
      )}

      {report && (
        <div className="panel" style={{ marginBottom: 12 }}>
          <div className="toolbar" style={{ marginBottom: report.failed.length ? 10 : 0 }}>
            <strong>Отправлено в 1С: {report.ok}</strong>
            {report.failed.length > 0 && (
              <span className="muted">, не удалось: {report.failed.length}</span>
            )}
            <div className="spacer" />
            <button onClick={() => setReport(null)}>Скрыть</button>
          </div>
          {report.failed.length > 0 && (
            <table>
              <thead>
                <tr>
                  <th style={{ width: '45%' }}>Документ</th>
                  <th>Причина</th>
                </tr>
              </thead>
              <tbody>
                {report.failed.map((f, i) => (
                  <tr key={i}>
                    <td>{f.name}</td>
                    <td className="muted">{f.message}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}

      <div className="panel" style={{ padding: 0 }}>
        {loading ? (
          <div className="empty">Загрузка…</div>
        ) : items.length === 0 ? (
          <div className="empty">
            Документов нет. Проведённые в 1С лежат в разделе «Архив».
          </div>
        ) : (
          <table>
            <thead>
              <tr>
                <th style={{ width: 34 }}>
                  {selectable.length === 0 ? (
                    <span
                      className="cb-blocked"
                      title="Нет документов, готовых к выгрузке"
                      onClick={() =>
                        setHint({ text: 'В списке нет документов, готовых к выгрузке в 1С' })
                      }
                    >
                      <input type="checkbox" checked={false} readOnly tabIndex={-1} />
                    </span>
                  ) : (
                    <input
                      type="checkbox"
                      checked={allSelected}
                      onChange={toggleAll}
                      disabled={Boolean(sending)}
                      title="Выбрать все, готовые к выгрузке"
                    />
                  )}
                </th>
                <th>Файл</th>
                <th>Компания</th>
                <th>Тип</th>
                <th>Размер</th>
                <th>Статус</th>
                <th>Загружен</th>
              </tr>
            </thead>
            <tbody>
              {items.map((d) => {
                const reason = blockReason(d, canApprove)
                return (
                  <tr key={d.id}>
                    <td>
                      {reason ? (
                        <span
                          className="cb-blocked"
                          title={reason}
                          onClick={() => setHint({ text: d.original_name + ': ' + reason })}
                        >
                          <input type="checkbox" checked={false} readOnly tabIndex={-1} />
                        </span>
                      ) : (
                        <input
                          type="checkbox"
                          checked={selected.has(d.id)}
                          onChange={() => toggleOne(d.id)}
                          disabled={Boolean(sending)}
                          title="Отправить в 1С"
                        />
                      )}
                    </td>
                    <td>
                      <Link to={'/documents/' + d.id}>{d.original_name}</Link>
                    </td>
                    <td className="muted">{d.company_name || '—'}</td>
                    <td>{d.recognition ? docTypeLabel(d.recognition.doc_type) : '—'}</td>
                    <td>{formatSize(d.size_bytes)}</td>
                    <td>
                      <StatusBadge status={d.status} />
                    </td>
                    <td className="muted">{formatDateTime(d.created_at)}</td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        )}
      </div>

      {!loading && items.length > 0 && (
        <div className="toolbar" style={{ marginTop: 10 }}>
          <span className="small muted">
            Показано {items.length} из {total}
            {blocked > 0 ? ' · не готовы к выгрузке: ' + blocked : ''}
          </span>
          <div className="spacer" />
          {items.length < total && (
            <button onClick={loadMore} disabled={loadingMore || Boolean(sending)}>
              {loadingMore ? 'Загружаю…' : 'Показать ещё'}
            </button>
          )}
        </div>
      )}
      {hint && (
        <div className="row-hint" onClick={() => setHint(null)} role="status">
          {hint.text}
        </div>
      )}

      {!loading && blocked > 0 && (
        <p className="small muted" style={{ marginTop: 4 }}>
          Нажмите на серый флажок (или наведите курсор), чтобы увидеть причину.
        </p>
      )}
    </div>
  )
}
