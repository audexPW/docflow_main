package recognize

import (
	"math"
	"sort"
	"strings"
)

// Восстановление координат отдельных слов.
//
// Surya (и tesseract в построчном режиме) отдаёт координаты СТРОКИ, а не слова:
// в JSON приходит по записи на слово, но прямоугольник у всех слов строки один
// и тот же. В логах это видно прямо:
//
//	291 949 640 966  Пеня
//	292 949 640 966  по
//	292 949 640 966  коммунальным
//	291 949 640 966  услугам
//
// Отсюда две беды, и обе видны в карточке документа.
//
// Первая — порядок слов. groupTokenRows сортирует слова строки по X0, а X0 у
// них отличается на пиксель дрожания: 291, 292, 292, 291. Сортировка ставит
// сначала все 291, потом все 292, и наименование выходит перемешанным —
// «Пеня услугам по коммунальным» вместо «Пеня по коммунальным услугам».
//
// Вторая — колонки. Ширина графы считается по боксам слов; когда бокс один на
// всю ячейку, соседние графы склеиваются, а подписи шапки сваливаются в кучу
// («Цена Кол-во производителя»).
//
// SplitLineBoxes разбирает это до всякого разбора таблицы: слова, поделившие
// один прямоугольник, раскладываются внутри него по ширине в символах и в том
// порядке, в каком их вернул движок. Если движок отдаёт настоящие координаты
// слов, функция ничего не меняет.
func SplitLineBoxes(words []wordBox) []wordBox {
	if len(words) == 0 {
		return words
	}

	out := make([]wordBox, 0, len(words)*2)
	i := 0
	for i < len(words) {
		j := i + 1
		for j < len(words) && sameLineBox(words[i], words[j]) {
			j++
		}
		group := words[i:j]
		if len(group) == 1 {
			parts := splitWordsInBox(group[0])
			for k := range parts {
				parts[k].Line = group[0].Line
				if group[0].Seq > 0 {
					parts[k].Seq = group[0].Seq + k
				}
			}
			out = append(out, parts...)
			i = j
			continue
		}
		// Все слова группы поделили один прямоугольник. Настоящие границы
		// строки — объединение боксов: движок нередко тянет правый край
		// только у последнего слова.
		x0, x1 := group[0].X0, group[0].X1
		y0, y1 := group[0].Y0, group[0].Y1
		texts := make([]string, 0, len(group))
		origin := make([]wordBox, 0, len(group))
		for _, w := range group {
			x0 = math.Min(x0, w.X0)
			x1 = math.Max(x1, w.X1)
			y0 = math.Min(y0, w.Y0)
			y1 = math.Max(y1, w.Y1)
			texts = append(texts, strings.TrimSpace(w.Text))
			if strings.TrimSpace(w.Text) != "" {
				origin = append(origin, w)
			}
		}
		spread := spreadWords(texts, x0, y0, x1, y1)
		// Порядок движка переносим на разложенные слова: spreadWords отдаёт
		// их в том же порядке, в каком получил тексты.
		if len(spread) == len(origin) {
			for k := range spread {
				spread[k].Line, spread[k].Seq = origin[k].Line, origin[k].Seq
			}
		}
		out = append(out, spread...)
		i = j
	}

	// Порядок на выходе — слева направо, сверху вниз. Дальше по конвейеру
	// слова всё равно группируются в строки, но так отладочные выгрузки
	// читаются глазами.
	sort.SliceStable(out, func(a, b int) bool {
		if math.Abs(out[a].Y0-out[b].Y0) > 3 {
			return out[a].Y0 < out[b].Y0
		}
		return out[a].X0 < out[b].X0
	})
	return out
}

// sameLineBox — два слова пришли с одним и тем же прямоугольником. Допуск в
// несколько пикселей: движок округляет координаты по-разному для каждого
// слова, отсюда разброс в единицу-другую.
func sameLineBox(a, b wordBox) bool {
	const tol = 3.0
	if math.Abs(a.Y0-b.Y0) > tol || math.Abs(a.Y1-b.Y1) > tol {
		return false
	}
	if math.Abs(a.X0-b.X0) > tol {
		return false
	}
	// Правый край может отличаться сильно (движок отдаёт его только у
	// последнего слова), но не настолько, чтобы это были разные ячейки:
	// левый край и высота уже совпали.
	return true
}

// splitWordsInBox делит бокс, в тексте которого несколько слов. Так приходит
// строка от tesseract и склеенная ячейка от surya. Раньше это разбиралось
// только на этапе укладки в графы (splitAcrossBands), а границы самих граф
// считались по целому боксу — и графы слипались.
func splitWordsInBox(w wordBox) []wordBox {
	fields := strings.Fields(w.Text)
	if len(fields) < 2 {
		if strings.TrimSpace(w.Text) == "" {
			return nil
		}
		w.Text = strings.TrimSpace(w.Text)
		return []wordBox{w}
	}
	return spreadWords(fields, w.X0, w.Y0, w.X1, w.Y1)
}

// spreadWords раскладывает слова внутри прямоугольника пропорционально их
// длине в символах. Точных координат внутри строки движок не даёт, но графа
// всегда шире одного символа, и такой оценки достаточно, чтобы слово попало в
// свою колонку.
func spreadWords(texts []string, x0, y0, x1, y1 float64) []wordBox {
	clean := make([]string, 0, len(texts))
	for _, t := range texts {
		t = strings.TrimSpace(t)
		if t != "" {
			clean = append(clean, t)
		}
	}
	if len(clean) == 0 {
		return nil
	}
	if len(clean) == 1 {
		return []wordBox{{Text: clean[0], X0: x0, Y0: y0, X1: x1, Y1: y1}}
	}

	total := 0
	for _, t := range clean {
		total += len([]rune(t))
	}
	total += len(clean) - 1 // пробелы между словами
	width := x1 - x0
	if width <= 0 || total <= 0 {
		out := make([]wordBox, 0, len(clean))
		for _, t := range clean {
			out = append(out, wordBox{Text: t, X0: x0, Y0: y0, X1: x1, Y1: y1})
		}
		return out
	}
	per := width / float64(total)

	out := make([]wordBox, 0, len(clean))
	cur := x0
	for _, t := range clean {
		n := float64(len([]rune(t)))
		out = append(out, wordBox{
			Text: t,
			X0:   cur,
			Y0:   y0,
			X1:   cur + n*per,
			Y1:   y1,
		})
		cur += (n + 1) * per
	}
	return out
}
