package recognize

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"docflow/internal/config"
)

// paddleClient обращается к локальному paddle-ocr сервису (Python/FastAPI),
// который держит модели PaddleOCR (детекция + распознавание, кириллица) в
// памяти и распознаёт присланное изображение страницы. Сервис разворачивается
// на инфраструктуре заказчика и наружу ничего не шлёт.
//
// Таймаут намеренно не задаётся на самом http.Client — распознавание на CPU
// может идти долго, а верхнюю границу задаёт контекст вызова (OCR_TIMEOUT).
type paddleClient struct {
	url  string
	http *http.Client
}

func newPaddleClient(cfg config.OCRConfig) *paddleClient {
	return &paddleClient{
		url:  strings.TrimRight(cfg.PaddleURL, "/"),
		http: &http.Client{},
	}
}

type paddleResponse struct {
	Text  string `json:"text"`
	Flat  string `json:"flat"`
	Lines int    `json:"lines"`
	Error string `json:"error"`
}

// ocr отправляет изображение страницы в paddle-ocr и возвращает распознанный текст.
func (c *paddleClient) ocr(ctx context.Context, imagePath string) (pageText, error) {
	f, err := os.Open(imagePath)
	if err != nil {
		return pageText{}, fmt.Errorf("open page: %w", err)
	}
	defer f.Close()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", filepath.Base(imagePath))
	if err != nil {
		return pageText{}, err
	}
	if _, err := io.Copy(part, f); err != nil {
		return pageText{}, err
	}
	if err := mw.Close(); err != nil {
		return pageText{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+"/ocr", &body)
	if err != nil {
		return pageText{}, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := c.http.Do(req)
	if err != nil {
		return pageText{}, fmt.Errorf("paddle request: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return pageText{}, fmt.Errorf("paddle returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var parsed paddleResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return pageText{}, fmt.Errorf("decode paddle response: %w", err)
	}
	if parsed.Error != "" {
		return pageText{}, fmt.Errorf("paddle error: %s", parsed.Error)
	}
	flat := parsed.Flat
	if strings.TrimSpace(flat) == "" {
		flat = stripCellSeparators(parsed.Text)
	}
	return pageText{Layout: parsed.Text, Flat: flat}, nil
}
