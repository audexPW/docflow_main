import { createContext, useContext, useState, useCallback } from 'react'
import { api, saveSession, clearSession, getSession } from './api.js'
import { unregisterForPush } from './lib/push.js'

const AuthContext = createContext(null)

export function AuthProvider({ children }) {
  const [session, setSession] = useState(() => getSession())
  // Признак «пароль выдан администратором и его нужно сменить» приходит при
  // входе. В localStorage не кладём: он живёт ровно одну сессию.
  const [mustChangePassword, setMustChange] = useState(false)

  const login = useCallback(async (loginName, password) => {
    const res = await api.login(loginName, password)
    saveSession(res)
    setSession({ token: res.token, role: res.role })
    setMustChange(!!res.must_change_password)
    return res
  }, [])

  const clearMustChange = useCallback(() => setMustChange(false), [])

  const logout = useCallback(() => {
    // Отвязываем push-токен устройства (пока ещё есть авторизация), затем чистим сессию.
    unregisterForPush().finally(() => {
      clearSession()
      setSession(null)
    })
  }, [])

  const value = {
    session,
    role: session?.role || null,
    isAuthenticated: !!session,
    mustChangePassword,
    clearMustChange,
    login,
    logout,
  }
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function useAuth() {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth вне AuthProvider')
  return ctx
}
