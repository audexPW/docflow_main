package recognize

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

// DocTypeUnknown — тип не опознан ни правилами, ни моделью.
const DocTypeUnknown = "unknown"

// Реестр типов документов — единственное место, где описан тип: признаки для
// классификации, обязательные реквизиты, наличие табличной части и то, во что
// этот тип превращается на стороне 1С.
//
// ТЗ §5: система принимает любые документы, перечень типов не закрыт. Поэтому
// реестр расширяется JSON-файлом (DOCTYPES_PATH) без пересборки: файл
// накладывается поверх встроенных значений по slug — существующие типы
// дополняются, новые добавляются.

// OneCTarget — куда документ должен попасть в базе 1С.
//
// Object — полное имя объекта метаданных как оно записано в конфигурации
// заказчика, например "Документ.ПоступлениеТоваровУслуг" или
// "Справочник.ДоговорыКонтрагентов". Kind — что это за объект: document |
// catalog | file (присоединённый файл без создания учётного объекта).
//
// Пустой Object означает «маппинг не согласован». Такие документы не уходят в
// 1С автоматически — они отправляются оператору на проверку, потому что
// приёмник не сможет решить, что создавать.
type OneCTarget struct {
	Object string `json:"object"`
	Kind   string `json:"kind,omitempty"`
}

// DocTypeSpec — описание одного типа документа.
type DocTypeSpec struct {
	Slug     string   `json:"slug"`               // внутренний код типа
	Title    string   `json:"title"`              // подпись для интерфейса
	Synonyms []string `json:"synonyms,omitempty"` // как тип может назвать модель или оператор
	Anchors  []string `json:"anchors,omitempty"`  // фразы-признаки в тексте (нижний регистр)
	Weight   float64  `json:"weight,omitempty"`   // вклад одного совпадения в уверенность
	// Required — обязательные реквизиты. Токен @taxid резолвится в unp или inn
	// по локали (см. SetLocale).
	Required []string   `json:"required,omitempty"`
	HasLines bool       `json:"has_lines,omitempty"` // табличная часть обязательна
	OneC     OneCTarget `json:"onec"`
	// Comment — место для пояснений в JSON-файле реестра. Разбор строгий
	// (неизвестные поля — ошибка), чтобы опечатка в имени реквизита не
	// проходила молча, поэтому комментарий описан явным полем.
	Comment json.RawMessage `json:"_comment,omitempty"`
}

// Catalog — реестр типов с индексами для быстрого поиска.
type Catalog struct {
	specs            []DocTypeSpec
	bySlug           map[string]DocTypeSpec
	byAlias          map[string]string // нормализованное имя → slug
	fallbackRequired []string
}

// defaultFallbackRequired — минимум для типа, которого нет в реестре.
//
// Намеренно без total: незнакомый документ может быть доверенностью или актом
// приёмки, где суммы нет вовсе. Требовать её значило бы навсегда оставить такой
// документ в статусе «нужно уточнение».
var defaultFallbackRequired = []string{"number", "date"}

