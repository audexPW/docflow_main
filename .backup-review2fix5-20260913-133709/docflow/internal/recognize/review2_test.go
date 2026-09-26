package recognize

import (
	"os"
	"strings"
	"testing"

	"docflow/internal/domain"
)

func withLocaleBY(t *testing.T) {
	t.Helper()
	prev := primaryTaxKey
	SetLocale("by")
	t.Cleanup(func() { primaryTaxKey = prev })
}

func TestModelDocTypeClosedList(t *testing.T) {
	if got := NormalizeModelDocType("Акт использования имущества"); got != "act" {
		t.Fatalf("акт аренды должен лечь в act, получили %q", got)
	}
	if got := NormalizeModelDocType("Справка о чём-то своём"); got != DocTypeUnknown {
		t.Fatalf("выдуманный тип принят как %q", got)
	}
	if got := NormalizeModelDocType("Счёт-фактура"); got != "schet_faktura" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveDocTypeConfidenceIsMeasured(t *testing.T) {
	head := "СЧЕТ-ФАКТУРА № 42 от 31 июля 2023"
	_, agree := ResolveDocType("schet_faktura", 1, "schet_faktura", head)
	_, modelOnlyEvidence := ResolveDocType(DocTypeUnknown, 0, "schet_faktura", head)
	_, modelOnlyBare := ResolveDocType(DocTypeUnknown, 0, "schet_faktura", "без названия")
	typ, none := ResolveDocType(DocTypeUnknown, 0, "Что-то своё", head)
	if !(agree > modelOnlyEvidence && modelOnlyEvidence > modelOnlyBare) {
		t.Fatalf("уверенность не различает свидетельства: %v %v %v", agree, modelOnlyEvidence, modelOnlyBare)
	}
	if typ != DocTypeUnknown || none != 0 {
		t.Fatalf("выдуманный тип не отклонён: %q %v", typ, none)
	}
}

func TestResolvePartiesSwapByCompanyUNP(t *testing.T) {
	withLocaleBY(t)
	text := "Продавец: КУП «Чижовский рынок» УНП 100000017\nПокупатель: ООО «КофеВенд» УНП 192226948"
	rec := domain.Recognition{Fields: map[string]domain.Field{
		// Как на счёте рынка: номера сторон перекрещены.
		"counterparty":     {Value: "ООО КофеВенд", Confidence: 0.8, Source: "model"},
		"unp":              {Value: "192226948", Confidence: 0.8, Source: "model"},
		"organization":     {Value: "КУП Чижовский рынок", Confidence: 0.8, Source: "model"},
		"organization_unp": {Value: "100000017", Confidence: 0.8, Source: "model"},
	}}
	ResolveParties(&rec, text, ProcessHint{Company: &CompanyHint{Name: "ООО «КофеВенд»", UNP: "192226948"}})
	f := rec.Fields
	if f["unp"].Value != "100000017" || f["organization_unp"].Value != "192226948" {
		t.Fatalf("УНП сторон не расставлены: %+v", f)
	}
	if !strings.Contains(f["counterparty"].Value, "Чижовский") || f["organization"].Value != "ООО «КофеВенд»" {
		t.Fatalf("наименования сторон не расставлены: %+v", f)
	}
	if !rec.Checks[CheckCompanySide] {
		t.Fatal("сторона компании не отмечена найденной")
	}
}

type fakeDir map[string]string

func (d fakeDir) ByUNP(unp string) (string, bool) { n, ok := d[unp]; return n, ok }
func (d fakeDir) ByName(name string) (string, string, bool) {
	for u, n := range d {
		if strings.Contains(strings.ToLower(name), "больниц") && strings.Contains(strings.ToLower(n), "больниц") {
			return u, n, true
		}
	}
	return "", "", false
}

func TestResolvePartiesDirectory(t *testing.T) {
	withLocaleBY(t)
	dir := fakeDir{"600000013": "УЗ «Минская центральная районная клиническая больница»"}
	// Акт аренды: УНП на листе нет, берём по наименованию.
	rec := domain.Recognition{Fields: map[string]domain.Field{
		"counterparty": {Value: "Учреждение здравоохранения «Минская центральная районная клиническая больница»", Source: "model"},
		"organization": {Value: "Общество с ограниченной ответственностью «КофеВенд»", Source: "model"},
	}}
	ResolveParties(&rec, "акт без номеров", ProcessHint{
		Company:   &CompanyHint{Name: "ООО «КофеВенд»", UNP: "192226948"},
		Directory: dir,
	})
	if rec.Fields["unp"].Value != "600000013" || !rec.Checks[CheckCounterpartyKnow] {
		t.Fatalf("УНП не взят из справочника: %+v %v", rec.Fields, rec.Checks)
	}
	if !rec.Checks[CheckCompanySide] {
		t.Fatal("сторона компании по наименованию не определена")
	}
}

func TestMergedEntitiesInCounterparty(t *testing.T) {
	rec := domain.Recognition{Fields: map[string]domain.Field{
		"counterparty": {Value: "ООО «КофеВенд» ЗАО «Суперпрод»", Source: "model"},
	}}
	ResolveParties(&rec, "", ProcessHint{Company: &CompanyHint{Name: "ООО КофеВенд", UNP: "192226948"}})
	if got := rec.Fields["counterparty"].Value; !strings.Contains(got, "Суперпрод") || strings.Contains(got, "КофеВенд") {
		t.Fatalf("склейка юрлиц не разобрана: %q", got)
	}
}

func TestEnforceFormatsRejectsAndRecovers(t *testing.T) {
	withLocaleBY(t)
	rec := domain.Recognition{Fields: map[string]domain.Field{
		"unp":  {Value: "1018968", Confidence: 0.55, Source: "model"},
		"date": {Value: "31.08.1023", Confidence: 0.6, Source: "model"},
	}}
	EnforceFormats(&rec, "УНП 591018968 продавца")
	if rec.Fields["unp"].Value != "591018968" {
		t.Fatalf("обрезанный УНП не восстановлен: %+v", rec.Fields["unp"])
	}
	if _, ok := rec.Fields["date"]; ok || rec.Rejected["date"] == "" {
		t.Fatalf("невозможная дата не отклонена: %+v %v", rec.Fields, rec.Rejected)
	}
	rec2 := domain.Recognition{Fields: map[string]domain.Field{"unp": {Value: "1018968", Source: "model"}}}
	EnforceFormats(&rec2, "в тексте номера нет")
	if _, ok := rec2.Fields["unp"]; ok {
		t.Fatal("неверный УНП ушёл бы в выгрузку")
	}
}

func TestCheckAccountsSelfPosting(t *testing.T) {
	rec := domain.Recognition{Fields: map[string]domain.Field{
		FieldAccountDebit:  {Value: "60.1.1", Source: "model"},
		FieldAccountCredit: {Value: "60.1.1", Source: "rule"},
	}}
	CheckAccounts(&rec)
	if _, ok := rec.Fields[FieldAccountDebit]; ok {
		t.Fatal("проводка сама на себя не поймана")
	}
	if rec.Fields[FieldAccountCredit].Value != "60.1.1" {
		t.Fatal("кредит потерян")
	}
}

func TestFillAmountsFromActText(t *testing.T) {
	flat, err := os.ReadFile("testdata/act371-flat.txt")
	if err != nil {
		t.Fatal(err)
	}
	rec := domain.Recognition{Fields: map[string]domain.Field{}}
	FillAmounts(&rec, string(flat))
	if rec.Fields["total"].Value != "30.42" || rec.Fields["vat_amount"].Value != "5.07" || rec.Fields["amount_no_vat"].Value != "25.35" {
		t.Fatalf("суммы из текста акта: %+v", rec.Fields)
	}
}

func TestDecideAutoPass(t *testing.T) {
	withLocaleBY(t)
	dir := t.TempDir() + "/doctypes.json"
	os.WriteFile(dir, []byte(`{"types":[{"slug":"act","required":["number","date","total","@taxid","counterparty"],"onec":{"object":"Документ.ПолучениеУслуг"}}]}`), 0o644)
	if _, err := LoadDocTypes(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		LoadDocTypes("")
		catalogMu.Lock()
		catalog = newCatalog(defaultSpecs, defaultFallbackRequired)
		catalogMu.Unlock()
	})

	good := func() domain.Recognition {
		return domain.Recognition{
			DocType: "act", DocTypeConfidence: 0.9,
			Fields: map[string]domain.Field{
				"number":        {Value: "370", Confidence: 0.9},
				"date":          {Value: "31.08.2023", Confidence: 0.9},
				"total":         {Value: "28.25", Confidence: 0.9},
				"vat_amount":    {Value: "4.71", Confidence: 0.9},
				"amount_no_vat": {Value: "23.54", Confidence: 0.9},
				"unp":           {Value: "600000013", Confidence: 0.98, Source: "directory"},
				"counterparty":  {Value: "УЗ МЦРКБ", Confidence: 0.98, Source: "directory"},
			},
			Checks: map[string]bool{CheckCompanySide: true, CheckCounterpartyKnow: true},
		}
	}
	hint := ProcessHint{Company: &CompanyHint{Name: "ООО КофеВенд", UNP: "192226948"}}

	rec := good()
	DecideAutoPass(&rec, hint)
	if !rec.AutoPass {
		t.Fatalf("сошедшийся документ не прошёл: %v %v", rec.Review, rec.ReviewNotes)
	}

	rec = good()
	f := rec.Fields["total"]
	f.Value = "30.00"
	rec.Fields["total"] = f
	rec.Checks[CheckCounterpartyKnow] = false
	DecideAutoPass(&rec, hint)
	if rec.AutoPass {
		t.Fatal("несошедшаяся арифметика прошла мимо главбуха")
	}
	want := map[string]bool{"total": true, "counterparty": true, "vat_amount": true, "amount_no_vat": true}
	for _, k := range rec.Review {
		delete(want, k)
	}
	if len(want) != 0 {
		t.Fatalf("подсвечены не те поля: %v", rec.Review)
	}
}

