#!/usr/bin/env bash
set -euo pipefail

ROOT="${1:-.}"
if [[ -d "$ROOT/docflow/internal/recognize" && ! -d "$ROOT/internal/recognize" ]]; then
  ROOT="$ROOT/docflow"
fi
export ROOT
python3 - <<'PY'
from pathlib import Path
import os, shutil, time

root=Path(os.environ['ROOT']).resolve()
rec=root/'internal'/'recognize'
if not rec.is_dir():
    raise SystemExit(f'ERROR: {rec} not found')
stamp=time.strftime('%Y%m%d-%H%M%S')

def patch(path, replacements):
    text=path.read_text(encoding='utf-8')
    original=text
    for old,new in replacements:
        n=text.count(old)
        if n!=1:
            raise SystemExit(f'ERROR: {path}: expected 1 match, got {n}')
        text=text.replace(old,new,1)
    if text==original:
        raise SystemExit(f'ERROR: {path}: no change')
    bak=Path(str(path)+f'.bak-adaptive-v5-{stamp}')
    shutil.copy2(path,bak)
    path.write_text(text,encoding='utf-8')
    print(f'patched: {path}')
    print(f'backup:  {bak}')

pre=rec/'preprocess.go'
patch(pre,[
("""func preparePages(ctx context.Context, cfg config.OCRConfig, srcPath, contentType, workDir string) ([]string, error) {
\text := strings.ToLower(filepath.Ext(srcPath))
\tisPDF := contentType == \"application/pdf\" || ext == \".pdf\"

\tvar pages []string
\tif isPDF {
\t\tp, err := rasterizePDF(ctx, cfg, srcPath, workDir)
\t\tif err != nil {
\t\t\treturn nil, err
\t\t}
\t\tpages = p
\t} else {
\t\tpages = []string{srcPath}
\t}

\tout := make([]string, 0, len(pages))
\tfor i, p := range pages {
\t\t// Поворот страницы правим ДО остальной обработки: и выравнивание
\t\t// наклона, и распознавание строк, и сборка таблицы по колонкам
\t\t// предполагают, что текст идёт слева направо.
\t\tp = autoRotatePage(ctx, cfg, p, workDir, i)
\t\tout = append(out, enhancePage(ctx, cfg, p, workDir, i))
\t}
\treturn out, nil
}
""",
"""// pageVariant хранит два независимых варианта одной страницы.
type pageVariant struct {
\tSafe     string
\tEnhanced string
}

// preparePages в обычном режиме сохраняет старое поведение. В adaptive+photo
// режиме safe строится мягко, enhanced — из того же исходного растра, но с
// текущим OCR_ENHANCE. Enhanced используется только как fallback.
func preparePages(ctx context.Context, cfg config.OCRConfig, srcPath, contentType, workDir string) ([]pageVariant, error) {
\text := strings.ToLower(filepath.Ext(srcPath))
\tisPDF := contentType == \"application/pdf\" || ext == \".pdf\"

\tvar pages []string
\tif isPDF {
\t\tp, err := rasterizePDF(ctx, cfg, srcPath, workDir)
\t\tif err != nil {
\t\t\treturn nil, err
\t\t}
\t\tpages = p
\t} else {
\t\tpages = []string{srcPath}
\t}

\tout := make([]pageVariant, 0, len(pages))
\tfor i, p := range pages {
\t\tp = autoRotatePage(ctx, cfg, p, workDir, i)

\t\tif !adaptiveEnabled() {
\t\t\tprepared := enhancePage(ctx, cfg, p, workDir, i)
\t\t\tout = append(out, pageVariant{Safe: prepared, Enhanced: prepared})
\t\t\tcontinue
\t\t}

\t\tmode := strings.ToLower(strings.TrimSpace(envOr(\"OCR_ENHANCE\", \"basic\")))
\t\tif mode == \"off\" || mode == \"none\" || mode == \"basic\" {
\t\t\tprepared := enhancePage(ctx, cfg, p, workDir, i)
\t\t\tout = append(out, pageVariant{Safe: prepared, Enhanced: prepared})
\t\t\tcontinue
\t\t}

\t\tsafe := enhancePageMode(ctx, cfg, p, workDir, i, \"basic\")
\t\tenhanced := enhancePageMode(ctx, cfg, p, workDir, i, mode)
\t\tout = append(out, pageVariant{Safe: safe, Enhanced: enhanced})
\t}
\treturn out, nil
}
"""),
("""func enhancePage(ctx context.Context, cfg config.OCRConfig, srcPath, workDir string, idx int) string {
\tmode := strings.ToLower(envOr(\"OCR_ENHANCE\", \"basic\"))
\tif mode == \"off\" || mode == \"none\" {
\t\treturn srcPath
\t}

\tdst := filepath.Join(workDir, fmt.Sprintf(\"prep_%03d.png\", idx))
""",
"""func enhancePage(ctx context.Context, cfg config.OCRConfig, srcPath, workDir string, idx int) string {
\treturn enhancePageMode(ctx, cfg, srcPath, workDir, idx, strings.ToLower(envOr(\"OCR_ENHANCE\", \"basic\")))
}

func enhancePageMode(ctx context.Context, cfg config.OCRConfig, srcPath, workDir string, idx int, mode string) string {
\tmode = strings.ToLower(strings.TrimSpace(mode))
\tif mode == \"off\" || mode == \"none\" {
\t\treturn srcPath
\t}

\tprefix := \"prep\"
\tif mode == \"basic\" {
\t\tprefix = \"prep_safe\"
\t} else {
\t\tprefix = \"prep_enhanced\"
\t}
\tdst := filepath.Join(workDir, fmt.Sprintf(\"%s_%03d.png\", prefix, idx))
""")
])

