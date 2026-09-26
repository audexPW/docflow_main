package recognize

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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
	// minHeadRunes — короче этого шапка считается обрезанной: на реальных
	// бланках блок реквизитов сторон один занимает больше.
	minHeadRunes = 300

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
	if !ok || headLostParties(head) {
		// Координат слов нет (tesseract без раскладки), полосу таблицы найти
		// не удалось, либо в шапку не попал блок реквизитов сторон — отдаём
		// модели весь текст как шапку. Хуже, чем раздельно, но не хуже
		// прежнего единого промпта, и несравнимо лучше, чем отдать модели
		// обрывок «Основание … Продавец:» и ждать от неё контрагента.
		head = flatText
		foot = ""
	}
	dc.Head = clip(strings.TrimSpace(head), maxHeadChars)
	dc.Foot = clip(strings.TrimSpace(foot), maxFootChars)
	return dc
}

// headLostParties — в шапке не осталось реквизитов сторон.
//
// findTableBand ищет табличную полосу по многоколоночности строк, а блок
// «Продавец / Покупатель» на бланках как раз напечатан в две колонки. На
// счетах-фактурах ЖКХ и актах он целиком попадал внутрь полосы: в шапке
// оставалось «Основание … Продавец:», а то и вовсе пусто, и модели неоткуда
// было взять контрагента, организацию и УНП. Такую шапку доверять нельзя.
func headLostParties(head string) bool {
	h := strings.ToLower(strings.TrimSpace(head))
	if utf8.RuneCountInString(h) < minHeadRunes {
		return true
	}
	hasTax := strings.Contains(h, "унп") || strings.Contains(h, "унн") ||
		strings.Contains(h, "инн")
	hasBuyer := strings.Contains(h, "покупател") || strings.Contains(h, "плательщик") ||
		strings.Contains(h, "заказчик") || strings.Contains(h, "арендатор") ||
		strings.Contains(h, "грузополучател")
	return !hasTax && !hasBuyer
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
		if headRes.Flat {
			p.log.Info("модель ответила плоским JSON без обёртки fields, разобрано как реквизиты")
			tr.Set("ответ_реквизитов_плоский", true)
		}
		tr.Set("полей_от_модели", len(headRes.Fields))
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
