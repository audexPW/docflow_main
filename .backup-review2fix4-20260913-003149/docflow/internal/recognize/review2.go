package recognize

import (
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"docflow/internal/domain"
	"docflow/internal/parties"
)

// Пункты 2 и 4–8 разбора от 10.09.2026: закрытый список типов, стороны по
// известному УНП компании, отклонение заведомо неверных значений, справочник
// контрагентов, сумма без НДС и правило, по которому документ проходит мимо
// главбуха.

// ---------------------------------------------------------------------------
// Контекст обработки: чья это компания и что известно о контрагентах.
// ---------------------------------------------------------------------------

// CompanyHint — компания, под которой загружен документ.
type CompanyHint struct {
	Name string
	UNP  string
}

// CounterpartyDirectory — справочник контрагентов компании. Ключ — УНП,
// наименование лишь помогает найти запись, когда номера на листе нет.
type CounterpartyDirectory interface {
	ByUNP(unp string) (name string, ok bool)
	ByName(name string) (unp, canonical string, ok bool)
}

// ProcessHint — всё, что обработка знает о документе помимо самого файла.
type ProcessHint struct {
	Company   *CompanyHint
	Directory CounterpartyDirectory
}

const (
	CheckCompanySide      = "company_side"
	CheckCounterpartyKnow = "counterparty_known"
)

func setCheck(rec *domain.Recognition, key string, v bool) {
	if rec.Checks == nil {
		rec.Checks = map[string]bool{}
	}
	rec.Checks[key] = v
}

func setNote(rec *domain.Recognition, key, note string) {
	if rec.ReviewNotes == nil {
		rec.ReviewNotes = map[string]string{}
	}
	if old, ok := rec.ReviewNotes[key]; ok && old != "" && !strings.Contains(old, note) {
		note = old + "; " + note
	}
	rec.ReviewNotes[key] = note
}

// ---------------------------------------------------------------------------
// Пункт 4. Тип документа — только из реестра, уверенность считается.
// ---------------------------------------------------------------------------

// NormalizeModelDocType — тип от модели приводится к slug реестра, всё прочее
// становится unknown. Раньше промпт разрешал «назвать тип своими словами»,
// система принимала «Акт использования имущества» как код типа, и под этот
// код не подходило ни одно правило: документ обнулялся целиком.
func NormalizeModelDocType(raw string) string {
	key := normalizeKey(raw)
	if key == "" {
		return DocTypeUnknown
	}
	c := current()
	if slug, ok := c.byAlias[key]; ok {
		return slug
	}
	if _, ok := c.bySlug[key]; ok {
		return key
	}
	return DocTypeUnknown
}

// typeEvidenceInText — в шапке документа напечатано название типа.
func typeEvidenceInText(docType, text string) bool {
	spec, ok := LookupDocType(docType)
	if !ok {
		return false
	}
	low := strings.ToLower(strings.ReplaceAll(text, "ё", "е"))
	if len(low) > 1200 {
		low = low[:1200]
	}
	for _, a := range append(append([]string{spec.Title}, spec.Anchors...), spec.Synonyms...) {
		a = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(a), "ё", "е"))
		// Однословные синонимы («счет», «акт») слишком общие для
		// свидетельства.
		if len([]rune(a)) < 6 {
			continue
		}
		if strings.Contains(low, a) {
			return true
		}
	}
	return false
}

// ResolveDocType сводит тип правил и тип модели и считает уверенность.
//
// Константа 0.85 на всех восемнадцати прогонах была самым решающим и при
// этом неизмеренным значением. Теперь уверенность складывается из того, что
// можно проверить: согласны ли правила и модель и напечатано ли название типа
// в начале документа.
func ResolveDocType(rulesType string, rulesConf float64, modelRaw, text string) (string, float64) {
	modelType := NormalizeModelDocType(modelRaw)
	rulesKnown := rulesType != "" && rulesType != DocTypeUnknown && KnownDocType(rulesType)
	modelKnown := modelType != DocTypeUnknown
	round := func(v float64) float64 { return math.Round(math.Min(v, 0.98)*100) / 100 }
	switch {
	case !rulesKnown && !modelKnown:
		return DocTypeUnknown, 0
	case rulesKnown && !modelKnown:
		if rulesConf >= 0.5 && typeEvidenceInText(rulesType, text) {
			return rulesType, round(math.Max(0.8, rulesConf*0.9))
		}
		return rulesType, round(rulesConf * 0.9)
	case !rulesKnown && modelKnown:
		if typeEvidenceInText(modelType, text) {
			return modelType, 0.7
		}
		return modelType, 0.5
	case rulesType == modelType:
		return modelType, round(0.75 + 0.25*rulesConf)
	}
	// Правила и модель разошлись.
	if typeEvidenceInText(modelType, text) && !typeEvidenceInText(rulesType, text) {
		return modelType, 0.55
	}
	return rulesType, round(rulesConf * 0.7)
}