func TestSentenceTotalOverridesContractDate(t *testing.T) {
	flat, _ := os.ReadFile("testdata/act371-flat.txt")
	rec := domain.Recognition{Fields: map[string]domain.Field{
		"total": {Value: "16.11", Confidence: 0.3, Source: "rule"},
	}}
	FillAmounts(&rec, string(flat))
	if rec.Fields["total"].Value != "30.42" || rec.Fields["amount_no_vat"].Value != "25.35" {
		t.Fatalf("итог из даты договора не перекрыт: %+v", rec.Fields)
	}
}

func TestUnlabelledNumberNotTakenAsCounterpartyUNP(t *testing.T) {
	withLocaleBY(t)
	text := "Покупатель ООО КофеВенд УНП 590888079\nКод 101452539"
	rec := domain.Recognition{Fields: map[string]domain.Field{"counterparty": {Value: "Рынок", Source: "model"}}}
	ResolveParties(&rec, text, ProcessHint{Company: &CompanyHint{Name: "ООО КофеВенд", UNP: "590888079"}})
	if v := rec.Fields["unp"].Value; v != "" {
		t.Fatalf("неподписанный номер взят УНП контрагента: %s", v)
	}
}

func TestTaxIDsAdjacent(t *testing.T) {
	got := taxIDsIn("УНП 590888079 101452539, р/с BY26AKBB30128000003605100000")
	if len(got) != 2 || got[1] != "101452539" {
		t.Fatalf("второй номер через пробел потерян: %v", got)
	}
}

