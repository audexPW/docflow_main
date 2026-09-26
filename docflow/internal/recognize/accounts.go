package recognize

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"docflow/internal/domain"
)

// Подбор счетов бухгалтерского учёта по содержимому документа.
//
// Требование заказчика: после выгрузки бухгалтер тратил столько же времени,
// сколько при ручном вводе, — потому что счёт учёта в 1С всё равно выбирался
// руками для каждой строки. Здесь система решает это сама: по наименованию
// позиции и типу документа подбирается счёт дебета, счёт кредита берётся из
// расчётов с поставщиком, счёт НДС — из настройки. Всё это уходит в выгрузку,
// и приёмнику остаётся подставить готовые значения.
//
// Правила лежат в JSON (ACCOUNTS_PATH) и правятся бухгалтерией без пересборки:
// план счетов у каждого заказчика свой, зашивать его в код нельзя. Файл не
// задан — подбор выключен, поведение прежнее.

// Ключи полей, в которых система отдаёт подобранные счета.
const (
	FieldAccountDebit  = "account_debit"
	FieldAccountCredit = "account_credit"
	FieldAccountVAT    = "account_vat"
)

// AccountEntry — счёт плана счетов: код и что на нём учитывается.
type AccountEntry struct {
	Code    string `json:"code"`
	Title   string `json:"title"`
	Comment string `json:"_comment,omitempty"`
}

// AccountRule — правило «в наименовании встретилось слово → такой счёт».
// Правила проверяются сверху вниз, первое совпавшее выигрывает: порядок в
// файле и есть приоритет, и его видно глазами.
type AccountRule struct {
	Account  string   `json:"account"`
	Match    []string `json:"match"`
	DocTypes []string `json:"doc_types,omitempty"`
	Comment  string   `json:"_comment,omitempty"`
}

// DocTypeAccounts — счета по умолчанию для типа документа. Применяются, когда
// по наименованию позиции ничего не подобралось.
type DocTypeAccounts struct {
	Debit   string `json:"debit,omitempty"`
	Credit  string `json:"credit,omitempty"`
	VAT     string `json:"vat,omitempty"`
	Comment string `json:"_comment,omitempty"`
}

type accountsFile struct {
	Comment       json.RawMessage            `json:"_comment,omitempty"`
	CreditDefault string                     `json:"credit_default"`
	VATDefault    string                     `json:"vat_default"`
	ByDebit       map[string]DocTypeAccounts `json:"by_debit"`
	ByDocType     map[string]DocTypeAccounts `json:"by_doc_type"`
	Rules         []AccountRule              `json:"rules"`
	Chart         []AccountEntry             `json:"chart"`
}

type accountsRegistry struct {
	loaded        bool
	creditDefault string
	vatDefault    string
	byDebit       map[string]DocTypeAccounts
	byDocType     map[string]DocTypeAccounts
	rules         []AccountRule
	chart         []AccountEntry
	known         map[string]string // код счёта → подпись
}

var (
	accountsMu sync.RWMutex
	accounts   = &accountsRegistry{}
)