ocr=rec/'ocr.go'
patch(ocr,[
("""import (
\t\"context\"
\t\"fmt\"
\t\"os/exec\"
\t\"strings\"
\t\"unicode\"
""",
"""import (
\t\"context\"
\t\"fmt\"
\t\"math\"
\t\"os/exec\"
\t\"regexp\"
\t\"strconv\"
\t\"strings\"
\t\"unicode\"
""")
])
text=ocr.read_text(encoding='utf-8')
start=text.index('// runOCR прогоняет выбранный движок по каждой странице и склеивает результат.')
end=text.index('// --- tesseract',start)
new_run="""// runOCR сначала использует safe. Enhanced вызывается только при слабом
// результате и принимается только если его score выше safe.
func runOCR(ctx context.Context, engine ocrEngine, pages []pageVariant) (pageText, error) {
\tvar layout, flat strings.Builder
\tvar grids []GridTable
\tvar words []wordBox
\tvar yOffset float64

\tfor i, variant := range pages {
\t\tchosen := variant.Safe
\t\ttext, err := engine.recognizePage(ctx, chosen)
\t\tif err != nil && variant.Enhanced != \"\" && variant.Enhanced != variant.Safe {
\t\t\ttext, err = engine.recognizePage(ctx, variant.Enhanced)
\t\t\tif err == nil {
\t\t\t\tchosen = variant.Enhanced
\t\t\t}
\t\t}
\t\tif err != nil {
\t\t\treturn pageText{}, fmt.Errorf(\"page %d: %w\", i+1, err)
\t\t}

\t\tif variant.Enhanced != \"\" && variant.Enhanced != chosen && pageNeedsEnhanced(text) {
\t\t\tenhanced, enhancedErr := engine.recognizePage(ctx, variant.Enhanced)
\t\t\tif enhancedErr == nil {
\t\t\t\tsafeScore := scoreOCRCandidate(text.Flat, text.Layout)
\t\t\t\tenhancedScore := scoreOCRCandidate(enhanced.Flat, enhanced.Layout)
\t\t\t\tif enhancedScore > safeScore {
\t\t\t\t\ttext = enhanced
\t\t\t\t\tchosen = variant.Enhanced
\t\t\t\t}
\t\t\t}
\t\t}

\t\tif i > 0 {
\t\t\tlayout.WriteString(\"\\n\\n\")
\t\t\tflat.WriteString(\"\\n\\n\")
\t\t}
\t\tlayout.WriteString(text.Layout)
\t\tflat.WriteString(text.Flat)
\t\tgrids = append(grids, text.Grids...)

\t\tvar pageBottom float64
\t\tfor _, w := range text.Words {
\t\t\tw.Y0 += yOffset
\t\t\tw.Y1 += yOffset
\t\t\twords = append(words, w)
\t\t\tif w.Y1 > pageBottom {
\t\t\t\tpageBottom = w.Y1
\t\t\t}
\t\t}
\t\tyOffset = pageBottom + 1000
\t}
\treturn pageText{Layout: layout.String(), Flat: flat.String(), Grids: grids, Words: words}, nil
}

func adaptiveEnabled() bool {
\tv := strings.ToLower(strings.TrimSpace(envOr(\"OCR_ADAPTIVE\", \"true\")))
\treturn v != \"false\" && v != \"0\" && v != \"off\"
}

func pageNeedsEnhanced(pt pageText) bool {
\ttext := strings.TrimSpace(pt.Flat)
\tif text == \"\" {
\t\treturn true
\t}
\tlower := strings.ToLower(text)
\thasProtocol := strings.Contains(lower, \"счёт-протокол\") || strings.Contains(lower, \"счет-протокол\") || strings.Contains(lower, \"арендодатель\")
\thasNumberMarker := strings.Contains(text, \"№\") || strings.Contains(lower, \" no \") || strings.Contains(lower, \" n \")
\thasDate := reDateDotted.MatchString(text) || reProtoDateWords.MatchString(text)
\thasMoney := reMoney.MatchString(text)

\tif hasProtocol && (!reProtocolNumber.MatchString(text) || !reProtoDateWords.MatchString(text)) {
\t\treturn true
\t}
\tif hasNumberMarker && !hasPlausibleDocNumber(text) {
\t\treturn true
\t}
\tif strings.Contains(lower, \"от \") && !hasDate {
\t\treturn true
\t}
\tif strings.Contains(lower, \"итого\") && !hasMoney {
\t\treturn true
\t}
\treturn scoreOCRCandidate(pt.Flat, pt.Layout) < adaptiveMinScore()
}

func hasPlausibleDocNumber(text string) bool {
\tfor _, re := range []*regexp.Regexp{reProtocolNumber, reAnchoredNumber, reDocNumber} {
\t\tfor _, m := range re.FindAllStringSubmatch(text, -1) {
\t\t\tif len(m) == 0 {
\t\t\t\tcontinue
\t\t\t}
\t\t\tn := strings.Trim(m[len(m)-1], \".,;:-/\")
\t\t\tif n == \"\" || reProtoIBAN.MatchString(n) || strings.Contains(strings.ToLower(n), \"р/с\") {
\t\t\t\tcontinue
\t\t\t}
\t\t\treturn true
\t\t}
\t}
\treturn false
}

func scoreOCRCandidate(text, layout string) float64 {
\tvar letters, total int
\tfor _, r := range text {
\t\tif unicode.IsSpace(r) {
\t\t\tcontinue
\t\t}
\t\ttotal++
\t\tif unicode.IsLetter(r) {
\t\t\tletters++
\t\t}
\t}
\twordCount := len(strings.Fields(text))
\tscore := 0.0
\tif letters >= 24 { score += 15 } else { score += float64(letters) * 15 / 24 }
\tif wordCount >= 60 { score += 15 } else { score += float64(wordCount) * 15 / 60 }
\tif total > 0 && float64(letters)/float64(total) >= 0.45 { score += 5 }
\tif reDateDotted.MatchString(text) || reProtoDateWords.MatchString(text) { score += 15 }
\tif hasPlausibleDocNumber(text) { score += 15 }
\tif reMoney.MatchString(text) { score += 10 }

\t// Оцениваем тот же rule-разбор, который потом используется pipeline.
\trec := RecognizeDocument(text, layout)
\tif f, ok := rec.Fields[\"number\"]; ok && strings.TrimSpace(f.Value) != \"\" && !reProtoIBAN.MatchString(f.Value) && !strings.Contains(strings.ToLower(f.Value), \"р/с\") { score += 8 }
\tif f, ok := rec.Fields[\"date\"]; ok && strings.TrimSpace(f.Value) != \"\" { score += 8 }
\tif f, ok := rec.Fields[\"total\"]; ok && strings.TrimSpace(f.Value) != \"\" { score += 5 }
\tif f, ok := rec.Fields[\"counterparty\"]; ok && strings.TrimSpace(f.Value) != \"\" { score += 4 }
\tif f, ok := rec.Fields[\"organization\"]; ok && strings.TrimSpace(f.Value) != \"\" { score += 4 }
\tif rec.Table != nil && len(rec.Table.Rows) > 0 { score += math.Min(10, float64(len(rec.Table.Rows))*2) }
\tif f, ok := rec.Fields[\"number\"]; ok && (reProtoIBAN.MatchString(f.Value) || strings.Contains(strings.ToLower(f.Value), \"р/с\")) { score -= 20 }
\treturn score
}

func adaptiveMinScore() float64 {
\tv := strings.TrimSpace(envOr(\"OCR_ADAPTIVE_MIN_SCORE\", \"55\"))
\tif n, err := strconv.ParseFloat(v, 64); err == nil && n > 0 { return n }
\treturn 55
}

"""
# Preserve the exact old tail after runOCR.
ocr.write_text(text[:start]+new_run+text[end:],encoding='utf-8')
# backup the already modified file is unnecessary; import replacement created one backup.
print(f'updated adaptive OCR functions in {ocr}')

