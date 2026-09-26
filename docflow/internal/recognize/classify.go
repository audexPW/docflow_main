package recognize

import "strings"

// Тип документа не зашит в жёсткий перечень: правила дают быстрый ответ для
// частых форм, а всё, что не опознано уверенно, уходит на разбор модели.
// Признаки типов живут в реестре (doctypes.go) и пополняются файлом
// DOCTYPES_PATH без пересборки.

// classifyByRules возвращает наиболее вероятный тип и уверенность в диапазоне 0..1.
func classifyByRules(ocrText string) (string, float64) {
	text := strings.ToLower(strings.ReplaceAll(ocrText, "ё", "е"))

	best := DocTypeUnknown
	bestScore := 0.0
	for _, spec := range DocTypes() {
		weight := spec.Weight
		if weight <= 0 {
			weight = 0.5
		}
		score := 0.0
		for _, a := range spec.Anchors {
			if strings.Contains(text, strings.ReplaceAll(strings.ToLower(a), "ё", "е")) {
				score += weight
			}
		}
		if score > bestScore {
			bestScore = score
			best = spec.Slug
		}
	}

	if bestScore > 1 {
		bestScore = 1
	}
	return best, bestScore
}
