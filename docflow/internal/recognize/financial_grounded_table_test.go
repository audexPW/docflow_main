package recognize

import (
	"docflow/internal/domain"
	"testing"
)

func makeMoneyTestTable(cols []domain.TableColumn, input [][]string) *domain.Table {
	t := &domain.Table{Columns: cols}
	for i, r := range input {
		row := domain.TableRow{Index: i + 1}
		for j, val := range r {
			row.Cells = append(row.Cells, domain.TableCell{Column: j + 1, Role: cols[j].Role, Value: val})
		}
		t.Rows = append(t.Rows, row)
	}
	return t
}

func TestGroundedSingleLineAmounts(t *testing.T) {
	t.Run("012", func(t *testing.T) {
		cols := []domain.TableColumn{{1, "№ nn", "name"}, {2, "Наименование работы (услуги)", "unit"}, {3, "Кол-во", "qty"}, {4, "Цена, руб.коп.", "price"}, {5, "Сумма б/НДС, Ставка НДС, руб.коп. %", "vat_rate"}, {6, "Сумма НДС, руб.коп.", "vat"}, {7, "Сумма, руб.коп.", "amount"}}
		rec := &domain.Recognition{Table: makeMoneyTestTable(cols, [][]string{{"", "Услуги по хранению, руб.", "", "587.50", "587.50 20", "117.50", "705.00"}}), Fields: map[string]domain.Field{"amount_no_vat": {Value: "578.50", Source: "derived"}, "vat_amount": {Value: "171.5", Source: "model"}, "total": {Value: "750", Source: "model"}}}
		if !ReconcileGroundedSingleLineAmounts(rec, "Услуги по хранению, руб. 587.50 587.50 20 117.50 705.00") {
			t.Fatal("no repair")
		}
		for key, want := range map[string]string{"amount_no_vat": "587.50", "vat_amount": "117.50", "total": "705.00"} {
			if got := rec.Fields[key].Value; got != want {
				t.Errorf("%s=%s want %s", key, got, want)
			}
		}
	})
	t.Run("051", func(t *testing.T) {
		cols := []domain.TableColumn{{1, "товара (продукции, услуги)", "name"}, {2, "Кол-во", "qty"}, {3, "продукции в бел. руб.", "other"}, {4, "руб.", "other"}, {5, "ндс, %", "vat_rate"}, {6, "ндс, руб.", "vat"}, {7, "с НДС, руб.", "amount"}}
		rec := &domain.Recognition{Table: makeMoneyTestTable(cols, [][]string{{"Предоплата за коммунальные услуги", "0", "0.00", "176.38", "0", "0.00", "176.38"}, {"Сумма НДС: Налогового om 29.12.2009 2. Без согласно", "", "Итогс: НДС.Освобождение кодекса", "176.38", "", "", "176.38"}}), Fields: map[string]domain.Field{"amount_no_vat": {Value: "3176.38", Source: "derived"}, "vat_amount": {Value: "0", Source: "model"}, "total": {Value: "3176.38", Source: "model"}}}
		if !ReconcileGroundedSingleLineAmounts(rec, "Предоплата 0.00 176.38 Всего с НДС: 3 176.38") {
			t.Fatal("no repair")
		}
		for _, key := range []string{"amount_no_vat", "total"} {
			if got := rec.Fields[key].Value; got != "176.38" {
				t.Errorf("%s=%s", key, got)
			}
		}
	})
	t.Run("048 skip multiposition", func(t *testing.T) {
		cols := []domain.TableColumn{{1, "Электроэнергия (кВт-ч)", "name"}, {2, "Кол-во", "qty"}, {3, "тариф", "price"}, {4, "Стоимость", "amount"}, {5, "НДС, %", "vat_rate"}, {6, "НДС, руб.", "vat"}}
		rec := &domain.Recognition{Table: makeMoneyTestTable(cols, [][]string{{"Техническое обслуживание сигнализации", "69.00", "0.33621", "23.20", "20.0%", "4.64"}, {"Дезинсекция и дератизация", "", "", "0.02", "20%", "0.00"}}), Fields: map[string]domain.Field{"total": {Value: "27.97", Source: "model"}}}
		if ReconcileGroundedSingleLineAmounts(rec, "23.20 4.64 27.97 0.02 0.00") {
			t.Fatal("must skip")
		}
		if got := rec.Fields["total"].Value; got != "27.97" {
			t.Fatal(got)
		}
	})
	t.Run("059 skip multiposition", func(t *testing.T) {
		cols := []domain.TableColumn{{1, "Наименование услуг", "name"}, {2, "Сумма в руб.", "amount"}, {3, "Сумма НДС", "vat"}, {4, "Итого с НДС", "amount"}}
		rec := &domain.Recognition{Table: makeMoneyTestTable(cols, [][]string{{"Теплоэнергия", "2.86", "0.57", "3.43"}, {"Электроэнергия", "0.06", "0.00", "0.06"}}), Fields: map[string]domain.Field{"total": {Value: "30.27", Source: "model"}}}
		if ReconcileGroundedSingleLineAmounts(rec, "2.86 0.57 3.43 0.06 0.00 30.27") {
			t.Fatal("must skip")
		}
	})
	t.Run("manual protects", func(t *testing.T) {
		cols := []domain.TableColumn{{1, "Наименование", "name"}, {2, "НДС, руб.", "vat"}, {3, "Сумма с НДС", "amount"}}
		rec := &domain.Recognition{Table: makeMoneyTestTable(cols, [][]string{{"Хранение услуги", "5.00", "30.00"}}), Fields: map[string]domain.Field{"total": {Value: "99.00", Source: "manual"}}}
		if ReconcileGroundedSingleLineAmounts(rec, "25.00 5.00 30.00") {
			t.Fatal("manual overwritten")
		}
		if rec.Fields["total"].Value != "99.00" {
			t.Fatal("manual changed")
		}
	})
}
