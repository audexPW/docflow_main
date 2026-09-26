package push

import (
	"fmt"
	"log/slog"

	"docflow/internal/config"
)

// New строит Service по конфигурации. Если push выключен — возвращает Noop.
// Если включён, но какой-то транспорт настроен неполно/с ошибкой — этот транспорт
// пропускается (с предупреждением в лог), остальное продолжает работать.
func New(cfg config.PushConfig, store TokenStore, log *slog.Logger) (Notifier, error) {
	if !cfg.Enabled {
		log.Info("push disabled; mobile clients fall back to status polling")
		return Noop{}, nil
	}

	s := &Service{store: store, log: log}

	if cfg.FCMProjectID != "" && cfg.FCMCredentialsFile != "" {
		fcm, err := newFCM(cfg.FCMProjectID, cfg.FCMCredentialsFile)
		if err != nil {
			log.Warn("push: FCM disabled (bad config)", "error", err)
		} else {
			s.fcm = fcm
			log.Info("push: FCM enabled", "project", cfg.FCMProjectID)
		}
	} else {
		log.Warn("push: FCM not configured (FCM_PROJECT_ID / FCM_CREDENTIALS_FILE)")
	}

	if cfg.APNSKeyFile != "" && cfg.APNSKeyID != "" && cfg.APNSTeamID != "" {
		apns, err := newAPNS(cfg)
		if err != nil {
			log.Warn("push: APNs disabled (bad config)", "error", err)
		} else {
			s.apns = apns
			log.Info("push: APNs enabled", "topic", cfg.APNSTopic, "production", cfg.APNSProduction)
		}
	} else {
		log.Warn("push: APNs not configured (APNS_KEY_FILE / APNS_KEY_ID / APNS_TEAM_ID)")
	}

	if s.fcm == nil && s.apns == nil {
		return Noop{}, fmt.Errorf("push enabled but no transport is configured")
	}
	return s, nil
}
