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

// suryaClient обращается к локальному surya-ocr сервису (Python/FastAPI),
// который держит модели Surya в памяти и распознаёт присланное изображение.
// Сервис разворачивается на инфраструктуре заказчика и наружу ничего не шлёт.
//
// Таймаут намеренно не задаётся на самом http.Client — распознавание на CPU
// может идти долго, а верхнюю границу задаёт контекст вызова (OCR_TIMEOUT).
type suryaClient struct {
	url  string
	http *http.Client
}

func newSuryaClient(cfg config.OCRConfig) *suryaClient {
	return &suryaClient{
		url:  strings.TrimRight(cfg.SuryaURL, "/"),
		http: &http.Client{},
	}
}

type suryaResponse struct {
	// Text — текст с восстановленной раскладкой (колонки разделены « | »),
	// Flat — построчный, как раньше. Старая версия сервиса Flat не отдаёт —
	// тогда оба вида берём из Text.
	Text  string `json:"text"`
	Flat  string `json:"flat"`
	Lines int    `json:"lines"`
	// Tables — сетка от детектора таблиц. Старая версия сервиса поля не
	// отдаёт: тогда список пуст и разбор идёт по раскладке, как раньше.
	Tables []GridTable `json:"tables"`
	// Boxes — слова страницы с координатами. Старая версия сервиса поля не
	// отдаёт: тогда проекция колонок не работает и в дело идут прежние пути.
	Boxes []suryaBox `json:"boxes"`
	Error string     `json:"error"`
}

// suryaBox — слово с прямоугольником [x0,y0,x1,y1] в координатах страницы.
type suryaBox struct {
	Text string     `json:"text"`
	BBox [4]float64 `json:"bbox"`
}

// ocr отправляет изображение страницы в surya-ocr и возвращает распознанный текст.
func (c *suryaClient) ocr(ctx context.Context, imagePath string) (pageText, error) {
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
		return pageText{}, fmt.Errorf("surya request: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return pageText{}, fmt.Errorf("surya returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var parsed suryaResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return pageText{}, fmt.Errorf("decode surya response: %w", err)
	}
	if parsed.Error != "" {
		return pageText{}, fmt.Errorf("surya error: %s", parsed.Error)
	}
	flat := parsed.Flat
	if strings.TrimSpace(flat) == "" {
		flat = stripCellSeparators(parsed.Text)
	}
	words := make([]wordBox, 0, len(parsed.Boxes))
	for _, b := range parsed.Boxes {
		if strings.TrimSpace(b.Text) == "" {
			continue
		}
		words = append(words, wordBox{
			Text: b.Text,
			X0:   b.BBox[0], Y0: b.BBox[1], X1: b.BBox[2], Y1: b.BBox[3],
		})
	}
	return pageText{Layout: parsed.Text, Flat: flat, Grids: parsed.Tables, Words: words}, nil
}

// stripCellSeparators убирает границы ячеек из текста с раскладкой: регулярки
// в extract.go рассчитаны на обычный построчный текст.
func stripCellSeparators(s string) string {
	s = strings.ReplaceAll(s, " | ", " ")
	return strings.ReplaceAll(s, "|", " ")
}
