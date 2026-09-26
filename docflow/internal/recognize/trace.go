package recognize

// Трассировка распознавания.
//
// Логи стенда отвечают на вопрос «что случилось», но не на вопрос «почему в
// карточке пусто». Между загруженным файлом и полями в 1С стоит шесть стадий
// (подготовка страницы → OCR → правила → сборка таблицы → два промпта к модели
// → сшивка), и по одной строке лога нельзя сказать, на какой из них потерялось
// значение: не распознал OCR, не нашли правила, не собралась таблица, модель не
// увидела нужный кусок текста или её ответ отбросили при разборе.
//
// Здесь каждая стадия пишет своё промежуточное состояние в отдельный каталог по
// документу. Разбор жалобы «плохо распознаёт» сводится к чтению этого каталога:
// видно и что подали на вход OCR, и что он вернул, и какой именно текст ушёл в
// модель, и что она ответила дословно.
//
// Включается переменной TRACE_DIR (пусто — трассировка выключена целиком, ни
// одного лишнего действия в конвейере). Ключи:
//
//	TRACE_DIR=/var/lib/docflow/trace  куда складывать (пусто — выключено)
//	TRACE_PAGES=true                  класть подготовленные страницы (PNG)
//	TRACE_SOURCE=true                 класть исходный файл документа
//	TRACE_KEEP=20                     сколько последних прогонов хранить (0 — все)
//
// Каталог содержит персональные данные документов заказчика, поэтому в проде
// TRACE_DIR держат пустым.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"docflow/internal/domain"
)

// Tracer — каталог одного прогона. Все методы безопасны на nil-приёмнике:
// когда трассировка выключена, NewTracer возвращает nil и вызовы в конвейере
// становятся пустыми.
type Tracer struct {
	dir   string
	name  string
	start time.Time

	mu      sync.Mutex
	meta    map[string]any
	order   []string
	timings map[string]string
}

func traceRoot() string { return strings.TrimSpace(os.Getenv("TRACE_DIR")) }

// TraceEnabled — включена ли трассировка. Нужна снаружи пакета, чтобы не
// готовить данные, которые всё равно никто не запишет.
func TraceEnabled() bool { return traceRoot() != "" }

func traceFlag(key string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if v == "" {
		return def
	}
	switch v {
	case "1", "true", "yes", "on", "да":
		return true
	}
	return false
}

// NewTracer заводит каталог под документ. name — имя файла или id документа,
// оно попадает в название каталога.
func NewTracer(name string) *Tracer {
	root := traceRoot()
	if root == "" {
		return nil
	}
	dir := filepath.Join(root, time.Now().Format("20060102-150405")+"_"+sanitizeTraceName(name))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil
	}
	t := &Tracer{
		dir:     dir,
		name:    name,
		start:   time.Now(),
		meta:    map[string]any{},
		timings: map[string]string{},
	}
	t.Set("документ", name)
	t.Set("начало", time.Now().Format(time.RFC3339))
	t.Set("настройки", traceEnvSnapshot())
	return t
}

// Dir — каталог прогона (пусто, если трассировка выключена).
func (t *Tracer) Dir() string {
	if t == nil {
		return ""
	}
	return t.dir
}

// Set кладёт значение в сводку 00-meta.json.
func (t *Tracer) Set(key string, value any) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, seen := t.meta[key]; !seen {
		t.order = append(t.order, key)
	}
	t.meta[key] = value
}

// Mark записывает длительность стадии.
func (t *Tracer) Mark(stage string, d time.Duration) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.timings[stage] = d.Round(time.Millisecond).String()
	t.mu.Unlock()
}

// Text пишет текстовый файл стадии.
func (t *Tracer) Text(file, content string) {
	if t == nil {
		return
	}
	_ = os.WriteFile(filepath.Join(t.dir, file), []byte(content), 0o644)
}

// JSON пишет структуру стадии. Ошибка сериализации не должна ронять разбор
// документа, поэтому она только отмечается в файле.
func (t *Tracer) JSON(file string, v any) {
	if t == nil {
		return
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		b = []byte(fmt.Sprintf("не сериализуется: %v", err))
	}
	_ = os.WriteFile(filepath.Join(t.dir, file), b, 0o644)
}

// CopyIn копирует файл (страницу, исходник) в каталог прогона.
func (t *Tracer) CopyIn(dstName, srcPath string) {
	if t == nil {
		return
	}
	src, err := os.Open(srcPath)
	if err != nil {
		return
	}
	defer src.Close()
	dst, err := os.Create(filepath.Join(t.dir, dstName))
	if err != nil {
		return
	}
	defer dst.Close()
	_, _ = io.Copy(dst, src)
}