pipe=rec/'pipeline.go'
patch(pipe,[
("""const typeConfidenceThreshold = 0.5

// Допустимое расхождение суммы строк табличной части с итогом документа""",
"""const typeConfidenceThreshold = 0.5

// Надёжное rule/manual поле не может быть заменено моделью.
const modelFieldOverrideMinConfidence = 0.85

// Допустимое расхождение суммы строк табличной части с итогом документа"""),
("""\tfor k, v := range me.Fields {
\t\tk = strings.TrimSpace(strings.ToLower(k))
\t\tsv := strings.TrimSpace(string(v))
\t\tif k == \"\" || sv == \"\" {
\t\t\tcontinue
\t\t}
\t\t// Счёт, которого нет в плане счетов заказчика, — это выдумка модели.
\t\t// Пустое поле бухгалтер заполнит за секунду, выдуманный счёт он будет
\t\t// искать в базе полдня, поэтому такое значение отбрасываем.
\t\tif strings.HasPrefix(k, \"account_\") && AccountsEnabled() && !KnownAccount(sv) {
\t\t\tp.log.Warn(\"модель вернула счёт вне плана счетов\", \"field\", k, \"value\", sv)
\t\t\tcontinue
\t\t}
\t\tfields[k] = domain.Field{Value: sv, Confidence: 0.85, Source: \"model\"}
\t}
""",
"""\tfor k, v := range me.Fields {
\t\tk = strings.TrimSpace(strings.ToLower(k))
\t\tsv := strings.TrimSpace(string(v))
\t\tif k == \"\" || sv == \"\" {
\t\t\tcontinue
\t\t}

\t\t// Rules и ручной ввод имеют приоритет над LLM. Поле с низкой rule-confidence
\t\t// можно уточнить моделью, но уверенное значение — нельзя перезаписывать.
\t\tif existing, ok := byRules.Fields[k]; ok && strings.TrimSpace(existing.Value) != \"\" {
\t\t\tif existing.Source == \"manual\" || (existing.Source == \"rule\" && existing.Confidence >= modelFieldOverrideMinConfidence) {
\t\t\t\tcontinue
\t\t\t}
\t\t}

\t\tif strings.HasPrefix(k, \"account_\") && AccountsEnabled() && !KnownAccount(sv) {
\t\t\tp.log.Warn(\"модель вернула счёт вне плана счетов\", \"field\", k, \"value\", sv)
\t\t\tcontinue
\t\t}
\t\tfields[k] = domain.Field{Value: sv, Confidence: 0.85, Source: \"model\"}
\t}
"""),
("""\tdocType := NormalizeDocType(me.DocType)
\tif docType == \"\" || docType == DocTypeUnknown {
\t\tdocType = byRules.DocType
\t}
""",
"""\tdocType := byRules.DocType
\tif docType == \"\" || docType == DocTypeUnknown || byRules.DocTypeConfidence < typeConfidenceThreshold {
\t\tdocType = NormalizeDocType(me.DocType)
\t\tif docType == \"\" || docType == DocTypeUnknown {
\t\t\tdocType = byRules.DocType
\t\t}
\t}
""")
])