func TestDirectoryDoesNotOverwriteOtherEntity(t *testing.T) {
	withLocaleBY(t)
	rec := domain.Recognition{Fields: map[string]domain.Field{
		"counterparty": {Value: "ЗАО «Суперпрод»", Source: "model"},
		"unp":          {Value: "101084576", Source: "model"},
	}}
	ResolveParties(&rec, "УНП 101084576", ProcessHint{Directory: fakeDir{"101084576": "ООО «Совсем другое»"}})
	if rec.Fields["counterparty"].Value != "ЗАО «Суперпрод»" || rec.Checks[CheckCounterpartyKnow] {
		t.Fatalf("наименование с листа затёрто справочником: %+v", rec.Fields)
	}
}

// Требование заказчика от 13.09.2026: при ошибках распознавания статус не
// должен позволять сформировать XML. Ошибка распознавания и незнакомый
// контрагент — разные вещи: второе повод для согласования, не для возврата.
func TestFaultsSeparatedFromBusinessChecks(t *testing.T) {
	withLocaleBY(t)
	hint := ProcessHint{Company: &CompanyHint{Name: "ООО КофеВенд", UNP: "192226948"}}

	// Поля распознаны, но контрагента нет в справочнике.
	clean := domain.Recognition{
		DocType: "invoice", DocTypeConfidence: 0.9,
		Fields: map[string]domain.Field{
			"number": {Value: "145", Confidence: 0.9},
			"date":   {Value: "12.03.2026", Confidence: 0.9},
			"total":  {Value: "375.00", Confidence: 0.9},
			"unp":    {Value: "191234567", Confidence: 0.9},
		},
		Checks: map[string]bool{CheckCompanySide: true, CheckCounterpartyKnow: false},
	}
	DecideAutoPass(&clean, hint)
	if clean.AutoPass {
		t.Fatal("незнакомый контрагент должен отправлять документ главбуху")
	}
	if len(clean.Faults) != 0 || RecognitionFaulty(clean) {
		t.Fatalf("незнакомый контрагент принят за ошибку распознавания: %v", clean.Faults)
	}

	// То же, но сумма не распознана.
	broken := clean
	broken.Fields = map[string]domain.Field{
		"number": {Value: "145", Confidence: 0.9},
		"date":   {Value: "12.03.2026", Confidence: 0.9},
		"unp":    {Value: "191234567", Confidence: 0.4},
	}
	broken.Checks = map[string]bool{CheckCompanySide: true, CheckCounterpartyKnow: true}
	DecideAutoPass(&broken, hint)
	if !RecognitionFaulty(broken) {
		t.Fatalf("ошибки распознавания не отмечены: faults=%v review=%v", broken.Faults, broken.Review)
	}
	if PartialExportEnabled() {
		t.Fatal("частичная выгрузка должна быть выключена по умолчанию")
	}
}
