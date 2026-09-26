#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# DocFlow: таблица через Surya Table Recognition + Markdown + раздельные
# промпты к Qwen (реквизиты и таблица считаются параллельно).
#
# Запуск:   cd /opt/docflow-deploy && bash patch-surya-table-md.sh
# Откат:    каждый изменённый файл сохраняется рядом как *.bak.<метка>
# ---------------------------------------------------------------------------
set -euo pipefail

ROOT="${DOCFLOW_DIR:-$(pwd)}"
STAMP="$(date +%Y-%m-%d_%H-%M-%S)"
cd "$ROOT"

say() { printf '\n\033[1;33m==> %s\033[0m\n' "$*"; }
ok()  { printf '    \033[0;32m%s\033[0m\n' "$*"; }
warn(){ printf '    \033[0;31m%s\033[0m\n' "$*"; }

[ -f docker-compose.yml ] || { warn "docker-compose.yml не найден. Запускать из каталога docflow-deploy."; exit 1; }
[ -d docflow/internal/recognize ] || { warn "нет docflow/internal/recognize — не тот каталог"; exit 1; }

backup() {
  if [ -f "$1" ]; then
    cp -a "$1" "$1.bak.$STAMP"
    ok "бэкап: $1.bak.$STAMP"
  fi
  return 0
}

# ---------------------------------------------------------------------------
say "1/5  Go: Markdown-представление таблицы"
# ---------------------------------------------------------------------------
cat > docflow/internal/recognize/markdown.go <<'DF_MD_EOF'
package recognize

import (
	"strings"

	"docflow/internal/domain"
)

// Табличная часть в Markdown.
//
// Модель получала таблицу «шапкой через |» вперемешку с текстом всего
// документа и регулярно путала строки: то склеивала две позиции в одну, то
// растаскивала перенесённое наименование по соседним строкам. Markdown с
// разделителем «|---|» — единственный табличный формат, который Qwen видел в
// обучении в товарных количествах, и на нём разбор строк заметно устойчивее.
//
// Формат намеренно строгий: одинаковое число ячеек в каждой строке, перевод
// строки внутри ячейки заменяется на пробел, вертикальная черта экранируется.
// Иначе одна ячейка с «|» съезжает и утаскивает за собой всю строку.

// mdCell готовит значение ячейки к вставке в Markdown.
func mdCell(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return " "
	}
	return s
}

// mdRow собирает одну строку Markdown-таблицы, дополняя её до нужной ширины.
func mdRow(cells []string, width int) string {
	var b strings.Builder
	b.WriteString("|")
	for i := 0; i < width; i++ {
		v := " "
		if i < len(cells) {
			v = mdCell(cells[i])
		}
		b.WriteString(" ")
		b.WriteString(v)
		b.WriteString(" |")
	}
	return b.String()
}

// FreeTableMarkdown отдаёт свободную таблицу как Markdown. Пустая строка —
// таблицы нет или в ней нет ни одной строки данных.
func FreeTableMarkdown(ft *domain.FreeTable) string {
	if ft == nil || len(ft.Rows) == 0 {
		return ""
	}
	width := len(ft.Columns)
	for _, r := range ft.Rows {
		if len(r) > width {
			width = len(r)
		}
	}
	if len(ft.Totals) > width {
		width = len(ft.Totals)
	}
	if width == 0 {
		return ""
	}

	cols := make([]string, width)
	for i := range cols {
		if i < len(ft.Columns) && strings.TrimSpace(ft.Columns[i]) != "" {
			cols[i] = ft.Columns[i]
			continue
		}
		// Безымянная графа всё равно должна остаться на своём месте, иначе
		// ячейки строк уедут влево.
		cols[i] = "гр." + itoa(i+1)
	}

	var b strings.Builder
	b.WriteString(mdRow(cols, width))
	b.WriteString("\n|")
	for i := 0; i < width; i++ {
		b.WriteString(" --- |")
	}
	for _, r := range ft.Rows {
		b.WriteString("\n")
		b.WriteString(mdRow(r, width))
	}
	if len(ft.Totals) > 0 {
		b.WriteString("\n")
		b.WriteString(mdRow(ft.Totals, width))
	}
	return b.String()
}

