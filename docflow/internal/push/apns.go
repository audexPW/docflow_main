package push

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"docflow/internal/config"

	"github.com/golang-jwt/jwt/v5"
)

// apnsProvider отправляет через APNs (token-based auth, ключ .p8, ES256).
// Go negotiates HTTP/2 автоматически по TLS, отдельная библиотека не нужна.
type apnsProvider struct {
	host   string
	topic  string
	keyID  string
	teamID string
	key    *ecdsa.PrivateKey
	http   *http.Client

	mu        sync.Mutex
	cachedJWT string
	issuedAt  time.Time
}

func newAPNS(cfg config.PushConfig) (*apnsProvider, error) {
	raw, err := os.ReadFile(cfg.APNSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("read key: %w", err)
	}
	key, err := jwt.ParseECPrivateKeyFromPEM(raw)
	if err != nil {
		return nil, fmt.Errorf("parse .p8 key: %w", err)
	}
	host := "https://api.push.apple.com"
	if !cfg.APNSProduction {
		host = "https://api.sandbox.push.apple.com"
	}
	topic := cfg.APNSTopic
	if topic == "" {
		topic = "ru.docflow.app"
	}
	return &apnsProvider{
		host:   host,
		topic:  topic,
		keyID:  cfg.APNSKeyID,
		teamID: cfg.APNSTeamID,
		key:    key,
		http:   &http.Client{Timeout: 15 * time.Second},
	}, nil
}

func (a *apnsProvider) providerToken() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	// APNs допускает токен возрастом до часа; обновляем каждые ~50 минут.
	if a.cachedJWT != "" && time.Since(a.issuedAt) < 50*time.Minute {
		return a.cachedJWT, nil
	}
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss": a.teamID,
		"iat": now.Unix(),
	})
	tok.Header["kid"] = a.keyID
	signed, err := tok.SignedString(a.key)
	if err != nil {
		return "", err
	}
	a.cachedJWT = signed
	a.issuedAt = now
	return signed, nil
}

func (a *apnsProvider) send(ctx context.Context, token string, n Notification) (bool, error) {
	jwtTok, err := a.providerToken()
	if err != nil {
		return false, fmt.Errorf("apns jwt: %w", err)
	}

	payload := map[string]any{
		"aps": map[string]any{
			"alert": map[string]string{"title": n.Title, "body": n.Body},
			"sound": "default",
		},
	}
	for k, v := range n.Data {
		payload[k] = v
	}
	body, _ := json.Marshal(payload)

	url := a.host + "/3/device/" + token
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("authorization", "bearer "+jwtTok)
	req.Header.Set("apns-topic", a.topic)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", "10")

	resp, err := a.http.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))

	if resp.StatusCode == http.StatusOK {
		return false, nil
	}
	// 410 Gone / 400 BadDeviceToken — токен пора удалить.
	invalid := resp.StatusCode == http.StatusGone ||
		(resp.StatusCode == http.StatusBadRequest && bytes.Contains(snippet, []byte("BadDeviceToken")))
	return invalid, fmt.Errorf("apns responded %d: %s", resp.StatusCode, bytes.TrimSpace(snippet))
}
