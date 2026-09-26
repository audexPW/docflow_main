#!/usr/bin/env bash
# patch-trace.sh — трассировка распознавания DocFlow.
#
# Ставит пофазовый дамп разбора: по каждому документу создаётся каталог, где
# лежит всё промежуточное состояние конвейера — подготовленные страницы, текст
# OCR, слова с координатами, все варианты табличной части с оценками, оба
# промпта к модели и её ответы дословно, результат правил, результат после
# модели и итог. По этому каталогу видно, на какой именно стадии теряется
# значение, без гадания по логам.
#
# Запуск из каталога развёртывания:
#
#   cd /opt/docflow-deploy
#   bash patch-trace.sh
#
# Переменные:
#   FORCE=1     применять, даже если файлы на сервере отличаются от ожидаемых
#   NO_BUILD=1  не пересобирать образ (только правка исходников)
set -euo pipefail

TS="$(date +%Y%m%d-%H%M%S)"
BACKUP=".backup-trace-$TS"
FORCE="${FORCE:-0}"
NO_BUILD="${NO_BUILD:-0}"

if [[ ! -f docker-compose.yml || ! -d docflow/internal/recognize ]]; then
  echo "Запускать из каталога развёртывания (там, где docker-compose.yml и docflow/)." >&2
  exit 1
fi

mkdir -p "$BACKUP"
echo "Резервные копии: $BACKUP"

check_sha() {
  local path="$1" want="$2" patched="$3"
  [[ -z "$want" ]] && return 0
  local have
  have="$(sha256sum "$path" | cut -d" " -f1)"
  if [[ "$have" == "$patched" ]]; then
    echo "  $path уже пропатчен, перезаписываю тем же содержимым"
    return 0
  fi
  if [[ "$have" != "$want" ]]; then
    if [[ "$FORCE" == "1" ]]; then
      echo "ВНИМАНИЕ: $path отличается от ожидаемого, применяю принудительно (FORCE=1)."
    else
      echo "ОШИБКА: $path на сервере отличается от версии, под которую сделан патч." >&2
      echo "  ожидалось: $want" >&2
      echo "  на диске:  $have" >&2
      echo "Файл менялся после отправки архива. Перезапустите с FORCE=1, если правки не нужны." >&2
      exit 1
    fi
  fi
}

if [[ -f "docflow/internal/recognize/trace.go" ]]; then cp -a "docflow/internal/recognize/trace.go" "$BACKUP/"; fi
echo "  пишу docflow/internal/recognize/trace.go"
cat > "docflow/internal/recognize/trace.go" <<'PATCH_TRACE_EOF_TRACE_GO'
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
PATCH_TRACE_EOF_TRACE_GO

check_sha "docflow/internal/recognize/pipeline.go" "4131b116883af7786474ac0a7f96207c7adf483d85ab821954bdb49c99ca47e6" "7e53e1d94bc38f90b83d95cb2c8beb703e1c561374f3d12b87a314e982460106"
cp -a "docflow/internal/recognize/pipeline.go" "$BACKUP/"
echo "  пишу docflow/internal/recognize/pipeline.go"
cat > "docflow/internal/recognize/pipeline.go" <<'PATCH_TRACE_EOF_PIPELINE_GO'
package recognize

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"docflow/internal/config"
	"docflow/internal/domain"
	"docflow/internal/filestore"
)

// Порог, ниже которого доверяем разбор типа модели, а не только правилам.
const typeConfidenceThreshold = 0.5

// Допустимое расхождение суммы строк табличной части с итогом документа
// (доля от итога). Больше — считаем таблицу разобранной неверно.
const LinesTolerance = 0.01

type Pipeline struct {
	cfg   config.Config
	files *filestore.Store
	model *modelClient
	ocr   ocrEngine
	log   *slog.Logger
}

func NewPipeline(cfg config.Config, files *filestore.Store, log *slog.Logger) *Pipeline {
	ocr := newOCREngine(cfg.OCR)
	log.Info("ocr engine selected", "engine", ocr.name())
	return &Pipeline{
		cfg:   cfg,
		files: files,
		model: newModelClient(cfg.LLM),
		ocr:   ocr,
		log:   log,
	}
}

type Result struct {
	Recognition domain.Recognition
	OCRText     string
}

