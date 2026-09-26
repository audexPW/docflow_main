#!/usr/bin/env bash
set -euo pipefail

ROOT="${DOCFLOW_ROOT:-/opt/docflow-deploy}"
PKG="$ROOT/docflow/internal/recognize"
PIPE="$PKG/pipeline.go"
REFINE="$PKG/refine_split.go"
ENV="$ROOT/.env"

if [[ ! -f "$PIPE" || ! -f "$REFINE" ]]; then
  echo "Не найден DocFlow в $ROOT"
  echo "При необходимости: DOCFLOW_ROOT=/путь/к/docflow-deploy bash $0"
  exit 1
fi

STAMP="$(date +%Y%m%d-%H%M%S)"
cp -a "$PIPE" "$PIPE.backup-debug-$STAMP"
cp -a "$REFINE" "$REFINE.backup-debug-$STAMP"
[[ -f "$ENV" ]] && cp -a "$ENV" "$ENV.backup-debug-$STAMP"

export PIPE REFINE ENV
python3 - <<'PY'
from pathlib import Path
import os

pipe = Path(os.environ['PIPE'])
refine = Path(os.environ['REFINE'])
env = Path(os.environ['ENV'])

# ---------- debug.go ----------
debug_go = pipe.parent / 'debug.go'
if not debug_go.exists():
    debug_go.write_text(r'''package recognize

import (
    "log/slog"
    "sort"
    "strconv"
    "strings"

    "docflow/internal/domain"
)

// recognitionDebugEnabled включает подробную диагностику ТОЛЬКО по env.
// По умолчанию false, чтобы в боевых логах не было текста документов.
func recognitionDebugEnabled() bool {
    v := strings.ToLower(strings.TrimSpace(envOr("RECOGNITION_DEBUG", "false")))
    return v == "1" || v == "true" || v == "yes" || v == "on"
}

func debugClip(s string, max int) string {
    s = strings.TrimSpace(s)
    if max <= 0 || len([]rune(s)) <= max {
        return s
    }
    r := []rune(s)
    return strings.TrimSpace(string(r[:max])) + " …[обрезано]"
}

func debugFields(fields map[string]domain.Field) string {
    if len(fields) == 0 {
        return "<пусто>"
    }
    keys := make([]string, 0, len(fields))
    for k := range fields {
        keys = append(keys, k)
    }
    sort.Strings(keys)
    parts := make([]string, 0, len(keys))
    for _, k := range keys {
        f := fields[k]
        parts = append(parts, k+"="+strconv.Quote(f.Value)+"(conf="+strconv.FormatFloat(f.Confidence, 'f', 2, 64)+",src="+f.Source+")")
    }
    return strings.Join(parts, "; ")
}

func debugLines(lines []domain.LineItem, maxRows int) string {
    if len(lines) == 0 {
        return "<пусто>"
    }
    if maxRows < 1 || maxRows > len(lines) {
        maxRows = len(lines)
    }
    parts := make([]string, 0, maxRows)
    for i := 0; i < maxRows; i++ {
        l := lines[i]
        parts = append(parts,
            "#"+strconv.Itoa(i+1)+" name="+strconv.Quote(l.Name)+
                " qty="+strconv.Quote(l.Qty)+
                " unit="+strconv.Quote(l.Unit)+
                " price="+strconv.Quote(l.Price)+
                " amountNoVAT="+strconv.Quote(l.AmountNoVAT)+
                " amount="+strconv.Quote(l.Amount)+
                " vat="+strconv.Quote(l.VAT)+
                " account="+strconv.Quote(l.Account))
    }
    if len(lines) > maxRows {
        parts = append(parts, "… и ещё "+strconv.Itoa(len(lines)-maxRows)+" строк")
    }
    return strings.Join(parts, " | ")
}

func debugFreeTable(ft *domain.FreeTable, maxRows int) string {
    if ft == nil {
        return "<nil>"
    }
    var b strings.Builder
    b.WriteString("source=")
    b.WriteString(ft.Source)
    b.WriteString(" cols=[")
    b.WriteString(strings.Join(ft.Columns, " | "))
    b.WriteString("]")
    b.WriteString(" rows=")
    limit := len(ft.Rows)
    if maxRows > 0 && limit > maxRows {
        limit = maxRows
    }
    for i := 0; i < limit; i++ {
        b.WriteString("\n  #")
        b.WriteString(strconv.Itoa(i+1))
        b.WriteString(" [")
        b.WriteString(strings.Join(ft.Rows[i], " | "))
        b.WriteString("]")
    }
    if len(ft.Rows) > limit {
        b.WriteString("\n  … и ещё ")
        b.WriteString(strconv.Itoa(len(ft.Rows)-limit))
        b.WriteString(" строк")
    }
    if len(ft.Totals) > 0 {
        b.WriteString("\n  TOTAL [")
        b.WriteString(strings.Join(ft.Totals, " | "))
        b.WriteString("]")
    }
    return debugClip(b.String(), 7000)
}

func debugModelTable(mt *modelTable, maxRows int) string {
    if mt == nil {
        return "<nil>"
    }
    var b strings.Builder
    b.WriteString("cols=[")
    cols := make([]string, 0, len(mt.Columns))
    for _, c := range mt.Columns {
        cols = append(cols, strings.TrimSpace(string(c)))
    }
    b.WriteString(strings.Join(cols, " | "))
    b.WriteString("] rows=")
    limit := len(mt.Rows)
    if maxRows > 0 && limit > maxRows {
        limit = maxRows
    }
    for i := 0; i < limit; i++ {
        row := make([]string, 0, len(mt.Rows[i]))
        for _, c := range mt.Rows[i] {
            row = append(row, strings.TrimSpace(string(c)))
        }
        b.WriteString("\n  #")
        b.WriteString(strconv.Itoa(i+1))
        b.WriteString(" [")
        b.WriteString(strings.Join(row, " | "))
        b.WriteString("]")
    }
    if len(mt.Rows) > limit {
        b.WriteString("\n  … и ещё ")
        b.WriteString(strconv.Itoa(len(mt.Rows)-limit))
        b.WriteString(" строк")
    }
    return debugClip(b.String(), 7000)
}

func debugLogRec(log *slog.Logger, stage string, rec domain.Recognition) {
    if !recognitionDebugEnabled() {
        return
    }
    missing := MissingRequired(rec)
    log.Warn("RECOG_DEBUG "+stage,
        "type", rec.DocType,
        "type_conf", rec.DocTypeConfidence,
        "missing", strings.Join(missing, ","),
        "fields", debugFields(rec.Fields),
        "lines_count", len(rec.Lines),
        "lines", debugLines(rec.Lines, 12),
        "table", debugFreeTable(func() *domain.FreeTable {
            if rec.Table != nil {
                return rec.Table.RawCells
            }
            return nil
        }(), 12),
    )
}
''', encoding='utf-8')

