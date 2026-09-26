import { useRef, useState, useEffect, useCallback } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, ApiError } from '../api.js'
import { useAuth } from '../auth.jsx'
import { formatSize, formatDateTime } from '../lib/format.js'
import { enqueue, listOutbox, flushOutbox, onOutboxChange } from '../lib/offlineQueue.js'
import { hasNativeCamera, takePhoto } from '../lib/camera.js'
import ImageEditor from '../components/ImageEditor.jsx'

const ACCEPT = 'image/jpeg,image/png,image/heic,image/heif,application/pdf'

// Правка доступна только для растровых форматов, которые умеет рисовать браузер.
const EDITABLE = new Set(['image/jpeg', 'image/png'])

export default function Upload() {
  const navigate = useNavigate()
  const { role } = useAuth()
  const fileRef = useRef(null)
  const cameraRef = useRef(null)
  const [queue, setQueue] = useState([]) // { file, status: 'wait'|'sending'|'done'|'offline'|'error', error?, id? }
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [editing, setEditing] = useState(null) // индекс правящегося файла в очереди

  // Юрлицо, за которое загружаются документы. Оператору и админу его нужно
  // выбрать (клиент приехал в офис — снимаем за него); у клиента компания уже
  // задана в учётке, и выбора нет.
  const [companies, setCompanies] = useState([])
  const [companyId, setCompanyId] = useState('')
  const canPickCompany = role === 'operator' || role === 'admin'
  // Оператор обязан указать, за кого снимает: без компании документ уйдёт в
  // общую папку, и разбирать его придётся администратору вручную. Админу
  // загрузка без компании разрешена — он может забирать документ «в общее».
  const companyRequired = role === 'operator'

  useEffect(() => {
    if (!canPickCompany) return
    let cancelled = false
    api
      .listCompanies()
      .then((res) => {
        if (cancelled) return
        const list = (res.items || []).filter((c) => c.is_active)
        setCompanies(list)
        if (list.length === 1) setCompanyId(list[0].id)
      })
      .catch(() => {})
    return () => {
      cancelled = true
    }
  }, [canPickCompany])

  // Оффлайн-очередь (ждут связи)
  const [outbox, setOutbox] = useState([])
  const refreshOutbox = useCallback(async () => {
    try {
      setOutbox(await listOutbox())
    } catch {
      setOutbox([])
    }
  }, [])
  useEffect(() => {
    refreshOutbox()
    return onOutboxChange(refreshOutbox)
  }, [refreshOutbox])

  function addFiles(fileList) {
    const files = Array.from(fileList || [])
    if (files.length === 0) return
    setError('')
    setQueue((q) => [...q, ...files.map((file) => ({ file, status: 'wait' }))])
  }

  // Снять камерой: в приложении — нативный плагин, в браузере — <input capture>.
  async function shoot() {
    if (hasNativeCamera()) {
      try {
        const file = await takePhoto()
        if (file) addFiles([file])
        return
      } catch {
        /* откат на системный ввод */
      }
    }
    cameraRef.current?.click()
  }

  function applyEdited(idx, editedFile) {
    setQueue((q) => q.map((it, i) => (i === idx ? { ...it, file: editedFile, status: 'wait' } : it)))
    setEditing(null)
  }

  async function sendAll() {
    if (companyRequired && !companyId) {
      setError('Выберите компанию, за которую загружаете документы')
      return
    }

    setBusy(true)
    setError('')
    let lastId = null
    let sentCount = 0
    let offlineCount = 0

    for (let i = 0; i < queue.length; i++) {
      if (queue[i].status === 'done' || queue[i].status === 'offline') continue

      if (!navigator.onLine) {
        await enqueue(queue[i].file)
        offlineCount++
        setQueue((q) => q.map((it, idx) => (idx === i ? { ...it, status: 'offline' } : it)))
        continue
      }

      setQueue((q) => q.map((it, idx) => (idx === i ? { ...it, status: 'sending' } : it)))
      try {
        const doc = await api.uploadDocument(queue[i].file, companyId)
        lastId = doc.id
        sentCount++
        setQueue((q) => q.map((it, idx) => (idx === i ? { ...it, status: 'done', id: doc.id } : it)))
      } catch (err) {
        if (err instanceof ApiError && err.status === 0) {
          await enqueue(queue[i].file)
          offlineCount++
          setQueue((q) => q.map((it, idx) => (idx === i ? { ...it, status: 'offline' } : it)))
        } else {
          setQueue((q) =>
            q.map((it, idx) => (idx === i ? { ...it, status: 'error', error: err.message } : it))
          )
        }
      }
    }

    setBusy(false)
    if (sentCount === 1 && offlineCount === 0 && lastId) {
      navigate('/documents/' + lastId)
    } else if (sentCount > 1) {
      navigate('/documents')
    }
  }

  async function sendNow() {
    setBusy(true)
    try {
      await flushOutbox()
      await refreshOutbox()
    } finally {
      setBusy(false)
    }
  }

  const pending = queue.filter((q) => q.status !== 'done' && q.status !== 'offline').length

  return (
    <div>
      <h1>Загрузка документов</h1>
      {error && <div className="error">{error}</div>}

      <div className="panel">
        <p className="muted small" style={{ marginTop: 0 }}>
          Поддерживаются JPG, PNG, HEIC и PDF. Можно выбрать сразу несколько файлов
          или снять документ камерой. Фото перед отправкой можно обрезать, повернуть
          и подправить контраст. Если связи нет, документы сохранятся на устройстве и
          отправятся сами, как только появится сеть.
        </p>

        {canPickCompany && companies.length > 0 && (
          <div className="field" style={{ maxWidth: 320 }}>
            <label>Компания, за которую загружаем</label>
            <select value={companyId} onChange={(e) => setCompanyId(e.target.value)}>
              <option value="">
                {companyRequired ? '— выберите компанию —' : '— без компании —'}
              </option>
              {companies.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
            </select>
            <p className="muted small" style={{ marginBottom: 0 }}>
              {companyRequired && !companyId
                ? 'Укажите компанию — без неё документ не отправится.'
                : 'Документ попадёт в папку обмена этой компании и будет виден её главбуху.'}
            </p>
          </div>
        )}

        <div className="toolbar" style={{ marginBottom: 0 }}>
          <button onClick={() => fileRef.current?.click()} disabled={busy}>
            Выбрать файлы
          </button>
          <button onClick={shoot} disabled={busy}>
            Снять камерой
          </button>
        </div>

        <input
          ref={fileRef}
          type="file"
          accept={ACCEPT}
          multiple
          style={{ display: 'none' }}
          onChange={(e) => {
            addFiles(e.target.files)
            e.target.value = ''
          }}
        />
        <input
          ref={cameraRef}
          type="file"
          accept="image/*"
          capture="environment"
          style={{ display: 'none' }}
          onChange={(e) => {
            addFiles(e.target.files)
            e.target.value = ''
          }}
        />
      </div>

      {queue.length > 0 && (
        <div className="panel">
          <ul className="upload-list">
            {queue.map((it, idx) => (
              <li key={idx}>
                <span>
                  {it.file.name} <span className="muted small">({formatSize(it.file.size)})</span>
                </span>
                <span className="toolbar" style={{ margin: 0, gap: 8, alignItems: 'center' }}>
                  {EDITABLE.has(it.file.type) && it.status !== 'done' && (
                    <button
                      className="small"
                      style={{ padding: '2px 10px' }}
                      onClick={() => setEditing(idx)}
                      disabled={busy}
                    >
                      Изменить
                    </button>
                  )}
                  {renderStatus(it)}
                </span>
              </li>
            ))}
          </ul>
          <div className="toolbar" style={{ marginTop: 14, marginBottom: 0 }}>
            <button
              className="btn-primary"
              onClick={sendAll}
              disabled={busy || pending === 0 || (companyRequired && !companyId)}
              title={companyRequired && !companyId ? 'Сначала выберите компанию' : undefined}
            >
              {busy ? 'Отправка…' : 'Отправить' + (pending > 1 ? ` (${pending})` : '')}
            </button>
            <button onClick={() => setQueue([])} disabled={busy}>
              Очистить
            </button>
          </div>
        </div>
      )}

      {editing !== null && queue[editing] && (
        <ImageEditor
          file={queue[editing].file}
          onCancel={() => setEditing(null)}
          onApply={(edited) => applyEdited(editing, edited)}
        />
      )}

      {outbox.length > 0 && (
        <div className="panel">
          <h2>Ждут связи</h2>
          <p className="muted small" style={{ marginTop: 0 }}>
            Эти документы сохранены на устройстве и отправятся автоматически при
            появлении сети. Можно попробовать отправить сейчас.
          </p>
          <ul className="upload-list">
            {outbox.map((it) => (
              <li key={it.id}>
                <span>
                  {it.name} <span className="muted small">({formatSize(it.size)})</span>
                </span>
                <span className="muted small">{formatDateTime(it.addedAt)}</span>
              </li>
            ))}
          </ul>
          <div className="toolbar" style={{ marginTop: 14, marginBottom: 0 }}>
            <button onClick={sendNow} disabled={busy}>
              Отправить сейчас
            </button>
          </div>
        </div>
      )}
    </div>
  )
}

function renderStatus(it) {
  switch (it.status) {
    case 'wait':
      return <span className="muted small">в очереди</span>
    case 'sending':
      return <span className="muted small">отправка…</span>
    case 'done':
      return <span className="small" style={{ color: 'var(--ok)' }}>отправлен</span>
    case 'offline':
      return <span className="small muted">сохранён, отправится позже</span>
    case 'error':
      return <span className="small" style={{ color: 'var(--danger)' }}>{it.error || 'ошибка'}</span>
    default:
      return null
  }
}
