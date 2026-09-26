package recognize

import (
	"strings"

	"docflow/internal/domain"
)

// Табличная часть в Markdown.
//
// Модель получала таблицу «шапкой через |» вперемешку с текстом всего
// документа и регулярно путала строки: то склеивала две позиции в одну, то
// растаскивала перенесённое наименование по соседним строкам. Markdown с
// разделителем «|---|» — единственный табличный формат, который Qwen видел в
// обучении в товарных количествах, и на нём разбор строк заметно устойчивее.
//
// Формат намеренно строгий: одинаковое число ячеек в каждой строке, перевод
// строки внутри ячейки заменяется на пробел, вертикальная черта экранируется.
// Иначе одна ячейка с «|» съезжает и утаскивает за собой всю строку.

// mdCell готовит значение ячейки к вставке в Markdown.
func mdCell(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return " "
	}
	return s
}

// mdRow собирает одну строку Markdown-таблицы, дополняя её до нужной ширины.
func mdRow(cells []string, width int) string {
	var b strings.Builder
	b.WriteString("|")
	for i := 0; i < width; i++ {
		v := " "
		if i < len(cells) {
			v = mdCell(cells[i])
		}
		b.WriteString(" ")
		b.WriteString(v)
		b.WriteString(" |")
	}
	return b.String()
}

// FreeTableMarkdown отдаёт свободную таблицу как Markdown. Пустая строка —
// таблицы нет или в ней нет ни одной строки данных.
func FreeTableMarkdown(ft *domain.FreeTable) string {
	if ft == nil || len(ft.Rows) == 0 {
		return ""
	}
	width := len(ft.Columns)
	for _, r := range ft.Rows {
		if len(r) > width {
			width = len(r)
		}
	}
	if len(ft.Totals) > width {
		width = len(ft.Totals)
	}
	if width == 0 {
		return ""
	}

	cols := make([]string, width)
	for i := range cols {
		if i < len(ft.Columns) && strings.TrimSpace(ft.Columns[i]) != "" {
			cols[i] = ft.Columns[i]
			continue
		}
		// Безымянная графа всё равно должна остаться на своём месте, иначе
		// ячейки строк уедут влево.
		cols[i] = "гр." + itoa(i+1)
	}

	var b strings.Builder
	b.WriteString(mdRow(cols, width))
	b.WriteString("\n|")
	for i := 0; i < width; i++ {
		b.WriteString(" --- |")
	}
	for _, r := range ft.Rows {
		b.WriteString("\n")
		b.WriteString(mdRow(r, width))
	}
	if len(ft.Totals) > 0 {
		b.WriteString("\n")
		b.WriteString(mdRow(ft.Totals, width))
	}
	return b.String()
}

// TableMarkdown — то же самое для уже приведённой таблицы. Приоритет у RawCells:
// там колонки документа как есть, без приведения к ролям.
func TableMarkdown(t *domain.Table) string {
	if t == nil {
		return ""
	}
	if t.RawCells != nil {
		if md := FreeTableMarkdown(t.RawCells); md != "" {
			return md
		}
	}
	if len(t.Rows) == 0 {
		return ""
	}
	width := len(t.Columns)
	for _, r := range t.Rows {
		for _, c := range r.Cells {
			if c.Column+1 > width {
				width = c.Column + 1
			}
		}
	}
	if width == 0 {
		return ""
	}
	cols := make([]string, width)
	for _, c := range t.Columns {
		if c.Index >= 0 && c.Index < width {
			cols[c.Index] = c.Title
		}
	}
	ft := &domain.FreeTable{Columns: cols}
	for _, r := range t.Rows {
		row := make([]string, width)
		for _, c := range r.Cells {
			if c.Column >= 0 && c.Column < width {
				row[c.Column] = c.Value
			}
		}
		ft.Rows = append(ft.Rows, row)
	}
	if len(t.TotalsRow) > 0 {
		row := make([]string, width)
		for _, c := range t.TotalsRow {
			if c.Column >= 0 && c.Column < width {
				row[c.Column] = c.Value
			}
		}
		ft.Totals = row
	}
	return FreeTableMarkdown(ft)
}

// itoa — маленький помощник, чтобы не тянуть strconv ради одной подписи.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
