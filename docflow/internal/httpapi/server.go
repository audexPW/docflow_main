package httpapi

import (
	"context"
	"log/slog"
	"net/http"

	"docflow/internal/auth"
	"docflow/internal/config"
	"docflow/internal/domain"
	"docflow/internal/filestore"
	"docflow/internal/storage"

	"github.com/google/uuid"
)

// Notifier — уведомление владельца документа о смене статуса.
type Notifier interface {
	NotifyStatus(ctx context.Context, ownerID uuid.UUID, doc domain.Document)
}

type Server struct {
	cfg              config.Config
	db               *storage.DB
	tokens           *auth.TokenManager
	files            *filestore.Store
	log              *slog.Logger
	notifier         Notifier
	onecInboundToken string
	logins           *loginAttempts
}

func NewServer(cfg config.Config, db *storage.DB, tokens *auth.TokenManager, files *filestore.Store, log *slog.Logger) *Server {
	return &Server{
		cfg:              cfg,
		db:               db,
		tokens:           tokens,
		files:            files,
		log:              log,
		onecInboundToken: cfg.OneC.InboundToken,
		logins:           newLoginAttempts(),
	}
}

// WithNotifier подключает пуш-уведомления. Необязательно: без него сервер
// работает, просто молча.
func (s *Server) WithNotifier(n Notifier) *Server {
	s.notifier = n
	return s
}

