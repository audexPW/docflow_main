import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, saveSession } from '../api.js'
import { useAuth } from '../auth.jsx'

// Смена собственного пароля.
//
// Отдельный экран, а не пункт настроек, потому что на него принудительно
// перебрасывает после первого входа: пароль администратора печатается при
// установке в консоль и оседает в истории терминала, поэтому работать с ним
// постоянно нельзя.
export default function ChangePassword() {
  const navigate = useNavigate()
  const { mustChangePassword, clearMustChange } = useAuth()

  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [repeat, setRepeat] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [done, setDone] = useState(false)

  const tooShort = next.length > 0 && next.length < 10
  const mismatch = repeat.length > 0 && next !== repeat
  const canSubmit = current && next.length >= 10 && next === repeat && !busy

  async function submit(e) {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      const res = await api.changePassword(current, next)
      // Прежние токены аннулированы сервером — сохраняем выданный новый,
      // иначе следующий же запрос выбросит из системы.
      if (res && res.token) saveSession(res)
      clearMustChange()
      setDone(true)
      setTimeout(() => navigate('/documents'), 1200)
    } catch (err) {
      setError(err.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div>
      <h1>Смена пароля</h1>

      {mustChangePassword && !done && (
        <div className="notice">
          Вы вошли с паролем, который был выдан при установке системы. Задайте
          свой — прежний пароль виден в файле настроек на сервере.
        </div>
      )}

      {error && <div className="error">{error}</div>}
      {done && <div className="notice">Пароль изменён.</div>}

      <div className="panel" style={{ maxWidth: 460 }}>
        <form onSubmit={submit}>
          <div className="field">
            <label>Текущий пароль</label>
            <input
              type="password"
              value={current}
              onChange={(e) => setCurrent(e.target.value)}
              autoComplete="current-password"
            />
          </div>

          <div className="field">
            <label>Новый пароль</label>
            <input
              type="password"
              value={next}
              onChange={(e) => setNext(e.target.value)}
              autoComplete="new-password"
            />
            <div className="muted" style={{ fontSize: 13 }}>
              Не короче 10 символов, буквы и цифры.
            </div>
            {tooShort && <div className="error">Слишком короткий пароль</div>}
          </div>

          <div className="field">
            <label>Новый пароль ещё раз</label>
            <input
              type="password"
              value={repeat}
              onChange={(e) => setRepeat(e.target.value)}
              autoComplete="new-password"
            />
            {mismatch && <div className="error">Пароли не совпадают</div>}
          </div>

          <button type="submit" className="btn-primary" disabled={!canSubmit}>
            {busy ? 'Сохраняю…' : 'Сменить пароль'}
          </button>
        </form>
      </div>

      <p className="muted" style={{ maxWidth: 460 }}>
        После смены пароля все прежние сеансы завершаются — на других
        устройствах потребуется войти заново.
      </p>
    </div>
  )
}
