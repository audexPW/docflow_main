package config

import (
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Env            string
	HTTPAddr       string
	DatabaseURL    string
	JWTSecret      []byte
	JWTTTL         time.Duration
	MaxUploadBytes int64
	CORSOrigins    []string
	// Locale — страновая специфика (ru|by). Влияет на распознавание налогового
	// идентификатора: by → УНП (9 цифр), ru → ИНН.
	Locale string

	// DocTypesPath — JSON-реестр типов документов и их соответствия объектам
	// 1С. Пусто — используется встроенный набор (без маппинга на 1С).
	DocTypesPath string

	// AccountsPath — JSON-план счетов и правил подбора счёта учёта по
	// содержимому документа. Пусто — счёт не подбирается, и бухгалтер
	// выбирает его в 1С руками, как до этой доработки.
	AccountsPath string

	Files FilesConfig
	OCR   OCRConfig
	LLM   LLMConfig
	OneC  OneCConfig
	Push  PushConfig

	// AutoExport включает автоматическую отправку в 1С: полностью распознанные
	// документы уходят сразу, при неполном распознавании — частично, с досылкой
	// вручную введённых полей. Если выключено — всё идёт на проверку оператором.
	AutoExport bool

	RecognizeWorkers int
	ExportWorkers    int
}

// PushConfig — серверные push-уведомления (FCM для Android, APNs для iOS).
type PushConfig struct {
	Enabled            bool
	FCMProjectID       string
	FCMCredentialsFile string
	APNSKeyFile        string
	APNSKeyID          string
	APNSTeamID         string
	APNSTopic          string
	APNSProduction     bool
}

type FilesConfig struct {
	Dir string
	Key []byte // AES-256, ровно 32 байта
}

type OCRConfig struct {
	// Engine выбирает движок распознавания: tesseract (быстрый, лёгкий по CPU/RAM),
	// paddle (PaddleOCR, точнее на грязных сканах — отдельный python-сервис),
	// surya (альтернативный тяжёлый движок), либо auto (сначала tesseract, при
	// слабом результате эскалация на тяжёлый движок из AutoFallback).
	Engine       string
	AutoFallback string // paddle|surya — на что эскалирует engine=auto
	TesseractBin string
	PdftoppmBin  string
	MagickBin    string
	PaddleURL    string // базовый URL paddle-ocr сервиса (для engine paddle|auto)
	SuryaURL     string // базовый URL surya-ocr сервиса (для engine surya|auto)
	Languages    string
	DPI          int
	Timeout      time.Duration
	// Layout — восстанавливать раскладку страницы по координатам слов
	// (колонки таблицы). Выключение возвращает прежний построчный разбор.
	Layout bool
	// LayoutGap — во сколько средних ширин символа должен быть горизонтальный
	// разрыв, чтобы считаться границей колонки.
	LayoutGap float64
}

// LLM — локальный сервис структуризации текста (llama.cpp / любой OpenAI-совместимый).
type LLMConfig struct {
	Enabled     bool
	BaseURL     string
	Model       string
	APIKey      string
	Timeout     time.Duration
	MaxTokens   int
	Temperature float64
}

type OneCConfig struct {
	Mode       string // rest | enterprisedata | file | disabled
	RESTURL    string
	AuthHeader string
	FileDir    string
	// RequireTypeMapping — отправлять в 1С только те типы, для которых в
	// реестре задан объект-приёмник. Остальное уходит оператору.
	RequireTypeMapping bool
	Timeout            time.Duration
	MaxAttempts        int
	// InboundToken — токен, которым 1С аутентифицируется, когда сообщает
	// статус обработки обратно (ТЗ §2, §10). Пусто — канал выключен.
	InboundToken string
	// OutboxKeepDays — через сколько дней архивировать выгруженные файлы,
	// которые 1С уже забрала. 0 — не трогать.
	OutboxKeepDays int
	// AckTimeout — если 1С не подтвердила обработку за это время, документ
	// попадает в отчёт «выгружено, но не подтверждено».
	AckTimeout time.Duration
	// ArchiveStatuses — статусы из 1С, после которых документ считается
	// закрытым и уезжает в архив. По умолчанию только posted («проведён»):
	// accepted означает лишь «файл принят», работа над ним ещё идёт.
	ArchiveStatuses []string
	// ArchiveKeepDays — сколько дней архивный документ хранится, прежде чем
	// быть удалённым вместе с файлом и своей выгрузкой. 0 — не удалять.
	ArchiveKeepDays int
}

// ArchivesOn отвечает, закрывает ли этот статус из 1С работу над документом.
func (c OneCConfig) ArchivesOn(status string) bool {
	for _, s := range c.ArchiveStatuses {
		if strings.EqualFold(strings.TrimSpace(s), status) {
			return true
		}
	}
	return false
}

