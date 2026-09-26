import { useEffect, useState, useCallback } from 'react'
import { api } from '../api.js'
import { roleLabel } from '../lib/labels.js'
import { formatDateTime } from '../lib/format.js'

export default function Users() {
  const [items, setItems] = useState([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')

  const [companies, setCompanies] = useState([])
  const [login, setLogin] = useState('')
  const [password, setPassword] = useState('')
  const [role, setRole] = useState('client')
  const [companyId, setCompanyId] = useState('')
  const [busy, setBusy] = useState(false)
  const [actingOn, setActingOn] = useState(null)
  // Компании главбухов: id учётки -> массив id компаний. Держим отдельно от
  // items, потому что это не поле пользователя, а строки company_accountants.
  const [accCompanies, setAccCompanies] = useState({})
  // Какая строка сейчас раскрыта. Одна на всю таблицу: два открытых списка
  // одновременно только мешают.
  const [accMenuFor, setAccMenuFor] = useState(null)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const [res, comp] = await Promise.all([api.listUsers(), api.listCompanies()])
      const users = res.items || []
      setItems(users)
      setCompanies(comp.items || [])

      // Закрепления тянем только для главбухов — у остальных ролей их не бывает.
      const pairs = await Promise.all(
        users
          .filter((u) => u.role === 'accountant')
          .map((u) =>
            api
              .listUserCompanies(u.id)
              .then((r) => [u.id, r.company_ids || []])
              .catch(() => [u.id, []])
          )
      )
      setAccCompanies(Object.fromEntries(pairs))
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

  async function create(e) {
    e.preventDefault()
    setBusy(true)
    setError('')
    setNotice('')
    try {
      await api.createUser({ login: login.trim(), password, role, company_id: companyId })
      setNotice('Пользователь создан')
      setLogin('')
      setPassword('')
      setRole('client')
      setCompanyId('')
      load()
    } catch (err) {
      setError(err.message)
    } finally {
      setBusy(false)
    }
  }

  // Общая обёртка для действий над учёткой: блокирует строку на время запроса
  // и показывает ошибку сервера как есть (там осмысленные тексты — например,
  // «это последний активный администратор»).
  async function act(id, fn, successText) {
    setActingOn(id)
    setError('')
    setNotice('')
    try {
      await fn()
      setNotice(successText)
      await load()
      setAccMenuFor(null)
    } catch (err) {
      setError(err.message)
    } finally {
      setActingOn(null)
    }
  }

  function toggleActive(u) {
    const next = !u.is_active
    if (!next && !window.confirm(`Заблокировать «${u.login}»? Сеансы завершатся сразу.`)) return
    act(u.id, () => api.setUserActive(u.id, next), next ? 'Доступ восстановлен' : 'Доступ заблокирован')
  }

  function resetPassword(u) {
    const pass = window.prompt(
      `Новый пароль для «${u.login}»\n\nНе короче 10 символов, буквы и цифры.\nПередайте его сотруднику — он сможет сменить пароль сам.`
    )
    if (!pass) return
    act(u.id, () => api.setUserPassword(u.id, pass), 'Пароль сброшен')
  }

  function changeRole(u, nextRole) {
    if (nextRole === u.role) return
    act(u.id, () => api.setUserRole(u.id, nextRole), 'Роль изменена')
  }

  // Привязка к юрлицу задаёт область видимости: клиент и оператор компании
  // видят только её документы. Главбуху компании назначаются отдельно, на
  // странице «Компании».
  function changeCompany(u, nextCompany) {
    if ((u.company_id || '') === nextCompany) return
    act(u.id, () => api.setUserCompany(u.id, nextCompany), 'Компания изменена')
  }

  // Главбух ведёт сколько угодно юрлиц, поэтому у него не выбор одной компании,
  // а набор галочек. Сохраняем весь набор целиком — сервер заменяет закрепление
  // одной транзакцией, частично применённого состояния не бывает.
  // Подпись на кнопке: пусто, одно название или «N компаний» — иначе строка
  // таблицы разъезжается, как только компаний станет больше трёх.
  function accCompanyLabel(u) {
    const ids = accCompanies[u.id] || []
    if (ids.length === 0) return '— не закреплён —'
    if (ids.length === 1) {
      const c = companies.find((x) => x.id === ids[0])
      return c ? c.name : '1 компания'
    }
    const tail = ids.length % 10 === 1 && ids.length % 100 !== 11 ? 'компания' : 'компаний'
    return ids.length + ' ' + tail
  }

  function toggleAccCompany(u, companyId, checked) {
    const current = accCompanies[u.id] || []
    const next = checked ? [...current, companyId] : current.filter((id) => id !== companyId)
    setAccCompanies((prev) => ({ ...prev, [u.id]: next }))
    act(
      u.id,
      () => api.setUserCompanies(u.id, next),
      checked ? 'Компания закреплена' : 'Компания снята'
    )
  }

  return (
    <div>
      <h1>Пользователи</h1>
      {error && <div className="error">{error}</div>}
      {notice && <div className="notice">{notice}</div>}

      <div className="panel">
        <h2>Новый пользователь</h2>
        <form onSubmit={create}>
          <div className="row">
            <div className="field">
              <label>Логин</label>
              <input
                type="text"
                value={login}
                onChange={(e) => setLogin(e.target.value)}
                autoComplete="off"
              />
            </div>
            <div className="field">
              <label>Пароль (не короче 8 символов)</label>
              <input
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                autoComplete="new-password"
              />
            </div>
            <div className="field" style={{ maxWidth: 180 }}>
              <label>Роль</label>
              <select value={role} onChange={(e) => setRole(e.target.value)}>
                <option value="client">Клиент</option>
                <option value="operator">Оператор</option>
                <option value="accountant">Главбух</option>
                <option value="admin">Администратор</option>
              </select>
            </div>
            <div className="field" style={{ maxWidth: 220 }}>
              <label>Компания</label>
              <select value={companyId} onChange={(e) => setCompanyId(e.target.value)}>
                <option value="">— без привязки —</option>
                {companies.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name}
                  </option>
                ))}
              </select>
            </div>
          </div>
          <button
            type="submit"
            className="btn-primary"
            disabled={busy || !login.trim() || password.length < 8}
          >
            Создать
          </button>
        </form>
      </div>

      <div className="panel" style={{ padding: 0 }}>
        {loading ? (
          <div className="empty">Загрузка…</div>
        ) : (
          <table>
            <thead>
              <tr>
                <th>Логин</th>
                <th>Роль</th>
                <th>Компания</th>
                <th>Доступ</th>
                <th>Создан</th>
                <th>Действия</th>
              </tr>
            </thead>
            <tbody>
              {items.map((u) => (
                <tr key={u.id} style={{ opacity: u.is_active ? 1 : 0.55 }}>
                  <td>{u.login}</td>
                  <td>
                    <select
                      value={u.role}
                      disabled={actingOn === u.id}
                      onChange={(e) => changeRole(u, e.target.value)}
                      aria-label={`Роль пользователя ${u.login}`}
                    >
                      <option value="client">{roleLabel('client')}</option>
                      <option value="operator">{roleLabel('operator')}</option>
                      <option value="accountant">{roleLabel('accountant')}</option>
                      <option value="admin">{roleLabel('admin')}</option>
                    </select>
                  </td>
                  <td>
                    {u.role === 'accountant' ? (
                      companies.length === 0 ? (
                        <span className="muted">компаний пока нет</span>
                      ) : (
                        <div style={{ position: 'relative' }}>
                          <button
                            type="button"
                            disabled={actingOn === u.id}
                            onClick={() => setAccMenuFor(accMenuFor === u.id ? null : u.id)}
                            style={{
                              width: 220,
                              textAlign: 'left',
                              display: 'flex',
                              justifyContent: 'space-between',
                              gap: 8,
                            }}
                          >
                            <span
                              style={{
                                overflow: 'hidden',
                                textOverflow: 'ellipsis',
                                whiteSpace: 'nowrap',
                              }}
                            >
                              {accCompanyLabel(u)}
                            </span>
                            <span aria-hidden="true">▾</span>
                          </button>
                          {accMenuFor === u.id && (
                            <div
                              style={{
                                position: 'absolute',
                                zIndex: 20,
                                top: '100%',
                                left: 0,
                                width: 220,
                                marginTop: 2,
                                padding: '6px 8px',
                                background: '#fff',
                                border: '1px solid #c7ccd4',
                                borderRadius: 4,
                                boxShadow: '0 4px 12px rgba(0,0,0,0.15)',
                              }}
                            >
                              {companies.map((c) => (
                                <label
                                  key={c.id}
                                  style={{ display: 'block', fontWeight: 'normal' }}
                                >
                                  <input
                                    type="checkbox"
                                    checked={(accCompanies[u.id] || []).includes(c.id)}
                                    disabled={actingOn === u.id}
                                    onChange={(e) => toggleAccCompany(u, c.id, e.target.checked)}
                                  />{' '}
                                  {c.name}
                                </label>
                              ))}
                              <div className="small muted" style={{ marginTop: 4 }}>
                                главбух видит документы отмеченных юрлиц
                              </div>
                            </div>
                          )}
                        </div>
                      )
                    ) : (
                      <select
                        value={u.company_id || ''}
                        disabled={actingOn === u.id}
                        onChange={(e) => changeCompany(u, e.target.value)}
                        aria-label={`Компания пользователя ${u.login}`}
                      >
                        <option value="">— без привязки —</option>
                        {companies.map((c) => (
                          <option key={c.id} value={c.id}>
                            {c.name}
                          </option>
                        ))}
                      </select>
                    )}
                  </td>
                  <td>
                    {u.is_active ? (
                      <span className="muted">активен</span>
                    ) : (
                      <strong>заблокирован</strong>
                    )}
                  </td>
                  <td className="muted">{formatDateTime(u.created_at)}</td>
                  <td>
                    <button
                      type="button"
                      disabled={actingOn === u.id}
                      onClick={() => resetPassword(u)}
                    >
                      Сбросить пароль
                    </button>{' '}
                    <button
                      type="button"
                      disabled={actingOn === u.id}
                      onClick={() => toggleActive(u)}
                    >
                      {u.is_active ? 'Заблокировать' : 'Разблокировать'}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  )
}
