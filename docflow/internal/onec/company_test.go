package onec

import (
	"context"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"docflow/internal/config"
	"docflow/internal/domain"

	"github.com/google/uuid"
)

// У каждого юрлица своя папка обмена: 1С компании А не должна видеть выгрузки
// компании Б.
func TestFileExporterWritesIntoCompanyFolder(t *testing.T) {
	dir := t.TempDir()
	exp, err := newFileExporter(config.OneCConfig{FileDir: dir})
	if err != nil {
		t.Fatal(err)
	}

	p := Payload{DocumentID: uuid.NewString(), DocType: "invoice", CompanyFolder: "191234567"}
	if err := exp.Export(context.Background(), p); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(filepath.Join(dir, "191234567"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("файл не попал в папку компании: %v %v", entries, err)
	}
}

// Попытка увести запись за пределы каталога обмена именем папки.
func TestCompanyFolderCannotEscapeOutbox(t *testing.T) {
	if got := sanitizeFolder("../../etc"); got != "etc" {
		t.Errorf("имя папки не обезврежено: %q", got)
	}
}

// Табличная часть должна уходить в 1С структурой: колонки объявлены, ячейки
// ссылаются на колонку. Раньше приёмник получал строки текста и разбирал их
// заново.
func TestTableGoesToXMLAsTable(t *testing.T) {
	doc := domain.Document{
		ID: uuid.New(),
		Recognition: &domain.Recognition{
			DocType: "waybill",
			Table: &domain.Table{
				Source: "layout",
				Columns: []domain.TableColumn{
					{Index: 1, Title: "Наименование", Role: "name"},
					{Index: 2, Title: "Кол-во", Role: "qty"},
					{Index: 3, Title: "Сумма", Role: "amount"},
				},
				Rows: []domain.TableRow{{Index: 1, Cells: []domain.TableCell{
					{Column: 1, Role: "name", Value: "Бумага офисная А4"},
					{Column: 2, Role: "qty", Value: "10"},
					{Column: 3, Role: "amount", Value: "125.00"},
				}}},
			},
		},
	}

	p := BuildPayload(doc, domain.ExportKindCreate).WithCompany(domain.Company{
		Name: `ООО "Василёк"`, UNP: "200111222", Folder: "200111222",
	})
	body, err := xml.MarshalIndent(toEnterpriseData(p), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	out := string(body)

	for _, want := range []string{
		`<Table source="layout" rows="1">`,
		`<Column index="1" role="name">Наименование</Column>`,
		`<Cell column="3" role="amount">125.00</Cell>`,
		"<Folder>200111222</Folder>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("в XML нет %q\n%s", want, out)
		}
	}
}

// Обрывки распознанного текста не должны уезжать в AdditionalFields: там
// место реквизитам, а не строкам страницы.
func TestJunkFieldsAreNotExported(t *testing.T) {
	// Номер договора выгружается своим элементом ContractNumber и в
	// AdditionalFields не дублируется; туда идут только реквизиты, у которых
	// своего элемента нет.
	p := Payload{
		ContractNumber: "12/2026",
		Fields: map[string]string{
			"contract_number": "12/2026",
			"payment_purpose": "Оплата за бумагу",
			"строка таблицы":  "Бумага | 10 | шт",
		},
	}
	ed := toEnterpriseData(p)
	for _, f := range ed.Body.Document.Extra {
		if strings.Contains(f.Value, "|") {
			t.Errorf("мусорное поле уехало в 1С: %+v", f)
		}
		if f.Name == "contract_number" {
			t.Errorf("номер договора продублирован в дополнительных полях: %+v", f)
		}
	}
	if len(ed.Body.Document.Extra) != 1 || ed.Body.Document.Extra[0].Name != "payment_purpose" {
		t.Errorf("ожидалось одно дополнительное поле payment_purpose, получено %+v", ed.Body.Document.Extra)
	}
	if ed.Body.Document.ContractNumber != "12/2026" {
		t.Errorf("номер договора не попал в ContractNumber: %q", ed.Body.Document.ContractNumber)
	}
}