func Load() (Config, error) {
	c := Config{
		Env:              env("APP_ENV", "development"),
		HTTPAddr:         env("HTTP_ADDR", ":8080"),
		DatabaseURL:      env("DATABASE_URL", "postgres://docflow:docflow@localhost:5432/docflow?sslmode=disable"),
		JWTTTL:           durEnv("JWT_TTL", 12*time.Hour),
		MaxUploadBytes:   int64Env("MAX_UPLOAD_BYTES", 25<<20),
		CORSOrigins:      splitEnv("CORS_ORIGINS", "*"),
		Locale:           env("LOCALE", "ru"),
		DocTypesPath:     env("DOCTYPES_PATH", ""),
		AccountsPath:     env("ACCOUNTS_PATH", ""),
		RecognizeWorkers: intEnv("RECOGNIZE_WORKERS", 2),
		ExportWorkers:    intEnv("EXPORT_WORKERS", 1),
	}

	secret := env("JWT_SECRET", "")
	if secret == "" {
		return c, fmt.Errorf("JWT_SECRET is required")
	}
	c.JWTSecret = []byte(secret)

	c.Files.Dir = env("FILES_DIR", "./data/files")
	key, err := decodeKey(env("FILES_ENCRYPTION_KEY", ""))
	if err != nil {
		return c, fmt.Errorf("FILES_ENCRYPTION_KEY: %w", err)
	}
	c.Files.Key = key

	c.OCR = OCRConfig{
		Engine:       env("OCR_ENGINE", "tesseract"),
		AutoFallback: env("OCR_AUTO_FALLBACK", "surya"),
		TesseractBin: env("TESSERACT_BIN", "tesseract"),
		PdftoppmBin:  env("PDFTOPPM_BIN", "pdftoppm"),
		MagickBin:    env("MAGICK_BIN", "convert"),
		PaddleURL:    env("PADDLE_URL", "http://paddle-ocr:8423"),
		SuryaURL:     env("SURYA_URL", "http://surya-ocr:8422"),
		Languages:    env("OCR_LANGUAGES", "rus+eng"),
		DPI:          intEnv("OCR_DPI", 300),
		Timeout:      durEnv("OCR_TIMEOUT", 90*time.Second),
		Layout:       boolEnv("OCR_LAYOUT", true),
		LayoutGap:    floatEnv("OCR_LAYOUT_GAP", 2.5),
	}

	c.LLM = LLMConfig{
		Enabled:     boolEnv("LLM_ENABLED", true),
		BaseURL:     env("LLM_BASE_URL", "http://127.0.0.1:8081/v1"),
		Model:       env("LLM_MODEL", "local"),
		APIKey:      env("LLM_API_KEY", ""),
		Timeout:     durEnv("LLM_TIMEOUT", 60*time.Second),
		MaxTokens:   intEnv("LLM_MAX_TOKENS", 3072),
		Temperature: floatEnv("LLM_TEMPERATURE", 0.0),
	}

	c.OneC = OneCConfig{
		Mode:               env("ONEC_MODE", "file"),
		RESTURL:            env("ONEC_REST_URL", ""),
		AuthHeader:         env("ONEC_AUTH_HEADER", ""),
		FileDir:            env("ONEC_FILE_DIR", "./data/onec_outbox"),
		RequireTypeMapping: boolEnv("ONEC_REQUIRE_TYPE_MAPPING", true),
		Timeout:            durEnv("ONEC_TIMEOUT", 30*time.Second),
		MaxAttempts:        intEnv("ONEC_MAX_ATTEMPTS", 8),
		InboundToken:       env("ONEC_INBOUND_TOKEN", ""),
		// Эти три ключа лежали в .env, но не читались: уборка каталога обмена
		// и предупреждение о молчании 1С были выключены при любом значении.
		OutboxKeepDays:  intEnv("ONEC_OUTBOX_KEEP_DAYS", 14),
		AckTimeout:      durEnv("ONEC_ACK_TIMEOUT", 48*time.Hour),
		ArchiveStatuses: splitEnv("ONEC_ARCHIVE_STATUSES", "posted"),
		ArchiveKeepDays: intEnv("ARCHIVE_KEEP_DAYS", 21),
	}

	c.AutoExport = boolEnv("AUTO_EXPORT_ENABLED", true)

	c.Push = PushConfig{
		Enabled:            boolEnv("PUSH_ENABLED", false),
		FCMProjectID:       env("FCM_PROJECT_ID", ""),
		FCMCredentialsFile: env("FCM_CREDENTIALS_FILE", ""),
		APNSKeyFile:        env("APNS_KEY_FILE", ""),
		APNSKeyID:          env("APNS_KEY_ID", ""),
		APNSTeamID:         env("APNS_TEAM_ID", ""),
		APNSTopic:          env("APNS_TOPIC", "ru.docflow.app"),
		APNSProduction:     boolEnv("APNS_PRODUCTION", true),
	}

	return c, nil
}

func (c Config) IsProd() bool { return strings.EqualFold(c.Env, "production") }

func decodeKey(s string) ([]byte, error) {
	if s == "" {
		return nil, fmt.Errorf("required (hex-encoded 32 bytes)")
	}
	key, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("must be hex: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("must decode to 32 bytes, got %d", len(key))
	}
	return key, nil
}

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return def
}

func intEnv(k string, def int) int {
	if v, ok := os.LookupEnv(k); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func int64Env(k string, def int64) int64 {
	if v, ok := os.LookupEnv(k); ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

func floatEnv(k string, def float64) float64 {
	if v, ok := os.LookupEnv(k); ok {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			return n
		}
	}
	return def
}

func boolEnv(k string, def bool) bool {
	if v, ok := os.LookupEnv(k); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func durEnv(k string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv(k); ok {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func splitEnv(k, def string) []string {
	raw := env(k, def)
	parts := strings.Split(raw, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
