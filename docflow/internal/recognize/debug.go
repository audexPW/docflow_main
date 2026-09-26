package recognize

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
