package recognize

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"unicode"

	"docflow/internal/config"
)

// ocrEngine — абстракция движка распознавания одной страницы-изображения.
// Реализации: tesseract (локальный бинарник), paddle и surya (python-сервисы
// по HTTP), auto (tesseract с эскалацией на тяжёлый движок при слабом результате).
type ocrEngine interface {
	// recognizePage возвращает страницу в двух видах: с восстановленной
	// раскладкой (колонки таблицы разделены « | ») и построчно. Раскладка
	// нужна, чтобы таблица уходила в 1С таблицей, а не набором строк.
	recognizePage(ctx context.Context, imagePath string) (pageText, error)
	name() string
}

// newOCREngine выбирает движок по конфигурации. Значение по умолчанию —
// tesseract: он самый лёгкий по CPU/RAM и не требует отдельного сервиса.
func newOCREngine(cfg config.OCRConfig) ocrEngine {
	switch strings.ToLower(strings.TrimSpace(cfg.Engine)) {
	case "paddle":
		return &paddleEngine{client: newPaddleClient(cfg)}
	case "surya":
		return &suryaEngine{client: newSuryaClient(cfg)}
	case "auto":
		return &autoEngine{
			primary:  tesseractEngine{cfg: cfg},
			fallback: heavyEngine(cfg),
		}
	default:
		return tesseractEngine{cfg: cfg}
	}
}

// heavyEngine — тяжёлый движок, на который эскалирует auto. По умолчанию
// PaddleOCR; surya оставлена как альтернатива (OCR_AUTO_FALLBACK=surya).
func heavyEngine(cfg config.OCRConfig) ocrEngine {
	if strings.EqualFold(strings.TrimSpace(cfg.AutoFallback), "surya") {
		return &suryaEngine{client: newSuryaClient(cfg)}
	}
	return &paddleEngine{client: newPaddleClient(cfg)}
}

// runOCR прогоняет выбранный движок по каждой странице и склеивает результат.
func runOCR(ctx context.Context, engine ocrEngine, pages []string) (pageText, error) {
	var layout, flat strings.Builder
	var grids []GridTable
	var words []wordBox
	// Страницы сдвигаем по вертикали, иначе слова второго листа окажутся на
	// одной строке с первым и проекция колонок смешает две разные таблицы.
	var yOffset float64
	for i, page := range pages {
		text, err := engine.recognizePage(ctx, page)
		if err != nil {
			return pageText{}, fmt.Errorf("page %d: %w", i+1, err)
		}
		if i > 0 {
			layout.WriteString("\n\n")
			flat.WriteString("\n\n")
		}
		layout.WriteString(text.Layout)
		flat.WriteString(text.Flat)
		grids = append(grids, text.Grids...)

		var pageBottom float64
		for _, w := range text.Words {
			w.Y0 += yOffset
			w.Y1 += yOffset
			words = append(words, w)
			if w.Y1 > pageBottom {
				pageBottom = w.Y1
			}
		}
		yOffset = pageBottom + 1000
	}
	return pageText{Layout: layout.String(), Flat: flat.String(), Grids: grids, Words: words}, nil
}

// --- tesseract ---------------------------------------------------------------

type tesseractEngine struct {
	cfg config.OCRConfig
}

func (e tesseractEngine) name() string { return "tesseract" }

func (e tesseractEngine) recognizePage(ctx context.Context, imagePath string) (pageText, error) {
	// Режим tsv отдаёт слова с координатами: из одного прогона получаем и
	// раскладку с колонками, и обычный построчный текст. Прогонять страницу
	// дважды (текст + tsv) на этом железе слишком дорого.
	if e.cfg.Layout {
		if txt, err := tesseractTSV(ctx, e.cfg, imagePath); err == nil && strings.TrimSpace(txt.Flat) != "" {
			return txt, nil
		}
	}

	// stdout: tesseract пишет результат в "-" при указании stdout как выхода.
	cmd := exec.CommandContext(ctx, e.cfg.TesseractBin,
		imagePath, "stdout",
		"-l", e.cfg.Languages,
		"--oem", "1",
		"--psm", "3",
	)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return pageText{}, fmt.Errorf("tesseract: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return pageText{}, fmt.Errorf("tesseract: %w", err)
	}
	return pageText{Layout: string(out), Flat: string(out)}, nil
}

// --- paddle (HTTP-движок реализован в paddle.go) ----------------------------

type paddleEngine struct {
	client *paddleClient
}

func (e *paddleEngine) name() string { return "paddle" }

func (e *paddleEngine) recognizePage(ctx context.Context, imagePath string) (pageText, error) {
	return e.client.ocr(ctx, imagePath)
}

// --- surya (HTTP-движок реализован в surya.go) ------------------------------

type suryaEngine struct {
	client *suryaClient
}

func (e *suryaEngine) name() string { return "surya" }

func (e *suryaEngine) recognizePage(ctx context.Context, imagePath string) (pageText, error) {
	return e.client.ocr(ctx, imagePath)
}

// --- auto: tesseract с эскалацией на surya ----------------------------------

// autoEngine сначала пробует быстрый tesseract и, только если результат выглядит
// слабым (мало букв — типичный признак грязного скана, который tesseract не
// вытянул), догоняет более тяжёлым, но точным движком (PaddleOCR или Surya).
// Так основной поток идёт дёшево по CPU, а модель включается точечно.
type autoEngine struct {
	primary  tesseractEngine
	fallback ocrEngine
}

func (e *autoEngine) name() string { return "auto" }

func (e *autoEngine) recognizePage(ctx context.Context, imagePath string) (pageText, error) {
	text, err := e.primary.recognizePage(ctx, imagePath)
	if err == nil && ocrLooksSolid(text.Flat) {
		return text, nil
	}

	stext, serr := e.fallback.recognizePage(ctx, imagePath)
	if serr != nil {
		// тяжёлый движок недоступен/упал — отдаём хоть что-то от tesseract.
		if err != nil {
			return pageText{}, serr
		}
		return text, nil
	}
	return stext, nil
}

// ocrLooksSolid — грубая эвристика «текст распознан осмысленно»: достаточно
// букв и приличная доля буквенных символов. Порог намеренно мягкий — задача не
// оценить качество, а лишь отсеять почти пустой/мусорный результат tesseract.
func ocrLooksSolid(text string) bool {
	var letters, total int
	for _, r := range text {
		if unicode.IsSpace(r) {
			continue
		}
		total++
		if unicode.IsLetter(r) {
			letters++
		}
	}
	if letters < 24 {
		return false
	}
	return total == 0 || float64(letters)/float64(total) >= 0.5
}
