import { useEffect, useState, useCallback, useRef } from 'react'
import { useParams, useNavigate, Link } from 'react-router-dom'
import { api } from '../api.js'
import { useAuth } from '../auth.jsx'
import StatusBadge from '../components/StatusBadge.jsx'
import { docTypeLabel, docTypeOptions, docTypeInfo, fieldLabel } from '../lib/labels.js'
import { formatDateTime, formatSize, formatConfidence, confidenceLevel } from '../lib/format.js'
import { isNativeApp } from '../lib/platform.js'

// Отметка в recognition.missing о том, что табличная часть не сошлась с
// итогом. Приходит с бэкенда наравне с ключами реквизитов, но реквизитом не
// является: бэкенд помечает так документ, у которого позиции отброшены
// (worker/recognizer.go). В карточке она выводилась пустым полем «lines» с
// красной рамкой, и было непонятно, что туда вводить.
const TABLE_MARKER = 'lines'

export default function DocumentDetail() {
  const { id } = useParams()
  const navigate = useNavigate()
  const { role } = useAuth()
  // Главбух правит реквизиты наравне с оператором: он ведёт свои компании
  // целиком и не должен ждать, пока распознанное поправит кто-то другой.
  const canEdit = role === 'operator' || role === 'accountant' || role === 'admin'
  // Главбух не правит реквизиты — он согласовывает или возвращает документ.
  const canApprove = role === 'accountant' || role === 'admin'
  const isAdmin = role === 'admin'

  const [doc, setDoc] = useState(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState(false)

  // Локальное состояние правки распознавания
  const [docType, setDocType] = useState('')
  const [fields, setFields] = useState([]) // [{ key, value, confidence, source }]
  const [newKey, setNewKey] = useState('')
  const [newValue, setNewValue] = useState('')

  const [fileUrl, setFileUrl] = useState('')
  const fileUrlRef = useRef('')

  // Ручное дозаполнение недостающих полей (для статуса needs_input)
  const [manual, setManual] = useState({}) // { fieldKey: value }
  const [completing, setCompleting] = useState(false)

  // Решение главбуха и причина возврата
  const [approvalNote, setApprovalNote] = useState('')

  // Список компаний нужен администратору, чтобы проставить юрлицо документу,
  // загруженному под учёткой без привязки.
  const [companies, setCompanies] = useState([])

  const applyDoc = useCallback((d) => {
    setDoc(d)
    const rec = d.recognition
    setDocType(rec?.doc_type || '')
    const list = rec?.fields
      ? Object.entries(rec.fields).map(([key, f]) => ({
          key,
          value: f.value ?? '',
          confidence: f.confidence,
          source: f.source,
        }))
      : []

    // Обязательные реквизиты показываем всегда, даже если распознавание их не
    // нашло: бухгалтеру нужен один и тот же набор полей в любом документе, а
    // не тот, что получился. Недостающие добавляем пустыми — ниже они идут
    // красным, и их вносят руками.
    const seen = new Set(list.map((f) => f.key))
    for (const k of (rec?.missing || []).filter((k) => k !== TABLE_MARKER)) {
      if (!seen.has(k)) {
        list.push({ key: k, value: '', confidence: undefined, source: undefined })
        seen.add(k)
      }
    }

    // Порядок как в бухгалтерской карточке, а не по алфавиту: реквизиты
    // документа, стороны, договор, суммы, счета учёта.
    const order = [
      'date', 'number', 'currency',
      'counterparty', 'unp', 'inn',
      'organization', 'organization_unp', 'organization_inn',
      'contract_number', 'contract_date',
      'amount_no_vat', 'vat_amount', 'total',
      'account_debit', 'account_credit', 'account_vat',
    ]
    const rank = (k) => {
      const i = order.indexOf(k)
      return i === -1 ? order.length : i
    }
    list.sort((a, b) => rank(a.key) - rank(b.key) || a.key.localeCompare(b.key))
    setFields(list)

    // Подготовим пустые поля для ручного ввода недостающих реквизитов.
    const miss = (rec?.missing || []).filter((k) => k !== TABLE_MARKER)
    setManual((prev) => {
      const next = {}
      for (const k of miss) next[k] = prev[k] ?? ''
      return next
    })
  }, [])

  const load = useCallback(async () => {
    try {
      const d = await api.getDocument(id)
      applyDoc(d)
      setError('')
    } catch (err) {
      setError(err.message)
    } finally {
      setLoading(false)
    }
  }, [id, applyDoc])

  useEffect(() => {
    setLoading(true)
    load()
  }, [load])

  // Пока документ распознаётся — периодически обновляем
  useEffect(() => {
    if (!doc) return
    if (doc.status === 'received' || doc.status === 'processing') {
      const t = setTimeout(load, 3000)
      return () => clearTimeout(t)
    }
  }, [doc, load])

  // Предпросмотр файла (с авторизацией → object URL)
  useEffect(() => {
    let cancelled = false
    async function fetchFile() {
      try {
        const res = await api.fileResponse(id)
        const blob = await res.blob()
        if (cancelled) return
        const url = URL.createObjectURL(blob)
        fileUrlRef.current = url
        setFileUrl(url)
      } catch {
        // Предпросмотр не критичен — молча пропускаем
      }
    }
    fetchFile()
    return () => {
      cancelled = true
      if (fileUrlRef.current) {
        URL.revokeObjectURL(fileUrlRef.current)
        fileUrlRef.current = ''
      }
    }
  }, [id])

  function updateField(idx, value) {
    setFields((fs) => fs.map((f, i) => (i === idx ? { ...f, value, source: 'manual' } : f)))
  }

  function removeField(idx) {
    setFields((fs) => fs.filter((_, i) => i !== idx))
  }

  function addField() {
    const key = newKey.trim()
    if (!key) return
    setFields((fs) => {
      if (fs.some((f) => f.key === key)) return fs
      return [...fs, { key, value: newValue.trim(), source: 'manual', confidence: 1 }]
    })
    setNewKey('')
    setNewValue('')
  }

  async function save() {
    setBusy(true)
    setError('')
    setNotice('')
    try {
      const fieldMap = {}
      for (const f of fields) fieldMap[f.key] = f.value
      const updated = await api.updateRecognition(id, { doc_type: docType, fields: fieldMap })
      applyDoc(updated)
      setNotice('Изменения сохранены')
    } catch (err) {
      setError(err.message)
    } finally {
      setBusy(false)
    }
  }

  async function confirm() {
    setBusy(true)
    setError('')
    setNotice('')
    try {
      await api.confirmDocument(id)
      await load()
      setNotice('Документ подтверждён и поставлен в очередь на выгрузку в 1С')
    } catch (err) {
      setError(err.message)
    } finally {
      setBusy(false)
    }
  }

  async function completeManual() {
    setCompleting(true)
    setError('')
    setNotice('')
    try {
      const filled = {}
      for (const [k, v] of Object.entries(manual)) {
        if (String(v).trim() !== '') filled[k] = String(v).trim()
      }
      const updated = await api.completeDocument(id, filled)
      applyDoc(updated)
      if ((updated.recognition?.missing || []).length === 0) {
        setNotice('Данные дополнены и отправлены в 1С')
      } else {
        setNotice('Часть полей сохранена. Осталось заполнить оставшиеся.')
      }
    } catch (err) {
      setError(err.message)
    } finally {
      setCompleting(false)
    }
  }

  useEffect(() => {
    if (!isAdmin) return
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
  }, [isAdmin])

  async function assignCompany(companyId) {
    setBusy(true)
    setError('')
    setNotice('')
    try {
      const updated = await api.setDocumentCompany(id, companyId)
      applyDoc(updated)
      setNotice(
        companyId
          ? 'Компания проставлена. Уже выгруженный документ в новую папку не переедет — при необходимости отправьте его в 1С повторно.'
          : 'Привязка к компании снята'
      )
    } catch (err) {
      setError(err.message)
    } finally {
      setBusy(false)
    }
  }

  async function decide(approved) {
    if (!approved && !approvalNote.trim()) {
      setError('Укажите причину возврата — её увидит клиент')
      return
    }
    setBusy(true)
    setError('')
    setNotice('')
    try {
      const updated = approved
        ? await api.approveDocument(id, approvalNote.trim())
        : await api.rejectDocument(id, approvalNote.trim())
      applyDoc(updated)
      setApprovalNote('')
      setNotice(approved ? 'Документ согласован и отправлен в 1С' : 'Документ возвращён клиенту')
    } catch (err) {
      setError(err.message)
    } finally {
      setBusy(false)
    }
  }

  async function reprocess() {
    setBusy(true)
    setError('')
    setNotice('')
    try {
      await api.reprocessDocument(id)
      await load()
      setNotice('Документ отправлен на повторное распознавание')
    } catch (err) {
      setError(err.message)
    } finally {
      setBusy(false)
    }
  }

  if (loading) return <div className="empty">Загрузка…</div>
  if (!doc)
    return (
      <div>
        {error && <div className="error">{error}</div>}
        <Link to="/documents">← К списку</Link>
      </div>
    )

  const isImage = doc.content_type && doc.content_type.startsWith('image/')
  const isPdf = doc.content_type === 'application/pdf'
  const exported = doc.status === 'exported'
  const editable = canEdit && !exported
  const needsInput = doc.status === 'needs_input'
  const allMissing = doc.recognition?.missing || []
  // «lines» в списке недостающего — не реквизит, а отметка о табличной части:
  // сумма позиций не сошлась с итогом, поэтому позиции в 1С не отправлены.
  // Полем её показывать нельзя — вводить туда нечего.
  const linesUnreconciled = allMissing.includes(TABLE_MARKER)
  const missing = allMissing.filter((k) => k !== TABLE_MARKER)

  return (
    <div>
      <div className="toolbar">
        <Link to="/documents">← К списку</Link>
        <div className="spacer" />
        <StatusBadge status={doc.status} />
      </div>

      <h1 style={{ wordBreak: 'break-all' }}>{doc.original_name}</h1>

      {error && <div className="error">{error}</div>}
      {notice && <div className="notice">{notice}</div>}
      {doc.status === 'failed' && doc.error && (
        <div className="error">Ошибка распознавания: {doc.error}</div>
      )}

      {/* Ответ из 1С. Пока его нет, «отправлено» означает лишь то, что
          документ передан в обмен — принят он или проведён, знает только 1С. */}
      {doc.onec_status === 'posted' && (
        <div className="notice">
          Проведён в 1С{doc.onec_ref ? ` — ${doc.onec_ref}` : ''}
        </div>
      )}
      {/* Архив: работа над документом закончена, и он хранится ограниченный
          срок. Без этой строки удаление по сроку выглядело бы как пропажа. */}
      {doc.archived_at && (
        <div className="notice">
          В архиве с {formatDateTime(doc.archived_at)} — будет удалён вместе с файлом по
          истечении срока хранения. <Link to="/archive">Открыть архив</Link>
        </div>
      )}
      {(doc.onec_status === 'rejected' || doc.onec_status === 'error') && (
        <div className="error">
          1С отклонила документ{doc.onec_message ? `: ${doc.onec_message}` : ''}
        </div>
      )}
      {doc.onec_status === 'accepted' && (
        <div className="notice">Принят в 1С, ожидает проведения</div>
      )}
      {exported && !doc.onec_status && (
        <div className="notice">
          Передан в 1С. Подтверждение обработки пока не поступило.
        </div>
      )}

      {doc.status === 'rejected' && (
        <div className="error">
          Главбух вернул документ{doc.approval_note ? `: ${doc.approval_note}` : ''}
        </div>
      )}
      {doc.status !== 'rejected' && doc.approved_at && (
        <div className="notice">
          Согласовано главбухом {formatDateTime(doc.approved_at)}
          {doc.approval_note ? ` — ${doc.approval_note}` : ''}
        </div>
      )}

      {/* Решение главбуха. Пока он не согласует, документ этой компании в 1С
          не уходит — так настроено флагом компании. */}
      {canApprove && doc.status !== 'exported' && (
        <div className="panel" style={{ borderLeft: '4px solid var(--accent, #1f4d80)' }}>
          <h2 style={{ marginTop: 0 }}>
            {doc.status === 'needs_approval' ? 'Ждёт вашего решения' : 'Решение главбуха'}
          </h2>
          <div className="field">
            <label>Комментарий (при возврате — обязателен, его увидит клиент)</label>
            <input
              type="text"
              value={approvalNote}
              onChange={(e) => setApprovalNote(e.target.value)}
              placeholder="например: нет печати поставщика"
            />
          </div>
          <div className="toolbar" style={{ marginTop: 12, marginBottom: 0 }}>
            <button className="btn-primary" onClick={() => decide(true)} disabled={busy}>
              Согласовать и отправить в 1С
            </button>
            <button className="btn-danger" onClick={() => decide(false)} disabled={busy}>
              Вернуть клиенту
            </button>
          </div>
        </div>
      )}

      {/* Позиции таблицы не сошлись с итогом документа. Это не реквизит и
          руками сюда ничего не вводят: либо правят табличную часть ниже,
          либо отправляют документ на повторное распознавание. */}
      {linesUnreconciled && (
        <div className="panel" style={{ borderLeft: '4px solid var(--warn, #d98a00)' }}>
          <h2 style={{ marginTop: 0 }}>Позиции не сошлись с итогом</h2>
          <p className="muted small" style={{ marginTop: 0, marginBottom: 0 }}>
            Сумма строк табличной части не совпала с итогом документа, поэтому
            позиции в 1С не отправлены — ушла только шапка. Проверьте табличную
            часть ниже: скорее всего, разъехались графы. Реквизиты документа это
            не задерживает.
          </p>
        </div>
      )}

      {/* Не все обязательные поля распознаны — просим ввести их вручную.
          Распознанное уже ушло в 1С частично; дозаполненное дошлётся туда же. */}
      {needsInput && missing.length > 0 && (
        <div className="panel" style={{ borderLeft: '4px solid var(--warn, #d98a00)' }}>
          <h2 style={{ marginTop: 0 }}>Нужно уточнение</h2>
          <p className="muted small" style={{ marginTop: 0 }}>
            Часть реквизитов не удалось распознать. Распознанные данные уже отправлены в
            1С. Заполните недостающие поля — они дошлются к тому же документу.
          </p>
          <table>
            <tbody>
              {missing.map((k) => (
                <tr key={k}>
                  <th style={{ width: 200 }}>{fieldLabel(k)}</th>
                  <td>
                    <input
                      type="text"
                      value={manual[k] ?? ''}
                      placeholder="ввести вручную"
                      onChange={(e) => setManual((m) => ({ ...m, [k]: e.target.value }))}
                    />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          <div className="toolbar" style={{ marginTop: 14, marginBottom: 0 }}>
            <button className="btn-primary" onClick={completeManual} disabled={completing}>
              {completing ? 'Отправка…' : 'Дополнить и отправить в 1С'}
            </button>
          </div>
        </div>
      )}

      <div className="panel">
        <table>
          <tbody>
            <tr>
              <th style={{ width: 160 }}>Компания</th>
              <td>
                {isAdmin ? (
                  <>
                    <select
                      value={doc.company_id || ''}
                      disabled={busy}
                      onChange={(e) => assignCompany(e.target.value)}
                    >
                      <option value="">— не проставлена —</option>
                      {companies.map((c) => (
                        <option key={c.id} value={c.id}>
                          {c.name}
                        </option>
                      ))}
                    </select>
                    {!doc.company_id && (
                      <div className="small muted">
                        Загружено под учёткой без привязки к компании — документ ушёл в общую
                        папку обмена.
                      </div>
                    )}
                  </>
                ) : (
                  doc.company_name || <span className="muted">—</span>
                )}
              </td>
            </tr>
            <tr>
              <th style={{ width: 160 }}>Размер</th>
              <td>{formatSize(doc.size_bytes)}</td>
            </tr>
            <tr>
              <th>Формат</th>
              <td>{doc.content_type}</td>
            </tr>
            <tr>
              <th>Загружен</th>
              <td>{formatDateTime(doc.created_at)}</td>
            </tr>
            <tr>
              <th>Обновлён</th>
              <td>{formatDateTime(doc.updated_at)}</td>
            </tr>
          </tbody>
        </table>
      </div>

      {/* Распознанные реквизиты */}
      <div className="panel">
        <h2>Реквизиты</h2>
        {!doc.recognition ? (
          <p className="muted">
            {doc.status === 'received' || doc.status === 'processing'
              ? 'Документ распознаётся, реквизиты появятся автоматически.'
              : 'Реквизиты ещё не распознаны.'}
          </p>
        ) : (
          <>
            <div className="field" style={{ maxWidth: 340 }}>
              <label>Тип документа</label>
              {editable ? (
                <>
                  <select value={docType} onChange={(e) => setDocType(e.target.value)}>
                    {docTypeOptions().map((t) => (
                      <option key={t} value={t}>
                        {docTypeLabel(t)}
                      </option>
                    ))}
                    {docType && !docTypeOptions().includes(docType) && (
                      <option value={docType}>{docTypeLabel(docType)}</option>
                    )}
                  </select>
                  {docTypeInfo(docType) && !docTypeInfo(docType).routable && (
                    <p className="muted">
                      Для этого типа не задан объект-приёмник в 1С — документ
                      останется на проверке. Настраивается в реестре типов на сервере.
                    </p>
                  )}
                </>
              ) : (
                <div>
                  {docTypeLabel(doc.recognition.doc_type)}
                  <span className="conf">
                    {formatConfidence(doc.recognition.doc_type_confidence)}
                  </span>
                </div>
              )}
            </div>

            <table>
              <thead>
                <tr>
                  <th style={{ width: 200 }}>Поле</th>
                  <th>Значение</th>
                  {editable && <th style={{ width: 40 }}></th>}
                </tr>
              </thead>
              <tbody>
                {fields.length === 0 && (
                  <tr>
                    <td colSpan={editable ? 3 : 2} className="muted">
                      Полей нет.
                    </td>
                  </tr>
                )}
                {fields.map((f, idx) => (
                  <tr
                    key={f.key}
                    className={[
                      missing.includes(f.key) ? 'field-missing' : '',
                      f.source !== 'manual' && f.confidence !== undefined && f.confidence < 0.5
                        ? 'field-lowconf'
                        : '',
                    ].filter(Boolean).join(' ') || undefined}
                  >
                    <td>
                      {fieldLabel(f.key)}
                      {missing.includes(f.key) && (
                        <span className="conf field-missing-mark" title="Обязательный реквизит не распознан — заполните вручную">
                          не распознано
                        </span>
                      )}
                      {f.confidence !== undefined && f.source !== 'manual' && (
                        <span
                          className={'conf ' + confidenceLevel(f.confidence)}
                          title={
                            f.confidence < 0.5
                              ? 'Значение не подтверждается документом — проверьте обязательно'
                              : f.confidence < 0.75
                                ? 'Значение подтверждено частично — стоит проверить'
                                : 'Значение подтверждено несколькими проверками'
                          }
                        >
                          {formatConfidence(f.confidence)}
                        </span>
                      )}
                      {f.source === 'manual' && (
                        <span className="conf source-manual">правка</span>
                      )}
                    </td>
                    <td>
                      {editable ? (
                        <input
                          type="text"
                          value={f.value}
                          onChange={(e) => updateField(idx, e.target.value)}
                        />
                      ) : (
                        f.value || <span className="muted">—</span>
                      )}
                    </td>
                    {editable && (
                      <td>
                        <button
                          className="btn-danger"
                          style={{ padding: '2px 8px' }}
                          onClick={() => removeField(idx)}
                          title="Убрать поле"
                        >
                          ×
                        </button>
                      </td>
                    )}
                  </tr>
                ))}
              </tbody>
            </table>

            {editable && (
              <div className="row" style={{ marginTop: 12, alignItems: 'flex-end' }}>
                <div className="field" style={{ marginBottom: 0 }}>
                  <label>Новое поле</label>
                  <input
                    type="text"
                    placeholder="ключ, напр. inn"
                    value={newKey}
                    onChange={(e) => setNewKey(e.target.value)}
                  />
                </div>
                <div className="field" style={{ marginBottom: 0 }}>
                  <label>Значение</label>
                  <input
                    type="text"
                    value={newValue}
                    onChange={(e) => setNewValue(e.target.value)}
                  />
                </div>
                <button onClick={addField} disabled={!newKey.trim()}>
                  Добавить
                </button>
              </div>
            )}
          </>
        )}

        {editable && (
          <div className="toolbar" style={{ marginTop: 16, marginBottom: 0 }}>
            <button className="btn-primary" onClick={save} disabled={busy}>
              Сохранить
            </button>
            <button onClick={confirm} disabled={busy || !doc.recognition}>
              Подтвердить и выгрузить в 1С
            </button>
            <button onClick={reprocess} disabled={busy}>
              Распознать заново
            </button>
          </div>
        )}
      </div>

      {/* Табличная часть. Показываем именно таблицей — в том же виде, в каком
          она уходит в 1С: колонки объявлены, ячейки разложены по колонкам. */}
      {doc.recognition?.table?.rows?.length > 0 && (
        <div className="panel">
          <h2>Табличная часть</h2>
          {/* Колонки теперь берутся из документа, а их может быть и восемь, и
              четырнадцать — оборачиваем в прокрутку, чтобы широкая таблица не
              ломала вёрстку карточки на телефоне. */}
          <div className="table-scroll">
          <table className="doc-table">
            <thead>
              <tr>
                {doc.recognition.table.columns.map((c) => (
                  <th key={c.index}>{c.title || c.role}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {doc.recognition.table.rows.map((row) => (
                <tr key={row.index}>
                  {row.cells.map((cell) => (
                    <td
                      key={cell.column}
                      className={cell.role && cell.role !== 'name' ? 'num' : undefined}
                    >
                      {cell.value}
                    </td>
                  ))}
                </tr>
              ))}
              {doc.recognition.table.totals_row?.length > 0 && (
                <tr className="totals">
                  {doc.recognition.table.totals_row.map((cell) => (
                    <td key={cell.column}>{cell.value}</td>
                  ))}
                </tr>
              )}
            </tbody>
          </table>
          </div>
          <p className="muted small" style={{ marginBottom: 0 }}>
            Разобрано: {tableSourceLabel(doc.recognition.table.source)}. В 1С уходит той же
            структурой — колонками и ячейками.
          </p>
        </div>
      )}

      {/* Предпросмотр файла */}
      <div className="panel preview">
        <h2>Оригинал</h2>
        {!fileUrl ? (
          <p className="muted">Загрузка предпросмотра…</p>
        ) : isImage ? (
          <img src={fileUrl} alt={doc.original_name} />
        ) : isPdf ? (
          // В браузере PDF показываем прямо, во встроенном WebView его нет —
          // отдаём файл системному просмотрщику телефона.
          isNativeApp() ? (
            <p>
              <a className="btn" href={fileUrl} target="_blank" rel="noopener" download={doc.original_name}>
                Открыть PDF
              </a>
            </p>
          ) : (
            <iframe src={fileUrl} title={doc.original_name} />
          )
        ) : (
          <a href={fileUrl} download={doc.original_name}>
            Скачать файл
          </a>
        )}
      </div>

      {/* Распознанный текст */}
      {doc.ocr_text && (
        <div className="panel">
          <details>
            <summary>Распознанный текст (OCR)</summary>
            <div className="ocr">{doc.ocr_text}</div>
          </details>
        </div>
      )}
    </div>
  )
}

// Чем разобрана таблица: по координатам OCR, правилами по шапке или моделью.
// Оператору это подсказывает, насколько внимательно стоит её проверить.
function tableSourceLabel(source) {
  // Здесь была проверка необъявленной переменной src — ReferenceError ронял
  // отрисовку всей карточки документа, а не только подписи под таблицей.
  switch (source) {
    case 'columns':
      return 'по колонкам документа'
    case 'grid':
      return 'по сетке таблицы'
    case 'layout':
      return 'по расположению колонок на странице'
    case 'model':
      return 'локальной моделью'
    case 'rules':
      return 'по шапке таблицы'
    default:
      return 'автоматически'
  }
}
