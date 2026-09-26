import { useEffect, useState } from 'react'
import { NavLink, Outlet, useLocation } from 'react-router-dom'
import { useAuth } from '../auth.jsx'
import { api } from '../api.js'
import { roleLabel, setDocTypeCatalog } from '../lib/labels.js'
import NotificationWatcher from './NotificationWatcher.jsx'
import PushRegistrar from './PushRegistrar.jsx'
import OfflineBadge from './OfflineBadge.jsx'

// Клиенту и оператору пароль назначает администратор — экран смены пароля им
// не показываем. Запрет продублирован на сервере.
export function canChangeOwnPassword(role) {
  return role === 'accountant' || role === 'admin'
}

export default function Layout() {
  const { role, logout } = useAuth()
  const isStaff = role === 'operator' || role === 'admin'
  const [menuOpen, setMenuOpen] = useState(false)
  const location = useLocation()

  // Пунктов меню у админа много (7 штук) — на телефоне они не умещаются в
  // строку, поэтому на узких экранах прячем их за гамбургер. Закрываем меню
  // при каждом переходе, иначе оно остаётся открытым поверх новой страницы.
  useEffect(() => {
    setMenuOpen(false)
  }, [location.pathname])

  // Список типов документов открыт и меняется на сервере без пересборки
  // фронтенда, поэтому подтягиваем его один раз за сессию. Ошибка не критична:
  // подписи деградируют до кодов, интерфейс продолжает работать.
  useEffect(() => {
    let cancelled = false
    api
      .listDocTypes()
      .then((data) => {
        if (!cancelled) setDocTypeCatalog(data && data.types)
      })
      .catch(() => {})
    return () => {
      cancelled = true
    }
  }, [])

  return (
    <>
      <header className="topbar">
        <span className="brand">ПартнерБухгалтер</span>
        <nav className={menuOpen ? 'nav-open' : ''}>
          <NavLink to="/documents">Документы</NavLink>
          <NavLink to="/archive">Архив</NavLink>
          {role !== 'accountant' && <NavLink to="/upload">Загрузить</NavLink>}
          {role === 'admin' && <NavLink to="/companies">Компании</NavLink>}
          {role === 'admin' && <NavLink to="/users">Пользователи</NavLink>}
          {role === 'admin' && <NavLink to="/audit">Журнал</NavLink>}
          {canChangeOwnPassword(role) && <NavLink to="/password">Пароль</NavLink>}
        </nav>
        <span className="user">
          <OfflineBadge />
          <span className="user-role">{roleLabel(role)}</span>
          {' · '}
          <a
            href="#"
            onClick={(e) => {
              e.preventDefault()
              logout()
            }}
          >
            Выйти
          </a>
        </span>
        <button
          type="button"
          className={`nav-toggle${menuOpen ? ' open' : ''}`}
          aria-label={menuOpen ? 'Закрыть меню' : 'Открыть меню'}
          aria-expanded={menuOpen}
          onClick={() => setMenuOpen((v) => !v)}
        >
          <span />
          <span />
          <span />
        </button>
      </header>
      {menuOpen && <div className="nav-scrim" onClick={() => setMenuOpen(false)} />}
      <NotificationWatcher />
      <PushRegistrar />
      <main className="page">
        <Outlet />
      </main>
      {/* Логотип компании — виден на всех страницах внутри Layout.
          Экран входа рендерится вне Layout, поэтому там его нет. */}
      <img
        className="corner-logo"
        src="/logo.svg?v=3"
        alt="Аудиторская экспертиза"
      />
    </>
  )
}