// Process выполняет полный разбор документа по его файлу в хранилище.
func (p *Pipeline) Process(ctx context.Context, doc domain.Document) (Result, error) {
	workDir, err := os.MkdirTemp("", "docflow-*")
	if err != nil {
		return Result{}, fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(workDir)

	srcPath, err := p.files.ExtractTo(doc.StorageKey, workDir, "source"+ext(doc))
	if err != nil {
		return Result{}, fmt.Errorf("extract source: %w", err)
	}

	tr := NewTracer(doc.OriginalName)
	defer tr.Close()
	tr.Set("id_документа", doc.ID.String())
	tr.Set("имя_файла", doc.OriginalName)
	tr.Set("mime", doc.ContentType)
	tr.Set("размер_байт", doc.SizeBytes)
	if dir := tr.Dir(); dir != "" {
		p.log.Info("трассировка распознавания", "id", doc.ID, "каталог", dir)
	}

	return p.processPath(ctx, srcPath, doc.ContentType, workDir, tr)
}

// ProcessFile разбирает файл, лежащий на диске в открытом виде, минуя
// зашифрованное хранилище. Нужен для замера точности на выборке заказчика
// (cmd/accuracy) — там документы просто лежат в каталоге.
func (p *Pipeline) ProcessFile(ctx context.Context, path string) (Result, error) {
	workDir, err := os.MkdirTemp("", "docflow-acc-*")
	if err != nil {
		return Result{}, fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(workDir)

	contentType := "application/octet-stream"
	if strings.EqualFold(filepath.Ext(path), ".pdf") {
		contentType = "application/pdf"
	}
	tr := NewTracer(filepath.Base(path))
	defer tr.Close()
	tr.Set("имя_файла", filepath.Base(path))

	return p.processPath(ctx, path, contentType, workDir, tr)
}

func (p *Pipeline) processPath(ctx context.Context, srcPath, contentType, workDir string, tr *Tracer) (Result, error) {
	ocrCtx, cancel := context.WithTimeout(ctx, p.cfg.OCR.Timeout)
	defer cancel()

	tr.Set("движок_ocr", p.ocr.name())
	tr.Source(srcPath)

	t0 := time.Now()
	pages, err := preparePages(ocrCtx, p.cfg.OCR, srcPath, contentType, workDir)
	tr.Mark("подготовка_страниц", time.Since(t0))
	if err != nil {
		tr.Set("ошибка", "подготовка страниц: "+err.Error())
		return Result{}, fmt.Errorf("prepare pages: %w", err)
	}
	tr.Set("страниц", len(pages))
	tr.Pages(pages)

	t0 = time.Now()
	page, err := runOCR(ocrCtx, p.ocr, pages)
	tr.Mark("ocr", time.Since(t0))
	if err != nil {
		tr.Set("ошибка", "ocr: "+err.Error())
		return Result{}, fmt.Errorf("ocr: %w", err)
	}
	tr.Text("03-ocr-flat.txt", page.Flat)
	tr.Text("04-ocr-layout.txt", page.Layout)
	tr.Words(page.Words)
	tr.JSON("06-ocr-grids.json", page.Grids)
	tr.Set("символов_текста", len([]rune(strings.TrimSpace(page.Flat))))
	tr.Set("символов_раскладки", len([]rune(page.Layout)))
	tr.Set("слов_с_координатами", len(page.Words))

	ocrText := strings.TrimSpace(page.Flat)
	layoutText := page.Layout
	if ocrText == "" {
		// Движок не отдал построчный текст — работаем по раскладке, убрав
		// границы ячеек: регулярки шапки рассчитаны на обычный текст.
		ocrText = strings.TrimSpace(stripCellSeparators(layoutText))
	}
	if ocrText == "" {
		tr.Set("ошибка", "OCR не вернул текста")
		return Result{}, fmt.Errorf("no text recognized")
	}

	byRules := RecognizeDocument(ocrText, layoutText)
	tr.JSON("07-rules.json", byRules)
	tr.Set("тип_по_правилам", byRules.DocType)
	tr.Set("уверенность_типа", byRules.DocTypeConfidence)
	tr.Set("не_хватает_по_правилам", MissingRequired(byRules))

	// Кандидаты на табличную часть. Раньше приоритет между ними был зашит
	// в код и не зависел от того, что каждый из них выдал на этом бланке.
	// Теперь кандидаты чистятся и оцениваются по арифметике документа
	// (кол-во x цена = сумма, без НДС + НДС = с НДС, сумма строк = «Итого»),
	// и берётся тот разбор, который сходится.
	cands := make([]*domain.FreeTable, 0, 3)
	if byRules.Table != nil && byRules.Table.RawCells != nil {
		cands = append(cands, byRules.Table.RawCells)
	}
	if ft := TableFromWords(page.Words); ft != nil {
		p.log.Info("таблица собрана проекцией колонок",
			"колонок", len(ft.Columns), "строк", len(ft.Rows),
			"шапка", strings.Join(ft.Columns, " | "))
		cands = append(cands, ft)
	} else if len(page.Words) > 0 {
		p.log.Info("проекция колонок не дала таблицу", "слов", len(page.Words))
	}

	cellCount := 0
	for _, g := range page.Grids {
		cellCount += len(g.Cells)
	}
	p.log.Info("grid diag: получено от surya", "tables", len(page.Grids), "cells", cellCount)
	if ft := FreeTableFromGrid2(page.Grids); ft != nil {
		p.log.Info("grid diag: таблица собрана", "columns", len(ft.Columns), "rows", len(ft.Rows))
		cands = append(cands, ft)
	} else {
		p.log.Warn("grid diag: сетка не использована", "причина", GridDiag())
	}

	best, diag := PickBestFreeTable(cands)
	tr.Candidates("08-table-candidates.json", cands, best, diag)
	tr.Set("кандидатов_таблицы", len(cands))
	if best != nil {
		tr.Set("таблица_источник", best.Source)
		tr.Set("таблица_строк", len(best.Rows))
		tr.Set("таблица_колонок", len(best.Columns))
	} else {
		tr.Set("таблица_источник", "нет")
		tr.Set("таблица_строк", 0)
		tr.Set("сетка_не_использована", GridDiag())
	}

	if best != nil {
		p.log.Info("табличная часть выбрана", "источник", best.Source,
			"колонок", len(best.Columns), "строк", len(best.Rows),
			"шапка", strings.Join(best.Columns, " | "), "оценки", diag)
		for i, r := range best.Rows {
			p.log.Info("таблица: строка", "n", i+1, "ячейки", strings.Join(r, " | "))
		}
		byRules.Table = FreeTableToTable(best)
		byRules.Lines = FreeTableToLines(best)
	} else {
		p.log.Warn("табличная часть не собрана ни одним разборщиком")
	}

	typeConf := byRules.DocTypeConfidence

	// Модель подключается, когда правилам не хватило: тип не опознан уверенно,
	// либо опознан, но не собраны обязательные реквизиты, либо не разобрана
	// табличная часть у документа, который её обязан иметь. ТЗ §5: система
	// принимает любые документы, поэтому шаблонного разбора недостаточно.
	// Модель локальная, наружу ничего не уходит.
	rec := byRules
	reason := needsModelReason(byRules, typeConf)
	switch {
	case !p.model.enabled():
		tr.Set("модель_вызвана", false)
		tr.Set("модель_причина", "LLM выключена в конфигурации (LLM_ENABLED/LLM_BASE_URL)")
	case reason == "":
		tr.Set("модель_вызвана", false)
		tr.Set("модель_причина", "правилам хватило: тип опознан, обязательные поля собраны, таблица не вызывает подозрений")
	default:
		tr.Set("модель_вызвана", true)
		tr.Set("модель_причина", reason)
	}
	if p.model.enabled() && needsModel(byRules, typeConf) {
		dc := BuildDocContext(page.Words, ocrText, byRules.Table)
		p.log.Info("контекст для модели",
			"шапка_симв", len([]rune(dc.Head)),
			"подвал_симв", len([]rune(dc.Foot)),
			"таблица_симв", len([]rune(dc.Table)),
			"источник_таблицы", dc.TableSource)
		tr.Text("09-llm-context-head.txt", dc.Head)
		tr.Text("09-llm-context-foot.txt", dc.Foot)
		tr.Text("09-llm-context-table.md", dc.Table)
		tr.Set("контекст_шапка_симв", len([]rune(dc.Head)))
		tr.Set("контекст_подвал_симв", len([]rune(dc.Foot)))
		tr.Set("контекст_таблица_симв", len([]rune(dc.Table)))
		t0 = time.Now()
		refined, ok := p.refineSplit(ctx, dc, byRules, tr)
		tr.Mark("модель", time.Since(t0))
		if ok {
			rec = refined
		} else {
			tr.Set("модель_результат", "оба запроса не дали разбора — остался результат правил")
		}
	}

	// Реквизиты сверяем с текстом документа: выдуманные моделью номера
	// отбрасываем, стороны привязываем к подписанным «УНП продавца» /
	// «УНП покупателя», из наименований убираем банковские хвосты.
	tr.JSON("14-before-postprocess.json", rec)
	FixFields(rec.Fields, ocrText)
	FillTotalsFromTable(&rec)

	// Счета учёта подбираем последним шагом — по уже собранным строкам и типу
	// документа. Это то, что бухгалтер иначе выбирал бы в 1С руками для каждой
	// позиции, сводя на нет выигрыш от распознавания.
	ApplyAccounts(&rec)

	tr.JSON("16-final.json", rec)
	tr.FieldsDiff(byRules, rec)
	tr.Set("тип_итог", rec.DocType)
	tr.Set("не_хватает_итог", MissingRequired(rec))
	tr.Set("итог_сумма", rec.Fields["total"].Value)
	tr.Set("строк_итог", len(rec.Lines))
	tr.Set("сумма_строк_не_сходится_с_итогом", LinesTotalMismatch(rec, LinesTolerance))

	return Result{Recognition: rec, OCRText: ocrText}, nil
}

func tableLooksSuspicious(rec domain.Recognition) bool {
	if !typeHasLines(rec.DocType) {
		return false
	}

	if rec.Table == nil || rec.Table.RawCells == nil {
		return len(rec.Lines) == 0
	}

	ft := rec.Table.RawCells

	// Таблица с одной колонкой или без строк — практически всегда
	// означает, что структура не восстановилась.
	if len(ft.Columns) < 2 || len(ft.Rows) == 0 {
		return true
	}

	// Слишком много пустых клеток — вероятна потеря текста/колонок.
	// Порог специально низкий: реальные бухгалтерские таблицы могут
	// содержать пустые значения.
	total := len(ft.Columns) * len(ft.Rows)
	filled := 0

	for _, row := range ft.Rows {
		for _, value := range row {
			if strings.TrimSpace(value) != "" {
				filled++
			}
		}
	}

	if total > 0 && float64(filled)/float64(total) < 0.20 {
		return true
	}

	// Одинаковые строки подряд — типичный артефакт неправильной сетки.
	seen := make(map[string]bool)

	for _, row := range ft.Rows {
		key := strings.TrimSpace(strings.Join(row, "\x00"))

		if key == "" {
			continue
		}

		if seen[key] {
			return true
		}

		seen[key] = true
	}

	// Документ обязан иметь строки, но Lines не получилось получить.
	if len(rec.Lines) == 0 {
		return true
	}

	// Оставляем бухгалтерскую проверку суммы как дополнительный сигнал.
	return LinesTotalMismatch(rec, LinesTolerance)
}

// needsModel решает, звать ли модель после прохода правил.
func needsModel(rec domain.Recognition, typeConf float64) bool {
	if rec.DocType == DocTypeUnknown || rec.DocType == "" {
		return true
	}
	if typeConf < typeConfidenceThreshold {
		return true
	}
	if len(MissingRequired(rec)) > 0 {
		return true
	}
	// Тип предполагает номенклатуру, а таблица не разобрана — правила бессильны,
	// пусть попробует модель.
	if len(rec.Lines) == 0 && typeHasLines(rec.DocType) {
		return true
	}
	// Даже если обязательные поля и итог совпали, сама таблица может
	// быть структурно испорчена. В этом случае нужен дополнительный
	// semantic pass локальной модели.
	return tableLooksSuspicious(rec)
}

// RecognizeText — разбор документа одними правилами, без OCR и без модели.
// Вынесено отдельно, чтобы результат правил можно было проверить на любом
// тексте: это то, что получит 1С, если локальная модель выключена.
func RecognizeText(ocrText string) domain.Recognition {
	return RecognizeDocument(ocrText, "")
}

// RecognizeDocument — разбор правилами по двум представлениям страницы:
// плоский текст для реквизитов шапки, текст с раскладкой — для таблицы.
// Таблица разбирается по колонкам; если раскладки нет (старый движок), в дело
// идёт прежний построчный разбор.
func RecognizeDocument(ocrText, layoutText string) domain.Recognition {
	docType, conf := classifyByRules(ocrText)
	rec := domain.Recognition{
		DocType:           docType,
		DocTypeConfidence: conf,
		Fields:            extractByRules(ocrText),
	}

	// Сначала пробуем взять таблицу как есть — со всеми графами оригинала.
	// Приведение к шести типовым колонкам оставлено запасным вариантом: оно
	// теряет графы, которых в схеме нет, и сдвигает значения влево.
	if ft := FreeTableFromGrid(layoutText); ft != nil {
		rec.Table = FreeTableToTable(ft)
		rec.Lines = FreeTableToLines(ft)
	}
	if rec.Table == nil {
		if table := ParseTable(layoutText); table != nil {
			rec.Table = table
			rec.Lines = TableToLines(table)
		}
	}
	if len(rec.Lines) == 0 {
		if lines := ExtractLines(ocrText); len(lines) > 0 {
			rec.Lines = lines
			rec.Table = LinesToTable(lines, "rules")
		}
	}
	return rec
}

type modelLine struct {
	Name    jsonStr `json:"name"`
	Qty     jsonStr `json:"qty"`
	Unit    jsonStr `json:"unit"`
	Price   jsonStr `json:"price"`
	Amount  jsonStr `json:"amount"`
	VAT     jsonStr `json:"vat"`
	Account jsonStr `json:"account"`
}

// UnmarshalJSON принимает позицию и объектом, и массивом.
//
// В промпте таблицы рядом стоят "table"."rows" (массивы ячеек) и "lines"
// (объекты с именованными полями). Модель по инерции отдаёт вторым тем же
// форматом, что и первым, и весь ответ разваливался на разборе — вместе с
// корректной таблицей: "cannot unmarshal array into Go struct field
// modelExtract.lines of type recognize.modelLine". Терять из-за формы одного
// поля весь разбор нельзя, поэтому массив раскладываем по позициям в том
// порядке, в каком поля перечислены в схеме промпта.
func (l *modelLine) UnmarshalJSON(b []byte) error {
	t := strings.TrimSpace(string(b))
	if t == "" || t == "null" {
		return nil
	}

	if t[0] == '[' {
		var cells []jsonStr
		if err := json.Unmarshal(b, &cells); err != nil {
			return err
		}
		// Порядок ячеек — как в схеме промпта: наименование, количество,
		// единица, цена, сумма, НДС, счёт учёта. Строка короче схемы —
		// остаток остаётся пустым; длиннее — хвост отбрасываем, выдумывать
		// назначение лишних граф здесь нельзя.
		put := []*jsonStr{&l.Name, &l.Qty, &l.Unit, &l.Price, &l.Amount, &l.VAT, &l.Account}
		for i := range put {
			if i < len(cells) {
				*put[i] = cells[i]
			}
		}
		return nil
	}

	// Обычный объект. Через псевдоним, иначе UnmarshalJSON вызовет сам себя.
	type rawLine struct {
		Name    jsonStr `json:"name"`
		Qty     jsonStr `json:"qty"`
		Unit    jsonStr `json:"unit"`
		Price   jsonStr `json:"price"`
		Amount  jsonStr `json:"amount"`
		VAT     jsonStr `json:"vat"`
		Account jsonStr `json:"account"`
	}
	var raw rawLine
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	l.Name, l.Qty, l.Unit = raw.Name, raw.Qty, raw.Unit
	l.Price, l.Amount, l.VAT, l.Account = raw.Price, raw.Amount, raw.VAT, raw.Account
	return nil
}

// modelTable — табличная часть в ответе модели: колонки строками заголовков,
// строки — массивы ячеек той же длины. Модель ничего не классифицирует, она
// только переносит таблицу из документа; какая графа за что отвечает, решает
// уже система по заголовкам.
type modelTable struct {
	Columns []jsonStr   `json:"columns"`
	Rows    [][]jsonStr `json:"rows"`
	Totals  []jsonStr   `json:"totals"`
}

type modelExtract struct {
	DocType string             `json:"doc_type"`
	Fields  map[string]jsonStr `json:"fields"`
	Lines   []modelLine        `json:"lines"`
	Table   *modelTable        `json:"table"`
}

// convertModelTable приводит ответ модели к свободной таблице. Строки короче
// шапки дополняются пустыми ячейками, длиннее — схлопываются в последнюю
// колонку: пусть лучше значение окажется в соседней графе, чем исчезнет.
func convertModelTable(mt *modelTable) *domain.FreeTable {
	if mt == nil || len(mt.Columns) == 0 || len(mt.Rows) == 0 {
		return nil
	}
	cols := make([]string, 0, len(mt.Columns))
	for _, c := range mt.Columns {
		cols = append(cols, strings.TrimSpace(string(c)))
	}
	ft := &domain.FreeTable{Columns: cols, Roles: rolesFor(cols), Source: "model"}
	for _, r := range mt.Rows {
		cells := make([]string, 0, len(r))
		empty := true
		for _, c := range r {
			v := strings.TrimSpace(string(c))
			if v != "" {
				empty = false
			}
			cells = append(cells, v)
		}
		if empty {
			continue
		}
		row := padRow(cells, len(cols))
		// Модель нередко отдаёт шапку в одном порядке, а ячейки — в
		// порядке документа. Позиционная укладка тогда сдвигает всю
		// строку. Ловим это арифметикой строки.
		row = repairRowOrder(row, ft.Roles)
		if reTotalsRow.MatchString(strings.Join(row, " ")) {
			if len(ft.Totals) == 0 {
				ft.Totals = row
			}
			continue
		}
		ft.Rows = append(ft.Rows, row)
	}
	if len(mt.Totals) > 0 {
		tot := make([]string, 0, len(mt.Totals))
		for _, c := range mt.Totals {
			tot = append(tot, strings.TrimSpace(string(c)))
		}
		ft.Totals = padRow(tot, len(cols))
	}
	if len(ft.Rows) == 0 {
		return nil
	}
	return ft
}

func (p *Pipeline) refineWithModel(ctx context.Context, ocrText string, byRules domain.Recognition) (domain.Recognition, bool) {
	system := "Ты разбираешь распознанный текст бухгалтерского документа. " +
		"Определи тип документа, извлеки реквизиты и строки табличной части. " +
		"Ответь строго одним JSON-объектом без пояснений и без markdown."

	grid := TableAsGrid(byRules.Table)
	if byRules.Table != nil && byRules.Table.RawCells != nil {
		grid = FreeTableAsGrid(byRules.Table.RawCells)
	}
	user := buildExtractPrompt(ocrText, byRules.DocType, grid)

	raw, err := p.model.complete(ctx, system, user)
	if err != nil {
		p.log.Warn("model refine failed, falling back to rules", "error", err)
		return domain.Recognition{}, false
	}

	var me modelExtract
	if err := decodeJSONObject(raw, &me); err != nil {
		p.log.Warn("model returned unparseable json", "error", err)
		return domain.Recognition{}, false
	}

	fields := make(map[string]domain.Field, len(byRules.Fields)+len(me.Fields))
	for k, v := range byRules.Fields {
		fields[k] = v
	}
	for k, v := range me.Fields {
		k = strings.TrimSpace(strings.ToLower(k))
		sv := strings.TrimSpace(string(v))
		if k == "" || sv == "" {
			continue
		}
		// Счёт, которого нет в плане счетов заказчика, — это выдумка модели.
		// Пустое поле бухгалтер заполнит за секунду, выдуманный счёт он будет
		// искать в базе полдня, поэтому такое значение отбрасываем.
		if strings.HasPrefix(k, "account_") && AccountsEnabled() && !KnownAccount(sv) {
			p.log.Warn("модель вернула счёт вне плана счетов", "field", k, "value", sv)
			continue
		}
		// Поля модели идут с уверенностью 0.85 и молча перекрывают правила.
		// В номере документа модель иногда отдаёт банковский счёт из шапки
		// («р/с № BY29ALFA…»): выглядит достоверно, а в 1С уходит мусор.
		// Отбрасываем — останется номер, найденный правилами.
		if k == "number" && looksLikeAccountNumber(sv) {
			p.log.Warn("модель вернула в номере банковский счёт, значение отброшено", "value", sv)
			continue
		}
		fields[k] = domain.Field{Value: sv, Confidence: 0.85, Source: "model"}
	}

	// Модель отвечает свободным текстом («Товарная накладная», «Invoice»), а
	// таблица обязательных полей и приёмник 1С работают по slug из реестра.
	// Без нормализации документ получил бы тип, которого никто не знает.
	docType := NormalizeDocType(me.DocType)
	if docType == "" || docType == DocTypeUnknown {
		docType = byRules.DocType
	}

	// Табличную часть от модели берём только если она непустая; иначе
	// оставляем разобранную по раскладке — она точнее, потому что опирается
	// на координаты, а не на пересказ.
	lines := byRules.Lines
	table := byRules.Table
	// Свободная таблица от модели — основной вариант: в ней сохранён состав
	// граф документа. Плоский lines[] остаётся запасным на случай, когда
	// модель вернула только его (старая версия промпта, короткий ответ).
	// Сетка от table_rec опирается на координаты ячеек на странице, модель —
	// на её пересказ. Там, где сетка собралась, переписывание только портит
	// раскладку: графы едут, строки склеиваются. Поэтому модельную таблицу
	// берём лишь когда сетки нет вовсе.
	gridOK := tableIsGeometric(byRules.Table)
	if ft := convertModelTable(me.Table); ft != nil && !gridOK {
		table = FreeTableToTable(ft)
		lines = FreeTableToLines(ft)
		// Счета учёта модель отдаёт в lines[]; переносим их по совпадению
		// наименования, иначе бухгалтеру пришлось бы проставлять счёт руками.
		if acc := convertModelLines(me.Lines); len(acc) > 0 {
			for i := range lines {
				for _, a := range acc {
					if a.Account != "" && strings.EqualFold(a.Name, lines[i].Name) {
						lines[i].Account = a.Account
						break
					}
				}
			}
		}
	} else if gridOK {
		// Таблица уже собрана по координатам. Модель здесь только дополняет:
		// переносим счёт учёта по совпадению наименования и на этом всё.
		//
		// Раньше эта ветка срабатывала И в этом случае тоже: условие выше
		// ложно как при ft == nil, так и при gridOK == true, и полноценная
		// таблица на девять граф молча заменялась шестиколоночной
		// LinesToTable. Отсюда и вечные шесть колонок в карточке.
		if acc := convertModelLines(me.Lines); len(acc) > 0 {
			for i := range lines {
				for _, a := range acc {
					if a.Account != "" && strings.EqualFold(a.Name, lines[i].Name) {
						lines[i].Account = a.Account
						break
					}
				}
			}
		}
	} else if converted := convertModelLines(me.Lines); len(converted) > 0 {
		// Настоящий запасной путь: геометрической таблицы нет вовсе.
		lines = converted
		table = LinesToTable(converted, "model")
	}

	return domain.Recognition{
		DocType:           docType,
		DocTypeConfidence: 0.85,
		Fields:            fields,
		Lines:             lines,
		Table:             table,
	}, true
}

// tableIsGeometric — таблица получена по координатам (проекция колонок или
// сетка детектора), а не пересказом модели и не регулярками. Такую таблицу
// переписывать нельзя: она знает, где на листе стоит каждая графа.
func tableIsGeometric(t *domain.Table) bool {
	if t == nil || t.RawCells == nil || len(t.RawCells.Rows) == 0 {
		return false
	}
	// layout сюда тоже входит: FreeTableFromGrid строит таблицу по раскладке,
	// то есть по координатам слов, а не по пересказу модели.
	switch t.RawCells.Source {
	case "columns", "grid", "layout":
		return true
	}
	return false
}

func convertModelLines(in []modelLine) []domain.LineItem {
	out := make([]domain.LineItem, 0, len(in))
	for _, l := range in {
		name := strings.TrimSpace(string(l.Name))
		if name == "" {
			continue
		}
		account := strings.TrimSpace(string(l.Account))
		if account != "" && AccountsEnabled() && !KnownAccount(account) {
			account = "" // счёт вне плана счетов — пусть подберут правила
		}
		out = append(out, domain.LineItem{
			Name:    name,
			Qty:     normalizeNumber(strings.TrimSpace(string(l.Qty))),
			Unit:    strings.TrimSpace(string(l.Unit)),
			Price:   normalizeNumber(strings.TrimSpace(string(l.Price))),
			Amount:  normalizeNumber(strings.TrimSpace(string(l.Amount))),
			VAT:     normalizeNumber(strings.TrimSpace(string(l.VAT))),
			Account: account,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func buildExtractPrompt(ocrText, ruleType, tableGrid string) string {
	const maxChars = 6000
	ocrText = truncRunes(ocrText, maxChars)
	hint := "не определён"
	if ruleType != DocTypeUnknown && ruleType != "" {
		hint = ruleType
	}

	// Ключ налогового идентификатора берём из локали (by → unp, ru → inn),
	// иначе модель вернёт поле, которого система не ждёт, и документ навсегда
	// останется в статусе «нужны данные».
	fieldSchema := fieldSchemaMap()

	schema := map[string]any{
		"doc_type": docTypePromptHint(),
		"fields":   fieldSchema,
		"lines": []map[string]string{{
			"name":   "наименование товара или услуги",
			"qty":    "количество числом",
			"unit":   "единица измерения",
			"price":  "цена за единицу числом",
			"amount": "сумма по строке числом",
			"vat":    "ставка или сумма НДС",
		}},
	}
	if AccountsEnabled() {
		schema["lines"] = []map[string]string{{
			"name":    "наименование товара или услуги",
			"qty":     "количество числом",
			"unit":    "единица измерения",
			"price":   "цена за единицу числом",
			"amount":  "сумма по строке числом",
			"vat":     "ставка или сумма НДС",
			"account": "счёт учёта этой позиции, строго один код из списка допустимых счетов",
		}}
	}
	// Таблицу просим отдать как есть: колонки — заголовки из документа, строки
	// — массивы ячеек. Ни состава колонок, ни их порядка мы не навязываем;
	// именно навязанная схема и приводила к тому, что «Сумма с НДС» попадала в
	// графу «Цена», а «Ставка НДС» — в «Сумму».
	schema["table"] = map[string]any{
		"columns": []string{"заголовки колонок ровно как в шапке документа, слева направо"},
		"rows":    []any{[]string{"ячейки строки в том же порядке, что и колонки"}},
		"totals":  []string{"строка Итого теми же ячейками, если она есть"},
	}
	schemaJSON, _ := json.Marshal(schema)

	var b strings.Builder
	b.WriteString("Предварительная гипотеза по типу: ")
	b.WriteString(hint)
	b.WriteString(".\nВерни JSON строго по схеме (лишние поля можно опустить, пустые не выдумывай).\n")
	b.WriteString("Если табличной части в документе нет — верни \"lines\": [] и \"table\": null.\n")
	b.WriteString("Табличную часть переноси как в оригинале: сколько колонок в шапке документа — столько и в \"table\".\"columns\".\n")
	b.WriteString("Не объединяй, не переименовывай и не выбрасывай колонки, даже если их больше шести или их назначение непонятно.\n")
	b.WriteString("Каждая строка в \"table\".\"rows\" — массив той же длины, что и \"columns\"; пустая ячейка — пустая строка \"\".\n")
	b.WriteString("Поле \"lines\" заполняй дополнительно и упрощённо — оно нужно только для подбора счетов учёта.\n")
	b.WriteString("Суммы и количества — числами, разделитель дробной части — точка, без пробелов между разрядами.\n")
	b.Write(schemaJSON)
	if hint := AccountsPromptHint(); hint != "" {
		b.WriteString("\n\nДопустимые счета учёта (используй только код слева, ничего не придумывай): ")
		b.WriteString(hint)
		b.WriteString("\nЕсли счёт по содержимому не определяется — верни пустую строку.")
	}
	if strings.TrimSpace(tableGrid) != "" {
		// Таблица уже разобрана по координатам: колонки разделены «|».
		// Модели остаётся исправить ячейки, а не восстанавливать таблицу из
		// склеенного текста — так она перестаёт выдумывать строки.
		const maxGrid = 3000
		tableGrid = truncRunes(tableGrid, maxGrid)
		b.WriteString("\n\nТабличная часть, уже разобранная по колонкам (разделитель «|», первая строка — шапка).\n")
		b.WriteString("Сохрани этот состав колонок и строк дословно, только исправь ошибки распознавания символов:\n")
		b.WriteString(tableGrid)
	}
	b.WriteString("\n\nТекст документа:\n---\n")
	b.WriteString(ocrText)
	b.WriteString("\n---")
	return b.String()
}

func ext(d domain.Document) string {
	switch strings.ToLower(d.ContentType) {
	case "application/pdf":
		return ".pdf"
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/heic", "image/heif":
		return ".heic"
	}
	if i := strings.LastIndexByte(d.OriginalName, '.'); i >= 0 {
		return d.OriginalName[i:]
	}
	return ".bin"
}

// docTypePromptHint перечисляет модели известные системе типы, чтобы она
// отвечала кодом из реестра, а не произвольным названием. Хвост «или иной»
// оставлен намеренно: перечень открыт (ТЗ §5), незнакомую форму модель должна
// назвать своими словами, а не втискивать в ближайший знакомый код.
func docTypePromptHint() string {
	specs := DocTypes()
	codes := make([]string, 0, len(specs))
	for _, s := range specs {
		codes = append(codes, s.Slug+" — "+s.Title)
	}
	return "строка: код типа документа из списка [" + strings.Join(codes, "; ") +
		"]; если документ не подходит ни под один — назови тип своими словами"
}

// jsonStr принимает и "31.49", и 31.49: модель возвращает суммы то строкой,
// то числом, а одно числовое значение роняло разбор всего ответа целиком —
// вместе с датой, контрагентом и табличной частью.
type jsonStr string

func (s *jsonStr) UnmarshalJSON(b []byte) error {
	t := strings.TrimSpace(string(b))
	if t == "" || t == "null" {
		*s = ""
		return nil
	}
	switch t[0] {
	case '"':
		var v string
		if err := json.Unmarshal([]byte(t), &v); err != nil {
			return err
		}
		*s = jsonStr(v)
	case '{', '[':
		// вложенную структуру в скалярное поле не тащим
		*s = ""
	default:
		*s = jsonStr(t)
	}
	return nil
}

// truncRunes режет строку по рунам, а не по байтам. Прежняя обрезка
// ocrText[:6000] рвала кириллическую руну пополам: модель получала битый
// UTF-8 в хвосте промпта.
func truncRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	n := 0
	for i := range s {
		if n == max {
			return s[:i]
		}
		n++
	}
	return s
}
PATCH_TRACE_EOF_PIPELINE_GO

check_sha "docflow/internal/recognize/refine_split.go" "70f00c28e227aace70856af87f7feed25ea56de971105c3b4a55311eb4117d4b" "2e071cc1e168ef70b2c3be9adcafbf3e924e6f868b9888410ae8e2cba6eabbda"
cp -a "docflow/internal/recognize/refine_split.go" "$BACKUP/"
echo "  пишу docflow/internal/recognize/refine_split.go"
cat > "docflow/internal/recognize/refine_split.go" <<'PATCH_TRACE_EOF_REFINE_SPLIT_GO'
package recognize

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"docflow/internal/domain"
)

// Раздельный разбор документа моделью.
//
// Раньше в модель уходил один промпт: весь плоский текст страницы плюс таблица
// «через |» плюс схема ответа сразу на всё. На счетах и накладных это давало
// характерную ошибку — в табличную часть заезжали строки из шапки (адрес,
// расчётный счёт, «Грузоотправитель»), а поля документа модель собирала из
// строк таблицы. Оба промаха от одной причины: модель не знает, где кончается
// шапка и начинается таблица, и решает это сама по ходу генерации.
//
// Теперь запроса два, и они идут параллельно:
//   - реквизиты: текст ВЫШЕ и НИЖЕ табличной полосы, без самой таблицы;
//   - табличная часть: только Markdown-таблица, без текста документа.
//
// Каждый запрос отвечает за свою половину схемы, путать им нечего.

// DocContext — документ, разрезанный на части для раздельных промптов.
type DocContext struct {
	// Head — всё, что напечатано выше табличной полосы: наименование
	// документа, номер, дата, реквизиты сторон.
	Head string
	// Foot — всё, что ниже: итоги прописью, «в том числе НДС», подписи.
	Foot string
	// Table — табличная часть в Markdown. Пусто — таблицу собрать не удалось.
	Table string
	// TableSource — чем собрана таблица (grid/columns/layout/rules).
	TableSource string
}

const (
	maxHeadChars  = 3500
	maxFootChars  = 1500
	maxTableChars = 5000
)

// BuildDocContext режет документ на шапку, подвал и Markdown-таблицу.
func BuildDocContext(words []wordBox, flatText string, t *domain.Table) DocContext {
	dc := DocContext{Table: TableMarkdown(t)}
	if t != nil && t.RawCells != nil {
		dc.TableSource = t.RawCells.Source
	}
	if len(dc.Table) > maxTableChars {
		dc.Table = dc.Table[:maxTableChars]
	}

	head, foot, ok := splitAroundTable(words)
	if !ok {
		// Координат слов нет (tesseract без раскладки) или полосу таблицы
		// найти не удалось — отдаём модели весь текст как шапку. Хуже, чем
		// раздельно, но не хуже прежнего единого промпта.
		head = flatText
		foot = ""
	}
	dc.Head = clip(strings.TrimSpace(head), maxHeadChars)
	dc.Foot = clip(strings.TrimSpace(foot), maxFootChars)
	return dc
}

// clip обрезает текст по границе символа, а не байта.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	out := make([]rune, 0, len(s))
	total := 0
	for _, c := range s {
		total += len(string(c))
		if total > n {
			break
		}
		out = append(out, c)
	}
	return strings.TrimSpace(string(out))
}

// splitAroundTable отделяет текст выше и ниже табличной полосы по координатам
// слов. ok=false — полосу найти не удалось.
func splitAroundTable(words []wordBox) (head, foot string, ok bool) {
	toks := make([]token, 0, len(words))
	for _, w := range words {
		t := normalizeNoSign(StripMath(strings.TrimSpace(w.Text)))
		if t == "" || w.X1 <= w.X0 || w.Y1 <= w.Y0 {
			continue
		}
		toks = append(toks, token{Text: t, X0: w.X0, Y0: w.Y0, X1: w.X1, Y1: w.Y1})
	}
	if len(toks) < 8 {
		return "", "", false
	}
	rows := groupTokenRows(toks)
	if len(rows) < 3 {
		return "", "", false
	}
	h, _, last := findTableBand(rows)
	if h < 0 {
		return "", "", false
	}

	var hb, fb strings.Builder
	for i := 0; i < h && i < len(rows); i++ {
		hb.WriteString(rowText(rows[i]))
		hb.WriteByte('\n')
	}
	for i := last + 1; i < len(rows); i++ {
		fb.WriteString(rowText(rows[i]))
		fb.WriteByte('\n')
	}
	return hb.String(), fb.String(), true
}

// gridWins решает, перекрывает ли таблица от детектора (surya table_rec) уже
// собранную проекцией колонок.
//
// Раньше выигрывала проекция: сетку брали только когда координатной таблицы не
// было вовсе. На реальных бланках это оказалось хуже — проекция группирует
// слова в строки по Y, и наименование, перенесённое на две-три строки, рвёт
// позицию на несколько, а цифры соседних граф попадают не в свою строку.
// Детектор таблиц строит строки по линиям бланка и перенос внутри ячейки
// переносом строки не считает.
//
// TABLE_PRIORITY=columns возвращает прежний порядок.
func gridWins(grid *domain.FreeTable, current *domain.Table) bool {
	if grid == nil || len(grid.Rows) == 0 {
		return false
	}

	// table_rec не должен безусловно перетирать таблицу, уже собранную
	// по координатам OCR-слов. На реальных сканах table_rec может определить
	// правдоподобную сетку, но неправильно привязать к ней текст.
	//
	// Поэтому grid остаётся fallback:
	//   - если таблицы вообще нет — можно использовать grid;
	//   - если columns/layout уже дали таблицу — не затираем её.
	if current == nil || len(current.Columns) == 0 || len(current.Rows) == 0 {
		return len(grid.Columns) >= 2
	}

	return false
}

// fieldSchemaMap — схема реквизитов документа для промпта. Вынесена из
// buildExtractPrompt, чтобы её использовали оба промпта одинаково.
func fieldSchemaMap() map[string]string {
	fieldSchema := map[string]string{
		"number":           "номер документа",
		"date":             "дата в формате ДД.ММ.ГГГГ",
		"total":            "итоговая сумма числом",
		"vat_amount":       "сумма НДС по документу числом (не ставка)",
		"currency":         "код валюты: BYN, RUB, USD, EUR",
		"counterparty":     "наименование контрагента — того, кто выставил документ (поставщик, продавец, исполнитель)",
		"organization":     "наименование получателя документа (покупатель, плательщик, заказчик)",
		"organization_unp": "налоговый номер получателя — тот, что подписан «УНП покупателя» или «УНП заказчика»",
	}
	if AccountsEnabled() {
		fieldSchema["account_debit"] = "счёт учёта затрат или актива, строго один код из списка допустимых счетов"
	}
	switch PrimaryTaxKey() {
	case "unp":
		fieldSchema["unp"] = "УНП продавца — тот, что подписан «УНП продавца» или «УНП поставщика» (Беларусь, ровно 9 цифр)"
	default:
		fieldSchema["inn"] = "ИНН контрагента (10 или 12 цифр)"
		fieldSchema["kpp"] = "КПП контрагента (9 цифр)"
	}
	return fieldSchema
}

// buildFieldsPrompt — промпт только по реквизитам. Табличной части в нём нет
// вовсе, и модель прямо предупреждена, что таблицу разбирает не она.
func buildFieldsPrompt(dc DocContext, ruleType string) string {
	hint := "не определён"
	if ruleType != DocTypeUnknown && ruleType != "" {
		hint = ruleType
	}
	schema := map[string]any{
		"doc_type": docTypePromptHint(),
		"fields":   fieldSchemaMap(),
	}
	schemaJSON, _ := json.Marshal(schema)

	var b strings.Builder
	b.WriteString("Предварительная гипотеза по типу: ")
	b.WriteString(hint)
	b.WriteString(".\nПеред тобой ТОЛЬКО шапка и подвал документа — табличная часть вырезана и разбирается отдельно.\n")
	b.WriteString("Верни JSON строго по схеме. Ничего не выдумывай: если реквизита в тексте нет — не включай его в ответ.\n")
	b.WriteString("Итоговую сумму бери из строки итога или из суммы прописью, ставку НДС не путай с суммой НДС.\n")
	// Стороны модель путала стабильно: в счёте-фактуре продавец напечатан
	// первым, покупатель ниже, и контрагентом бралась сторона, стоящая ближе
	// к концу текста. Плюс в организацию заезжала строка банка («в банке ОАО
	// ...», «Дирекция ...»): она идёт сразу после названия стороны и выглядит
	// как продолжение наименования.
	b.WriteString("Стороны определяй по подписям в документе, а не по порядку строк.\n")
	b.WriteString("Контрагент — тот, кто выставил документ: продавец, поставщик, исполнитель, арендодатель.\n")
	b.WriteString("Организация — тот, кому документ выставлен: покупатель, плательщик, заказчик, арендатор.\n")
	b.WriteString("Каждой стороне бери её собственный налоговый номер: у продавца — УНП продавца, у покупателя — УНП покупателя.\n")
	b.WriteString("В наименования сторон не бери названия банков и строки реквизитов: «в банке», «р/с», «Дирекция», BIC и номер счёта стороной документа не являются.\n")
	b.Write(schemaJSON)
	if hint := AccountsPromptHint(); hint != "" {
		b.WriteString("\n\nДопустимые счета учёта (используй только код слева, ничего не придумывай): ")
		b.WriteString(hint)
		b.WriteString("\nЕсли счёт по содержимому не определяется — верни пустую строку.")
	}
	b.WriteString("\n\nШапка документа:\n---\n")
	b.WriteString(dc.Head)
	b.WriteString("\n---")
	if dc.Foot != "" {
		b.WriteString("\n\nПодвал документа (итоги, суммы прописью, подписи):\n---\n")
		b.WriteString(dc.Foot)
		b.WriteString("\n---")
	}
	return b.String()
}

// buildTablePrompt — промпт только по табличной части. На вход идёт Markdown,
// на выход — та же таблица ячейками плюс упрощённый lines[] для счетов учёта.
func buildTablePrompt(dc DocContext, ruleType string) string {
	schema := map[string]any{
		"table": map[string]any{
			"columns": []string{"заголовки колонок ровно как в шапке таблицы, слева направо"},
			"rows":    []any{[]string{"ячейки строки в том же порядке, что и колонки"}},
			"totals":  []string{"строка Итого теми же ячейками, если она есть"},
		},
		"lines": []map[string]string{{
			"name":   "наименование товара или услуги",
			"qty":    "количество числом",
			"unit":   "единица измерения",
			"price":  "цена за единицу числом",
			"amount": "сумма по строке числом",
			"vat":    "ставка или сумма НДС",
		}},
	}
	if AccountsEnabled() {
		schema["lines"] = []map[string]string{{
			"name":    "наименование товара или услуги",
			"qty":     "количество числом",
			"unit":    "единица измерения",
			"price":   "цена за единицу числом",
			"amount":  "сумма по строке числом",
			"vat":     "ставка или сумма НДС",
			"account": "счёт учёта этой позиции, строго один код из списка допустимых счетов",
		}}
	}
	schemaJSON, _ := json.Marshal(schema)

	var b strings.Builder
	b.WriteString("Перед тобой ТОЛЬКО табличная часть документа")
	if ruleType != DocTypeUnknown && ruleType != "" {
		b.WriteString(" (тип: " + ruleType + ")")
	}
	b.WriteString(" в формате Markdown. Реквизиты документа разбирает другой запрос — их здесь нет.\n")
	b.WriteString("Верни JSON строго по схеме, без пояснений и без markdown.\n")
	b.WriteString("Строк в ответе ровно столько же, сколько строк данных в таблице ниже: не объединяй и не разбивай их.\n")
	b.WriteString("Колонок в \"columns\" ровно столько же, сколько столбцов в таблице ниже, и в том же порядке.\n")
	b.WriteString("Каждая строка \"rows\" — массив той же длины, что и \"columns\"; пустая ячейка — пустая строка \"\".\n")
	b.WriteString("Ничего не добавляй от себя: если ячейка пустая в таблице, она пустая и в ответе.\n")
	b.WriteString("Исправляй только явные ошибки распознавания символов (О вместо 0, з вместо 3, слипшиеся цифры).\n")
	b.WriteString("Суммы и количества — числами, разделитель дробной части — точка, без пробелов между разрядами.\n")
	b.WriteString("Поле \"lines\" заполняй дополнительно и упрощённо — оно нужно только для подбора счетов учёта.\n")
	b.Write(schemaJSON)
	if hint := AccountsPromptHint(); hint != "" {
		b.WriteString("\n\nДопустимые счета учёта (используй только код слева, ничего не придумывай): ")
		b.WriteString(hint)
		b.WriteString("\nЕсли счёт по содержимому не определяется — верни пустую строку.")
	}
	b.WriteString("\n\nТабличная часть:\n")
	b.WriteString(dc.Table)
	return b.String()
}

// refineSplit гоняет два промпта параллельно и сшивает ответы.
// ok=false — оба запроса не удались, остаётся результат правил.
func (p *Pipeline) refineSplit(ctx context.Context, dc DocContext, byRules domain.Recognition, tr *Tracer) (domain.Recognition, bool) {
	const sysCommon = "Ты разбираешь распознанный текст бухгалтерского документа. " +
		"Отвечай строго одним JSON-объектом без пояснений и без markdown."

	// Таблицу спрашиваем, когда она есть в Markdown либо когда её вовсе не
	// собрали, а тип документа её предполагает.
	wantTable := strings.TrimSpace(dc.Table) != "" ||
		(len(byRules.Lines) == 0 && typeHasLines(byRules.DocType))

	var (
		wg              sync.WaitGroup
		headRes, tabRes modelExtract
		headOK, tabOK   bool
	)

	wg.Add(1)
	go func() {
		defer wg.Done()
		prompt := buildFieldsPrompt(dc, byRules.DocType)
		tr.Text("10-llm-fields-prompt.txt", prompt)
		started := time.Now()
		raw, err := p.model.complete(ctx, sysCommon+" Разбирай только реквизиты документа.", prompt)
		tr.Mark("промпт_реквизитов", time.Since(started))
		if err != nil {
			p.log.Warn("промпт реквизитов не отработал", "error", err)
			tr.Set("реквизиты_ошибка", err.Error())
			return
		}
		tr.Text("11-llm-fields-answer.txt", raw)
		if err := decodeJSONObject(raw, &headRes); err != nil {
			p.log.Warn("промпт реквизитов: ответ не разобран", "error", err)
			tr.Set("реквизиты_ошибка_разбора", err.Error())
			return
		}
		headOK = true
	}()

	if wantTable {
		wg.Add(1)
		go func() {
			defer wg.Done()
			user := buildTablePrompt(dc, byRules.DocType)
			if strings.TrimSpace(dc.Table) == "" {
				// Таблицы в разобранном виде нет — отдаём модели текст
				// документа, иначе разбирать ей нечего.
				user += "\n(таблица не распознана; собери её из текста)\n" + dc.Head + "\n" + dc.Foot
			}
			tr.Text("12-llm-table-prompt.txt", user)
			started := time.Now()
			raw, err := p.model.complete(ctx, sysCommon+" Разбирай только табличную часть.", user)
			tr.Mark("промпт_таблицы", time.Since(started))
			if err != nil {
				p.log.Warn("промпт таблицы не отработал", "error", err)
				tr.Set("таблица_ошибка", err.Error())
				return
			}
			tr.Text("13-llm-table-answer.txt", raw)
			if err := decodeJSONObject(raw, &tabRes); err != nil {
				p.log.Warn("промпт таблицы: ответ не разобран", "error", err)
				tr.Set("таблица_ошибка_разбора", err.Error())
				return
			}
			tabOK = true
		}()
	}
	wg.Wait()

	if !headOK && !tabOK {
		return domain.Recognition{}, false
	}
	p.log.Info("раздельный разбор моделью", "реквизиты", headOK, "таблица", tabOK,
		"источник_таблицы", dc.TableSource)

	var dropped []string
	fields := make(map[string]domain.Field, len(byRules.Fields)+len(headRes.Fields))
	for k, v := range byRules.Fields {
		fields[k] = v
	}
	for k, v := range headRes.Fields {
		k = strings.TrimSpace(strings.ToLower(k))
		sv := strings.TrimSpace(string(v))
		if k == "" || sv == "" {
			continue
		}
		if strings.HasPrefix(k, "account_") && AccountsEnabled() && !KnownAccount(sv) {
			p.log.Warn("модель вернула счёт вне плана счетов", "field", k, "value", sv)
			dropped = append(dropped, k+"="+sv+" (счёт вне плана счетов)")
			continue
		}
		if k == "number" && looksLikeAccountNumber(sv) {
			p.log.Warn("модель вернула в номере банковский счёт, значение отброшено", "value", sv)
			dropped = append(dropped, k+"="+sv+" (похоже на банковский счёт)")
			continue
		}
		fields[k] = domain.Field{Value: sv, Confidence: 0.85, Source: "model"}
	}
	if len(dropped) > 0 {
		tr.Set("поля_модели_отброшены", dropped)
	}

	docType := NormalizeDocType(headRes.DocType)
	if docType == "" || docType == DocTypeUnknown {
		docType = byRules.DocType
	}

	lines := byRules.Lines
	table := byRules.Table
	// Таблицу от модели не берём на веру и не отвергаем по источнику: она
	// проходит ту же чистку и ту же проверку арифметикой, что и разборы по
	// координатам. Побеждает та, что сходится с документом.
	if ft := convertModelTable(tabRes.Table); ft != nil {
		var geom *domain.FreeTable
		if byRules.Table != nil {
			geom = byRules.Table.RawCells
		}
		if best, diag := PickBestFreeTable([]*domain.FreeTable{geom, ft}); best != nil {
			p.log.Info("табличная часть после модели", "источник", best.Source, "оценки", diag)
			tr.Candidates("13b-table-after-model.json", []*domain.FreeTable{geom, ft}, best, "после модели: "+diag)
			tr.Set("таблица_источник_после_модели", best.Source)
			tr.Set("таблица_строк_после_модели", len(best.Rows))
			table = FreeTableToTable(best)
			lines = FreeTableToLines(best)
		}
	}
	// Счета учёта модель отдаёт в lines[]; переносим по совпадению наименования.
	if acc := convertModelLines(tabRes.Lines); len(acc) > 0 {
		if len(lines) == 0 && table == nil {
			lines = acc
			table = LinesToTable(acc, "model")
		} else {
			for i := range lines {
				for _, a := range acc {
					if a.Account != "" && strings.EqualFold(a.Name, lines[i].Name) {
						lines[i].Account = a.Account
						break
					}
				}
			}
		}
	}

	conf := byRules.DocTypeConfidence
	if headOK {
		conf = 0.85
	}
	return domain.Recognition{
		DocType:           docType,
		DocTypeConfidence: conf,
		Fields:            fields,
		Lines:             lines,
		Table:             table,
	}, true
}
PATCH_TRACE_EOF_REFINE_SPLIT_GO

# --- .env: ключи трассировки ------------------------------------------------
cp -a .env "$BACKUP/" 2>/dev/null || true
add_env() {
  local key="$1" val="$2" comment="$3"
  if ! grep -q "^${key}=" .env 2>/dev/null; then
    { echo ""; echo "# $comment"; echo "${key}=${val}"; } >> .env
    echo "  .env += ${key}=${val}"
  else
    echo "  .env: ${key} уже задан, не трогаю"
  fi
}
add_env TRACE_DIR "/var/lib/docflow/trace" "Трассировка распознавания: каталог с пофазовым дампом по каждому документу. Пусто — выключено (в проде держать пустым: там персональные данные)."
add_env TRACE_PAGES "true" "Класть в дамп подготовленные страницы (то, что реально видит OCR)."
add_env TRACE_SOURCE "true" "Класть в дамп исходный файл документа."
add_env TRACE_KEEP "20" "Сколько последних прогонов хранить; 0 — не удалять старые."

# --- монтирование каталога трассировки на хост ------------------------------
mkdir -p ./trace
chmod 777 ./trace
cp -a docker-compose.override.yml "$BACKUP/" 2>/dev/null || true
python3 - <<'PY'
import io, os, re
p = "docker-compose.override.yml"
mount = "      - ./trace:/var/lib/docflow/trace\n"
if not os.path.exists(p):
    open(p, "w").write("services:\n  docflow-backend:\n    volumes:\n" + mount)
    print("  создан docker-compose.override.yml с монтированием ./trace")
    raise SystemExit
s = open(p, encoding="utf-8").read()
if "/var/lib/docflow/trace" in s:
    print("  монтирование ./trace уже есть")
    raise SystemExit
m = re.search(r"(?m)^  docflow-backend:\n(?:.*\n)*?    volumes:\n", s)
if m:
    s = s[:m.end()] + mount + s[m.end():]
elif re.search(r"(?m)^  docflow-backend:\n", s):
    m2 = re.search(r"(?m)^  docflow-backend:\n", s)
    s = s[:m2.end()] + "    volumes:\n" + mount + s[m2.end():]
else:
    s = s.rstrip("\n") + "\n  docflow-backend:\n    volumes:\n" + mount
open(p, "w", encoding="utf-8").write(s)
print("  в docker-compose.override.yml добавлено монтирование ./trace")
PY

# --- сборщик дампа для отправки ---------------------------------------------
cat > collect-trace.sh <<'COLLECT'
#!/usr/bin/env bash
# Складывает каталог трассировки в архив для отправки.
set -euo pipefail
OUT="trace-$(date +%Y%m%d-%H%M%S).tar.gz"
tar -czf "$OUT" trace
echo "Готово: $(pwd)/$OUT ($(du -h "$OUT" | cut -f1))"
COLLECT
chmod +x collect-trace.sh

# --- сборка ------------------------------------------------------------------
if [[ "$NO_BUILD" == "1" ]]; then
  echo "NO_BUILD=1 — образ не пересобираю."
else
  echo "Пересобираю бэкенд…"
  docker compose build docflow-backend
  docker compose up -d docflow-backend
fi

cat <<'DONE'

Готово.

Дальше:
  1. Загрузите через интерфейс 5 документов (лучше разных: счёт, накладную,
     акт, фото с телефона, скан похуже).
  2. Дождитесь, пока все получат статус, и соберите дамп:
       cd /opt/docflow-deploy && ./collect-trace.sh
  3. Пришлите получившийся trace-*.tar.gz.

Быстрый взгляд на месте:
  column -t -s $'\t' trace/index.tsv | less -S
  cat trace/*/00-meta.json | less

Выключить трассировку: в .env поставить TRACE_DIR= (пусто) и
  docker compose up -d docflow-backend
DONE
