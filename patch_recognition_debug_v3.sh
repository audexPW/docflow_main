#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REC="$ROOT/docflow/internal/recognize"
PIPE="$REC/pipeline.go"
REF="$REC/refine_split.go"

if [[ ! -f "$PIPE" || ! -f "$REF" ]]; then
  echo "ОШИБКА: не найдены файлы pipeline.go или refine_split.go"
  exit 1
fi

cp -n "$PIPE" "$PIPE.bak-debug" 2>/dev/null || true
cp -n "$REF" "$REF.bak-debug" 2>/dev/null || true

python3 - "$PIPE" "$REF" <<'PY'
from pathlib import Path
import sys

pipe = Path(sys.argv[1])
ref = Path(sys.argv[2])
P = pipe.read_text()
R = ref.read_text()

if "DOCFLOW_DEBUG_V3_PIPE" in P or "DOCFLOW_DEBUG_V3_REF" in R:
    print("Диагностический патч v3 уже установлен.")
    raise SystemExit(0)

# sort import
if '\t"sort"\n' not in P:
    marker = '\t"path/filepath"\n\t"strings"\n'
    if marker not in P:
        raise SystemExit("pipeline: не найден import block")
    P = P.replace(marker, '\t"path/filepath"\n\t"sort"\n\t"strings"\n', 1)

old = '''\tbyRules := RecognizeDocument(ocrText, layoutText)

\t// Кандидаты на табличную часть.'''
new = '''\tbyRules := RecognizeDocument(ocrText, layoutText)

\t// DOCFLOW_DEBUG_V3_PIPE
\tif recognitionDebugEnabled() {
\t\tp.log.Info("RECOG_DEBUG OCR",
\t\t\t"flat_chars", len([]rune(ocrText)),
\t\t\t"layout_chars", len([]rune(layoutText)),
\t\t\t"words", len(page.Words),
\t\t\t"grids", len(page.Grids),
\t\t\t"ocr_preview", debugText(ocrText, 5000))
\t\tdebugLogFields(p.log, "RECOG_DEBUG RULE_FIELD", byRules.Fields)
\t\tp.log.Info("RECOG_DEBUG RULE_META",
\t\t\t"doc_type", byRules.DocType,
\t\t\t"type_conf", byRules.DocTypeConfidence,
\t\t\t"lines", len(byRules.Lines),
\t\t\t"table", byRules.Table != nil)
\t}

\t// Кандидаты на табличную часть.'''
if old not in P:
    raise SystemExit("pipeline anchor after RecognizeDocument not found")
P = P.replace(old, new, 1)

old = '''\tif best, diag := PickBestFreeTable(cands); best != nil {'''
new = '''\t// DOCFLOW_DEBUG_V3_PIPE
\tif recognitionDebugEnabled() {
\t\tforced := strings.ToLower(strings.TrimSpace(os.Getenv("TABLE_PRIORITY")))
\t\tfor i, c := range cands {
\t\t\tif c == nil {
\t\t\t\tp.log.Warn("RECOG_DEBUG TABLE_CAND", "index", i, "nil", true)
\t\t\t\tcontinue
\t\t\t}
\t\t\tcl := CleanFreeTable(c)
\t\t\tif cl == nil {
\t\t\t\tp.log.Warn("RECOG_DEBUG TABLE_CAND", "index", i, "source", c.Source, "clean", "nil")
\t\t\t\tcontinue
\t\t\t}
\t\t\ts, why := ScoreFreeTable(cl)
\t\t\tbonus := tableSourceBonus[cl.Source]
\t\t\tforcedBonus := 0.0
\t\t\tif forced != "" && forced == strings.ToLower(cl.Source) {
\t\t\t\tforcedBonus = 0.5
\t\t\t}
\t\t\tp.log.Info("RECOG_DEBUG TABLE_CAND",
\t\t\t\t"index", i, "source", cl.Source, "score_base", s,
\t\t\t\t"source_bonus", bonus, "forced_bonus", forcedBonus,
\t\t\t\t"score_final", s+bonus+forcedBonus,
\t\t\t\t"columns", len(cl.Columns), "rows", len(cl.Rows),
\t\t\t\t"header", strings.Join(cl.Columns, " | "), "diag", why)
\t\t\tfor r, row := range cl.Rows {
\t\t\t\tp.log.Info("RECOG_DEBUG TABLE_ROW", "source", cl.Source, "n", r+1, "cells", strings.Join(row, " | "))
\t\t\t}
\t\t\tif len(cl.Totals) > 0 {
\t\t\t\tp.log.Info("RECOG_DEBUG TABLE_TOTALS", "source", cl.Source, "cells", strings.Join(cl.Totals, " | "))
\t\t\t}
\t\t}
\t\tp.log.Info("RECOG_DEBUG TABLE_PRIORITY", "value", os.Getenv("TABLE_PRIORITY"))
\t}

\tif best, diag := PickBestFreeTable(cands); best != nil {'''
if old not in P:
    raise SystemExit("pipeline anchor PickBestFreeTable not found")