// defaultSpecs — встроенный набор. Поле OneC намеренно пустое: имена объектов
// метаданных берутся из конкретной базы заказчика и задаются в doctypes.json.
// Выдумывать их здесь нельзя — приёмник молча создаст не то.
var defaultSpecs = []DocTypeSpec{
	{
		Slug:     "schet_faktura",
		Title:    "Счёт-фактура (ЭСЧФ)",
		Synonyms: []string{"счет фактура", "счёт фактура", "эсчф", "электронный счет фактура", "invoice_vat", "vat_invoice"},
		Anchors:  []string{"счёт-фактура", "счет-фактура", "счетфактура", "эсчф", "электронный счёт-фактура", "электронный счет-фактура"},
		Weight:   0.6,
		Required: []string{"number", "date", "total", taxIDToken},
		HasLines: true,
	},
	{
		Slug:     "upd",
		Title:    "УПД",
		Synonyms: []string{"универсальный передаточный документ"},
		Anchors:  []string{"универсальный передаточный документ", "упд", "статус упд"},
		Weight:   0.6,
		Required: []string{"number", "date", "total", taxIDToken},
		HasLines: true,
	},
	{
		Slug:     "invoice",
		Title:    "Счёт на оплату",
		Synonyms: []string{"счет на оплату", "счёт на оплату", "счет", "счёт", "bill"},
		Anchors:  []string{"счёт на оплату", "счет на оплату", "счёт №", "счет №"},
		Weight:   0.5,
		Required: []string{"number", "date", "total", taxIDToken},
		HasLines: true,
	},
	{
		Slug:     "waybill",
		Title:    "Накладная (ТТН-1 / ТН-2)",
		Synonyms: []string{"накладная", "товарная накладная", "товарно транспортная накладная", "ттн", "тн-2", "торг-12"},
		Anchors:  []string{"товарная накладная", "товарно-транспортная накладная", "тту-1", "ттн-1", "тн-2", "торг-12", "торг 12", "накладная №"},
		Weight:   0.5,
		Required: []string{"number", "date", taxIDToken},
		HasLines: true,
	},
	{
		Slug:     "act",
		Title:    "Акт выполненных работ",
		Synonyms: []string{"акт", "акт выполненных работ", "акт оказанных услуг"},
		Anchors:  []string{"акт выполненных работ", "акт оказанных услуг", "акт сдачи-приёмки", "акт сдачи-приемки", "акт №"},
		Weight:   0.5,
		Required: []string{"number", "date", "total"},
	},
	{
		Slug:     "act_priemki",
		Title:    "Акт приёмки-передачи",
		Synonyms: []string{"акт приемки", "акт приёмки", "акт приема передачи", "акт приёма-передачи"},
		Anchors:  []string{"акт приёмки", "акт приемки", "акт приёма-передачи", "акт приема-передачи", "акт приёма", "акт приема"},
		Weight:   0.5,
		Required: []string{"number", "date"},
	},
	{
		Slug:     "act_sverki",
		Title:    "Акт сверки взаиморасчётов",
		Synonyms: []string{"акт сверки", "акт сверки взаиморасчетов"},
		Anchors:  []string{"акт сверки", "акт сверки взаимных расчётов", "акт сверки взаимных расчетов", "акт сверки взаиморасчётов"},
		Weight:   0.6,
		Required: []string{"date", taxIDToken},
	},
	{
		Slug:     "receipt",
		Title:    "Чек",
		Synonyms: []string{"чек", "кассовый чек", "товарный чек"},
		Anchors:  []string{"кассовый чек", "товарный чек", "фискальный", "ккт", "фн №"},
		Weight:   0.5,
		Required: []string{"date", "total"},
	},
	{
		Slug:     "contract",
		Title:    "Договор",
		Synonyms: []string{"договор", "контракт", "соглашение"},
		Anchors:  []string{"договор №", "договор поставки", "договор оказания", "настоящий договор"},
		Weight:   0.5,
		Required: []string{"number", "date", "counterparty"},
	},
	{
		Slug:     "payment_order",
		Title:    "Платёжное поручение",
		Synonyms: []string{"платежное поручение", "платёжное поручение", "платежка", "платёжка"},
		Anchors:  []string{"платёжное поручение", "платежное поручение", "плат. поручение"},
		Weight:   0.6,
		Required: []string{"number", "date", "total"},
	},
	{
		Slug:     "power_of_attorney",
		Title:    "Доверенность",
		Synonyms: []string{"доверенность"},
		Anchors:  []string{"доверенность №", "доверенность на получение"},
		Weight:   0.6,
		Required: []string{"number", "date"},
	},
}

var (
	catalogMu sync.RWMutex
	catalog   = newCatalog(defaultSpecs, defaultFallbackRequired)
)

func newCatalog(specs []DocTypeSpec, fallback []string) *Catalog {
	c := &Catalog{
		specs:            specs,
		bySlug:           make(map[string]DocTypeSpec, len(specs)),
		byAlias:          make(map[string]string, len(specs)*4),
		fallbackRequired: fallback,
	}
	for _, s := range specs {
		c.bySlug[s.Slug] = s
		for _, alias := range append([]string{s.Slug, s.Title}, s.Synonyms...) {
			if k := normalizeKey(alias); k != "" {
				// Первый заявивший алиас его и получает: явный slug важнее
				// случайного совпадения синонимов у соседнего типа.
				if _, busy := c.byAlias[k]; !busy {
					c.byAlias[k] = s.Slug
				}
			}
		}
	}
	return c
}

// normalizeKey приводит произвольное написание типа к сравнимому виду:
// нижний регистр, ё→е, всё несловесное — в подчёркивание.
func normalizeKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "ё", "е")
	var b strings.Builder
	prevSep := true
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r >= 'а' && r <= 'я':
			b.WriteRune(r)
			prevSep = false
		default:
			if !prevSep {
				b.WriteByte('_')
				prevSep = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}

// docTypesFile — формат внешнего файла реестра.
type docTypesFile struct {
	Comment          json.RawMessage `json:"_comment,omitempty"`
	FallbackRequired []string        `json:"fallback_required,omitempty"`
	Types            []DocTypeSpec   `json:"types"`
}

