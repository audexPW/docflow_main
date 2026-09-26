import { Routes, Route, Navigate, useLocation } from 'react-router-dom'
import { useAuth } from './auth.jsx'
import Layout, { canChangeOwnPassword } from './components/Layout.jsx'
import Login from './pages/Login.jsx'
import Documents from './pages/Documents.jsx'
import Archive from './pages/Archive.jsx'
import DocumentDetail from './pages/DocumentDetail.jsx'
import Upload from './pages/Upload.jsx'
import Users from './pages/Users.jsx'
import Companies from './pages/Companies.jsx'
import ChangePassword from './pages/ChangePassword.jsx'
import Audit from './pages/Audit.jsx'

function RequireAuth({ children, roles }) {
  const { isAuthenticated, role, mustChangePassword } = useAuth()
  const location = useLocation()
  if (!isAuthenticated) {
    return <Navigate to="/login" state={{ from: location }} replace />
  }
  // Пока установочный пароль не сменён, дальше экрана смены пароля не пускаем:
  // этот пароль лежит открытым текстом в .env на сервере. Клиента и оператора
  // это не касается — сменить пароль они не могут, и держать их на экране,
  // который им запрещён, значило бы закрыть им вход совсем.
  if (mustChangePassword && canChangeOwnPassword(role) && location.pathname !== '/password') {
    return <Navigate to="/password" replace />
  }
  if (roles && !roles.includes(role)) {
    return <Navigate to="/documents" replace />
  }
  return children
}

export default function App() {
  const { isAuthenticated } = useAuth()

  return (
    <Routes>
      <Route
        path="/login"
        element={isAuthenticated ? <Navigate to="/documents" replace /> : <Login />}
      />

      <Route
        element={
          <RequireAuth>
            <Layout />
          </RequireAuth>
        }
      >
        <Route path="/documents" element={<Documents />} />
        <Route path="/documents/:id" element={<DocumentDetail />} />
        <Route path="/archive" element={<Archive />} />
        <Route path="/upload" element={<Upload />} />
        <Route
          path="/password"
          element={
            <RequireAuth roles={['accountant', 'admin']}>
              <ChangePassword />
            </RequireAuth>
          }
        />
        <Route
          path="/users"
          element={
            <RequireAuth roles={['admin']}>
              <Users />
            </RequireAuth>
          }
        />
        <Route
          path="/companies"
          element={
            <RequireAuth roles={['admin']}>
              <Companies />
            </RequireAuth>
          }
        />
        <Route
          path="/audit"
          element={
            <RequireAuth roles={['admin']}>
              <Audit />
            </RequireAuth>
          }
        />
      </Route>

      <Route path="*" element={<Navigate to="/documents" replace />} />
    </Routes>
  )
}