P = P.replace(old, new, 1)

old = '''\ttypeConf := byRules.DocTypeConfidence

\t// Модель подключается, когда правилам не хватило:'''
new = '''\ttypeConf := byRules.DocTypeConfidence

\t// DOCFLOW_DEBUG_V3_PIPE
\tif recognitionDebugEnabled() {
\t\tmissing := MissingRequired(byRules)
\t\tp.log.Info("RECOG_DEBUG MODEL_DECISION",
\t\t\t"enabled", p.model.enabled(),
\t\t\t"needs_model", needsModel(byRules, typeConf),
\t\t\t"doc_type", byRules.DocType,
\t\t\t"type_conf", typeConf,
\t\t\t"missing", strings.Join(missing, ","),
\t\t\t"lines", len(byRules.Lines),
\t\t\t"table_suspicious", tableLooksSuspicious(byRules))
\t}

\t// Модель подключается, когда правилам не хватило:'''
if old not in P:
    raise SystemExit("pipeline anchor typeConf not found")
P = P.replace(old, new, 1)

old = '''\t\tdc := BuildDocContext(page.Words, ocrText, byRules.Table)
\t\tp.log.Info("контекст для модели",'''
new = '''\t\tdc := BuildDocContext(page.Words, ocrText, byRules.Table)
\t\t// DOCFLOW_DEBUG_V3_PIPE
\t\tif recognitionDebugEnabled() {
\t\t\tp.log.Info("RECOG_DEBUG MODEL_CONTEXT", "head", dc.Head, "foot", dc.Foot, "table", dc.Table, "table_source", dc.TableSource)
\t\t}
\t\tp.log.Info("контекст для модели",'''
if old not in P:
    raise SystemExit("pipeline anchor BuildDocContext not found")
P = P.replace(old, new, 1)

old = '''\t\tif refined, ok := p.refineSplit(ctx, dc, byRules); ok {
\t\t\trec = refined
\t\t}
\t}

\t// Реквизиты сверяем с текстом документа:'''
new = '''\t\tif refined, ok := p.refineSplit(ctx, dc, byRules); ok {
\t\t\trec = refined
\t\t\t// DOCFLOW_DEBUG_V3_PIPE
\t\t\tif recognitionDebugEnabled() {
\t\t\t\tdebugLogFields(p.log, "RECOG_DEBUG AFTER_MODEL_FIELD", rec.Fields)
\t\t\t\tp.log.Info("RECOG_DEBUG AFTER_MODEL_META", "doc_type", rec.DocType, "lines", len(rec.Lines), "table", rec.Table != nil)
\t\t\t}
\t\t}
\t}

\t// Реквизиты сверяем с текстом документа:'''
if old not in P:
    raise SystemExit("pipeline anchor refineSplit not found")
P = P.replace(old, new, 1)

old = '''\tFixFields(rec.Fields, ocrText)
\tFillTotalsFromTable(&rec)

\t// Счета учёта подбираем последним шагом'''
new = '''\tFixFields(rec.Fields, ocrText)
\tFillTotalsFromTable(&rec)

\t// DOCFLOW_DEBUG_V3_PIPE
\tif recognitionDebugEnabled() {
\t\tdebugLogFields(p.log, "RECOG_DEBUG FINAL_FIELD", rec.Fields)
\t\tp.log.Info("RECOG_DEBUG FINAL_META", "doc_type", rec.DocType, "type_conf", rec.DocTypeConfidence, "lines", len(rec.Lines), "table", rec.Table != nil)
\t\tfor i, l := range rec.Lines {
\t\t\tp.log.Info("RECOG_DEBUG FINAL_LINE", "n", i+1, "name", l.Name, "qty", l.Qty, "unit", l.Unit, "price", l.Price, "amount_no_vat", l.AmountNoVAT, "vat", l.VAT, "amount", l.Amount)
\t\t}
\t}

\t// Счета учёта подбираем последним шагом'''
if old not in P:
    raise SystemExit("pipeline anchor final block not found")
