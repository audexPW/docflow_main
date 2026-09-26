package worker

import (
	"testing"

	"docflow/internal/domain"
	"docflow/internal/recognize"
)

func newRouter(autoExport bool) *Recognizer {
	return &Recognizer{autoExport: autoExport}
}

func fullInvoice() domain.Recognition {
	return domain.Recognition{
		DocType: "invoice",
		// Уверенность выставлена явно: значения ниже
		// recognize.MinTrustedConfidence считаются догадками и приравниваются
		// к отсутствующим.
		Fields: map[string]domain.Field{
			"number": {Value: "145", Confidence: 0.7, Source: "rule"},
			"date":   {Value: "12.03.2026", Confidence: 0.7, Source: "rule"},
			"total":  {Value: "375.00", Confidence: 0.7, Source: "rule"},
			"unp":    {Value: "191234567", Confidence: 0.7, Source: "rule"},
		},
	}
}

// Полностью распознанный документ уходит в 1С сразу, без оператора.
func TestRouteFullyRecognizedGoesStraightToExport(t *testing.T) {
	t.Cleanup(func() { recognize.SetLocale("ru") })
	recognize.SetLocale("by")

	rec := fullInvoice()
	if got := newRouter(true).route(rec, recognize.MissingRequired(rec)); got != domain.StatusConfirmed {
		t.Fatalf("ожидался confirmed (сразу в 1С), получено %q", got)
	}
}

// Распознана часть — уходит частичная выгрузка, остальное сотрудник вводит руками.
func TestRoutePartialGoesToNeedsInput(t *testing.T) {
	t.Cleanup(func() { recognize.SetLocale("ru") })
	recognize.SetLocale("by")

	rec := fullInvoice()
	delete(rec.Fields, "unp")

	if got := newRouter(true).route(rec, recognize.MissingRequired(rec)); got != domain.StatusNeedsInput {
		t.Fatalf("ожидался needs_input (частичная выгрузка + ручной ввод), получено %q", got)
	}
}

// Не сошедшаяся табличная часть НЕ должна задерживать документ: шапка уходит в
// 1С как обычно, позиции сотрудник вносит руками.
func TestRouteUnreconciledLinesDoNotBlockExport(t *testing.T) {
	t.Cleanup(func() { recognize.SetLocale("ru") })
	recognize.SetLocale("by")

	rec := fullInvoice()
	rec.Lines = []domain.LineItem{
		{Name: "Бумага", Amount: "125.00"},
		{Name: "Картридж", Amount: "9999.00"}, // итог 375.00 — не сходится
	}
	if !recognize.LinesTotalMismatch(rec, recognize.LinesTolerance) {
		t.Fatal("подготовка теста: расхождение должно определяться")
	}

	if got := newRouter(true).route(rec, recognize.MissingRequired(rec)); got != domain.StatusConfirmed {
		t.Fatalf("документ не должен задерживаться из-за таблицы, получено %q", got)
	}
}

// Неопознанный тип по-прежнему уходит оператору: в 1С его мапить не по чему.
func TestRouteUnknownTypeGoesToOperator(t *testing.T) {
	rec := fullInvoice()
	rec.DocType = recognize.DocTypeUnknown

	if got := newRouter(true).route(rec, nil); got != domain.StatusNeedsReview {
		t.Fatalf("ожидался needs_review, получено %q", got)
	}
}

// Выключенный авто-экспорт возвращает прежнее поведение: всё через оператора.
func TestRouteAutoExportDisabled(t *testing.T) {
	rec := fullInvoice()
	if got := newRouter(false).route(rec, nil); got != domain.StatusNeedsReview {
		t.Fatalf("при AUTO_EXPORT_ENABLED=false ожидался needs_review, получено %q", got)
	}
}

// Тип опознан, но объект-приёмник в 1С для него не согласован: документ уходит
// оператору, а не в базу «куда-нибудь». Это защита от тихой раскладки не в тот
// справочник при появлении нового типа документа.
func TestRouteUnmappedTypeGoesToOperator(t *testing.T) {
	t.Cleanup(func() { recognize.SetLocale("ru") })
	recognize.SetLocale("by")

	r := &Recognizer{autoExport: true, requireMapping: true}
	rec := fullInvoice() // встроенный реестр идёт без маппинга на 1С

	if got := r.route(rec, recognize.MissingRequired(rec)); got != domain.StatusNeedsReview {
		t.Fatalf("без маппинга на 1С ожидался needs_review, получено %q", got)
	}
}
