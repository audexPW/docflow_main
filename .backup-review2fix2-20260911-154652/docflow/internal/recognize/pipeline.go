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
	return p.ProcessFor(ctx, doc, ProcessHint{})
}

// ProcessFor — то же, но с контекстом компании и её справочником контрагентов:
// по ним определяются стороны документа и решается, может ли он пройти мимо
// главбуха.
func (p *Pipeline) ProcessFor(ctx context.Context, doc domain.Document, hint ProcessHint) (Result, error) {
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

	if hint.Company != nil {
		tr.Set("компания", hint.Company.Name)
		tr.Set("компания_унп", hint.Company.UNP)
	}
	return p.processPath(ctx, srcPath, doc.ContentType, workDir, tr, hint)
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

	return p.processPath(ctx, path, contentType, workDir, tr, ProcessHint{})
}

func (p *Pipeline) processPath(ctx context.Context, srcPath, contentType, workDir string, tr *Tracer, hint ProcessHint) (Result, error) {
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
	// Координаты слов чиним до всякого разбора: движок отдаёт один
	// прямоугольник на строку, и без этого и порядок слов, и границы граф
	// считаются по дрожанию координат (linebox.go).
	if n := len(page.Words); n > 0 {
		// Порядок движка снимаем до разрезания боксов: после него слова
		// пересортированы по координатам (readorder.go).
		page.Words = AttachEngineOrder(page.Words, page.Flat)
		tr.Set("порядок_движка_подтверждён", len(page.Words) > 0 && page.Words[0].Line > 0)
		page.Words = SplitLineBoxes(page.Words)
		p.log.Info("координаты слов восстановлены", "было", n, "стало", len(page.Words))
	}
	// Раскладку собираем сами: у сервиса surya абзац, выровненный по
	// ширине, читался снизу вверх. Раскладку сервиса оставляем в трассировке
	// для сравнения. LAYOUT_FROM_WORDS=false возвращает прежнее поведение.
	if envBool("LAYOUT_FROM_WORDS", true) {
		if lw := LayoutFromWords(page.Words); strings.TrimSpace(lw) != "" {
			tr.Text("04-ocr-layout-engine.txt", page.Layout)
			page.Layout = lw
		}
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

	// Режем страницу на зоны: реквизиты шапки ищутся только выше таблицы,
	// суммы — в шапке, строке «Итого» и подвале, позиции в реквизиты не идут.
	zones := BuildZones(page.Words, ocrText)
	tr.Set("зоны_найдены", zones.OK)
	tr.Text("04a-zone-head.txt", zones.Head)
	tr.Text("04b-zone-body.txt", zones.Body)
	tr.Text("04c-zone-foot.txt", zones.Foot)

	byRules := RecognizeDocumentZoned(ocrText, layoutText, zones)

	// Номер по месту на листе: в бланках он стоит справа от названия
	// документа или под ним, часто вообще без знака «№».
	headerNum := HeaderNumberFromWords(page.Words)
	if headerNum != "" {
		tr.Set("номер_по_месту_на_листе", headerNum)
		cur, ok := byRules.Fields["number"]
		if !ok || strings.TrimSpace(cur.Value) == "" || cur.Confidence < ruleConfidence {
			byRules.Fields["number"] = domain.Field{
				Value: headerNum, Confidence: ruleConfidence, Source: "rule",
			}
		}
	}
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
		rec.DocType, rec.DocTypeConfidence = ResolveDocType(byRules.DocType, byRules.DocTypeConfidence, "", zones.HeaderText(ocrText))
	default:
		tr.Set("модель_вызвана", true)
		tr.Set("модель_причина", reason)
	}
	if p.model.enabled() && needsModel(byRules, typeConf) {
		dc := BuildDocContextZoned(zones, ocrText, byRules.Table)
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
	// Сумма без НДС: из итога таблицы или из текста «в том числе НДС».
	FillAmounts(&rec, ocrText)
	// Стороны — по известному УНП компании, контрагент — по справочнику.
	ResolveParties(&rec, ocrText, hint)

	// Счета учёта подбираем последним шагом — по уже собранным строкам и типу
	// документа. Это то, что бухгалтер иначе выбирал бы в 1С руками для каждой
	// позиции, сводя на нет выигрыш от распознавания.
	ApplyAccounts(&rec)

	// Уверенность считаем последним шагом — по всем свидетельствам сразу:
	// сходятся ли источники, есть ли значение в документе, стоит ли оно в
	// своей зоне листа, бьётся ли арифметика. До этого в поле стоял ярлык
	// происхождения, переодетый в проценты.
	ApplyConfidence(&rec, ConfidenceInput{
		Full:   ocrText,
		Zones:  zones,
		Rules:  byRules.Fields,
		Header: headerNum,
	})
	finalizeDerived(&rec)

	// Заведомо невозможное значение не понижается, а отклоняется: поле
	// пустое, документ идёт человеку. Плюс дебет не может совпадать с
	// кредитом.
	EnforceFormats(&rec, ocrText)
	CheckAccounts(&rec)

	// Решение, может ли документ пройти мимо главбуха. Сам маршрут выбирает
	// воркер, здесь только проверка и список непрошедших полей.
	rec.Missing = MissingRequired(rec)
	if LinesTotalMismatch(rec, LinesTolerance) {
		rec.Missing = append(rec.Missing, "lines")
	}
	DecideAutoPass(&rec, hint)
	tr.Set("отклонено_проверкой_формата", rec.Rejected)
	tr.Set("проверки", rec.Checks)
	tr.Set("авто_проход", rec.AutoPass)
	tr.Set("на_проверку_поля", rec.Review)
	tr.Set("на_проверку_причины", rec.ReviewNotes)
	tr.Set("уверенность_типа_итог", rec.DocTypeConfidence)

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
	return RecognizeDocumentZoned(ocrText, layoutText, Zones{})
}

// RecognizeDocumentZoned — тот же разбор, но реквизиты ищутся по зонам листа.
// Пустые зоны означают «зон нет», и поведение остаётся прежним.
func RecognizeDocumentZoned(ocrText, layoutText string, z Zones) domain.Recognition {
	docType, conf := classifyByRules(ocrText)
	rec := domain.Recognition{
		DocType:           docType,
		DocTypeConfidence: conf,
		Fields:            extractByRulesZoned(ocrText, z),
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

	// Flat — ответ пришёл без обёртки "fields", реквизиты лежали на верхнем
	// уровне. Только для диагностики.
	Flat bool `json:"-"`
}

// UnmarshalJSON принимает ответ модели и в схеме промпта, и в плоском виде.
//
// Промпт просит {"doc_type": ..., "fields": {...}}, но Qwen на большинстве
// документов отвечает {"doc_type": ..., "counterparty": ..., "total": ...} —
// реквизитами на верхнем уровне, без обёртки. Раньше такой ответ разбирался
// без ошибки, Fields оставалась пустой, и ВЕСЬ разбор модели молча уходил в
// мусор: в карточке оставались поля правил, а в логе всё выглядело успешным
// («раздельный разбор моделью реквизиты=true»). Отсюда и пустые контрагент,
// организация и УНП при том, что модель их правильно нашла.
func (m *modelExtract) UnmarshalJSON(b []byte) error {
	// Псевдоним, иначе UnmarshalJSON вызовет сам себя.
	type rawExtract struct {
		DocType string             `json:"doc_type"`
		Fields  map[string]jsonStr `json:"fields"`
		Lines   []modelLine        `json:"lines"`
		Table   *modelTable        `json:"table"`
	}
	var raw rawExtract
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	m.DocType, m.Fields, m.Lines, m.Table = raw.DocType, raw.Fields, raw.Lines, raw.Table
	if len(m.Fields) > 0 {
		return nil
	}

	var flat map[string]json.RawMessage
	if err := json.Unmarshal(b, &flat); err != nil {
		return nil // не объект — оставляем то, что разобралось схемой
	}
	fields := make(map[string]jsonStr, len(flat))
	for k, v := range flat {
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "doc_type", "fields", "lines", "table", "totals":
			continue
		}
		var sv jsonStr
		if err := sv.UnmarshalJSON(v); err != nil {
			continue
		}
		if strings.TrimSpace(string(sv)) == "" {
			continue
		}
		fields[k] = sv
	}
	if len(fields) > 0 {
		m.Fields = fields
		m.Flat = true
	}
	return nil
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
	docType, typeConf := ResolveDocType(byRules.DocType, byRules.DocTypeConfidence, me.DocType, ocrText)

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
		DocTypeConfidence: typeConf,
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

// docTypePromptHint перечисляет модели известные системе типы. Список закрыт:
// раньше промпт разрешал «назвать тип своими словами», модель честно
// выполняла инструкцию («Акт использования имущества»), и документ получал
// код, под который не подходило ни одно правило. Незнакомая форма — unknown,
// её разбирает человек; новый тип добавляется в doctypes.json.
func docTypePromptHint() string {
	specs := DocTypes()
	codes := make([]string, 0, len(specs))
	for _, s := range specs {
		codes = append(codes, s.Slug+" — "+s.Title)
	}
	return "строка: строго один код из списка [" + strings.Join(codes, "; ") +
		"]; если документ не подходит ни под один код — верни unknown; другие значения запрещены"
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