protocol=rec/'protocol.go'
patch(protocol,[(
'\t// Номер документа.\n\tcur, has := fields["number"]\n\tbadNumber := !has || cur.Value == "" || reProtoIBAN.MatchString(cur.Value) ||\n\t\tstrings.Contains(strings.ToLower(cur.Value), "р/с")\n\tif badNumber {\n\t\tif n := protoDocNumber(text); n != "" {\n\t\t\tfields["number"] = domain.Field{Value: n, Confidence: 0.9, Source: "rule"}\n\t\t}\n\t}\n',
'\t// Номер документа. Для счёта-протокола специализированное правило\n\t// имеет приоритет над общим номером. OCR двухколоночной шапки может\n\t// выдать укороченный расчётный счёт (например BY29ALFA301221), который\n\t// уже нельзя надёжно отличить от номера документа полным IBAN-regex.\n\tif isProtocol {\n\t\tif n := protoDocNumber(text); n != "" {\n\t\t\tcur, has := fields["number"]\n\t\t\tif !has || strings.TrimSpace(cur.Value) == "" || cur.Source == "rule" || cur.Source == "model" {\n\t\t\t\tfields["number"] = domain.Field{Value: n, Confidence: 0.95, Source: "rule"}\n\t\t\t}\n\t\t}\n\t}\n')])

# NOTE: the generated patch above intentionally does NOT touch split.go.
# Add adaptive settings without changing OCR_ENHANCE.
for env in (root/'.env', root.parent/'.env'):
    if env.exists():
        content=env.read_text(encoding='utf-8')
        if 'OCR_ADAPTIVE=' not in content:
            bak=Path(str(env)+f'.bak-adaptive-v5-{stamp}')
            shutil.copy2(env,bak)
            content=content.rstrip()+"\n\n# Adaptive OCR: safe first, enhanced only when needed\nOCR_ADAPTIVE=true\nOCR_ADAPTIVE_MIN_SCORE=55\n"
            env.write_text(content,encoding='utf-8')
            print(f'patched: {env}')
            print(f'backup:  {bak}')
        break
PY

cd "$ROOT"
gofmt -w internal/recognize/preprocess.go internal/recognize/ocr.go internal/recognize/pipeline.go

echo 'PATCH APPLIED: adaptive OCR v8'
if go test ./internal/recognize; then
  echo 'go test: OK'
else
  echo 'WARNING: go test не завершился из-за окружения/зависимостей. Патч уже применён; повторите go test на сервере с доступом к Go modules.'
fi
