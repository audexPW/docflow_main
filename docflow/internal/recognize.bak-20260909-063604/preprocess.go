package recognize

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"docflow/internal/config"
)

// preparePages приводит входной файл к списку изображений-страниц, готовых к OCR.
// PDF растрируется через pdftoppm, затем КАЖДАЯ страница (и одиночное
// изображение тоже) прогоняется через ImageMagick: выравнивание наклона,
// подавление шума, растяжка контраста. На мятых фотографиях и сканах с
// неравномерной подсветкой это даёт основной прирост качества OCR.
func preparePages(ctx context.Context, cfg config.OCRConfig, srcPath, contentType, workDir string) ([]string, error) {
	ext := strings.ToLower(filepath.Ext(srcPath))
	isPDF := contentType == "application/pdf" || ext == ".pdf"

	var pages []string
	if isPDF {
		p, err := rasterizePDF(ctx, cfg, srcPath, workDir)
		if err != nil {
			return nil, err
		}
		pages = p
	} else {
		pages = []string{srcPath}
	}

	out := make([]string, 0, len(pages))
	for i, p := range pages {
		// Поворот страницы правим ДО остальной обработки: и выравнивание
		// наклона, и распознавание строк, и сборка таблицы по колонкам
		// предполагают, что текст идёт слева направо.
		p = autoRotatePage(ctx, cfg, p, workDir, i)
		out = append(out, enhancePage(ctx, cfg, p, workDir, i))
	}
	return out, nil
}

// autoRotatePage разворачивает страницу, снятую боком или вверх ногами.
//
// Зачем отдельный шаг. В enhancePage есть -auto-orient и -deskew, но ни то ни
// другое здесь не помогает: -auto-orient читает только EXIF-тег Orientation,
// которого у сканов и страниц из PDF обычно нет, а -deskew правит наклон в
// пределах нескольких градусов и лист, повёрнутый на 90°, не трогает.
//
// В итоге боковая страница уходила в OCR как есть. Детектор строк на ней почти
// ничего не находит — отсюда документы вовсе без текста; а если что-то и
// находит, строки и графы меняются местами, и таблица получается
// транспонированной.
//
// Угол берём у Tesseract OSD (--psm 0): это отдельная модель ориентации, она
// работает по форме букв и не зависит от языка документа. Порог уверенности —
// OCR_ROTATE_MIN_CONF. Не уверены или OSD недоступен — оставляем страницу как
// есть, поведение прежнее.
//
//	OCR_AUTOROTATE      true (по умолчанию) | false
//	OCR_ROTATE_MIN_CONF минимальная уверенность OSD, по умолчанию 1.0
func autoRotatePage(ctx context.Context, cfg config.OCRConfig, srcPath, workDir string, idx int) string {
	if v := strings.ToLower(envOr("OCR_AUTOROTATE", "true")); v == "false" || v == "0" || v == "off" {
		return srcPath
	}
	deg := detectRotation(ctx, cfg, srcPath)
	if deg == 0 {
		return srcPath
	}

	dst := filepath.Join(workDir, fmt.Sprintf("rot_%03d.png", idx))
	// ImageMagick крутит по часовой стрелке, OSD сообщает, на сколько лист
	// повёрнут против; отсюда обратный знак.
	cmd := exec.CommandContext(ctx, cfg.MagickBin, srcPath,
		"-rotate", fmt.Sprintf("%d", 360-deg), "-strip", dst)
	if err := cmd.Run(); err != nil {
		return srcPath
	}
	if st, err := os.Stat(dst); err != nil || st.Size() == 0 {
		return srcPath
	}
	return dst
}

// detectRotation спрашивает у Tesseract OSD угол поворота страницы.
// Возвращает 0, 90, 180 или 270 — на сколько градусов лист повёрнут против
// часовой стрелки. 0 означает «не поворачивать»: либо страница ровная, либо
// определить не удалось.
func detectRotation(ctx context.Context, cfg config.OCRConfig, srcPath string) int {
	cmd := exec.CommandContext(ctx, cfg.TesseractBin, srcPath, "stdout", "--psm", "0", "-l", "osd")
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Чаще всего это отсутствующий пакет tesseract-ocr-osd: без файла
		// osd.traineddata режим --psm 0 не запускается. Молча работаем как
		// раньше — распознавание из-за этого падать не должно.
		return 0
	}

	minConf := 1.0
	if v := envOr("OCR_ROTATE_MIN_CONF", ""); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			minConf = f
		}
	}

	return parseOSD(string(out), minConf)
}

