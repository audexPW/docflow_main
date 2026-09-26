package onec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"docflow/internal/config"
)

type restExporter struct {
	url        string
	authHeader string
	http       *http.Client
}

func newRESTExporter(cfg config.OneCConfig) *restExporter {
	return &restExporter{
		url:        cfg.RESTURL,
		authHeader: cfg.AuthHeader,
		http:       &http.Client{Timeout: cfg.Timeout},
	}
}

func (e *restExporter) Export(ctx context.Context, p Payload) error {
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.authHeader != "" {
		name, value, ok := strings.Cut(e.authHeader, ":")
		if ok {
			req.Header.Set(strings.TrimSpace(name), strings.TrimSpace(value))
		}
	}

	resp, err := e.http.Do(req)
	if err != nil {
		return fmt.Errorf("post to 1c: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("1c responded %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return nil
}