// ---------------------------------------------------------------------------
// Пункты 5 и 7. Стороны по УНП компании, контрагент по справочнику.
// ---------------------------------------------------------------------------

var reNineDigits = regexp.MustCompile(`(?:^|\D)(\d{9})(?:\D|$)`)

// taxIDsIn — все девятизначные номера текста в порядке появления, без повторов.
func taxIDsIn(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range reNineDigits.FindAllStringSubmatch(text, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	// Номер, напечатанный с пробелами («192 226 948»), тоже номер.
	compact := regexp.MustCompile(`(\d{3}) (\d{3}) (\d{3})`)
	for _, m := range compact.FindAllStringSubmatch(text, -1) {
		v := m[1] + m[2] + m[3]
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

var reLabelledTaxID = regexp.MustCompile(`(?i)(?:унп|унн)[^0-9\n]{0,12}(\d{9})(?:\D|$)`)

// labelledTaxIDs — номера, перед которыми стоит подпись «УНП».
func labelledTaxIDs(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range reLabelledTaxID.FindAllStringSubmatch(text, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

func containsTaxID(text, id string) bool {
	for _, v := range taxIDsIn(text) {
		if v == id {
			return true
		}
	}
	return false
}

func orgTaxKey() string { return "organization_" + PrimaryTaxKey() }

// ResolveParties определяет стороны не угадыванием, а по известному УНП
// компании, под которой загружен документ.
//
// По прошлому промпту organization — получатель (компания заказчика), а
// counterparty — тот, кто выставил документ. На восемнадцати прогонах в девяти
// компания уехала в контрагенты; на счёте рынка номера сторон перекрестились,
// и обе с уверенностью 80%. Номер компании известен заранее: нашёлся на листе
// — сторона определена.
func ResolveParties(rec *domain.Recognition, text string, hint ProcessHint) {
	if rec == nil {
		return
	}
	if rec.Fields == nil {
		rec.Fields = map[string]domain.Field{}
	}
	f := rec.Fields
	taxKey, orgKey := PrimaryTaxKey(), orgTaxKey()
	get := func(k string) string { return strings.TrimSpace(f[k].Value) }

	// В поле контрагента склеены два юрлица — оставляем то, что не компания.
	if cp := get("counterparty"); parties.CountForms(cp) >= 2 {
		parts := parties.SplitEntities(cp)
		pick := ""
		for _, part := range parts {
			if hint.Company != nil && parties.Same(part, hint.Company.Name) {
				continue
			}
			pick = part
			break
		}
		if pick != "" && pick != cp {
			old := f["counterparty"]
			old.Value = pick
			f["counterparty"] = old
			setNote(rec, "counterparty", "в поле были склеены два юрлица: «"+cp+"»")
		}
	}

	if c := hint.Company; c != nil && parties.ValidUNP(c.UNP) {
		cp, org := get("counterparty"), get("organization")
		cpIsCompany := cp != "" && parties.Same(cp, c.Name)
		orgIsCompany := org != "" && parties.Same(org, c.Name)
		inText := containsTaxID(text, c.UNP)

		swapped := false
		if cpIsCompany && !orgIsCompany {
			f["counterparty"], f["organization"] = f["organization"], f["counterparty"]
			swapped = true
		}
		if get(taxKey) == c.UNP {
			f[taxKey], f[orgKey] = f[orgKey], f[taxKey]
			swapped = true
		}
		if swapped {
			setNote(rec, "counterparty", "стороны были перепутаны и переставлены по УНП компании")
		}

		if inText || cpIsCompany || orgIsCompany {
			conf := 0.98
			if !inText {
				conf = 0.9
			}
			f["organization"] = domain.Field{Value: c.Name, Confidence: 0.98, Source: "company"}
			f[orgKey] = domain.Field{Value: c.UNP, Confidence: conf, Source: "company"}
			setCheck(rec, CheckCompanySide, true)
		} else {
			setCheck(rec, CheckCompanySide, false)
		}

		// УНП контрагента: не номер компании и есть на листе. Иначе берём
		// единственный другой девятизначный номер документа.
		cur := get(taxKey)
		if cur == c.UNP || (cur != "" && !containsTaxID(text, cur)) {
			delete(f, taxKey)
			cur = ""
		}
		if cur == "" {
			// Единственный другой номер берём, только если он подписан «УНП»
			// и в документе вообще всего две стороны с номерами. Номер без
			// подписи может оказаться чем угодно — кодом банка, ОКПО, чужим
			// филиалом; такое поле останется пустым и уйдёт человеку.
			var cands []string
			for _, id := range labelledTaxIDs(text) {
				if id != c.UNP {
					cands = append(cands, id)
				}
			}
			if len(cands) == 1 && inText {
				f[taxKey] = domain.Field{Value: cands[0], Confidence: 0.7, Source: "rule"}
				setNote(rec, taxKey, "УНП контрагента взят как единственный другой подписанный номер на листе")
			}
		}
		if cp := get("counterparty"); cp != "" && parties.Same(cp, c.Name) {
			delete(f, "counterparty")
			setNote(rec, "counterparty", "в контрагенте стояла ваша компания")
		}
	}

	// Справочник: по УНП — каноническое наименование, по наименованию — УНП
	// (акты аренды, где номер стоит только на оттиске печати).
	setCheck(rec, CheckCounterpartyKnow, false)
	if hint.Directory == nil {
		return
	}
	if unp := get(taxKey); parties.ValidUNP(unp) {
		if name, ok := hint.Directory.ByUNP(unp); ok {
			printed := get("counterparty")
			f["counterparty"] = domain.Field{Value: name, Confidence: 0.98, Source: "directory"}
			f[taxKey] = domain.Field{Value: unp, Confidence: 0.98, Source: "directory"}
			setCheck(rec, CheckCounterpartyKnow, true)
			if printed != "" && !parties.Same(printed, name) {
				setNote(rec, "counterparty", "на листе «"+printed+"», в справочнике по этому УНП — «"+name+"»")
				setCheck(rec, CheckCounterpartyKnow, false)
			}
		}
		return
	}
	if cp := get("counterparty"); cp != "" {
		if unp, name, ok := hint.Directory.ByName(cp); ok {
			f["counterparty"] = domain.Field{Value: name, Confidence: 0.95, Source: "directory"}
			f[taxKey] = domain.Field{Value: unp, Confidence: 0.95, Source: "directory"}
			setCheck(rec, CheckCounterpartyKnow, true)
		}
	}
}

// ---------------------------------------------------------------------------
// Пункт 6. Проверка формата отклоняет, а не понижает.
// ---------------------------------------------------------------------------

func rejectReason(key string) string {
	switch key {
	case "unp", "organization_unp":
		return "УНП должен состоять из 9 цифр"
	case "inn", "organization_inn":
		return "ИНН должен состоять из 10 или 12 цифр"
	case "kpp":
		return "КПП должен состоять из 9 цифр"
	case "date", "contract_date":
		return "невозможная дата"
	case "currency":
		return "неизвестная валюта"
	case "total", "amount_no_vat", "vat_amount":
		return "сумма не является числом"
	case "number":
		return "похоже на банковский счёт, а не номер документа"
	}
	return "неверный формат"
}

// EnforceFormats очищает поля с заведомо невозможным значением.
//
// На счёте, где в тексте ясно написано 591018968, модель вернула 1018968.
// Постобработка это заметила и понизила уверенность, но значение ушло в поле
// и дальше в выгрузку. Теперь такое поле пустое, документ идёт человеку, а
// исходное значение и причина лежат в Rejected. Обрезанный номер, который
// целиком находится в тексте ровно один раз, восстанавливаем.
func EnforceFormats(rec *domain.Recognition, text string) {
	if rec == nil {
		return
	}
	keys := make([]string, 0, len(rec.Fields))
	for k := range rec.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fld := rec.Fields[k]
		v := strings.TrimSpace(fld.Value)
		if v == "" || fld.Source == "manual" || fld.Source == "company" || fld.Source == "directory" {
			continue
		}
		if formatValid(k, v) != -1 {
			continue
		}
		if k == "unp" || k == "organization_unp" {
			d := digitsOf(v)
			if len(d) >= 6 {
				var hits []string
				for _, id := range taxIDsIn(text) {
					if strings.Contains(id, d) {
						hits = append(hits, id)
					}
				}
				if len(hits) == 1 {
					rec.Fields[k] = domain.Field{Value: hits[0], Confidence: 0.75, Source: "rule"}
					setNote(rec, k, "номер «"+v+"» был обрезан, восстановлен по тексту")
					continue
				}
			}
		}
		if rec.Rejected == nil {
			rec.Rejected = map[string]string{}
		}
		rec.Rejected[k] = v + " — " + rejectReason(k)
		delete(rec.Fields, k)
	}
}

// CheckAccounts — дебет и кредит заполнены и не совпадают. Проводка «60 на 60»
// в учёте невозможна и должна ловиться до выгрузки, а не в 1С.
func CheckAccounts(rec *domain.Recognition) {
	if rec == nil {
		return
	}
	d, dok := rec.Fields[FieldAccountDebit]
	c, cok := rec.Fields[FieldAccountCredit]
	if !dok || !cok || strings.TrimSpace(d.Value) == "" || strings.TrimSpace(d.Value) != strings.TrimSpace(c.Value) {
		return
	}
	// Счёт расчётов на дебете входящего документа — ошибка дебета.
	drop := FieldAccountCredit
	for _, p := range []string{"60", "62", "76"} {
		if strings.HasPrefix(d.Value, p) {
			drop = FieldAccountDebit
		}
	}
	if drop == FieldAccountDebit && d.Source == "manual" {
		drop = FieldAccountCredit
	}
	if rec.Rejected == nil {
		rec.Rejected = map[string]string{}
	}
	rec.Rejected[drop] = rec.Fields[drop].Value + " — дебет и кредит совпадали, проводка сама на себя"
	delete(rec.Fields, drop)
}

// ---------------------------------------------------------------------------
// Пункт 8. Сумма без налога.
// ---------------------------------------------------------------------------

// «Арендная плата … составляет 28,25 (…) в месяц, в том числе НДС (20%) в
// размере 4,71». Между суммой и налогом стоит сумма прописью с цифрами копеек,
// поэтому промежуток ограничен длиной, а не отсутствием цифр.
var reAmountWithVAT = regexp.MustCompile(`(?is)(?:составля\S*|в\s+размере|стоимост\S*|сумм\S*|итого|всего)\s*[:\-]?\s*(\d[\d \x{00A0}]*[.,]\d{2})\b.{0,160}?в\s*(?:т\.?\s*ч\.?|том\s+числе)\s*ндс\s*(?:\(?\s*\d{1,2}\s*%\s*\)?)?[^0-9]{0,40}?(\d[\d \x{00A0}]*[.,]\d{2})\b`)

func moneyVal(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.ReplaceAll(normalizeMoney(s), " ", ""), 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func fmtMoney(v float64) string { return strconv.FormatFloat(math.Round(v*100)/100, 'f', 2, 64) }

// FillAmounts добирает сумму без НДС (а заодно итог и налог, если их нет):
// из итога таблицы, из текста рядом со словами «в том числе НДС», и сверяет
// три суммы между собой. На всех восемнадцати прогонах поле было пустым при
// том, что значение лежало либо в таблице, либо прямо в предложении.
func FillAmounts(rec *domain.Recognition, text string) {
	if rec == nil {
		return
	}
	if rec.Fields == nil {
		rec.Fields = map[string]domain.Field{}
	}
	f := rec.Fields
	has := func(k string) bool {
		_, ok := moneyVal(f[k].Value)
		return ok && strings.TrimSpace(f[k].Value) != ""
	}
	set := func(k string, v float64, src string) {
		f[k] = domain.Field{Value: fmtMoney(v), Confidence: ruleConfidence, Source: src}
	}

	// 1. Итог таблицы: графа «без НДС» и графа «Сумма НДС».
	if rec.Table != nil && rec.Table.RawCells != nil {
		ft := rec.Table.RawCells
		if len(ft.Totals) > 0 {
			for i, col := range ft.Columns {
				if i >= len(ft.Totals) {
					break
				}
				v, ok := cellNum(ft.Totals[i])
				if !ok || v <= 0 {
					continue
				}
				t := strings.ToLower(col)
				switch {
				case titleWithoutVAT(t) && !titleWithVAT(t) && !has("amount_no_vat"):
					set("amount_no_vat", v, "table")
				case strings.Contains(t, "ндс") && !titleWithVAT(t) && !titleWithoutVAT(t) &&
					!strings.Contains(t, "%") && !strings.Contains(t, "ставк") && !has("vat_amount"):
					set("vat_amount", v, "table")
				}
			}
		}
		if !has("amount_no_vat") && len(rec.Lines) > 0 {
			sum, n := 0.0, 0
			for _, l := range rec.Lines {
				if v, ok := moneyVal(l.AmountNoVAT); ok && l.AmountNoVAT != "" {
					sum += v
					n++
				}
			}
			if n == len(rec.Lines) && sum > 0 {
				set("amount_no_vat", sum, "table")
			}
		}
	}

	// 2. Текст: «составляет X … в том числе НДС … Y».
	if m := reAmountWithVAT.FindStringSubmatch(text); m != nil {
		tot, ok1 := moneyVal(m[1])
		vat, ok2 := moneyVal(m[2])
		if ok1 && ok2 && vat > 0 && vat < tot {
			// Итог из предложения с «в том числе НДС» перекрывает слабую
			// догадку: на актах аренды правило брало итогом «15.06» из даты
			// договора «от 15.06.2023».
			cur := f["total"]
			if !has("total") || (cur.Source != "manual" && cur.Value != fmtMoney(tot) && cur.Confidence < 0.5) {
				set("total", tot, "rule")
				if v := f["vat_amount"]; v.Source != "manual" {
					delete(f, "vat_amount")
				}
				if v := f["amount_no_vat"]; v.Source != "manual" && v.Source != "" {
					delete(f, "amount_no_vat")
				}
			}
			if !has("vat_amount") {
				set("vat_amount", vat, "rule")
			}
		}
	}

	// 3. Третья сумма из двух известных.
	tot, okT := moneyVal(f["total"].Value)
	vat, okV := moneyVal(f["vat_amount"].Value)
	no, okN := moneyVal(f["amount_no_vat"].Value)
	okT, okV, okN = okT && has("total"), okV && has("vat_amount"), okN && has("amount_no_vat")
	switch {
	case okT && okV && !okN && tot > vat:
		f["amount_no_vat"] = domain.Field{Value: fmtMoney(tot - vat), Confidence: ruleConfidence, Source: "derived"}
	case okN && okV && !okT:
		f["total"] = domain.Field{Value: fmtMoney(no + vat), Confidence: ruleConfidence, Source: "derived"}
	case okT && okN && !okV && tot >= no:
		f["vat_amount"] = domain.Field{Value: fmtMoney(tot - no), Confidence: ruleConfidence, Source: "derived"}
	case okT && okV && okN && math.Abs(no+vat-tot) > amountTolerance:
		setNote(rec, "total", fmt.Sprintf("без НДС %s + НДС %s ≠ итог %s", fmtMoney(no), fmtMoney(vat), fmtMoney(tot)))
	}
}

// finalizeDerived — у вычисленной суммы уверенность не выше, чем у слагаемых.
// Иначе арифметика подтверждала бы сама себя.
func finalizeDerived(rec *domain.Recognition) {
	for k, fld := range rec.Fields {
		if fld.Source != "derived" {
			continue
		}
		var src []string
		switch k {
		case "amount_no_vat", "vat_amount":
			src = []string{"total", "vat_amount", "amount_no_vat"}
		case "total":
			src = []string{"amount_no_vat", "vat_amount"}
		}
		conf := 0.98
		for _, s := range src {
			if s == k {
				continue
			}
			if o, ok := rec.Fields[s]; ok && o.Value != "" {
				conf = math.Min(conf, o.Confidence)
			}
		}
		fld.Confidence = math.Round((conf-0.02)*100) / 100
		rec.Fields[k] = fld
	}
}

// ---------------------------------------------------------------------------
// Пункт 2. Маршрутизация: документ идёт мимо главбуха, когда всё сошлось.
// ---------------------------------------------------------------------------

func envFloat(key string, def float64) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv(key)), 64); err == nil && v > 0 {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

// AutoPassEnabled — включено ли правило прохода мимо главбуха (AUTO_PASS_ENABLED).
func AutoPassEnabled() bool { return envBool("AUTO_PASS_ENABLED", true) }

// DecideAutoPass проверяет документ по правилу из разбора: уверенность
// обязательных полей выше порога, арифметика сошлась, контрагент найден по
// налоговому номеру. Сошлось — AutoPass. Не сошлось — Review перечисляет
// ровно те поля, что не прошли, с причиной у каждого.
func DecideAutoPass(rec *domain.Recognition, hint ProcessHint) {
	if rec == nil {
		return
	}
	minConf := envFloat("AUTO_PASS_MIN_CONFIDENCE", 0.75)
	minType := envFloat("AUTO_PASS_MIN_TYPE_CONFIDENCE", 0.75)
	needKnown := envBool("AUTO_PASS_REQUIRE_KNOWN_COUNTERPARTY", true)

	failed := map[string]bool{}
	fail := func(k, note string) {
		failed[k] = true
		setNote(rec, k, note)
	}

	switch {
	case rec.DocType == "" || rec.DocType == DocTypeUnknown:
		fail("doc_type", "тип документа не определён")
	case !ExportRoutable(rec.DocType):
		fail("doc_type", "для типа не задан объект-приёмник в 1С")
	case rec.DocTypeConfidence < minType:
		fail("doc_type", fmt.Sprintf("тип определён неуверенно: %.0f%% при пороге %.0f%%", rec.DocTypeConfidence*100, minType*100))
	}

	for _, k := range RequiredFields(rec.DocType) {
		fld, ok := rec.Fields[k]
		switch {
		case rec.Rejected[k] != "":
			fail(k, "значение отклонено: "+rec.Rejected[k])
		case !ok || strings.TrimSpace(fld.Value) == "":
			fail(k, "не распознано")
		case fld.Source == "manual":
		case fld.Confidence < minConf:
			fail(k, fmt.Sprintf("уверенность %.0f%% ниже порога %.0f%%", fld.Confidence*100, minConf*100))
		}
	}
	for k, v := range rec.Rejected {
		if !failed[k] {
			fail(k, "значение отклонено: "+v)
		}
	}

	arith := arithmeticAgrees(rec.Fields)
	lines := linesSumAgrees(*rec)
	if arith < 0 {
		for _, k := range []string{"amount_no_vat", "vat_amount", "total"} {
			fail(k, "без НДС + НДС не равно итогу")
		}
	}
	if lines < 0 {
		fail("lines", "сумма позиций не сходится с итогом")
	}
	if _, ok := rec.Fields["total"]; ok && arith <= 0 && lines <= 0 {
		fail("total", "итог не подтверждён ни суммами, ни позициями")
	}

	if hint.Company != nil && parties.ValidUNP(hint.Company.UNP) && !rec.Checks[CheckCompanySide] {
		fail("organization", "УНП вашей компании на листе не найден — стороны не подтверждены")
	}
	if needKnown && !rec.Checks[CheckCounterpartyKnow] {
		fail("counterparty", "контрагента нет в справочнике по УНП — первый документ от него проверяет человек")
	}

	rec.Review = rec.Review[:0]
	for k := range failed {
		rec.Review = append(rec.Review, k)
	}
	sort.Strings(rec.Review)
	rec.AutoPass = len(rec.Review) == 0
	if rec.AutoPass {
		rec.Review = nil
	}
	// Заметки к полям, которые проверку прошли, оставляем: они объясняют
	// исправления (переставленные стороны, восстановленный номер).
}