// Source кладёт исходный файл документа рядом с разбором.
func (t *Tracer) Source(path string) {
	if t == nil || !traceFlag("TRACE_SOURCE", true) {
		return
	}
	t.CopyIn("01-source"+strings.ToLower(filepath.Ext(path)), path)
}

// Pages кладёт страницы в том виде, в каком их получил OCR: именно по ним
// видно, не съела ли предобработка текст (пережатый порог, агрессивный sharpen).
func (t *Tracer) Pages(pages []string) {
	if t == nil || !traceFlag("TRACE_PAGES", true) {
		return
	}
	for i, p := range pages {
		t.CopyIn(fmt.Sprintf("02-page-%02d%s", i+1, strings.ToLower(filepath.Ext(p))), p)
	}
}

// Words пишет слова с координатами: по ним собирается таблица проекцией
// колонок, и по ним же видно, почему она не собралась.
func (t *Tracer) Words(words []wordBox) {
	if t == nil {
		return
	}
	var b strings.Builder
	b.WriteString("x0\ty0\tx1\ty1\ttext\n")
	for _, w := range words {
		b.WriteString(strconv.FormatFloat(w.X0, 'f', 1, 64) + "\t")
		b.WriteString(strconv.FormatFloat(w.Y0, 'f', 1, 64) + "\t")
		b.WriteString(strconv.FormatFloat(w.X1, 'f', 1, 64) + "\t")
		b.WriteString(strconv.FormatFloat(w.Y1, 'f', 1, 64) + "\t")
		b.WriteString(strings.ReplaceAll(w.Text, "\t", " ") + "\n")
	}
	t.Text("05-ocr-words.tsv", b.String())
}

type traceTableDump struct {
	Источник string     `json:"источник"`
	Колонок  int        `json:"колонок"`
	Строк    int        `json:"строк"`
	Колонки  []string   `json:"колонки"`
	Роли     []string   `json:"роли,omitempty"`
	Строки   [][]string `json:"строки"`
	Итого    []string   `json:"итого,omitempty"`
	Выбрана  bool       `json:"выбрана"`
}

// Candidates пишет все варианты табличной части и оценки, по которым выбран
// победитель. Именно здесь видно, почему в карточке шесть колонок вместо
// девяти или почему строки склеились.
func (t *Tracer) Candidates(file string, cands []*domain.FreeTable, chosen *domain.FreeTable, diag string) {
	if t == nil {
		return
	}
	dumps := make([]traceTableDump, 0, len(cands))
	for _, c := range cands {
		if c == nil {
			continue
		}
		dumps = append(dumps, traceTableDump{
			Источник: c.Source,
			Колонок:  len(c.Columns),
			Строк:    len(c.Rows),
			Колонки:  c.Columns,
			Роли:     c.Roles,
			Строки:   c.Rows,
			Итого:    c.Totals,
			Выбрана:  chosen != nil && c == chosen,
		})
	}
	t.JSON(file, map[string]any{
		"оценки":    diag,
		"кандидаты": dumps,
	})
}

// FieldsDiff — построчная сверка: что дали правила, что осталось в итоге и
// откуда взято значение. Одного взгляда хватает, чтобы понять, кто испортил
// поле — правила, модель или постобработка.
func (t *Tracer) FieldsDiff(byRules, final domain.Recognition) {
	if t == nil {
		return
	}
	keys := map[string]bool{}
	for k := range byRules.Fields {
		keys[k] = true
	}
	for k := range final.Fields {
		keys[k] = true
	}
	list := make([]string, 0, len(keys))
	for k := range keys {
		list = append(list, k)
	}
	sort.Strings(list)

	var b strings.Builder
	b.WriteString("поле\tправила\tитог\tисточник\tуверенность\n")
	for _, k := range list {
		r := byRules.Fields[k]
		f := final.Fields[k]
		b.WriteString(k + "\t" + oneLine(r.Value) + "\t" + oneLine(f.Value) + "\t" +
			f.Source + "\t" + strconv.FormatFloat(f.Confidence, 'f', 2, 64) + "\n")
	}
	t.Text("15-fields-diff.tsv", b.String())
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "\t", " ")
}

// Close дописывает сводку и строку в общий индекс прогонов.
func (t *Tracer) Close() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.meta["длительность"] = time.Since(t.start).Round(time.Millisecond).String()
	t.meta["стадии_сек"] = t.timings
	ordered := make(map[string]any, len(t.meta))
	for k, v := range t.meta {
		ordered[k] = v
	}
	t.mu.Unlock()

	t.JSON("00-meta.json", ordered)
	t.appendIndex(ordered)
	t.rotate()
}