// LoadAccounts читает план счетов и правила подбора. Пустой путь — подбор
// выключен: система ведёт себя как раньше и счёт не предлагает.
func LoadAccounts(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read accounts %s: %w", path, err)
	}

	var f accountsFile
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return fmt.Errorf("parse accounts %s: %w", path, err)
	}

	reg := &accountsRegistry{
		loaded:        true,
		creditDefault: strings.TrimSpace(f.CreditDefault),
		vatDefault:    strings.TrimSpace(f.VATDefault),
		byDebit:       map[string]DocTypeAccounts{},
		byDocType:     map[string]DocTypeAccounts{},
		known:         map[string]string{},
	}
	for _, e := range f.Chart {
		code := strings.TrimSpace(e.Code)
		if code == "" {
			return fmt.Errorf("accounts %s: счёт без кода", path)
		}
		reg.known[code] = strings.TrimSpace(e.Title)
		reg.chart = append(reg.chart, AccountEntry{Code: code, Title: strings.TrimSpace(e.Title)})
	}
	for slug, acc := range f.ByDocType {
		reg.byDocType[normalizeKey(slug)] = acc
	}
	for debit, acc := range f.ByDebit {
		reg.byDebit[strings.TrimSpace(debit)] = acc
	}
	for _, r := range f.Rules {
		r.Account = strings.TrimSpace(r.Account)
		if r.Account == "" || len(r.Match) == 0 {
			continue
		}
		lowered := make([]string, 0, len(r.Match))
		for _, m := range r.Match {
			m = strings.ToLower(strings.TrimSpace(m))
			if m != "" {
				lowered = append(lowered, m)
			}
		}
		r.Match = lowered
		for i, t := range r.DocTypes {
			r.DocTypes[i] = normalizeKey(t)
		}
		reg.rules = append(reg.rules, r)
	}

	// Счёт, которого нет в плане счетов, приёмник 1С не найдёт — а увидим мы
	// это только когда бухгалтер не досчитается документа. Проверяем на старте.
	if len(reg.known) > 0 {
		var unknown []string
		check := func(code, where string) {
			code = strings.TrimSpace(code)
			if code == "" {
				return
			}
			if _, ok := reg.known[code]; !ok {
				unknown = append(unknown, code+" ("+where+")")
			}
		}
		check(reg.creditDefault, "credit_default")
		check(reg.vatDefault, "vat_default")
		for slug, acc := range reg.byDocType {
			check(acc.Debit, slug+".debit")
			check(acc.Credit, slug+".credit")
			check(acc.VAT, slug+".vat")
		}
		for debit, acc := range reg.byDebit {
			check(debit, "by_debit")
			check(acc.Credit, debit+".credit")
			check(acc.VAT, debit+".vat")
		}
		for _, r := range reg.rules {
			check(r.Account, "rules")
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			return fmt.Errorf("accounts %s: счета не описаны в chart: %s",
				path, strings.Join(unknown, ", "))
		}
	}

	accountsMu.Lock()
	accounts = reg
	accountsMu.Unlock()
	return nil
}

func currentAccounts() *accountsRegistry {
	accountsMu.RLock()
	defer accountsMu.RUnlock()
	return accounts
}

// AccountsEnabled — подключён ли подбор счетов.
func AccountsEnabled() bool { return currentAccounts().loaded }

// KnownAccount проверяет код по плану счетов. Нужен, чтобы не пропустить в 1С
// счёт, который модель придумала: пустое значение бухгалтер поправит за
// секунду, выдуманное — будет искать полдня.
func KnownAccount(code string) bool {
	code = strings.TrimSpace(code)
	if code == "" {
		return false
	}
	reg := currentAccounts()
	if len(reg.known) == 0 {
		return false
	}
	_, ok := reg.known[code]
	return ok
}

// AccountTitle — подпись счёта для интерфейса и выгрузки.
func AccountTitle(code string) string {
	return currentAccounts().known[strings.TrimSpace(code)]
}

// AccountForName подбирает счёт по наименованию позиции. Пусто — правила
// молчат, и решение остаётся за типом документа или за человеком.
func AccountForName(name, docType string) string {
	reg := currentAccounts()
	if !reg.loaded || strings.TrimSpace(name) == "" {
		return ""
	}
	lower := strings.ToLower(name)
	docType = normalizeKey(docType)

	for _, r := range reg.rules {
		if len(r.DocTypes) > 0 && !containsKey(r.DocTypes, docType) {
			continue
		}
		for _, m := range r.Match {
			if strings.Contains(lower, m) {
				return r.Account
			}
		}
	}
	return ""
}