# ---------- pipeline.go ----------
s = pipe.read_text(encoding='utf-8')
old = '''\tbyRules := RecognizeDocument(ocrText, layoutText)\n\n\t// Кандидаты на табличную часть.'''
new = '''\tbyRules := RecognizeDocument(ocrText, layoutText)\n\tif recognitionDebugEnabled() {\n\t\tp.log.Warn("RECOG_DEBUG OCR",\n\t\t\t"source", filepath.Base(srcPath),\n\t\t\t"flat_chars", len([]rune(ocrText)),\n\t\t\t"layout_chars", len([]rune(layoutText)),\n\t\t\t"words", len(page.Words),\n\t\t\t"grids", len(page.Grids),\n\t\t\t"flat_text", debugClip(ocrText, 9000),\n\t\t\t"layout_text", debugClip(layoutText, 5000),\n\t\t)\n\t}\n\tdebugLogRec(p.log, "AFTER_RULES", byRules)\n\n\t// Кандидаты на табличную часть.'''
if old not in s:
    raise SystemExit('pipeline anchor 1 not found')
s = s.replace(old, new, 1)

old = '''\tif best, diag := PickBestFreeTable(cands); best != nil {'''
new = '''\tif recognitionDebugEnabled() {\n\t\tforced := strings.ToLower(strings.TrimSpace(os.Getenv("TABLE_PRIORITY")))\n\t\tfor _, c := range cands {\n\t\t\tcl := CleanFreeTable(c)\n\t\t\tif cl == nil {\n\t\t\t\tp.log.Warn("RECOG_DEBUG TABLE_CANDIDATE_REJECTED", "source", c.Source, "reason", "CleanFreeTable=nil")\n\t\t\t\tcontinue\n\t\t\t}\n\t\t\tsc, why := ScoreFreeTable(cl)\n\t\t\tsc += tableSourceBonus[cl.Source]\n\t\t\teffective := sc\n\t\t\tforcedHit := forced != "" && forced == strings.ToLower(cl.Source)\n\t\t\tif forcedHit {\n\t\t\t\teffective += 0.5\n\t\t\t}\n\t\t\tp.log.Warn("RECOG_DEBUG TABLE_CANDIDATE",\n\t\t\t\t"source", cl.Source,\n\t\t\t\t"score_before_forced", sc,\n\t\t\t\t"forced", forced,\n\t\t\t\t"forced_hit", forcedHit,\n\t\t\t\t"effective_score", effective,\n\t\t\t\t"diag", why,\n\t\t\t\t"table", debugFreeTable(cl, 12),\n\t\t\t)\n\t\t}\n\t}\n\n\tif best, diag := PickBestFreeTable(cands); best != nil {'''
if old not in s:
    raise SystemExit('pipeline anchor 2 not found')