// allRoles — маршруты, доступные любому вошедшему; что именно он увидит,
// решает область видимости (handlers_scope.go), а не список ролей в маршруте.
var allRoles = []domain.Role{domain.RoleClient, domain.RoleOperator, domain.RoleAccountant, domain.RoleAdmin}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("POST /api/auth/login", s.loginLimiter(s.handleLogin))

	// Свой пароль может сменить любой вошедший — учётку заводит администратор,
	// и выданный им пароль пользователь обязан иметь возможность заменить.
	// Клиент и оператор пароль себе не меняют: учётки им выдаёт администратор,
	// он же перевыпускает пароль при утере. Так администратор всегда знает,
	// каким паролем пользуется сотрудник клиента, и учётка не «уплывает».
	mux.HandleFunc("POST /api/auth/password", s.auth(s.handleChangeOwnPassword,
		domain.RoleAccountant, domain.RoleAdmin))

	// Обратная синхронизация статусов из 1С (ТЗ §2, §10).
	// Аутентификация — ONEC_INBOUND_TOKEN, не пользовательская сессия.
	mux.HandleFunc("POST /api/onec/status", s.handleOneCStatus)

	// Документы
	mux.HandleFunc("GET /api/doctypes", s.auth(s.handleListDocTypes, allRoles...))
	mux.HandleFunc("POST /api/documents", s.auth(s.handleUploadDocument, domain.RoleClient, domain.RoleOperator, domain.RoleAdmin))
	mux.HandleFunc("GET /api/documents", s.auth(s.handleListDocuments, allRoles...))
	mux.HandleFunc("GET /api/documents/{id}", s.auth(s.handleGetDocument, allRoles...))
	mux.HandleFunc("GET /api/documents/{id}/file", s.auth(s.handleDownloadFile, allRoles...))
	mux.HandleFunc("PATCH /api/documents/{id}", s.auth(s.handleUpdateRecognition, domain.RoleOperator, domain.RoleAccountant, domain.RoleAdmin))
	mux.HandleFunc("POST /api/documents/{id}/confirm", s.auth(s.handleConfirmDocument, domain.RoleOperator, domain.RoleAccountant, domain.RoleAdmin))
	// Правка реквизитов доступна и главбуху: он ведёт несколько юрлиц и
	// поправляет распознанное перед выгрузкой сам, не дожидаясь оператора.
	// Дозаполнение недостающих полей и досыл в 1С — доступно и клиенту (для своих документов).
	mux.HandleFunc("POST /api/documents/{id}/complete", s.auth(s.handleCompleteDocument, domain.RoleClient, domain.RoleOperator, domain.RoleAccountant, domain.RoleAdmin))
	// Возврат документа из архива: 1С отчиталась ошибочно или документ
	// переоткрыли. Доступно главбуху и администратору.
	mux.HandleFunc("POST /api/documents/{id}/unarchive", s.auth(s.handleUnarchiveDocument, domain.RoleAccountant, domain.RoleAdmin))
	mux.HandleFunc("POST /api/documents/{id}/reprocess", s.auth(s.handleReprocessDocument, domain.RoleOperator, domain.RoleAccountant, domain.RoleAdmin))

	// Согласование главбухом: он закреплён администратором за конкретными
	// компаниями и работает только с их документами.
	mux.HandleFunc("POST /api/documents/{id}/approve", s.auth(s.handleApproveDocument, domain.RoleAccountant, domain.RoleAdmin))
	mux.HandleFunc("POST /api/documents/{id}/reject", s.auth(s.handleRejectDocument, domain.RoleAccountant, domain.RoleAdmin))

	// Проставление юрлица документу вручную: для загрузок, сделанных под
	// учёткой без привязки к компании.
	mux.HandleFunc("POST /api/documents/{id}/company", s.auth(s.handleSetDocumentCompany, domain.RoleAdmin))

	// Компании (юрлица). Список видят все — каждый в своей области видимости;
	// заводит и правит только администратор.
	mux.HandleFunc("GET /api/companies", s.auth(s.handleListCompanies, allRoles...))
	mux.HandleFunc("POST /api/companies", s.auth(s.handleCreateCompany, domain.RoleAdmin))
	mux.HandleFunc("PATCH /api/companies/{id}", s.auth(s.handleUpdateCompany, domain.RoleAdmin))
	mux.HandleFunc("GET /api/companies/{id}/accountants", s.auth(s.handleListCompanyAccountants, domain.RoleAdmin))
	mux.HandleFunc("PUT /api/companies/{id}/accountants", s.auth(s.handleSetCompanyAccountants, domain.RoleAdmin))

	// Регистрация устройств для push-уведомлений
	mux.HandleFunc("POST /api/devices", s.auth(s.handleRegisterDevice, allRoles...))
	mux.HandleFunc("DELETE /api/devices", s.auth(s.handleUnregisterDevice, allRoles...))

	// Администрирование
	mux.HandleFunc("POST /api/users", s.auth(s.handleCreateUser, domain.RoleAdmin))
	mux.HandleFunc("GET /api/users", s.auth(s.handleListUsers, domain.RoleAdmin))
	mux.HandleFunc("POST /api/users/{id}/password", s.auth(s.handleAdminSetPassword, domain.RoleAdmin))
	mux.HandleFunc("POST /api/users/{id}/active", s.auth(s.handleSetUserActive, domain.RoleAdmin))
	mux.HandleFunc("POST /api/users/{id}/role", s.auth(s.handleSetUserRole, domain.RoleAdmin))
	mux.HandleFunc("POST /api/users/{id}/company", s.auth(s.handleSetUserCompany, domain.RoleAdmin))
	mux.HandleFunc("GET /api/users/{id}/companies", s.auth(s.handleListUserCompanies, domain.RoleAdmin))
	mux.HandleFunc("PUT /api/users/{id}/companies", s.auth(s.handleSetUserCompanies, domain.RoleAdmin))
	mux.HandleFunc("GET /api/audit", s.auth(s.handleListAudit, domain.RoleAdmin))

	var h http.Handler = mux
	h = requestLogger(s.log, h)
	h = cors(s.cfg.CORSOrigins, h)
	h = recoverer(s.log, h)
	return h
}

// auth — короткий помощник: аутентификация + проверка ролей.
func (s *Server) auth(handler http.HandlerFunc, roles ...domain.Role) http.HandlerFunc {
	return s.authenticate(requireRole(roles...)(handler))
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := s.db.PingContext(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
