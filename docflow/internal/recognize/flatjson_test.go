package recognize

import "testing"

// Реальные ответы Qwen со стенда: 6 из 7 пришли без обёртки "fields".
const answer0212 = `{
  "doc_type": "schet_faktura",
  "counterparty": "Гродненское городское коммунальное производственное унитарное предприятие",
  "currency": "BYN",
  "date": "31.08.2023",
  "number": "03428",
  "organization": "Общество с ограниченной ответственностью \"КофеВенд\"",
  "organization_unp": "590888079",
  "total": 29.62,
  "unp": "591023968",
  "vat_amount": 0
}`

const answer0204 = `{
  "doc_type": "Акт выполненных работ",
  "fields": {"counterparty": "КофеВенд ООО", "date": "31.08.2023", "total": "10.00"}
}`

func TestFlatModelAnswerParsed(t *testing.T) {
	var me modelExtract
	if err := decodeJSONObject(answer0212, &me); err != nil {
		t.Fatalf("разбор: %v", err)
	}
	if !me.Flat {
		t.Error("плоская форма не отмечена")
	}
	if got := len(me.Fields); got != 9 {
		t.Fatalf("полей %d, ожидалось 9: %v", got, me.Fields)
	}
	for k, want := range map[string]string{
		"unp":              "591023968",
		"organization_unp": "590888079",
		"total":            "29.62",
		"date":             "31.08.2023",
		"number":           "03428",
	} {
		if got := string(me.Fields[k]); got != want {
			t.Errorf("%s = %q, ожидалось %q", k, got, want)
		}
	}
	if me.DocType != "schet_faktura" {
		t.Errorf("тип %q", me.DocType)
	}
}

func TestSchemaAnswerStillParsed(t *testing.T) {
	var me modelExtract
	if err := decodeJSONObject(answer0204, &me); err != nil {
		t.Fatalf("разбор: %v", err)
	}
	if me.Flat {
		t.Error("ответ по схеме помечен как плоский")
	}
	if len(me.Fields) != 3 || string(me.Fields["counterparty"]) != "КофеВенд ООО" {
		t.Fatalf("поля: %v", me.Fields)
	}
}

func TestHeadLostParties(t *testing.T) {
	cases := map[string]bool{
		// IMG_0212: блок сторон уехал в табличную полосу
		"СЧЕТ-ФАКТУРА (ЖКХ) № 03428 112\nплан за август 2023 г.\n31 августа 2023 г.\nОснование Договор возмещение убытков №б/н 01.06.2023 г.Селиверстова на от Анна Иосифовна, бл.717\nПродавец:": true,
		// IMG_0176 и IMG_0128: шапки нет вовсе
		"": true,
		// IMG_0218: обе стороны с УНН на месте
		"Поставщик и его адрес: СЧЕТ-ФАКТУРА\nФилиал \"Троллейбусный парк №5\" государственного предприятия\n\"Минсктранс\" 118\n220070, г.Минск, ул.Солтыса,26\nУНН: 102299113 28 августа 2023 г.\nОКПО: 376289405010 к платежному поручению:\nВУ88АКВВ30120000086735200000 Р/сч в ОАО \"АСБ Беларусбанк\", ул.\nДолгобродская, 1, BIC AKBBBY2X OT\nПлательщик и его адрес:\nОАО \"КофеВенд\"\n220002 г. Минск, ул. Сторожевская, 8 помещ. 8\nУНН: 590888079\nОКПО:": false,
		// IMG_0146: продавец и покупатель с УНП
		`продавец: покупатель:
ОАО "Белмагистральавтотранс" КофеВенд общество с ограниченной
ответственностью
ул. Бабушкина, 39, 220024, г. Минск 220002г.Минск, ул.Сторожевская, 8,пом.8 почт. а/я 108
УНП: 101235482, р/с УНП: 590888079, BY96BPSB30123133260199330000 p/c
BY36TECN30121766200000000010
ОАО в "Технобанк", г.Минск, РБ, ул.Кропоткина, 44 ОАО в "БПС-Сбербанк" РЕГИОНАЛЬНАЯ ДИРЕКЦИЯ`: false,
	}
	for head, want := range cases {
		if got := headLostParties(head); got != want {
			t.Errorf("headLostParties(%.40q) = %v, ожидалось %v", head, got, want)
		}
	}
}
