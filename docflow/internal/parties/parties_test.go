package parties

import "testing"

func TestKeySameCompanyVariants(t *testing.T) {
	variants := []string{
		"Общество с ограниченной ответственностью «КофеВенд»",
		"ООО КофеВенд",
		"ООО Кофевенд",
		"ОАО \"КофеВенд\"",
	}
	for _, v := range variants {
		if Key(v) != "кофевенд" {
			t.Fatalf("Key(%q) = %q", v, Key(v))
		}
	}
	if f, _ := Split("ОАО КофеВенд"); f != "ОАО" {
		t.Fatalf("форма ОАО не выделена: %q", f)
	}
}

func TestSameBySubstring(t *testing.T) {
	if !Same("Учреждение здравоохранения «Минская центральная районная клиническая больница»",
		"УЗ Минская центральная районная клиническая больница") {
		t.Fatal("одно и то же учреждение не опознано")
	}
	if Same("ООО Мир", "ООО Мирный свет") {
		t.Fatal("короткий ключ не должен совпадать с чужим именем")
	}
}

func TestSplitEntities(t *testing.T) {
	parts := SplitEntities("ООО «КофеВенд» ЗАО «Суперпрод»")
	if len(parts) != 2 || Key(parts[0]) != "кофевенд" || Key(parts[1]) != "суперпрод" {
		t.Fatalf("склейка двух юрлиц не разрезана: %#v", parts)
	}
	if n := CountForms("Общество с ограниченной ответственностью «КофеВенд»"); n != 1 {
		t.Fatalf("CountForms = %d", n)
	}
}

func TestLongFormOrder(t *testing.T) {
	f, core := Split("Совместное общество с ограниченной ответственностью «Альфа»")
	if f != "СООО" || core != "Альфа" {
		t.Fatalf("got %q %q", f, core)
	}
}