// ApplyAccounts проставляет счета в распознанный документ: сначала по каждой
// строке табличной части, затем документу целиком.
//
// Счёт документа — самый частый счёт строк (по нему пойдёт основная сумма);
// если строк нет или ни одна не опознана, берётся счёт по типу документа.
// Значения, введённые человеком вручную, не трогаем: его правка главнее любого
// правила.
func ApplyAccounts(rec *domain.Recognition) {
	reg := currentAccounts()
	if !reg.loaded || rec == nil {
		return
	}
	if rec.Fields == nil {
		rec.Fields = map[string]domain.Field{}
	}

	byType := reg.byDocType[normalizeKey(rec.DocType)]

	// Строки: счёт у каждой позиции свой — канцтовары и аренда не должны
	// попасть на один счёт только потому, что оказались в одном акте.
	counts := map[string]int{}
	for i := range rec.Lines {
		if rec.Lines[i].Account != "" {
			counts[rec.Lines[i].Account]++
			continue
		}
		acc := AccountForName(rec.Lines[i].Name, rec.DocType)
		if acc == "" {
			acc = byType.Debit
		}
		if acc != "" {
			rec.Lines[i].Account = acc
			counts[acc]++
		}
	}

	debit := mostFrequent(counts)
	if debit == "" {
		// Табличной части нет (акт без номенклатуры, счёт услуг) — пробуем
		// подобрать по назначению платежа и наименованию контрагента.
		for _, key := range []string{"purpose", "description", "service", "counterparty"} {
			if f, ok := rec.Fields[key]; ok {
				if acc := AccountForName(f.Value, rec.DocType); acc != "" {
					debit = acc
					break
				}
			}
		}
	}
	if debit == "" {
		debit = byType.Debit
	}

	// Счета расчётов и НДС зависят не от типа документа, а от того, что
	// именно купили: у поставщика услуг и поставщика товара это разные
	// субсчета (60.1.3 против 60.1.1), и входной НДС по товарам учитывается
	// отдельно от НДС по услугам. Поэтому сначала смотрим на подобранный
	// счёт дебета и только потом откатываемся к типу документа.
	credit, vat := "", ""
	if m, ok := reg.byDebit[debit]; ok {
		credit, vat = m.Credit, m.VAT
	}
	if credit == "" {
		credit = byType.Credit
	}
	if credit == "" {
		credit = reg.creditDefault
	}
	if vat == "" {
		vat = byType.VAT
	}
	if vat == "" {
		vat = reg.vatDefault
	}
	// НДС на отдельном счёте показываем только если он в документе есть.
	if rec.Fields["vat_amount"].Value == "" {
		vat = ""
	}

	setSuggested(rec, FieldAccountDebit, debit)
	setSuggested(rec, FieldAccountCredit, credit)
	setSuggested(rec, FieldAccountVAT, vat)
}

// setSuggested записывает подобранное значение, не затирая ручную правку.
func setSuggested(rec *domain.Recognition, key, value string) {
	if value == "" {
		return
	}
	if old, ok := rec.Fields[key]; ok && (old.Source == "manual" || old.Source == "model") {
		return
	}
	rec.Fields[key] = domain.Field{Value: value, Confidence: 0.9, Source: "rule"}
}

func mostFrequent(counts map[string]int) string {
	best, bestN := "", 0
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys) // стабильный выбор при равенстве
	for _, k := range keys {
		if counts[k] > bestN {
			best, bestN = k, counts[k]
		}
	}
	return best
}

func containsKey(list []string, key string) bool {
	for _, v := range list {
		if v == key {
			return true
		}
	}
	return false
}

// AccountsPromptHint — список счетов для модели. Без него модель называет
// счета из российского плана или выдумывает субсчета, которых нет в базе.
func AccountsPromptHint() string {
	reg := currentAccounts()
	if !reg.loaded || len(reg.chart) == 0 {
		return ""
	}
	parts := make([]string, 0, len(reg.chart))
	for _, e := range reg.chart {
		if e.Title == "" {
			parts = append(parts, e.Code)
			continue
		}
		parts = append(parts, e.Code+" — "+e.Title)
	}
	return strings.Join(parts, "; ")
}
