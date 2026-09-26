package recognize

import "testing"

func TestParseMoneyWords(t *testing.T) {
	cases := map[string]string{
		"Сорок четыре рубля 00 копеек":                "44.00",
		"Семь рублей 26 копеек":                       "7.26",
		"Тридцать шесть белорусских рублей 78 копеек": "36.78",
		"Сто шестьдесят девять рублей 44 копейки":     "169.44",
		"Двадцать шесть рублей 57 копеек":             "26.57",
		"Одна тысяча двести рублей 05 копеек":         "1200.05",
		"Шесть рублей тринадцать копеек":              "6.13",
		"Триста восемнадцать рублей 31 копейка":       "318.31",
	}
	for in, want := range cases {
		if got := ParseMoneyWords(in); got != want {
			t.Errorf("%q → %q, ожидалось %q", in, got, want)
		}
	}
}

func TestVATFromWords(t *testing.T) {
	text := "К оплате : Сорок четыре рубля 00 копеек\nв т.ч. НДС: Семь рублей 26 копеек\n"
	if got := VATFromWords(text); got != "7.26" {
		t.Errorf("НДС прописью не разобран: %q", got)
	}
	// Без пометки «в том числе НДС» брать первую попавшуюся сумму нельзя.
	if got := VATFromWords("К оплате: Сорок четыре рубля 00 копеек"); got != "" {
		t.Errorf("взята сумма не того назначения: %q", got)
	}
}

// Частичный разбор опаснее отказа: «сорок» вместо «сорок четыре» уйдёт в
// бухгалтерию молча и никто не заметит.
func TestUnknownWordRejects(t *testing.T) {
	if got := parseRussianInt("сорок мяу"); got != -1 {
		t.Errorf("неизвестное слово не отвергнуто: %d", got)
	}
	if got := ParseMoneyWords("Сорок мяу рубля 00 копеек"); got != "" {
		t.Errorf("испорченная строка разобрана: %q", got)
	}
}

// Поля, которые заказчик требует показывать всегда, должны заполняться и
// когда сумма напечатана под таблицей словами.
func TestExtractVATFromWordsInDocument(t *testing.T) {
	text := "Предмет счета Количество Тариф Сумма б/НДС\n" +
		"Услуги по обращению с ТКО 0,04 1 0,04 20\n" +
		"ИТОГО к оплате: 44.00\n" +
		"К оплате : Сорок четыре рубля 00 копеек\n" +
		"в т.ч. НДС: Семь рублей 26 копеек\n"
	rec := RecognizeDocument(text, text)
	f, ok := rec.Fields["vat_amount"]
	if !ok || f.Value != "7.26" {
		t.Errorf("сумма НДС прописью не попала в поля: %+v", rec.Fields["vat_amount"])
	}
}

func TestExtractAmountNoVAT(t *testing.T) {
	text := "Стоимость - всего без НДС, руб.  134,51\nСумма НДС, руб. 26,90\n"
	rec := RecognizeDocument(text, text)
	if f := rec.Fields["amount_no_vat"]; f.Value != "134.51" {
		t.Errorf("сумма без НДС: %q", f.Value)
	}
}
