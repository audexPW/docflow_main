package push

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// fcmProvider отправляет через FCM HTTP v1. Авторизация — OAuth2 access token,
// полученный по service account (JWT bearer grant). Всё на стандартной библиотеке
// и golang-jwt, без внешних SDK.
type fcmProvider struct {
	projectID string
	endpoint  string
	oauth     *oauthSource
	http      *http.Client
}

type serviceAccount struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
	ProjectID   string `json:"project_id"`
}

func newFCM(projectID, credsFile string) (*fcmProvider, error) {
	raw, err := os.ReadFile(credsFile)
	if err != nil {
		return nil, fmt.Errorf("read credentials: %w", err)
	}
	var sa serviceAccount
	if err := json.Unmarshal(raw, &sa); err != nil {
		return nil, fmt.Errorf("parse credentials json: %w", err)
	}
	if sa.ClientEmail == "" || sa.PrivateKey == "" {
		return nil, fmt.Errorf("credentials missing client_email/private_key")
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(sa.PrivateKey))
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	tokenURI := sa.TokenURI
	if tokenURI == "" {
		tokenURI = "https://oauth2.googleapis.com/token"
	}

	return &fcmProvider{
		projectID: projectID,
		endpoint:  fmt.Sprintf("https://fcm.googleapis.com/v1/projects/%s/messages:send", projectID),
		oauth: &oauthSource{
			clientEmail: sa.ClientEmail,
			key:         key,
			tokenURI:    tokenURI,
			scope:       "https://www.googleapis.com/auth/firebase.messaging",
			http:        &http.Client{Timeout: 15 * time.Second},
		},
		http: &http.Client{Timeout: 15 * time.Second},
	}, nil
}

func (f *fcmProvider) send(ctx context.Context, token string, n Notification) (bool, error) {
	access, err := f.oauth.token(ctx)
	if err != nil {
		return false, fmt.Errorf("oauth token: %w", err)
	}

	msg := map[string]any{
		"message": map[string]any{
			"token": token,
			"notification": map[string]string{
				"title": n.Title,
				"body":  n.Body,
			},
			"data": n.Data,
		},
	}
	body, _ := json.Marshal(msg)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.endpoint, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")

	resp, err := f.http.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return false, nil
	}
	// Токен устройства больше не валиден — сигналим на удаление.
	if resp.StatusCode == http.StatusNotFound ||
		(resp.StatusCode == http.StatusBadRequest && strings.Contains(string(snippet), "registration-token-not-registered")) {
		return true, fmt.Errorf("fcm token invalid: %s", strings.TrimSpace(string(snippet)))
	}
	return false, fmt.Errorf("fcm responded %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
}

// oauthSource выдаёт и кэширует OAuth2 access token для service account.
type oauthSource struct {
	clientEmail string
	key         *rsa.PrivateKey
	tokenURI    string
	scope       string
	http        *http.Client

	mu      sync.Mutex
	cached  string
	expires time.Time
}

func (o *oauthSource) token(ctx context.Context) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.cached != "" && time.Until(o.expires) > time.Minute {
		return o.cached, nil
	}

	now := time.Now()
	claims := jwt.MapClaims{
		"iss":   o.clientEmail,
		"scope": o.scope,
		"aud":   o.tokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}
	assertion, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(o.key)
	if err != nil {
		return "", err
	}

	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
	form.Set("assertion", assertion)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.tokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := o.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(data, &tr); err != nil {
		return "", err
	}
	if tr.AccessToken == "" {
		return "", fmt.Errorf("empty access_token")
	}
	o.cached = tr.AccessToken
	o.expires = now.Add(time.Duration(tr.ExpiresIn) * time.Second)
	return o.cached, nil
}
