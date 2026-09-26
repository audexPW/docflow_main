package recognize

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"docflow/internal/domain"
)

// SplitPart — одна часть многодокументного файла. К моменту возврата PDF уже
// сохранён в хранилище, остаётся только завести под него запись документа.
type SplitPart struct {
	StorageKey   string
	SHA256       string
	SizeBytes    int64
	OriginalName string
	DocType      string
	FromPage     int
	ToPage       int
}

// Больше этого числа страниц не режем: скорее всего это один многостраничный
// документ (договор, приложение), а не пачка разных.
const splitMaxPages = 40

// Split проверяет, не лежит ли в одном файле несколько разных документов.
//
// Логика: каждая страница распознаётся отдельно и классифицируется ТОЛЬКО
// правилами (без обращения к модели — иначе разбор одного файла растянется на
// десятки минут). Подряд идущие страницы одного типа собираются в группу;
// страница, тип которой определить не удалось, считается продолжением
// предыдущего документа. Если группа получилась одна — делить нечего и метод
// возвращает nil, вызывающий код продолжает обычный разбор.
//
// Отключается переменной окружения SPLIT_MULTIDOC=false.
func (p *Pipeline) Split(ctx context.Context, doc domain.Document) ([]SplitPart, error) {
	if strings.EqualFold(strings.TrimSpace(envOr("SPLIT_MULTIDOC", "true")), "false") {
		return nil, nil
	}
	if !isPDFDoc(doc) {
		return nil, nil
	}

	workDir, err := os.MkdirTemp("", "docflow-split-*")
	if err != nil {
		return nil, fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(workDir)

	srcPath, err := p.files.ExtractTo(doc.StorageKey, workDir, "source.pdf")
	if err != nil {
		return nil, fmt.Errorf("extract source: %w", err)
	}

	pages, err := rasterizePDF(ctx, p.cfg.OCR, srcPath, workDir)
	if err != nil {
		return nil, fmt.Errorf("rasterize: %w", err)
	}
	if len(pages) < 2 || len(pages) > splitMaxPages {
		return nil, nil
	}

	types := make([]string, len(pages))
	for i, page := range pages {
		img := enhancePage(ctx, p.cfg.OCR, page, workDir, i)
		text, err := p.ocr.recognizePage(ctx, img)
		if err != nil {
			return nil, fmt.Errorf("page %d: %w", i+1, err)
		}
		types[i], _ = classifyByRules(text.Flat)
	}

	groups := groupPages(types)
	if len(groups) < 2 {
		return nil, nil
	}

	singles, err := separatePages(ctx, srcPath, workDir, len(pages))
	if err != nil {
		return nil, err
	}

	base := strings.TrimSuffix(doc.OriginalName, filepath.Ext(doc.OriginalName))
	parts := make([]SplitPart, 0, len(groups))
	for _, g := range groups {
		out := filepath.Join(workDir, fmt.Sprintf("part_%02d_%02d.pdf", g.from, g.to))
		if err := unitePages(ctx, singles[g.from-1:g.to], out); err != nil {
			return nil, err
		}

		f, err := os.Open(out)
		if err != nil {
			return nil, err
		}
		saved, saveErr := p.files.Save(f)
		f.Close()
		if saveErr != nil {
			return nil, fmt.Errorf("save part: %w", saveErr)
		}

		name := fmt.Sprintf("%s (стр. %d).pdf", base, g.from)
		if g.to > g.from {
			name = fmt.Sprintf("%s (стр. %d-%d).pdf", base, g.from, g.to)
		}

		parts = append(parts, SplitPart{
			StorageKey:   saved.Key,
			SHA256:       saved.SHA256,
			SizeBytes:    saved.Size,
			OriginalName: name,
			DocType:      g.docType,
			FromPage:     g.from,
			ToPage:       g.to,
		})
	}

	p.log.Info("multi-document file split",
		"id", doc.ID, "name", doc.OriginalName, "pages", len(pages), "parts", len(parts))
	return parts, nil
}

type pageGroup struct {
	from    int
	to      int
	docType string
}

func groupPages(types []string) []pageGroup {
	var groups []pageGroup
	for i, t := range types {
		page := i + 1
		known := t != "" && t != DocTypeUnknown

		if len(groups) == 0 {
			groups = append(groups, pageGroup{from: page, to: page, docType: t})
			continue
		}

		cur := &groups[len(groups)-1]
		curKnown := cur.docType != "" && cur.docType != DocTypeUnknown

		switch {
		case !known:
			// Тип страницы не опознан — считаем её продолжением текущего документа.
			cur.to = page
		case !curKnown:
			// Первые страницы были невнятными, а эта дала тип — присваиваем его группе.
			cur.to = page
			cur.docType = t
		case cur.docType == t:
			cur.to = page
		default:
			groups = append(groups, pageGroup{from: page, to: page, docType: t})
		}
	}
	return groups
}

// separatePages разбирает PDF на одностраничные файлы. Имена предсказуемы
// (pg-1.pdf … pg-N.pdf), поэтому обходимся без чтения каталога.
func separatePages(ctx context.Context, srcPath, workDir string, count int) ([]string, error) {
	pattern := filepath.Join(workDir, "pg-%d.pdf")
	cmd := exec.CommandContext(ctx, envOr("PDFSEPARATE_BIN", "pdfseparate"), srcPath, pattern)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("pdfseparate: %v: %s", err, strings.TrimSpace(string(out)))
	}

	files := make([]string, 0, count)
	for i := 1; i <= count; i++ {
		name := filepath.Join(workDir, fmt.Sprintf("pg-%d.pdf", i))
		if _, err := os.Stat(name); err != nil {
			return nil, fmt.Errorf("pdfseparate: страница %d не создана", i)
		}
		files = append(files, name)
	}
	return files, nil
}

func unitePages(ctx context.Context, pages []string, out string) error {
	if len(pages) == 0 {
		return fmt.Errorf("нечего объединять")
	}
	if len(pages) == 1 {
		return copyFile(pages[0], out)
	}
	args := append(append([]string{}, pages...), out)
	cmd := exec.CommandContext(ctx, envOr("PDFUNITE_BIN", "pdfunite"), args...)
	if o, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pdfunite: %v: %s", err, strings.TrimSpace(string(o)))
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, in)
	return err
}

func isPDFDoc(doc domain.Document) bool {
	if strings.Contains(strings.ToLower(doc.ContentType), "pdf") {
		return true
	}
	return strings.EqualFold(filepath.Ext(doc.OriginalName), ".pdf")
}
