// accuracy измеряет точность распознавания на выборке документов заказчика.
//
// ТЗ §10 требует «точность распознавания на тестовой выборке на согласованном
// уровне». Подтвердить это можно только цифрой, полученной на реальных
// документах, поэтому инструмент устроен так, чтобы замер занимал у заказчика
// один вечер, а не проект:
//
//  1. складываете сканы в каталог;
//  2. рядом с каждым файлом кладёте одноимённый .json с верными значениями
//     (или один expected.json на весь каталог);
//  3. запускаете и получаете таблицу по полям.
//
// Формат ожидаемых значений — ровно то, что заказчик видит в документе:
//
//	{
//	  "doc_type": "invoice",
//	  "number":   "145",
//	  "date":     "12.03.2026",
//	  "total":    "375.00",
//	  "unp":      "191234567"
//	}
//
// Запуск (в контейнере бэкенда, чтобы были OCR и модель):
//
//	docker compose exec docflow-backend /app/accuracy -dir /samples
//
// Результат — доля верно распознанных значений по каждому полю и общий итог.
// Именно эту таблицу подписывают как согласованный уровень точности.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"docflow/internal/config"

	"docflow/internal/filestore"
	"docflow/internal/recognize"
)

type expected map[string]string

type fieldStat struct {
	total   int
	correct int
	missed  int // не распозналось вовсе
	wrong   int // распозналось, но неверно
}

func main() {
	dir := flag.String("dir", "", "каталог с документами и ожидаемыми значениями")
	verbose := flag.Bool("v", false, "печатать разбор по каждому документу")
	flag.Parse()

	if *dir == "" {
		fmt.Fprintln(os.Stderr, "укажите каталог: -dir /samples")
		os.Exit(2)
	}
	if err := run(*dir, *verbose); err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}
}

func run(dir string, verbose bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	recognize.SetLocale(cfg.Locale)

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	files, err := filestore.New(cfg.Files.Dir, cfg.Files.Key)
	if err != nil {
		return err
	}
	pipeline := recognize.NewPipeline(cfg, files, log)

	docs, err := collectDocuments(dir)
	if err != nil {
		return err
	}
	if len(docs) == 0 {
		return fmt.Errorf("в %s нет документов с ожидаемыми значениями", dir)
	}

	fmt.Printf("Документов в выборке: %d\n\n", len(docs))

	stats := map[string]*fieldStat{}
	var typeTotal, typeCorrect int
	var processed, failed int
	start := time.Now()

	for _, d := range docs {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		res, err := pipeline.ProcessFile(ctx, d.path)
		cancel()
		if err != nil {
			failed++
			fmt.Printf("  %-40s ОШИБКА: %v\n", filepath.Base(d.path), err)
			continue
		}
		processed++

		if want, ok := d.want["doc_type"]; ok {
			typeTotal++
			if strings.EqualFold(res.Recognition.DocType, want) {
				typeCorrect++
			} else if verbose {
				fmt.Printf("  %-40s тип: получено %q, ожидалось %q\n",
					filepath.Base(d.path), res.Recognition.DocType, want)
			}
		}

		for key, want := range d.want {
			if key == "doc_type" {
				continue
			}
			st := stats[key]
			if st == nil {
				st = &fieldStat{}
				stats[key] = st
			}
			st.total++

			got := strings.TrimSpace(res.Recognition.Fields[key].Value)
			switch {
			case got == "":
				st.missed++
				if verbose {
					fmt.Printf("  %-40s %s: не распознано (ожидалось %q)\n",
						filepath.Base(d.path), key, want)
				}
			case sameValue(key, got, want):
				st.correct++
			default:
				st.wrong++
				if verbose {
					fmt.Printf("  %-40s %s: получено %q, ожидалось %q\n",
						filepath.Base(d.path), key, got, want)
				}
			}
		}
	}

	printReport(stats, typeCorrect, typeTotal, processed, failed, time.Since(start))
	return nil
}

type sample struct {
	path string
	want expected
}

