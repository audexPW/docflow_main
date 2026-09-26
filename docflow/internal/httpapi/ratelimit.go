package httpapi

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// Защита входа от перебора. Пароли проверяются PBKDF2 с 210 000 итераций,
// то есть каждая попытка стоит серверу заметного CPU — без ограничения
// достаточно одного скрипта, чтобы и подобрать пароль, и положить сервис.
//
// Ограничение по паре «IP + логин»: так один шумный офис за общим NAT не
// блокирует вход остальным сотрудникам.

const (
	loginMaxAttempts = 8
	loginWindow      = 5 * time.Minute
	loginBlockFor    = 15 * time.Minute
)

type loginAttempts struct {
	mu    sync.Mutex
	seen  map[string]*attemptState
	clean time.Time
}

type attemptState struct {
	count       int
	windowStart time.Time
	blockedTill time.Time
}

func newLoginAttempts() *loginAttempts {
	return &loginAttempts{seen: make(map[string]*attemptState)}
}

// allow сообщает, можно ли принять ещё одну попытку входа.
func (l *loginAttempts) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.gc(now)

	st, ok := l.seen[key]
	if !ok {
		l.seen[key] = &attemptState{windowStart: now}
		return true, 0
	}
	if now.Before(st.blockedTill) {
		return false, time.Until(st.blockedTill)
	}
	if now.Sub(st.windowStart) > loginWindow {
		st.count, st.windowStart = 0, now
	}
	return true, 0
}

// fail отмечает неудачную попытку и при переполнении блокирует пару.
func (l *loginAttempts) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()

	st, ok := l.seen[key]
	if !ok {
		st = &attemptState{windowStart: now}
		l.seen[key] = st
	}
	if now.Sub(st.windowStart) > loginWindow {
		st.count, st.windowStart = 0, now
	}
	st.count++
	if st.count >= loginMaxAttempts {
		st.blockedTill = now.Add(loginBlockFor)
		st.count = 0
		st.windowStart = now
	}
}

// success сбрасывает счётчик после удачного входа.
func (l *loginAttempts) success(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.seen, key)
}

// gc не даёт карте расти бесконечно при переборе случайных логинов.
func (l *loginAttempts) gc(now time.Time) {
	if now.Sub(l.clean) < loginWindow {
		return
	}
	l.clean = now
	for k, st := range l.seen {
		if now.After(st.blockedTill) && now.Sub(st.windowStart) > loginWindow {
			delete(l.seen, k)
		}
	}
}

func (s *Server) loginLimiter(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := clientIP(r)
		if ok, retryIn := s.logins.allow(key); !ok {
			w.Header().Set("Retry-After", formatSeconds(retryIn))
			writeError(w, http.StatusTooManyRequests, "too many sign-in attempts, try again later")
			return
		}
		next(w, r)
	}
}

func clientIP(r *http.Request) string {
	// За обратным прокси реальный адрес приходит заголовком; берём первый.
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		if i := indexByte(fwd, ','); i > 0 {
			return trimSpace(fwd[:i])
		}
		return trimSpace(fwd)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

func formatSeconds(d time.Duration) string {
	sec := int(d.Seconds())
	if sec < 1 {
		sec = 1
	}
	return itoa(sec)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