s = s.replace(old, new, 1)

old = '''\ttypeConf := byRules.DocTypeConfidence\n\n\t// Модель подключается,'''
new = '''\ttypeConf := byRules.DocTypeConfidence\n\tif recognitionDebugEnabled() {\n\t\treasons := modelNeedReasons(byRules, typeConf)\n\t\tp.log.Warn("RECOG_DEBUG MODEL_DECISION",\n\t\t\t"enabled", p.model.enabled(),\n\t\t\t"will_call", p.model.enabled() && len(reasons) > 0,\n\t\t\t"reasons", strings.Join(reasons, "; "),\n\t\t\t"type", byRules.DocType,\n\t\t\t"type_conf", typeConf,\n\t\t)\n\t}\n\n\t// Модель подключается,'''
if old not in s:
    raise SystemExit('pipeline anchor 3 not found')
s = s.replace(old, new, 1)

old = '''\tFixFields(rec.Fields, ocrText)\n\tFillTotalsFromTable(&rec)\n'''
new = '''\tdebugLogRec(p.log, "AFTER_MODEL", rec)\n\tFixFields(rec.Fields, ocrText)\n\tFillTotalsFromTable(&rec)\n\tdebugLogRec(p.log, "FINAL_AFTER_FIX", rec)\n'''
if old not in s:
    raise SystemExit('pipeline anchor 4 not found')
s = s.replace(old, new, 1)

old = '''// needsModel решает, звать ли модель после прохода правил.\nfunc needsModel(rec domain.Recognition, typeConf float64) bool {\n\tif rec.DocType == DocTypeUnknown || rec.DocType == "" {\n\t\treturn true\n\t}\n\tif typeConf < typeConfidenceThreshold {\n\t\treturn true\n\t}\n\tif len(MissingRequired(rec)) > 0 {\n\t\treturn true\n\t}\n\t// Тип предполагает номенклатуру, а таблица не разобрана — правила бессильны,\n\t// пусть попробует модель.\n\tif len(rec.Lines) == 0 && typeHasLines(rec.DocType) {\n\t\treturn true\n\t}\n\t// Даже если обязательные поля и итог совпали, сама таблица может\n\t// быть структурно испорчена. В этом случае нужен дополнительный\n\t// semantic pass локальной модели.\n\treturn tableLooksSuspicious(rec)\n}\n'''
new = '''// modelNeedReasons возвращает ТОЧНО те причины, по которым needsModel должен\n// вызвать модель. Это только диагностика/разложение условия, логика решения не меняется.\nfunc modelNeedReasons(rec domain.Recognition, typeConf float64) []string {\n\treasons := make([]string, 0, 5)\n\tif rec.DocType == DocTypeUnknown || rec.DocType == "" {\n\t\treasons = append(reasons, "unknown_doc_type")\n\t}\n\tif typeConf < typeConfidenceThreshold {\n\t\treasons = append(reasons, "type_conf<0.5")\n\t}\n\tif missing := MissingRequired(rec); len(missing) > 0 {\n\t\treasons = append(reasons, "missing_required=["+strings.Join(missing, ",")+"]")\n\t}\n\tif len(rec.Lines) == 0 && typeHasLines(rec.DocType) {\n\t\treasons = append(reasons, "lines_missing")\n\t}\n\tif tableLooksSuspicious(rec) {\n\t\treasons = append(reasons, "table_suspicious")\n\t}\n\treturn reasons\n}\n\n// needsModel решает, звать ли модель после прохода правил.\nfunc needsModel(rec domain.Recognition, typeConf float64) bool {\n\treturn len(modelNeedReasons(rec, typeConf)) > 0\n}\n'''
if old not in s:
    raise SystemExit('pipeline needsModel block not found')
s = s.replace(old, new, 1)

pipe.write_text(s, encoding='utf-8')

# ---------- refine_split.go ----------
s = refine.read_text(encoding='utf-8')
old = '''\tvar (\n\t\twg              sync.WaitGroup\n\t\theadRes, tabRes modelExtract\n\t\theadOK, tabOK   bool\n\t)\n'''
new = '''\tif recognitionDebugEnabled() {\n\t\tp.log.Warn("RECOG_DEBUG MODEL_INPUT",\n\t\t\t"rule_type", byRules.DocType,\n\t\t\t"table_source", dc.TableSource,\n\t\t\t"head", debugClip(dc.Head, 6000),\n\t\t\t"foot", debugClip(dc.Foot, 3000),\n\t\t\t"table", debugClip(dc.Table, 7000),\n\t\t)\n\t}\n\n\tvar (\n\t\twg              sync.WaitGroup\n\t\theadRes, tabRes modelExtract\n\t\theadOK, tabOK   bool\n\t)\n'''
if old not in s:
    raise SystemExit('refine anchor 1 not found')