// LoadDocTypes накладывает JSON-файл поверх встроенного реестра: типы с
// совпадающим slug дополняются (непустые поля файла побеждают), остальные
// добавляются. Пустой путь — оставить встроенный реестр как есть.
//
// Возвращает число типов без маппинга на 1С — вызывающий код логирует это
// предупреждением: такие документы автоматически в базу не пойдут.
func LoadDocTypes(path string) (unmapped []string, err error) {
	if strings.TrimSpace(path) == "" {
		catalogMu.RLock()
		defer catalogMu.RUnlock()
		return catalog.unmappedSlugs(), nil
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read doctypes %s: %w", path, err)
	}
	var f docTypesFile
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("parse doctypes %s: %w", path, err)
	}

	merged := make([]DocTypeSpec, len(defaultSpecs))
	copy(merged, defaultSpecs)
	index := make(map[string]int, len(merged))
	for i, s := range merged {
		index[s.Slug] = i
	}

	for _, s := range f.Types {
		s.Slug = normalizeKey(s.Slug)
		if s.Slug == "" {
			return nil, fmt.Errorf("doctypes %s: тип без slug", path)
		}
		if i, ok := index[s.Slug]; ok {
			merged[i] = mergeSpec(merged[i], s)
			continue
		}
		index[s.Slug] = len(merged)
		merged = append(merged, s)
	}

	fallback := defaultFallbackRequired
	if len(f.FallbackRequired) > 0 {
		fallback = f.FallbackRequired
	}

	c := newCatalog(merged, fallback)
	catalogMu.Lock()
	catalog = c
	catalogMu.Unlock()
	return c.unmappedSlugs(), nil
}

// mergeSpec накладывает override на base: непустые поля override побеждают,
// списки заменяются целиком (иначе нельзя убрать лишний обязательный реквизит).
func mergeSpec(base, override DocTypeSpec) DocTypeSpec {
	out := base
	if override.Title != "" {
		out.Title = override.Title
	}
	if len(override.Synonyms) > 0 {
		out.Synonyms = override.Synonyms
	}
	if len(override.Anchors) > 0 {
		out.Anchors = override.Anchors
	}
	if override.Weight > 0 {
		out.Weight = override.Weight
	}
	if len(override.Required) > 0 {
		out.Required = override.Required
	}
	if override.HasLines {
		out.HasLines = true
	}
	if override.OneC.Object != "" {
		out.OneC = override.OneC
	}
	out.Comment = nil
	return out
}

func (c *Catalog) unmappedSlugs() []string {
	var out []string
	for _, s := range c.specs {
		if strings.TrimSpace(s.OneC.Object) == "" {
			out = append(out, s.Slug)
		}
	}
	sort.Strings(out)
	return out
}

func current() *Catalog {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	return catalog
}

// DocTypes возвращает реестр целиком — для интерфейса оператора и диагностики.
func DocTypes() []DocTypeSpec {
	c := current()
	out := make([]DocTypeSpec, len(c.specs))
	copy(out, c.specs)
	return out
}

// LookupDocType возвращает описание типа по его slug.
func LookupDocType(docType string) (DocTypeSpec, bool) {
	s, ok := current().bySlug[docType]
	return s, ok
}

// NormalizeDocType приводит тип, названный моделью или оператором, к slug из
// реестра. Модель на локальном железе охотно отвечает «Товарная накладная» или
// «Invoice» вместо кода — без нормализации такой документ получил бы тип,
// которого не знает ни таблица обязательных полей, ни приёмник в 1С.
//
// Незнакомое название не выбрасывается: оно приводится к slug-виду и живёт
// дальше как новый тип (ТЗ §5 — перечень открыт), просто без маппинга на 1С.
func NormalizeDocType(raw string) string {
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
	return key
}

// DocTypeTitle — человекочитаемое название типа (для 1С и интерфейса).
func DocTypeTitle(docType string) string {
	if s, ok := LookupDocType(docType); ok && s.Title != "" {
		return s.Title
	}
	return docType
}

// OneCTargetFor возвращает согласованный объект 1С для типа документа.
// Пустой Object означает, что маппинг ещё не задан.
func OneCTargetFor(docType string) OneCTarget {
	if s, ok := LookupDocType(docType); ok {
		return s.OneC
	}
	return OneCTarget{}
}

// KnownDocType — тип есть в реестре (пусть даже без маппинга на 1С).
func KnownDocType(docType string) bool {
	_, ok := LookupDocType(docType)
	return ok
}

// ExportRoutable отвечает на вопрос «можно ли отправить такой документ в 1С без
// участия оператора». Нет — если тип неизвестен реестру или для него не
// согласован объект-приёмник: приёмник в 1С в этом случае не знает, что
// создавать, и документ либо потеряется, либо ляжет не туда.
func ExportRoutable(docType string) bool {
	if docType == "" || docType == DocTypeUnknown {
		return false
	}
	spec, ok := LookupDocType(docType)
	if !ok {
		return false
	}
	return strings.TrimSpace(spec.OneC.Object) != ""
}

// typeHasLines — у типа обязательна табличная часть.
func typeHasLines(docType string) bool {
	s, ok := LookupDocType(docType)
	return ok && s.HasLines
}