P = P.replace(old, new, 1)

helpers = r'''

// DOCFLOW_DEBUG_V3_PIPE
func recognitionDebugEnabled() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("RECOGNITION_DEBUG")))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

// DOCFLOW_DEBUG_V3_PIPE
func debugText(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + " ...[TRUNCATED]"
}

// DOCFLOW_DEBUG_V3_PIPE
func debugLogFields(log *slog.Logger, label string, fields map[string]domain.Field) {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		f := fields[k]
		log.Info(label, "field", k, "value", f.Value, "confidence", f.Confidence, "source", f.Source)
	}
}
'''
marker = '\nfunc ext(d domain.Document) string {'
if marker not in P:
    raise SystemExit("pipeline anchor ext not found")
P = P.replace(marker, helpers + marker, 1)

# refine_split import slog
if '\t"log/slog"\n' not in R:
    marker = 'import (\n\t"context"\n'
    if marker not in R:
        raise SystemExit("refine_split import block not found")
    R = R.replace(marker, 'import (\n\t"context"\n\t"log/slog"\n', 1)

old = '''\tif !headOK && !tabOK {
\t\treturn domain.Recognition{}, false
\t}
\tp.log.Info("раздельный разбор моделью", "реквизиты", headOK, "таблица", tabOK,
\t\t"источник_таблицы", dc.TableSource)
'''
new = '''\tif !headOK && !tabOK {
\t\tif recognitionDebugEnabled() {
\t\t\tp.log.Warn("RECOG_DEBUG MODEL_RESULT", "head_ok", false, "table_ok", false)
\t\t}
\t\treturn domain.Recognition{}, false
\t}
\tp.log.Info("раздельный разбор моделью", "реквизиты", headOK, "таблица", tabOK,
\t\t"источник_таблицы", dc.TableSource)
\t// DOCFLOW_DEBUG_V3_REF
\tif recognitionDebugEnabled() {
\t\tp.log.Info("RECOG_DEBUG MODEL_RESULT", "head_ok", headOK, "table_ok", tabOK, "model_doc_type", headRes.DocType, "model_field_count", len(headRes.Fields), "model_line_count", len(tabRes.Lines), "model_table_present", tabRes.Table != nil)
\t\tdebugLogJSONFields(p.log, "RECOG_DEBUG MODEL_FIELD", headRes.Fields)
\t\tif mt := convertModelTable(tabRes.Table); mt != nil {
\t\t\tp.log.Info("RECOG_DEBUG MODEL_TABLE", "columns", len(mt.Columns), "rows", len(mt.Rows), "header", strings.Join(mt.Columns, " | "))
\t\t\tfor i, row := range mt.Rows {
\t\t\t\tp.log.Info("RECOG_DEBUG MODEL_TABLE_ROW", "n", i+1, "cells", strings.Join(row, " | "))
\t\t\t}
\t\t\tif len(mt.Totals) > 0 {
\t\t\t\tp.log.Info("RECOG_DEBUG MODEL_TABLE_TOTALS", "cells", strings.Join(mt.Totals, " | "))
\t\t\t}
\t\t}
\t}
'''
if old not in R:
    raise SystemExit("refine_split model result block not found")
R = R.replace(old, new, 1)

R += r'''

// DOCFLOW_DEBUG_V3_REF
func debugLogJSONFields(log *slog.Logger, label string, fields map[string]jsonStr) {
	for k, v := range fields {
		log.Info(label, "field", k, "value", strings.TrimSpace(string(v)))
	}
}
'''

pipe.write_text(P)
ref.write_text(R)
print("DIAGNOSTIC PATCH V3 APPLIED")
PY

cat <<'TXT'

Готово. Go и gofmt не нужны.

1) Открой .env:
   nano .env

2) Добавь:
   RECOGNITION_DEBUG=true

3) Пересобери backend:
   docker compose up -d --build docflow-backend

4) Распознай ОДИН проблемный документ.

5) Покажи лог:
   docker compose logs --tail=2000 docflow-backend | grep 'RECOG_DEBUG'

Пока больше ничего не меняй.
TXT
