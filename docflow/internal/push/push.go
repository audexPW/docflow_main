// Package push отправляет серверные уведомления на мобильные устройства через
// FCM (Android) и APNs (iOS). Токены устройств хранятся в БД и привязаны к
// пользователю; при доставке невалидному токену он удаляется.
//
// Это единственная часть системы, которая по своей природе обращается к внешним
// сервисам (Google/Apple) — иначе push на мобильных телефонах невозможен. Наружу
// уходит только сам факт смены статуса и имя документа, но не его содержимое и не
// распознанные данные. Если push не настроен, модуль работает как no-op, а
// приложение продолжает опрашивать статусы и показывать локальные уведомления.
package push

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"docflow/internal/domain"

	"github.com/google/uuid"
)

// Notification — одно уведомление.
type Notification struct {
	Title string
	Body  string
	Data  map[string]string
}

// Notifier — то, что дёргают воркеры при смене статуса документа.
type Notifier interface {
	NotifyStatus(ctx context.Context, ownerID uuid.UUID, doc domain.Document)
}

// TokenStore — доступ к токенам устройств (реализуется storage.DB).
type TokenStore interface {
	DeviceTokensForUser(ctx context.Context, userID uuid.UUID) ([]domain.DeviceToken, error)
	DeleteDeviceToken(ctx context.Context, token string) error
}

// Noop — заглушка, когда push отключён.
type Noop struct{}

func (Noop) NotifyStatus(context.Context, uuid.UUID, domain.Document) {}

// provider — конкретный транспорт (fcm/apns).
type provider interface {
	// send возвращает invalid=true, если токен нужно удалить (устройство отписалось).
	send(ctx context.Context, token string, n Notification) (invalid bool, err error)
}

// Service рассылает уведомления по токенам пользователя, выбирая транспорт по
// платформе токена.
type Service struct {
	store TokenStore
	fcm   provider // может быть nil
	apns  provider // может быть nil
	log   *slog.Logger
}

// NotifyStatus отправляет владельцу документа уведомление о его текущем статусе.
// Ошибки логируются, но не прерывают работу воркера.
func (s *Service) NotifyStatus(ctx context.Context, ownerID uuid.UUID, doc domain.Document) {
	title, body, ok := messageFor(doc)
	if !ok {
		return
	}

	tokens, err := s.store.DeviceTokensForUser(ctx, ownerID)
	if err != nil {
		s.log.Warn("push: load device tokens", "error", err, "user", ownerID)
		return
	}
	if len(tokens) == 0 {
		return
	}

	n := Notification{
		Title: title,
		Body:  body,
		Data: map[string]string{
			"document_id": doc.ID.String(),
			"status":      string(doc.Status),
		},
	}

	// Не блокируем воркер на сетевых операциях дольше разумного.
	sendCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	for _, t := range tokens {
		var prov provider
		switch t.Platform {
		case "android":
			prov = s.fcm
		case "ios":
			prov = s.apns
		default:
			continue // web обслуживается на стороне браузера
		}
		if prov == nil {
			continue
		}
		invalid, err := prov.send(sendCtx, t.Token, n)
		if err != nil {
			s.log.Warn("push: send failed", "platform", t.Platform, "error", err)
		}
		if invalid {
			if derr := s.store.DeleteDeviceToken(context.Background(), t.Token); derr != nil {
				s.log.Warn("push: delete stale token", "error", derr)
			}
		}
	}
}

// messageFor возвращает заголовок и текст уведомления для значимых статусов.
// Незначимые (processing, confirmed) уведомлений не порождают.
func messageFor(doc domain.Document) (title, body string, ok bool) {
	name := strings.TrimSpace(doc.OriginalName)
	if name == "" {
		name = "Документ"
	}
	// Ответ из 1С важнее внутреннего статуса: пользователю нужно знать, что
	// документ реально проведён или отклонён.
	switch doc.OneCStatus {
	case "posted":
		return "Проведён в 1С", name, true
	case "rejected", "error":
		text := strings.TrimSpace(doc.OneCMessage)
		if text == "" {
			text = name + " — 1С отклонила документ"
		}
		return "1С отклонила документ", text, true
	}

	switch doc.Status {
	case domain.StatusReceived:
		return "Документ принят", name, true
	case domain.StatusNeedsReview:
		return "Документ распознан", name + " — ожидает проверки", true
	case domain.StatusNeedsInput:
		return "Нужно уточнение", name + " — не все поля распознаны, укажите их вручную", true
	case domain.StatusExported:
		return "Отправлено в 1С", name, true
	case domain.StatusFailed:
		return "Ошибка распознавания", name, true
	}
	return "", "", false
}