// TableMarkdown — то же самое для уже приведённой таблицы. Приоритет у RawCells:
// там колонки документа как есть, без приведения к ролям.
func TableMarkdown(t *domain.Table) string {
	if t == nil {
		return ""
	}
	if t.RawCells != nil {
		if md := FreeTableMarkdown(t.RawCells); md != "" {
			return md
		}
	}
	if len(t.Rows) == 0 {
		return ""
	}
	width := len(t.Columns)
	for _, r := range t.Rows {
		for _, c := range r.Cells {
			if c.Column+1 > width {
				width = c.Column + 1
			}
		}
	}
	if width == 0 {
		return ""
	}
	cols := make([]string, width)
	for _, c := range t.Columns {
		if c.Index >= 0 && c.Index < width {
			cols[c.Index] = c.Title
		}
	}
	ft := &domain.FreeTable{Columns: cols}
	for _, r := range t.Rows {
		row := make([]string, width)
		for _, c := range r.Cells {
			if c.Column >= 0 && c.Column < width {
				row[c.Column] = c.Value
			}
		}
		ft.Rows = append(ft.Rows, row)
	}
	if len(t.TotalsRow) > 0 {
		row := make([]string, width)
		for _, c := range t.TotalsRow {
			if c.Column >= 0 && c.Column < width {
				row[c.Column] = c.Value
			}
		}
		ft.Totals = row
	}
	return FreeTableMarkdown(ft)
}

// itoa — маленький помощник, чтобы не тянуть strconv ради одной подписи.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
DF_MD_EOF
ok "docflow/internal/recognize/markdown.go"

