#!/usr/bin/env bash
set -euo pipefail

ROOT="/opt/docflow-deploy/docflow/internal/recognize"
PIPE="$ROOT/pipeline.go"
SPLIT="$ROOT/refine_split.go"

if [[ ! -f "$PIPE" ]]; then
  echo "ОШИБКА: не найден $PIPE"
  exit 1
fi
if [[ ! -f "$SPLIT" ]]; then
  echo "ОШИБКА: не найден $SPLIT"
  exit 1
fi

cp -n "$PIPE" "$PIPE.backup.debug.$(date +%Y%m%d-%H%M%S)"
cp -n "$SPLIT" "$SPLIT.backup.debug.$(date +%Y%m%d-%H%M%S)"

python3 - "$PIPE" "$SPLIT" <<'PY'
import sys
from pathlib import Path

pipe = Path(sys.argv[1])
split = Path(sys.argv[2])

p = pipe.read_text()
s = split.read_text()

# 1) Добавляем helper в pipeline.go после NewPipeline.
anchor = '''func NewPipeline(cfg config.Config, files *filestore.Store, log *slog.Logger) *Pipeline {\n'''
if 'func recogDebugEnabled() bool' not in p:
    helper = r'''func recogDebugEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("RECOGNITION_DEBUG")))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func (p *Pipeline) debugRecognition(stage string, rec domain.Recognition) {
	if p == nil || p.log == nil || !recogDebugEnabled() {
		return
	}
	missing := MissingRequired(rec)
	fields := make(map[string]string, len(rec.Fields))
	for k, v := range rec.Fields {
		fields[k] = fmt.Sprintf("%q conf=%.2f src=%s", v.Value, v.Confidence, v.Source)
	}
	p.log.Info("RECOG_DEBUG recognition",
		"stage", stage,
		"doc_type", rec.DocType,
		"doc_type_conf", rec.DocTypeConfidence,
		"missing", strings.Join(missing, ", "),
		"fields", fmt.Sprintf("%v", fields),
		"lines", len(rec.Lines),
		"table", rec.Table != nil,
	)
	if rec.Table != nil && rec.Table.RawCells != nil {
		ft := rec.Table.RawCells
		p.log.Info("RECOG_DEBUG table selected",
			"stage", stage,
			"source", ft.Source,
			"columns", len(ft.Columns),
			"rows", len(ft.Rows),
			"headers", strings.Join(ft.Columns, " | "),
		)
		limit := len(ft.Rows)
		if limit > 30 {
			limit = 30
		}
		for i := 0; i < limit; i++ {
			p.log.Info("RECOG_DEBUG table row", "stage", stage, "n", i+1, "cells", strings.Join(ft.Rows[i], " | "))
		}
	}
}

'''
    idx = p.find(anchor)
    if idx < 0:
        raise SystemExit('pipeline anchor NewPipeline not found')
    p = p[:idx] + helper + p[idx:]

# 2) Логи OCR и правил сразу после RecognizeDocument.
old = '''\tbyRules := RecognizeDocument(ocrText, layoutText)\n\n\t// Кандидаты на табличную часть.'''
new = '''\tbyRules := RecognizeDocument(ocrText, layoutText)\n\tif recogDebugEnabled() {\n\t\tp.log.Info("RECOG_DEBUG OCR",\n\t\t\t"flat_chars", len([]rune(ocrText)),\n\t\t\t"flat_preview", clip(ocrText, 5000),\n\t\t\t"layout_chars", len([]rune(layoutText)),\n\t\t\t"words", len(page.Words),\n\t\t\t"grids", len(page.Grids),\n\t\t)\n\t}\n\tp.debugRecognition("after_rules", byRules)\n\n\t// Кандидаты на табличную часть.'''
if old not in p:
    raise SystemExit('pipeline anchor after RecognizeDocument not found')
p = p.replace(old, new, 1)