// parseOSD разбирает ответ Tesseract OSD. Вынесено отдельно от запуска
// процесса, чтобы разбор можно было проверить тестом.
func parseOSD(out string, minConf float64) int {
	deg, conf := 0, 0.0
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "Rotate:"):
			v, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Rotate:")))
			if err == nil {
				deg = v
			}
		case strings.HasPrefix(line, "Orientation confidence:"):
			f, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(line, "Orientation confidence:")), 64)
			if err == nil {
				conf = f
			}
		}
	}

	deg = ((deg % 360) + 360) % 360
	if deg%90 != 0 || deg == 0 {
		return 0
	}
	if conf < minConf {
		return 0
	}
	return deg
}

func rasterizePDF(ctx context.Context, cfg config.OCRConfig, srcPath, workDir string) ([]string, error) {
	prefix := filepath.Join(workDir, "page")
	cmd := exec.CommandContext(ctx, cfg.PdftoppmBin,
		"-png",
		"-r", fmt.Sprintf("%d", cfg.DPI),
		"-gray",
		srcPath, prefix,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("pdftoppm: %v: %s", err, strings.TrimSpace(string(out)))
	}

	matches, err := filepath.Glob(prefix + "*.png")
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("pdftoppm produced no pages")
	}
	sort.Strings(matches)
	return matches, nil
}

// enhancePage улучшает одну страницу через ImageMagick. Поведение настраивается
// переменными окружения, чтобы подбирать параметры без пересборки образа:
//
//	OCR_ENHANCE   basic (по умолчанию) | photo | off
//	OCR_DESKEW    порог выравнивания наклона, например 40% (off — выключить)
//	OCR_LEVEL     растяжка уровней для режима basic, например 12%,92%
//	OCR_LAT       адаптивная бинаризация для режима photo, например 25x25-10%
//	OCR_SHARPEN   резкость, например 0x1 (off — выключить)
//	OCR_UPSCALE   масштабирование, например 150% (пусто — не менять)
//
// Если ImageMagick недоступен или упал — возвращаем исходный файл, OCR
// отработает и по нему.
func enhancePage(ctx context.Context, cfg config.OCRConfig, srcPath, workDir string, idx int) string {
	mode := strings.ToLower(envOr("OCR_ENHANCE", "basic"))
	if mode == "off" || mode == "none" {
		return srcPath
	}

	dst := filepath.Join(workDir, fmt.Sprintf("prep_%03d.png", idx))

	args := []string{
		srcPath,
		"-auto-orient",
		"-background", "white",
		"-alpha", "remove",
		"-colorspace", "Gray",
	}

	if v := envOr("OCR_DESKEW", "40%"); v != "off" {
		args = append(args, "-deskew", v)
	}

	args = append(args, "-despeckle", "-normalize")

	if mode == "photo" {
		// Адаптивный порог по окрестности: вытягивает текст на фотографиях с
		// бликами и неравномерной подсветкой, где глобальная бинаризация
		// выжигает половину листа.
		args = append(args, "-lat", envOr("OCR_LAT", "25x25-10%"))
	} else {
		args = append(args, "-level", envOr("OCR_LEVEL", "12%,92%"))
	}

	if v := envOr("OCR_SHARPEN", "0x1"); v != "off" {
		args = append(args, "-sharpen", v)
	}
	if v := envOr("OCR_UPSCALE", ""); v != "" {
		args = append(args, "-resize", v)
	}

	args = append(args, "-strip", dst)

	cmd := exec.CommandContext(ctx, cfg.MagickBin, args...)
	if err := cmd.Run(); err != nil {
		return srcPath
	}
	if st, err := os.Stat(dst); err != nil || st.Size() == 0 {
		return srcPath
	}
	return dst
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
