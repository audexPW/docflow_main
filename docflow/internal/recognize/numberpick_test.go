package recognize

import (
	"testing"

	"docflow/internal/domain"
)

func TestPreferBlankNumber(t *testing.T) {
	cases := []struct {
		name, cur, source, blank, text, want string
	}{
		{"модель взяла номер договора (IMG_0169)", "23/23-КФ от 12.07.2023", "model", "666", "Счет-фактура 666 от 31 Августа 2023 г. Договор 23/23-КФ от 12.07.2023", "666"},
		{"модель взяла слово с бланка (IMG_0122)", "NHHILL", "model", "322", "Счет 322 от 31 января 2023 г NHHILL", "322"},
		{"правила взяли название формы", "ТТН", "rule", "0357107", "ТТН-1 № 0357107", "0357107"},
		{"совпадают со знаком №", "№ 1284", "model", "1284", "Счет-фактура № 1284", "№ 1284"},
		{"бланк прочитан не целиком", "322/1", "model", "322", "Счет 322/1 от 31 января", "322/1"},
		{"номера не было", "", "", "618", "Счет 618", "618"},
		{"введён вручную", "618-А", "manual", "618", "Счет 618", "618-А"},
		{"на бланке номер не найден", "136", "model", "", "Счет 136", "136"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := domain.Recognition{Fields: map[string]domain.Field{}}
			if c.cur != "" {
				rec.Fields["number"] = domain.Field{Value: c.cur, Source: c.source, Confidence: 0.85}
			}
			PreferBlankNumber(&rec, c.blank, c.text)
			if got := rec.Fields["number"].Value; got != c.want {
				t.Fatalf("номер %q, ожидалось %q", got, c.want)
			}
		})
	}
	t.Run("выключено", func(t *testing.T) {
		t.Setenv("NUMBER_PREFER_BLANK", "false")
		rec := domain.Recognition{Fields: map[string]domain.Field{"number": {Value: "NHHILL", Source: "model"}}}
		PreferBlankNumber(&rec, "322", "Счет 322")
		if rec.Fields["number"].Value != "NHHILL" {
			t.Fatal("NUMBER_PREFER_BLANK=false не должен трогать номер")
		}
	})
}