func (t *Tracer) appendIndex(meta map[string]any) {
	f, err := os.OpenFile(filepath.Join(filepath.Dir(t.dir), "index.tsv"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	st, _ := f.Stat()
	if st != nil && st.Size() == 0 {
		fmt.Fprintln(f, strings.Join([]string{
			"каталог", "документ", "ocr", "символов", "слов", "таблица_источник",
			"строк", "тип", "модель", "не_хватает", "итог_сумма", "длительность",
		}, "\t"))
	}
	cell := func(key string) string {
		v, ok := meta[key]
		if !ok || v == nil {
			return "-"
		}
		return oneLine(fmt.Sprint(v))
	}
	fmt.Fprintln(f, strings.Join([]string{
		filepath.Base(t.dir),
		cell("документ"),
		cell("движок_ocr"),
		cell("символов_текста"),
		cell("слов_с_координатами"),
		cell("таблица_источник"),
		cell("таблица_строк"),
		cell("тип_итог"),
		cell("модель_вызвана"),
		cell("не_хватает_итог"),
		cell("итог_сумма"),
		cell("длительность"),
	}, "\t"))
}

// rotate оставляет TRACE_KEEP последних прогонов: каталог со страницами и
// исходниками весит мегабайты, и на потоке заказчика он за сутки съест диск.
func (t *Tracer) rotate() {
	keep := 20
	if v := strings.TrimSpace(os.Getenv("TRACE_KEEP")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			keep = n
		}
	}
	if keep <= 0 {
		return
	}
	root := filepath.Dir(t.dir)
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	dirs := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	if len(dirs) <= keep {
		return
	}
	sort.Strings(dirs) // имя начинается с метки времени
	for _, name := range dirs[:len(dirs)-keep] {
		_ = os.RemoveAll(filepath.Join(root, name))
	}
}

func sanitizeTraceName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "doc"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	for strings.Contains(out, "__") {
		out = strings.ReplaceAll(out, "__", "_")
	}
	out = strings.Trim(out, "_")
	if out == "" {
		out = "doc"
	}
	if len([]rune(out)) > 60 {
		out = string([]rune(out)[:60])
	}
	return out
}

// traceEnvSnapshot — значения ключей, от которых зависит разбор. Без них дамп
// нельзя сопоставить с настройками стенда: одна и та же картинка при
// OCR_ENGINE=tesseract и OCR_ENGINE=surya даёт разный текст.
func traceEnvSnapshot() map[string]string {
	keys := []string{
		"OCR_ENGINE", "OCR_AUTO_FALLBACK", "OCR_LANGUAGES", "OCR_DPI", "OCR_TIMEOUT",
		"OCR_ENHANCE", "OCR_DESKEW", "OCR_SHARPEN", "OCR_UPSCALE", "OCR_MAX_PX",
		"OCR_LAYOUT", "OCR_LAYOUT_GAP", "OCR_ROTATE_MIN_CONF",
		"LLM_ENABLED", "LLM_BASE_URL", "LLM_MODEL", "LLM_TIMEOUT", "LLM_MAX_TOKENS",
		"LLM_TEMPERATURE", "LLM_CTX", "LLM_PARALLEL", "LLM_THREADS",
		"TABLE_PRIORITY", "SPLIT_MULTIDOC", "RECOGNIZE_WORKERS",
		"ACCOUNTS_PATH", "DOCTYPES_PATH", "LOCALE",
	}
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			out[k] = v
		}
	}
	return out
}

// needsModelReason — та же проверка, что и needsModel, но с объяснением. Нужна
// только для дампа: без неё непонятно, почему модель отработала на одном
// документе и не отработала на соседнем.
func needsModelReason(rec domain.Recognition, typeConf float64) string {
	if rec.DocType == DocTypeUnknown || rec.DocType == "" {
		return "тип не опознан правилами"
	}
	if typeConf < typeConfidenceThreshold {
		return fmt.Sprintf("низкая уверенность типа (%.2f < %.2f)", typeConf, typeConfidenceThreshold)
	}
	if miss := MissingRequired(rec); len(miss) > 0 {
		return "не хватает обязательных полей: " + strings.Join(miss, ", ")
	}
	if len(rec.Lines) == 0 && typeHasLines(rec.DocType) {
		return "тип предполагает табличную часть, а строк нет"
	}
	if tableLooksSuspicious(rec) {
		return "табличная часть выглядит битой"
	}
	return ""
}