// collectDocuments собирает пары «документ + ожидаемые значения». Ожидания
// берутся из одноимённого .json рядом с файлом либо из общего expected.json,
// где ключ — имя файла.
func collectDocuments(dir string) ([]sample, error) {
	shared := expected{}
	sharedByFile := map[string]expected{}
	if raw, err := os.ReadFile(filepath.Join(dir, "expected.json")); err == nil {
		if err := json.Unmarshal(raw, &sharedByFile); err != nil {
			return nil, fmt.Errorf("expected.json: %w", err)
		}
	}
	_ = shared

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var out []sample
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		switch ext {
		case ".pdf", ".jpg", ".jpeg", ".png", ".heic", ".heif":
		default:
			continue
		}

		want := expected{}
		sidecar := filepath.Join(dir, strings.TrimSuffix(name, filepath.Ext(name))+".json")
		if raw, err := os.ReadFile(sidecar); err == nil {
			if err := json.Unmarshal(raw, &want); err != nil {
				return nil, fmt.Errorf("%s: %w", filepath.Base(sidecar), err)
			}
		} else if w, ok := sharedByFile[name]; ok {
			want = w
		} else {
			continue // без ожидаемых значений документ не с чем сравнивать
		}

		out = append(out, sample{path: filepath.Join(dir, name), want: want})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, nil
}

// sameValue сравнивает по смыслу, а не побуквенно: «375,00» и «375.00» — одна
// сумма, а лишние пробелы в наименовании контрагента значения не имеют.
func sameValue(key, got, want string) bool {
	norm := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		s = strings.ReplaceAll(s, "\u00a0", " ")
		s = strings.Join(strings.Fields(s), " ")
		return s
	}
	switch key {
	case "total", "vat_amount":
		return money(got) == money(want)
	case "counterparty", "organization":
		// Кавычки и организационная форма пишутся по-разному, сравниваем ядро.
		return strings.Contains(norm(got), core(want)) || strings.Contains(norm(want), core(got))
	}
	return norm(got) == norm(want)
}

func money(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), " ", "")
	s = strings.ReplaceAll(s, "\u00a0", "")
	s = strings.ReplaceAll(s, ",", ".")
	s = strings.TrimSuffix(s, ".00")
	return s
}

func core(s string) string {
	s = strings.ToLower(s)
	for _, junk := range []string{`"`, `«`, `»`, "ооо", "чуп", "ип", "оао", "зао", "уп", "одо"} {
		s = strings.ReplaceAll(s, junk, " ")
	}
	return strings.Join(strings.Fields(s), " ")
}

func printReport(stats map[string]*fieldStat, typeCorrect, typeTotal, processed, failed int, took time.Duration) {
	fmt.Printf("\n%s\n", strings.Repeat("─", 68))
	fmt.Printf(" ТОЧНОСТЬ РАСПОЗНАВАНИЯ\n")
	fmt.Printf("%s\n", strings.Repeat("─", 68))
	fmt.Printf(" обработано: %d   ошибок обработки: %d   время: %s\n\n", processed, failed, took.Round(time.Second))

	if typeTotal > 0 {
		fmt.Printf(" %-18s %6.1f%%   (%d из %d)\n", "тип документа",
			pct(typeCorrect, typeTotal), typeCorrect, typeTotal)
		fmt.Println()
	}

	keys := make([]string, 0, len(stats))
	for k := range stats {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fmt.Printf(" %-18s %8s %8s %8s %8s\n", "поле", "верно", "неверно", "пусто", "точность")
	var allTotal, allCorrect int
	for _, k := range keys {
		st := stats[k]
		allTotal += st.total
		allCorrect += st.correct
		fmt.Printf(" %-18s %8d %8d %8d %7.1f%%\n", k, st.correct, st.wrong, st.missed, pct(st.correct, st.total))
	}

	fmt.Printf("\n%s\n", strings.Repeat("─", 68))
	fmt.Printf(" ИТОГО по реквизитам: %.1f%%  (%d из %d)\n", pct(allCorrect, allTotal), allCorrect, allTotal)
	fmt.Printf("%s\n\n", strings.Repeat("─", 68))

	// Неверное значение опаснее пустого: пустое сотрудник заполнит, неверное
	// проведёт. Поэтому эти два случая считаются раздельно.
	var wrong int
	for _, st := range stats {
		wrong += st.wrong
	}
	if wrong > 0 {
		fmt.Printf("Внимание: %d значений распознаны НЕВЕРНО (не пусто, а с ошибкой).\n", wrong)
		fmt.Printf("Это важнее общего процента — запустите с -v, чтобы увидеть какие.\n\n")
	}
}

func pct(part, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(part) * 100 / float64(total)
}
