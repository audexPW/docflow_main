package onec

import (
	"encoding/xml"
	"strings"
	"testing"

	"docflow/internal/domain"
	"docflow/internal/recognize"

	"github.com/google/uuid"
)

const belarusInvoiceText = `
Счёт на оплату № 145 от 12.03.2026

Поставщик: ООО "Ромашка", УНП 191234567, адрес: г. Минск, ул. Ленина, 1
Покупатель: ЧУП "Василёк", УНП 200111222

Наименование  Кол-во  Ед.  Цена  Сумма
Бумага офисная А4  10  шт  12,50  125,00
Картридж HP CF217A  2  шт  95,00  190,00
Ручка шариковая  50  шт  1,20  60,00

Итого: 375,00
В том числе НДС 20%: 62,50
Всего к оплате: 375,00 бел. руб.
`

// Сквозная проверка: текст документа → правила → payload → XML для 1С.
// Каждое поле должно оказаться в своём элементе, а не в AdditionalFields.
func TestTextReachesCorrectXMLElements(t *testing.T) {
	t.Cleanup(func() { recognize.SetLocale("ru") })
	recognize.SetLocale("by")

	rec := recognize.RecognizeText(belarusInvoiceText)
	rec.Missing = recognize.MissingRequired(rec)

	if rec.DocType != "invoice" {
		t.Fatalf("тип документа: %q", rec.DocType)
	}
	if len(rec.Missing) != 0 {
		t.Fatalf("обязательные реквизиты не собраны: %v — документ не уйдёт в 1С автоматом", rec.Missing)
	}
	if recognize.LinesTotalMismatch(rec, recognize.LinesTolerance) {
		t.Fatal("сумма строк не сошлась с итогом — позиции будут отброшены")
	}

	p := BuildPayload(domain.Document{
		ID:           uuid.New(),
		OriginalName: "schet-145.pdf",
		Recognition:  &rec,
	}, domain.ExportKindCreate)

	body, err := xml.MarshalIndent(toEnterpriseData(p), "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(body)

	want := map[string]string{
		"<Number>145</Number>":                  "номер",
		"<Date>12.03.2026</Date>":               "дата",
		"<Total>375.00</Total>":                 "итог",
		"<VATAmount>62.50</VATAmount>":          "сумма НДС (не ставка)",
		"<Currency>BYN</Currency>":              "валюта",
		"<UNP>191234567</UNP>":                  "УНП контрагента",
		"<OrganizationUNP>200111222</Organizat": "УНП своей организации",
		"Ромашка":                               "контрагент",
		"Василёк":                               "организация",
		"<Qty>10</Qty>":                         "количество в строке",
		"<Amount>125.00</Amount>":               "сумма строки",
	}
	for frag, what := range want {
		if !strings.Contains(out, frag) {
			t.Errorf("в XML нет %s (%s)\n%s", what, frag, out)
		}
	}

	// Всё разложено по своим элементам — служебная свалка должна быть пустой.
	if strings.Contains(out, "<AdditionalFields><Field") {
		t.Errorf("поля утекли в AdditionalFields:\n%s", out)
	}
	// УНП покупателя не должен подменить УНП контрагента.
	if strings.Contains(out, "<UNP>200111222</UNP>") {
		t.Error("в контрагенты попал УНП покупателя — документ уйдёт на самого заказчика")
	}
}
