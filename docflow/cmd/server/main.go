package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"

	"syscall"
	"time"

	"docflow/internal/auth"
	"docflow/internal/config"
	"docflow/internal/domain"
	"docflow/internal/filestore"
	"docflow/internal/httpapi"
	"docflow/internal/onec"
	"docflow/internal/push"
	"docflow/internal/recognize"
	"docflow/internal/storage"
	"docflow/internal/worker"
)

func main() {
	// Docker healthcheck запускает этот же бинарник с -healthcheck: так в
	// образе не нужны ни curl, ни wget, и проверка ходит по тому же адресу,
	// который слушает сервис.
	healthcheck := flag.Bool("healthcheck", false, "проверить живость сервиса и выйти")
	flag.Parse()
	if *healthcheck {
		if err := probeHealth(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if err := run(log); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// В production небезопасная конфигурация — повод не подняться вовсе.
	// Упавший при старте сервис заметят сразу; работающий с ключом из
	// примера — не заметит никто.
	if err := cfg.Validate(); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := storage.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := db.Migrate(ctx); err != nil {
		return err
	}
	if err := bootstrapAdmin(ctx, db, log); err != nil {
		return err
	}

	files, err := filestore.New(cfg.Files.Dir, cfg.Files.Key)
	if err != nil {
		return err
	}

	exporter, err := onec.NewExporter(cfg.OneC, log)
	if err != nil {
		return err
	}

	notifier, err := push.New(cfg.Push, db, log)
	if err != nil {
		return err
	}

	tokens := auth.NewTokenManager(cfg.JWTSecret, cfg.JWTTTL)
	recognize.SetLocale(cfg.Locale)

	// Реестр типов документов и их соответствия объектам 1С. Типы без маппинга
	// не уходят в базу автоматически — о них нужно знать сразу на старте, а не
	// когда бухгалтер не найдёт документ.
	unmapped, err := recognize.LoadDocTypes(cfg.DocTypesPath)
	if err != nil {
		log.Error("doctypes load failed", "error", err)
		os.Exit(1)
	}
	if len(unmapped) > 0 {
		log.Warn("типы документов без маппинга на 1С — уйдут оператору на проверку",
			"types", strings.Join(unmapped, ","), "doctypes_path", cfg.DocTypesPath)
	}
	// План счетов и правила подбора счёта учёта. Без него система работает
	// как раньше, но бухгалтер выбирает счёт в 1С руками для каждой позиции —
	// ради этого доработка и делалась, поэтому отсутствие файла заметное.
	if err := recognize.LoadAccounts(cfg.AccountsPath); err != nil {
		log.Error("accounts load failed", "error", err, "accounts_path", cfg.AccountsPath)
		os.Exit(1)
	}
	if recognize.AccountsEnabled() {
		log.Info("подбор счетов учёта включён", "accounts_path", cfg.AccountsPath)
	} else {
		log.Warn("подбор счетов учёта выключен — ACCOUNTS_PATH не задан; счёт в 1С придётся выбирать вручную")
	}

	pipeline := recognize.NewPipeline(cfg, files, log)

	recognizer := worker.NewRecognizer(db, pipeline, notifier, cfg.AutoExport, cfg.OneC.RequireTypeMapping, log)
	exportWorker := worker.NewExporter(db, exporter, notifier, cfg.OneC.MaxAttempts, log)
	housekeeper := worker.NewHousekeeper(db, files, cfg.OneC, log)
	go recognizer.Run(ctx, cfg.RecognizeWorkers)
	go exportWorker.Run(ctx, cfg.ExportWorkers)
	go housekeeper.Run(ctx)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpapi.NewServer(cfg, db, tokens, files, log).WithNotifier(notifier).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Info("http server listening", "addr", cfg.HTTPAddr, "env", cfg.Env)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		return err
	case <-ctx.Done():
		log.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// bootstrapAdmin создаёт первого администратора при пустой базе. Пароль берётся
// из BOOTSTRAP_ADMIN_PASSWORD либо генерируется и печатается в лог один раз.
func bootstrapAdmin(ctx context.Context, db *storage.DB, log *slog.Logger) error {
	count, err := db.CountUsers(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	login := getenv("BOOTSTRAP_ADMIN_LOGIN", "admin")
	password := os.Getenv("BOOTSTRAP_ADMIN_PASSWORD")
	generated := false
	if password == "" {
		buf := make([]byte, 12)
		if _, err := rand.Read(buf); err != nil {
			return err
		}
		password = hex.EncodeToString(buf)
		generated = true
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	if _, err := db.CreateUser(ctx, login, hash, domain.RoleAdmin); err != nil {
		return err
	}

	if generated {
		log.Warn("created bootstrap admin with generated password — change it after first login",
			"login", login, "password", password)
	} else {
		log.Info("created bootstrap admin", "login", login)
	}
	return nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// probeHealth дёргает /healthz локально. Используется только Docker-ом.
func probeHealth() error {
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("некорректный HTTP_ADDR %q: %w", addr, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}

	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		return fmt.Errorf("сервис не отвечает: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("сервис ответил %d", resp.StatusCode)
	}
	return nil
}
