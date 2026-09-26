package config

import (
	"fmt"
	"strings"
)

// Известные значения из примеров конфигурации. Если они доехали до боевого
// сервера — это не «настройка по умолчанию», это открытая дверь.
var wellKnownSecrets = map[string]string{
	"change-me":                        "значение из .env.example",
	"docflow":                          "пароль по умолчанию",
	"postgres":                         "пароль по умолчанию",
	"changeme":                         "значение-заглушка",
	"secret":                           "значение-заглушка",
	"00000000000000000000000000000000": "нулевой ключ",
}

// Validate не даёт подняться в production с конфигурацией, которая выглядит
// безопасной в разработке и опасной в бою. Падение при старте лучше, чем
// работающая система с ключом из примера: первое заметят сразу, второе — нет.
func (c Config) Validate() error {
	var problems []string

	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	if len(c.JWTSecret) < 32 {
		add("JWT_SECRET короче 32 байт")
	}
	if len(c.Files.Key) != 32 {
		add("FILES_ENCRYPTION_KEY должен быть ровно 32 байта (64 hex-символа)")
	}

	if !c.IsProd() {
		if len(problems) == 0 {
			return nil
		}
		return fmt.Errorf("конфигурация: %s", strings.Join(problems, "; "))
	}

	// Дальше — только для production.
	if reason, bad := wellKnownSecrets[strings.ToLower(string(c.JWTSecret))]; bad {
		add("JWT_SECRET — %s", reason)
	}
	if dsnHasWellKnownPassword(c.DatabaseURL) {
		add("пароль PostgreSQL взят из примера — смените POSTGRES_PASSWORD")
	}
	for _, o := range c.CORSOrigins {
		if strings.TrimSpace(o) == "*" {
			add("CORS_ORIGINS=* в production — укажите адрес сайта заказчика")
		}
	}
	if c.OneC.Mode != "disabled" && c.OneC.InboundToken == "" {
		add("ONEC_INBOUND_TOKEN пуст — 1С не сможет вернуть статус обработки (ТЗ §2, §10)")
	}

	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("небезопасная конфигурация для APP_ENV=production:\n  - %s",
		strings.Join(problems, "\n  - "))
}

func dsnHasWellKnownPassword(dsn string) bool {
	// postgres://user:password@host/db
	at := strings.LastIndex(dsn, "@")
	scheme := strings.Index(dsn, "://")
	if at < 0 || scheme < 0 || at < scheme {
		return false
	}
	creds := dsn[scheme+3 : at]
	_, pass, ok := strings.Cut(creds, ":")
	if !ok {
		return false
	}
	_, bad := wellKnownSecrets[strings.ToLower(pass)]
	return bad
}