# 3) Логируем needsModel до вызова модели.
old = '''\trec := byRules\n\tif p.model.enabled() && needsModel(byRules, typeConf) {\n\t\tdc := BuildDocContext(page.Words, ocrText, byRules.Table)'''
new = '''\trec := byRules\n\tmodelNeeded := needsModel(byRules, typeConf)\n\tif recogDebugEnabled() {\n\t\tp.log.Info("RECOG_DEBUG model decision",\n\t\t\t"enabled", p.model.enabled(),\n\t\t\t"needs_model", modelNeeded,\n\t\t\t"missing", strings.Join(MissingRequired(byRules), ", "),\n\t\t\t"lines", len(byRules.Lines),\n\t\t\t"table_suspicious", tableLooksSuspicious(byRules),\n\t\t\t"doc_type", byRules.DocType,\n\t\t\t"doc_type_conf", typeConf,\n\t\t)\n\t}\n\tif p.model.enabled() && modelNeeded {\n\t\tdc := BuildDocContext(page.Words, ocrText, byRules.Table)'''
if old not in p:
    raise SystemExit('pipeline model decision anchor not found')
p = p.replace(old, new, 1)

# 4) Логируем точные контексты модели.
old = '''\t\tp.log.Info("контекст для модели",\n\t\t\t"шапка_симв", len([]rune(dc.Head)),\n\t\t\t"подвал_симв", len([]rune(dc.Foot)),\n\t\t\t"таблица_симв", len([]rune(dc.Table)),\n\t\t\t"источник_таблицы", dc.TableSource)'''
new = '''\t\tp.log.Info("контекст для модели",\n\t\t\t"шапка_симв", len([]rune(dc.Head)),\n\t\t\t"подвал_симв", len([]rune(dc.Foot)),\n\t\t\t"таблица_симв", len([]rune(dc.Table)),\n\t\t\t"источник_таблицы", dc.TableSource)\n\t\tif recogDebugEnabled() {\n\t\t\tp.log.Info("RECOG_DEBUG model context",\n\t\t\t\t"head", dc.Head,\n\t\t\t\t"foot", dc.Foot,\n\t\t\t\t"table", dc.Table)\n\t\t}'''
if old not in p:
    raise SystemExit('pipeline context anchor not found')
p = p.replace(old, new, 1)

# 5) Финал после FixFields.
old = '''\tFixFields(rec.Fields, ocrText)\n\tFillTotalsFromTable(&rec)'''
new = '''\tFixFields(rec.Fields, ocrText)\n\tFillTotalsFromTable(&rec)\n\tp.debugRecognition("final_after_fix", rec)'''
if old not in p:
    raise SystemExit('pipeline final anchor not found')
p = p.replace(old, new, 1)

# 6) В refineSplit логируем model responses после decode обоих ответов.
old = '''\t\theadOK = true\n\t}()'''
new = '''\t\theadOK = true\n\t\tif recogDebugEnabled() {\n\t\t\tp.log.Info("RECOG_DEBUG model fields response", "doc_type", headRes.DocType, "fields", fmt.Sprintf("%v", headRes.Fields))\n\t\t}\n\t}()'''
if old not in s:
    raise SystemExit('refine_split head response anchor not found')
s = s.replace(old, new, 1)

old = '''\t\t\ttabOK = true\n\t\t}()'''
new = '''\t\t\ttabOK = true\n\t\t\tif recogDebugEnabled() {\n\t\t\t\tp.log.Info("RECOG_DEBUG model table response",\n\t\t\t\t\t"doc_type", tabRes.DocType,\n\t\t\t\t\t"columns", len(tabRes.Table.Columns),\n\t\t\t\t\t"rows", len(tabRes.Table.Rows),\n\t\t\t\t\t"totals", len(tabRes.Table.Totals),\n\t\t\t\t\t"lines", len(tabRes.Lines))\n\t\t\t}\n\t\t}()'''
if old not in s:
    raise SystemExit('refine_split table response anchor not found')
s = s.replace(old, new, 1)

# add fmt import in refine_split if not present
if '"fmt"' not in s.split(')',1)[0]:
    s = s.replace('"encoding/json"\n\t"strings"', '"encoding/json"\n\t"fmt"\n\t"strings"', 1)

pipe.write_text(p)
split.write_text(s)
PY

echo "Проверяю, что патч установлен..."
grep -n "RECOG_DEBUG" "$PIPE" "$SPLIT" | head -40

echo

echo "ГО не нужен. gofmt НЕ запускается."
echo "Готово. Теперь включи диагностику в /opt/docflow-deploy/.env: RECOGNITION_DEBUG=true"