s = s.replace(old, new, 1)

old = '''\t\theadOK = true\n\t}()\n'''
new = '''\t\theadOK = true\n\t\tif recognitionDebugEnabled() {\n\t\t\tp.log.Warn("RECOG_DEBUG MODEL_FIELDS_RESULT",\n\t\t\t\t"doc_type", headRes.DocType,\n\t\t\t\t"fields", debugFields(func() map[string]domain.Field {\n\t\t\t\t\tout := make(map[string]domain.Field, len(headRes.Fields))\n\t\t\t\t\tfor k, v := range headRes.Fields {\n\t\t\t\t\t\tout[k] = domain.Field{Value: strings.TrimSpace(string(v)), Confidence: 0.85, Source: "model"}\n\t\t\t\t\t}\n\t\t\t\t\treturn out\n\t\t\t\t}()),\n\t\t\t)\n\t\t}\n\t}()\n'''
if old not in s:
    raise SystemExit('refine anchor 2 not found')
s = s.replace(old, new, 1)

old = '''\t\t\ttabOK = true\n\t\t}()\n'''
new = '''\t\t\ttabOK = true\n\t\t\tif recognitionDebugEnabled() {\n\t\t\t\tp.log.Warn("RECOG_DEBUG MODEL_TABLE_RESULT",\n\t\t\t\t\t"table", debugModelTable(tabRes.Table, 12),\n\t\t\t\t\t"lines_count", len(tabRes.Lines),\n\t\t\t\t\t)\n\t\t\t}\n\t\t}()\n'''
if old not in s:
    raise SystemExit('refine anchor 3 not found')
s = s.replace(old, new, 1)

old = '''\treturn domain.Recognition{\n\t\tDocType:           docType,\n\t\tDocTypeConfidence: conf,\n\t\tFields:            fields,\n\t\tLines:             lines,\n\t\tTable:             table,\n\t}, true\n}\n'''
new = '''\tout := domain.Recognition{\n\t\tDocType:           docType,\n\t\tDocTypeConfidence: conf,\n\t\tFields:            fields,\n\t\tLines:             lines,\n\t\tTable:             table,\n\t}\n\tif recognitionDebugEnabled() {\n\t\tp.log.Warn("RECOG_DEBUG MODEL_MERGED",\n\t\t\t"head_ok", headOK,\n\t\t\t"table_ok", tabOK,\n\t\t\t"doc_type", out.DocType,\n\t\t\t"fields", debugFields(out.Fields),\n\t\t\t"lines", debugLines(out.Lines, 12),\n\t\t\t"table", debugFreeTable(func() *domain.FreeTable {\n\t\t\t\tif out.Table != nil {\n\t\t\t\t\treturn out.Table.RawCells\n\t\t\t\t}\n\t\t\t\treturn nil\n\t\t\t}(), 12),\n\t\t)\n\t}\n\treturn out, true\n}\n'''
if old not in s:
    raise SystemExit('refine return block not found')
s = s.replace(old, new, 1)

refine.write_text(s, encoding='utf-8')

# ---------- .env ----------
if env.exists():
    lines = env.read_text(encoding='utf-8').splitlines()
    found = False
    out = []
    for line in lines:
        if line.startswith('RECOGNITION_DEBUG='):
            out.append('RECOGNITION_DEBUG=true')
            found = True
        else:
            out.append(line)
    if not found:
        out.append('RECOGNITION_DEBUG=true')
    env.write_text('\n'.join(out) + '\n', encoding='utf-8')
else:
    env.write_text('RECOGNITION_DEBUG=true\n', encoding='utf-8')
PY

# gofmt -w "$PKG/debug.go" "$PIPE" "$REFINE"

echo "=============================================="
echo " DocFlow recognition DEBUG patch applied"
echo " $STAMP"
echo "=============================================="
echo "Добавлено подробное логирование без изменения алгоритма распознавания."
echo
printf '%s\n' "После проверки документа смотри только строки RECOG_DEBUG:" 
printf '%s\n' "  docker compose logs --tail=500 docflow-backend | grep RECOG_DEBUG"
echo
echo "Для удобства можно следить онлайн:"
printf '%s\n' "  docker compose logs -f docflow-backend | grep RECOG_DEBUG"
echo
printf '%s\n' "Подробный debug включён в .env: RECOGNITION_DEBUG=true"
printf '%s\n' "После того как пришлёшь логи, этот режим лучше выключить."
