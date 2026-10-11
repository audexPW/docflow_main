package recognize

// EffectiveDocType converts the internal classification
// into the final business document type.
// It must be called only after recognition and validation.
func EffectiveDocType(source string) string {
	switch source {
	case "act",
		"schet_faktura",
		"act_priemki",
		"invoice",
		"receipt",
		"payment_order",
		"act_sverki":
		return "act"
	default:
		return source
	}
}
