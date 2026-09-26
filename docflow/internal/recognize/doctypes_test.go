package recognize

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeDocTypeAcceptsModelWording(t *testing.T) {
	cases := map[string]string{
		"Товарная накладная": "waybill",
		"ТТН":                "waybill",
		"invoice":            "invoice",
		"Счёт на оплату":     "invoice",
		"ЭСЧФ":               "schet_faktura",
		"Акт сверки":         "act_sverki",
		"Доверенность":       "power_of_attorney",
		"":                   DocTypeUnknown,
	}
	for in, want := range cases {
		if got := NormalizeDocType(in); got != want {
			t.Errorf("NormalizeDocType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeDocTypeKeepsUnknownAsNewType(t *testing.T) {
	// Незнакомая форма не должна схлопываться в unknown: перечень типов открыт.
	got := NormalizeDocType("Заявка на возврат тары")
	if got == DocTypeUnknown || got == "" {
		t.Fatalf("незнакомый тип потерян: %q", got)
	}
	if KnownDocType(got) {
		t.Fatalf("%q не должен считаться известным реестру", got)
	}
	// И он не уходит в 1С автоматически — маппить не по чему.
	if ExportRoutable(got) {
		t.Fatalf("тип без маппинга не должен быть routable")
	}
}

func TestRequiredFieldsFallbackHasNoTotal(t *testing.T) {
	// Для незнакомого типа сумма не обязательна: это может быть доверенность,
	// где её нет вовсе, и документ навсегда завис бы в «нужно уточнение».
	for _, f := range RequiredFields("zayavka_na_vozvrat_tary") {
		if f == "total" {
			t.Fatal("total не должен быть обязательным для незнакомого типа")
		}
	}
}

func TestLoadDocTypesMergesMappingAndAddsTypes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doctypes.json")
	body := `{
	  "types": [
	    {"slug": "waybill", "onec": {"object": "Документ.ПоступлениеТоваровУслуг", "kind": "document"}},
	    {"slug": "putevoy_list", "title": "Путевой лист", "synonyms": ["путевой лист"],
	     "anchors": ["путевой лист"], "required": ["number", "date"],
	     "onec": {"object": "Документ.ПутевойЛист", "kind": "document"}}
	  ]
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = LoadDocTypes("") })

	unmapped, err := LoadDocTypes(path)
	if err != nil {
		t.Fatal(err)
	}

	// Встроенный тип дополнился маппингом, признаки классификации сохранились.
	if got := OneCTargetFor("waybill").Object; got != "Документ.ПоступлениеТоваровУслуг" {
		t.Fatalf("waybill onec object = %q", got)
	}
	if !ExportRoutable("waybill") {
		t.Fatal("waybill должен стать routable после маппинга")
	}
	if dt, _ := classifyByRules("Товарно-транспортная накладная №5"); dt != "waybill" {
		t.Fatalf("классификация сломалась после мерджа: %q", dt)
	}

	// Новый тип подхватился целиком.
	if NormalizeDocType("Путевой лист") != "putevoy_list" {
		t.Fatal("новый тип не попал в реестр")
	}
	if dt, _ := classifyByRules("ПУТЕВОЙ ЛИСТ грузового автомобиля"); dt != "putevoy_list" {
		t.Fatalf("новый тип не классифицируется: %q", dt)
	}

	// Типы без маппинга по-прежнему перечисляются на старте.
	if len(unmapped) == 0 {
		t.Fatal("ожидались типы без маппинга")
	}
	for _, s := range unmapped {
		if s == "waybill" || s == "putevoy_list" {
			t.Fatalf("%s имеет маппинг, но попал в unmapped", s)
		}
	}
}

func TestClassifyIgnoresYoAndCase(t *testing.T) {
	if dt, _ := classifyByRules("СЧЕТ-ФАКТУРА №12"); dt != "schet_faktura" {
		t.Fatalf("got %q", dt)
	}
	if dt, _ := classifyByRules("акт приёмки-передачи оборудования"); dt != "act_priemki" {
		t.Fatalf("got %q", dt)
	}
}

// Реестр, который уходит заказчику, должен разбираться: строгий парсинг ловит
// опечатку в имени реквизита, а этот тест — опечатку в самом файле-примере.
func TestShippedExampleParses(t *testing.T) {
	const path = "../../../doctypes.example.json"
	if _, err := os.Stat(path); err != nil {
		t.Skip("файл-пример недоступен из этого контекста сборки")
	}
	t.Cleanup(func() { _, _ = LoadDocTypes("") })
	if _, err := LoadDocTypes(path); err != nil {
		t.Fatalf("doctypes.example.json не разбирается: %v", err)
	}
}
