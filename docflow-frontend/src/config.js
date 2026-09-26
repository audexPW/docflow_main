// Базовый адрес API.
// - В вебе оставляем пустым: запросы идут на тот же хост (/api/...),
//   nginx проксирует их на бэкенд.
// - В мобильном приложении указываем полный адрес сервера через
//   VITE_API_BASE_URL при сборке (см. .env.example).
export const API_BASE = (import.meta.env.VITE_API_BASE_URL || '').replace(/\/$/, '')
