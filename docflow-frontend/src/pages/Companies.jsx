import { useEffect, useState, useCallback } from 'react'
import { api } from '../api.js'
import { formatDateTime } from '../lib/format.js'

// Компании (юрлица) заказчика. У каждой своя папка обмена: 1С забирает
// документы компании А только из папки А. Заводит их администратор — он же
// закрепляет за компанией главбуха и переназначает его при необходимости.
export default function Companies() {
  const [items, setItems] = useState([])
  const [accountants, setAccountants] = useState([]) // все учётки с ролью «Главбух»
  const [assigned, setAssigned] = useState({}) // { companyId: [userId] }
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState(false)

  const [name, setName] = useState('')
  const [unp, setUnp] = useState('')
  const [folder, setFolder] = useState('')
  const [approval, setApproval] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const [companies, users] = await Promise.all([api.listCompanies(), api.listUsers()])
      const list = companies.items || []
      setItems(list)
      setAccountants((users.items || []).filter((u) => u.role === 'accountant'))

      const pairs = await Promise.all(
        list.map((c) =>
          api
            .listCompanyAccountants(c.id)
            .then((res) => [c.id, (res.items || []).map((u) => u.id)])
            .catch(() => [c.id, []])
        )
      )
      setAssigned(Object.fromEntries(pairs))
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
      await api.createCompany({
        name: name.trim(),
        unp: unp.trim(),
        folder: folder.trim(),
        approval_required: approval,
      })
      setNotice('Компания создана, папка обмена готова')
      setName('')
      setUnp('')
      setFolder('')
      setApproval(false)
      await load()
    } catch (err) {
      setError(err.message)
    } finally {
      setBusy(false)
    }
  }

  async function act(fn, successText) {
    setBusy(true)
    setError('')
    setNotice('')
    try {
      await fn()
      setNotice(successText)
      await load()
    } catch (err) {
      setError(err.message)
    } finally {
      setBusy(false)
    }
  }

  function toggleAccountant(companyId, userId, checked) {
    const current = assigned[companyId] || []
    const next = checked ? [...current, userId] : current.filter((id) => id !== userId)
    setAssigned((prev) => ({ ...prev, [companyId]: next }))
    act(
      () => api.setCompanyAccountants(companyId, next),
      checked ? 'Главбух закреплён за компанией' : 'Главбух снят с компании'
    )
  }

  return (
    <div>
      <h1>Компании</h1>
      {error && <div className="error">{error}</div>}
      {notice && <div className="notice">{notice}</div>}

      <div className="panel">
        <h2>Новая компания</h2>
        <p className="muted small" style={{ marginTop: 0 }}>
          Папка обмена создаётся вместе с компанией внутри каталога выгрузки. Если не
          заполнить, именем станет УНП. Латиница, цифры, дефис.
        </p>
        <form onSubmit={create}>
          <div className="row">
            <div className="field">
              <label>Название</label>
              <input type="text" value={name} onChange={(e) => setName(e.target.value)} />
            </div>
            <div className="field" style={{ maxWidth: 180 }}>
              <label>УНП</label>
              <input type="text" value={unp} onChange={(e) => setUnp(e.target.value)} />
            </div>
            <div className="field" style={{ maxWidth: 220 }}>
              <label>Папка обмена</label>
              <input
                type="text"
                value={folder}
                placeholder="по умолчанию — УНП"
                onChange={(e) => setFolder(e.target.value)}
              />
            </div>
          </div>
          <label style={{ display: 'block', marginBottom: 12 }}>
            <input
              type="checkbox"
              checked={approval}
              onChange={(e) => setApproval(e.target.checked)}
            />{' '}
            Документы уходят в 1С только после согласования главбухом
          </label>
          <button type="submit" className="btn-primary" disabled={busy || !name.trim()}>
            Создать
          </button>
        </form>
      </div>

      {loading ? (
        <div className="empty">Загрузка…</div>
      ) : items.length === 0 ? (
        <div className="empty">Компаний ещё нет. Заведите первую — под неё создастся папка.</div>
      ) : (
        items.map((c) => (
          <div className="panel" key={c.id} style={{ opacity: c.is_active ? 1 : 0.6 }}>
            <div className="toolbar" style={{ marginBottom: 10 }}>
              <h2 style={{ margin: 0 }}>{c.name}</h2>
              <div className="spacer" />
              <button
                type="button"
                disabled={busy}
                onClick={() =>
                  act(
                    () => api.updateCompany(c.id, { is_active: !c.is_active }),
                    c.is_active ? 'Компания отключена' : 'Компания включена'
                  )
                }
              >
                {c.is_active ? 'Отключить' : 'Включить'}
              </button>
            </div>

            <table>
              <tbody>
                <tr>
                  <th style={{ width: 200 }}>УНП</th>
                  <td>{c.unp || <span className="muted">—</span>}</td>
                </tr>
                <tr>
                  <th>Папка обмена</th>
                  <td>
                    <code>{c.folder}</code>
                  </td>
                </tr>
                <tr>
                  <th>Согласование главбухом</th>
                  <td>
                    <label>
                      <input
                        type="checkbox"
                        checked={c.approval_required}
                        disabled={busy}
                        onChange={(e) =>
                          act(
                            () => api.updateCompany(c.id, { approval_required: e.target.checked }),
                            'Порядок согласования изменён'
                          )
                        }
                      />{' '}
                      {c.approval_required
                        ? 'документ ждёт решения главбуха'
                        : 'документы уходят в 1С сразу'}
                    </label>
                  </td>
                </tr>
                <tr>
                  <th>Создана</th>
                  <td className="muted">{formatDateTime(c.created_at)}</td>
                </tr>
                <tr>
                  <th>Главбухи</th>
                  <td>
                    {accountants.length === 0 ? (
                      <span className="muted">
                        Нет учёток с ролью «Главбух» — заведите их на странице «Пользователи».
                      </span>
                    ) : (
                      accountants.map((u) => (
                        <label key={u.id} style={{ display: 'block' }}>
                          <input
                            type="checkbox"
                            disabled={busy}
                            checked={(assigned[c.id] || []).includes(u.id)}
                            onChange={(e) => toggleAccountant(c.id, u.id, e.target.checked)}
                          />{' '}
                          {u.login}
                        </label>
                      ))
                    )}
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        ))
      )}
    </div>
  )
}