# ---------------------------------------------------------------------------
say "2/5  Go: раздельные промпты (реквизиты | таблица) + приоритет сетки"
# ---------------------------------------------------------------------------
cat > docflow/internal/recognize/refine_split.go <<'DF_RS_EOF'
package recognize

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"

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
	if strings.EqualFold(strings.TrimSpace(os.Getenv("TABLE_PRIORITY")), "columns") {
		return !tableIsGeometric(current)
	}
	if !tableIsGeometric(current) {
		return true
	}
	// Обе таблицы геометрические. Сетку не берём, только если она заметно
	// беднее: меньше и строк, и колонок — тогда детектор нашёл не ту рамку.
	cur := current.RawCells
	if cur == nil {
		return true
	}
	if len(grid.Rows) < len(cur.Rows) && len(grid.Columns) < len(cur.Columns) {
		return false
	}
	return true
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
		"organization_unp": "налоговый номер получателя документа",
	}
	if AccountsEnabled() {
		fieldSchema["account_debit"] = "счёт учёта затрат или актива, строго один код из списка допустимых счетов"
	}
	switch PrimaryTaxKey() {
	case "unp":
		fieldSchema["unp"] = "УНП контрагента (Беларусь, ровно 9 цифр)"
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
func (p *Pipeline) refineSplit(ctx context.Context, dc DocContext, byRules domain.Recognition) (domain.Recognition, bool) {
	const sysCommon = "Ты разбираешь распознанный текст бухгалтерского документа. " +
		"Отвечай строго одним JSON-объектом без пояснений и без markdown."

	gridOK := tableIsGeometric(byRules.Table)
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
		raw, err := p.model.complete(ctx, sysCommon+" Разбирай только реквизиты документа.",
			buildFieldsPrompt(dc, byRules.DocType))
		if err != nil {
			p.log.Warn("промпт реквизитов не отработал", "error", err)
			return
		}
		if err := decodeJSONObject(raw, &headRes); err != nil {
			p.log.Warn("промпт реквизитов: ответ не разобран", "error", err)
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
			raw, err := p.model.complete(ctx, sysCommon+" Разбирай только табличную часть.", user)
			if err != nil {
				p.log.Warn("промпт таблицы не отработал", "error", err)
				return
			}
			if err := decodeJSONObject(raw, &tabRes); err != nil {
				p.log.Warn("промпт таблицы: ответ не разобран", "error", err)
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
			continue
		}
		if k == "number" && looksLikeAccountNumber(sv) {
			p.log.Warn("модель вернула в номере банковский счёт, значение отброшено", "value", sv)
			continue
		}
		fields[k] = domain.Field{Value: sv, Confidence: 0.85, Source: "model"}
	}

	docType := NormalizeDocType(headRes.DocType)
	if docType == "" || docType == DocTypeUnknown {
		docType = byRules.DocType
	}

	lines := byRules.Lines
	table := byRules.Table
	if ft := convertModelTable(tabRes.Table); ft != nil && !gridOK {
		table = FreeTableToTable(ft)
		lines = FreeTableToLines(ft)
	}
	// Счета учёта модель отдаёт в lines[]; переносим по совпадению наименования.
	if acc := convertModelLines(tabRes.Lines); len(acc) > 0 {
		if len(lines) == 0 && !gridOK && table == nil {
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
DF_RS_EOF
ok "docflow/internal/recognize/refine_split.go"

backup docflow/internal/recognize/pipeline.go
python3 - <<'DF_PIPE_EOF'
import sys

p = "docflow/internal/recognize/pipeline.go"
s = open(p, encoding="utf-8").read()
orig = s
done = []

# (1) сетка от table_rec перекрывает проекцию по Y
old = "\tif ft := FreeTableFromGrid2(page.Grids); ft != nil && !tableIsGeometric(byRules.Table) {"
new = "\tif ft := FreeTableFromGrid2(page.Grids); ft != nil && gridWins(ft, byRules.Table) {"
if old in s:
    s = s.replace(old, new); done.append("приоритет сетки")
elif new in s:
    done.append("приоритет сетки (уже был)")
else:
    print("!! не найден вызов FreeTableFromGrid2 — правку не применил"); sys.exit(2)

# (2) вызов раздельного разбора вместо единого промпта
old = """	rec := byRules
	if p.model.enabled() && needsModel(byRules, typeConf) {
		if refined, ok := p.refineWithModel(ctx, ocrText, byRules); ok {
			rec = refined
		}
	}"""
new = """	rec := byRules
	if p.model.enabled() && needsModel(byRules, typeConf) {
		dc := BuildDocContext(page.Words, ocrText, byRules.Table)
		p.log.Info("контекст для модели",
			"шапка_симв", len([]rune(dc.Head)),
			"подвал_симв", len([]rune(dc.Foot)),
			"таблица_симв", len([]rune(dc.Table)),
			"источник_таблицы", dc.TableSource)
		if refined, ok := p.refineSplit(ctx, dc, byRules); ok {
			rec = refined
		}
	}"""
if old in s:
    s = s.replace(old, new); done.append("раздельный разбор")
elif "p.refineSplit(ctx, dc, byRules)" in s:
    done.append("раздельный разбор (уже был)")
else:
    print("!! не найден вызов refineWithModel — правку не применил"); sys.exit(2)

# (3) схема реквизитов — в общую функцию, её используют оба промпта
if "fieldSchema := map[string]string{" in s:
    i = s.index("\tfieldSchema := map[string]string{")
    j = s.index("\tschema := map[string]any{", i)
    s = s[:i] + "\tfieldSchema := fieldSchemaMap()\n\n" + s[j:]
    done.append("общая схема реквизитов")
else:
    done.append("общая схема реквизитов (уже была)")

if s != orig:
    open(p, "w", encoding="utf-8").write(s)
print("    правки pipeline.go: " + ", ".join(done))
DF_PIPE_EOF

if command -v gofmt >/dev/null 2>&1; then gofmt -w docflow/internal/recognize/ && ok "gofmt пройден"; fi

# ---------------------------------------------------------------------------
say "3/5  surya-ocr: Table Recognition в сервисе"
# ---------------------------------------------------------------------------
mkdir -p surya-ocr
if [ -f surya-ocr/app.py ] && grep -q "TableRecPredictor" surya-ocr/app.py; then
  ok "app.py уже умеет table_rec — не трогаю (для замены: rm surya-ocr/app.py и запусти патч снова)"
else
  backup surya-ocr/app.py
  cat > surya-ocr/app.py <<'DF_APP_EOF'
"""surya-ocr — сервис распознавания страницы для DocFlow.

Отдаёт одним ответом четыре вещи:
  text     — страница с восстановленной раскладкой (границы граф « | »);
  flat     — та же страница построчно (под регулярки extract.go);
  boxes    — слова с координатами [x0,y0,x1,y1];
  tables   — таблицы от Surya Table Recognition: ячейки с номерами строки и
             колонки, текст ячейки собран из слов, попавших в её клетку.

Ключевое отличие от группировки по Y: строки таблицы берутся из детектора
структуры (surya.table_rec), а не из кластеризации слов по вертикали. Поэтому
наименование, перенесённое внутри ячейки на две-три строки, остаётся ОДНОЙ
позицией, а не превращается в несколько полупустых.

Наружу сервис ничего не отправляет: модели лежат в образе, работает офлайн.
"""

import logging
import os
import threading
from typing import Any, Dict, List, Optional, Sequence, Tuple

from fastapi import FastAPI, File, UploadFile
from fastapi.responses import JSONResponse
from PIL import Image
import io

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
log = logging.getLogger("surya-ocr")

# Порог склейки боксов в строку (доля высоты строки) и зазор между колонками
# в средних ширинах символа — те же ручки, что были у сервиса раньше.
ROW_FACTOR = float(os.getenv("SURYA_ROW_FACTOR", "0.35"))
COL_GAP = float(os.getenv("SURYA_COL_GAP", "1.2"))
GAP_FACTOR = float(os.getenv("SURYA_GAP_FACTOR", "1.2"))
TABLE_REC = os.getenv("SURYA_TABLE_REC", "1") not in ("0", "false", "False", "")

app = FastAPI(title="surya-ocr")

_lock = threading.Lock()
_predictors: Dict[str, Any] = {}


# --- загрузка моделей --------------------------------------------------------

def predictors() -> Dict[str, Any]:
    """Ленивая инициализация предикторов. Держим их в памяти между запросами."""
    if _predictors:
        return _predictors
    with _lock:
        if _predictors:
            return _predictors
        from surya.detection import DetectionPredictor
        from surya.recognition import RecognitionPredictor

        _predictors["det"] = DetectionPredictor()
        _predictors["rec"] = RecognitionPredictor()

        if TABLE_REC:
            try:
                from surya.table_rec import TableRecPredictor

                _predictors["table"] = TableRecPredictor()
            except Exception as exc:  # pragma: no cover
                log.warning("table_rec недоступен: %s", exc)
                _predictors["table"] = None
            try:
                from surya.layout import LayoutPredictor

                _predictors["layout"] = LayoutPredictor()
            except Exception as exc:  # pragma: no cover
                log.warning("layout недоступен, table_rec пойдёт по всей странице: %s", exc)
                _predictors["layout"] = None
        else:
            _predictors["table"] = None
            _predictors["layout"] = None
        return _predictors


def _bbox(obj: Any) -> Optional[List[float]]:
    b = getattr(obj, "bbox", None)
    if b is None and isinstance(obj, dict):
        b = obj.get("bbox")
    if not b or len(b) < 4:
        return None
    return [float(b[0]), float(b[1]), float(b[2]), float(b[3])]


def _text_of(obj: Any) -> str:
    t = getattr(obj, "text", None)
    if t is None and isinstance(obj, dict):
        t = obj.get("text")
    return (t or "").strip()


# --- OCR ---------------------------------------------------------------------

def _recognize(image: Image.Image):
    """Прогон распознавания с учётом разных сигнатур surya разных версий."""
    p = predictors()
    rec, det = p["rec"], p["det"]
    attempts = (
        lambda: rec([image], det_predictor=det, return_words=True),
        lambda: rec([image], det_predictor=det),
        lambda: rec([image], [None], det),
        lambda: rec([image], [["ru", "en"]], det),
    )
    last: Optional[Exception] = None
    for call in attempts:
        try:
            return call()[0]
        except TypeError as exc:
            last = exc
            continue
    raise RuntimeError(f"не подошла ни одна сигнатура RecognitionPredictor: {last}")


def _split_line(text: str, bbox: Sequence[float]) -> List[Tuple[str, List[float]]]:
    """Разрезает строку на слова, раскладывая их по ширине бокса пропорционально
    числу символов. Нужно, когда версия surya не отдаёт слова отдельно."""
    parts = [w for w in text.split() if w]
    if not parts:
        return []
    x0, y0, x1, y1 = [float(v) for v in bbox[:4]]
    total = sum(len(w) for w in parts) + max(len(parts) - 1, 0)
    if total <= 0 or x1 <= x0:
        return [(text, [x0, y0, x1, y1])]
    step = (x1 - x0) / total
    out: List[Tuple[str, List[float]]] = []
    cur = x0
    for w in parts:
        w0 = cur
        w1 = cur + step * len(w)
        out.append((w, [w0, y0, w1, y1]))
        cur = w1 + step  # пробел
    return out


def _words(result: Any) -> List[Tuple[str, List[float]]]:
    out: List[Tuple[str, List[float]]] = []
    for line in getattr(result, "text_lines", None) or []:
        lb = _bbox(line)
        words = getattr(line, "words", None)
        got = False
        for w in words or []:
            wb, wt = _bbox(w), _text_of(w)
            if wt and wb:
                out.append((wt, wb))
                got = True
        if got:
            continue
        lt = _text_of(line)
        if lt and lb:
            out.extend(_split_line(lt, lb))
    return out


# --- раскладка страницы ------------------------------------------------------

def _median(vals: List[float]) -> float:
    if not vals:
        return 0.0
    s = sorted(vals)
    return s[len(s) // 2]


def _layout(words: List[Tuple[str, List[float]]]) -> Tuple[str, str]:
    """Собирает текст с границами граф (« | ») и построчный текст."""
    if not words:
        return "", ""
    heights = [b[3] - b[1] for _, b in words if b[3] > b[1]]
    widths = [(b[2] - b[0]) / max(len(t), 1) for t, b in words if b[2] > b[0]]
    line_h = _median(heights) or 10.0
    char_w = _median(widths) or 5.0

    rows: List[List[Tuple[str, List[float]]]] = []
    for t, b in sorted(words, key=lambda w: w[1][1]):
        placed = False
        for row in rows:
            top = min(x[1][1] for x in row)
            bot = max(x[1][3] for x in row)
            ov = min(bot, b[3]) - max(top, b[1])
            if ov > max(bot - top, 1.0) * ROW_FACTOR:
                row.append((t, b))
                placed = True
                break
        if not placed:
            rows.append([(t, b)])
    rows.sort(key=lambda r: min(x[1][1] for x in r))

    gap = char_w * max(GAP_FACTOR, COL_GAP)
    layout_lines, flat_lines = [], []
    for row in rows:
        row.sort(key=lambda w: w[1][0])
        cells, plain = [], []
        prev_right = None
        for t, b in row:
            if prev_right is not None and b[0] - prev_right > gap:
                cells.append("|")
            cells.append(t)
            plain.append(t)
            prev_right = b[2]
        layout_lines.append(" ".join(cells))
        flat_lines.append(" ".join(plain))
    return "\n".join(layout_lines), "\n".join(flat_lines)


# --- таблицы -----------------------------------------------------------------

def _table_regions(image: Image.Image) -> List[List[float]]:
    p = predictors()
    lay = p.get("layout")
    if lay is None:
        return [[0.0, 0.0, float(image.width), float(image.height)]]
    try:
        res = lay([image])[0]
    except Exception as exc:
        log.warning("layout не отработал: %s", exc)
        return [[0.0, 0.0, float(image.width), float(image.height)]]
    regions = []
    for box in getattr(res, "bboxes", None) or []:
        label = str(getattr(box, "label", "")).lower()
        if label in ("table", "tableofcontents", "form"):
            b = _bbox(box)
            if b:
                regions.append(b)
    if not regions:
        return [[0.0, 0.0, float(image.width), float(image.height)]]
    return regions


def _bands(items: List[Tuple[int, List[float]]], axis: int) -> List[Tuple[int, float, float]]:
    """Приводит строки/колонки к полосам (индекс, начало, конец), 0-based по
    геометрии, а не по id из модели: id иногда идут с пропусками."""
    out = []
    for _, b in items:
        lo, hi = (b[1], b[3]) if axis == 1 else (b[0], b[2])
        out.append((lo, hi))
    out.sort()
    return [(i, lo, hi) for i, (lo, hi) in enumerate(out)]


def _pick(center: float, bands: List[Tuple[int, float, float]]) -> int:
    """Полоса, в которую попадает центр слова. Если ни в одну — ближайшая."""
    for idx, lo, hi in bands:
        if lo <= center <= hi:
            return idx
    best, dist = -1, None
    for idx, lo, hi in bands:
        d = min(abs(center - lo), abs(center - hi))
        if dist is None or d < dist:
            best, dist = idx, d
    return best


def _tables(image: Image.Image, words: List[Tuple[str, List[float]]]) -> List[Dict[str, Any]]:
    p = predictors()
    tab = p.get("table")
    if tab is None or not words:
        return []

    regions = _table_regions(image)
    crops, offsets = [], []
    for r in regions:
        x0, y0, x1, y1 = [int(round(v)) for v in r]
        x0, y0 = max(x0, 0), max(y0, 0)
        x1, y1 = min(x1, image.width), min(y1, image.height)
        if x1 - x0 < 40 or y1 - y0 < 40:
            continue
        crops.append(image.crop((x0, y0, x1, y1)))
        offsets.append((x0, y0))
    if not crops:
        return []

    try:
        results = tab(crops)
    except Exception as exc:
        log.warning("table_rec не отработал: %s", exc)
        return []

    tables: List[Dict[str, Any]] = []
    for (ox, oy), res in zip(offsets, results):
        rows_raw, cols_raw = [], []
        for r in getattr(res, "rows", None) or []:
            b = _bbox(r)
            if b:
                rows_raw.append((int(getattr(r, "row_id", 0) or 0),
                                 [b[0] + ox, b[1] + oy, b[2] + ox, b[3] + oy]))
        for c in getattr(res, "cols", None) or []:
            b = _bbox(c)
            if b:
                cols_raw.append((int(getattr(c, "col_id", 0) or 0),
                                 [b[0] + ox, b[1] + oy, b[2] + ox, b[3] + oy]))
        if len(rows_raw) < 2 or len(cols_raw) < 2:
            continue

        row_bands = _bands(rows_raw, axis=1)
        col_bands = _bands(cols_raw, axis=0)

        # Шапка: строки, помеченные моделью, иначе самая верхняя.
        header_rows = set()
        for i, (rid, b) in enumerate(sorted(rows_raw, key=lambda x: x[1][1])):
            src = None
            for r in getattr(res, "rows", None) or []:
                if int(getattr(r, "row_id", -1) or -1) == rid:
                    src = r
                    break
            if src is not None and bool(getattr(src, "is_header", False)):
                header_rows.add(i)
        if not header_rows:
            header_rows.add(0)

        # Границы таблицы — по крайним полосам, чтобы не тащить в неё слова
        # из-под документа.
        top = min(lo for _, lo, _ in row_bands)
        bot = max(hi for _, _, hi in row_bands)
        left = min(lo for _, lo, _ in col_bands)
        right = max(hi for _, _, hi in col_bands)

        buckets: Dict[Tuple[int, int], List[Tuple[str, List[float]]]] = {}
        for t, b in words:
            cx, cy = (b[0] + b[2]) / 2.0, (b[1] + b[3]) / 2.0
            if cy < top - 2 or cy > bot + 2 or cx < left - 2 or cx > right + 2:
                continue
            ri = _pick(cy, row_bands)
            ci = _pick(cx, col_bands)
            if ri < 0 or ci < 0:
                continue
            buckets.setdefault((ri, ci), []).append((t, b))

        cells = []
        for (ri, ci), items in sorted(buckets.items()):
            # Внутри ячейки слова читаются как обычно: сверху вниз, слева
            # направо. Перенос строки внутри ячейки становится пробелом.
            items.sort(key=lambda w: (round(w[1][1] / 6.0), w[1][0]))
            text = " ".join(t for t, _ in items).strip()
            if not text:
                continue
            cells.append({
                "row": ri,
                "col": ci,
                "rowspan": 1,
                "colspan": 1,
                "header": ri in header_rows,
                "text": text,
            })
        if len(cells) >= 4:
            tables.append({"cells": cells})
    return tables


def _markdown(table: Dict[str, Any]) -> str:
    cells = table.get("cells") or []
    if not cells:
        return ""
    width = max(c["col"] for c in cells) + 1
    height = max(c["row"] for c in cells) + 1
    grid = [["" for _ in range(width)] for _ in range(height)]
    for c in cells:
        grid[c["row"]][c["col"]] = c["text"].replace("|", "\\|")
    out = ["| " + " | ".join(v or " " for v in grid[0]) + " |",
           "|" + "|".join(" --- " for _ in range(width)) + "|"]
    for row in grid[1:]:
        out.append("| " + " | ".join(v or " " for v in row) + " |")
    return "\n".join(out)


# --- HTTP --------------------------------------------------------------------

@app.get("/health")
def health() -> Dict[str, Any]:
    return {"status": "ok", "table_rec": TABLE_REC}


@app.post("/ocr")
async def ocr(file: UploadFile = File(...)) -> JSONResponse:
    try:
        raw = await file.read()
        image = Image.open(io.BytesIO(raw))
        image.load()
        if image.mode != "RGB":
            image = image.convert("RGB")

        result = _recognize(image)
        words = _words(result)
        layout_text, flat_text = _layout(words)

        tables: List[Dict[str, Any]] = []
        try:
            tables = _tables(image, words)
        except Exception as exc:
            log.warning("таблицы не собраны: %s", exc)

        log.info("страница: слов=%d строк=%d таблиц=%d",
                 len(words), len(flat_text.splitlines()), len(tables))

        return JSONResponse({
            "text": layout_text,
            "flat": flat_text,
            "lines": len(flat_text.splitlines()),
            "boxes": [{"text": t, "bbox": b} for t, b in words],
            "tables": tables,
            "markdown": "\n\n".join(_markdown(t) for t in tables),
            "error": "",
        })
    except Exception as exc:  # pragma: no cover
        log.exception("ошибка распознавания")
        return JSONResponse(status_code=500, content={"error": str(exc)})
DF_APP_EOF
  ok "surya-ocr/app.py"
fi

if [ ! -f surya-ocr/requirements.txt ]; then
  cat > surya-ocr/requirements.txt <<'DF_REQ_EOF'
surya-ocr>=0.14.0
fastapi>=0.110
uvicorn[standard]>=0.29
python-multipart>=0.0.9
pillow>=10.0
numpy>=1.26
DF_REQ_EOF
  ok "surya-ocr/requirements.txt (создан)"
fi

if [ ! -f surya-ocr/Dockerfile ]; then
  cat > surya-ocr/Dockerfile <<'DF_DOCKER_EOF'
FROM python:3.11-slim
ENV PYTHONUNBUFFERED=1 TORCH_DEVICE=cpu
RUN apt-get update && apt-get install -y --no-install-recommends \
      libgl1 libglib2.0-0 && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY app.py .
ARG WARMUP=1
RUN if [ "$WARMUP" = "1" ]; then \
      python -c "from surya.detection import DetectionPredictor; from surya.recognition import RecognitionPredictor; from surya.table_rec import TableRecPredictor; from surya.layout import LayoutPredictor; DetectionPredictor(); RecognitionPredictor(); TableRecPredictor(); LayoutPredictor()"; \
    fi
EXPOSE 8422
CMD ["uvicorn", "app:app", "--host", "0.0.0.0", "--port", "8422", "--workers", "1"]
DF_DOCKER_EOF
  ok "surya-ocr/Dockerfile (создан)"
fi

# ---------------------------------------------------------------------------
say "4/5  compose и .env: два слота у llama.cpp, ключи движка"
# ---------------------------------------------------------------------------
backup docker-compose.yml
python3 - <<'DF_COMPOSE_EOF'
p = "docker-compose.yml"
s = open(p, encoding="utf-8").read()
orig = s

if "-np ${LLM_PARALLEL" not in s:
    old = "      -t ${LLM_THREADS:-4}"
    new = "      -t ${LLM_THREADS:-4}\n      -np ${LLM_PARALLEL:-2}"
    if old in s:
        s = s.replace(old, new)
        print("    llm: добавлен -np (параллельные слоты)")
    else:
        print("    !! не нашёл -t ${LLM_THREADS} в команде llm — добавь -np вручную")
else:
    print("    llm: -np уже есть")

if "SURYA_TABLE_REC" not in s:
    old = "      OCR_THREADS: ${SURYA_THREADS:-4}"
    new = "      OCR_THREADS: ${SURYA_THREADS:-4}\n      SURYA_TABLE_REC: ${SURYA_TABLE_REC:-1}"
    if old in s:
        s = s.replace(old, new)
        print("    surya-ocr: добавлен SURYA_TABLE_REC")
    else:
        print("    !! не нашёл OCR_THREADS у surya-ocr — добавь SURYA_TABLE_REC вручную")
else:
    print("    surya-ocr: SURYA_TABLE_REC уже есть")

if s != orig:
    open(p, "w", encoding="utf-8").write(s)
DF_COMPOSE_EOF

backup .env
python3 - <<'DF_ENV_EOF'
import re, os

p = ".env"
if not os.path.exists(p):
    print("    .env не найден — пропускаю"); raise SystemExit(0)
s = open(p, encoding="utf-8").read()
orig = s

def setkey(text, key, value, comment):
    global changed
    if re.search(r"(?m)^%s=" % re.escape(key), text):
        return re.sub(r"(?m)^%s=.*$" % re.escape(key), "%s=%s" % (key, value), text)
    return text.rstrip("\n") + "\n\n# %s\n%s=%s\n" % (comment, key, value)

s = setkey(s, "LLM_PARALLEL", "2", "Слотов у llama.cpp: реквизиты и таблица считаются параллельно.")
s = setkey(s, "LLM_CTX", "8192", "Контекст делится между слотами: 8192 / 2 слота = 4096 на запрос.")
s = setkey(s, "TABLE_PRIORITY", "grid", "grid — таблица от surya table_rec главнее проекции по Y; columns — как было.")
s = setkey(s, "SURYA_TABLE_REC", "1", "Table Recognition в surya-ocr.")

if s != orig:
    open(p, "w", encoding="utf-8").write(s)
    print("    .env: LLM_PARALLEL=2, LLM_CTX=8192, TABLE_PRIORITY=grid, SURYA_TABLE_REC=1")
else:
    print("    .env: уже настроен")
DF_ENV_EOF

# ---------------------------------------------------------------------------
say "5/5  Сборка"
# ---------------------------------------------------------------------------
if command -v go >/dev/null 2>&1; then
  ( cd docflow && go build ./... ) && ok "go build прошёл"
else
  ok "go локально нет — соберётся в докере"
fi

cat <<'DF_NEXT_EOF'

Дальше руками:

  # 1. пересобрать бэкенд и сервис OCR
  docker compose build docflow-backend surya-ocr

  # 2. поднять (профиль ai поднимает llm, ai-surya — surya-ocr)
  COMPOSE_PROFILES=ai,ai-surya docker compose up -d

  # 3. проверить, что surya отдаёт таблицы
  docker compose logs -f docflow-backend | grep -E "grid diag|контекст для модели|раздельный разбор"

Что искать в логах после загрузки счёта:
  grid diag: получено от surya  tables=1 cells=NN     <- table_rec работает
  grid diag: таблица собрана    columns=9 rows=5
  контекст для модели  шапка_симв=… таблица_симв=…    <- документ разрезан
  раздельный разбор моделью  реквизиты=true таблица=true

Если tables=0 — layout не нашёл рамку таблицы; проверь логи surya-ocr:
  docker compose logs surya-ocr | tail -50

Откат: рядом с каждым изменённым файлом лежит *.bak.<метка>, новые файлы
markdown.go и refine_split.go можно просто удалить.
DF_NEXT_EOF
