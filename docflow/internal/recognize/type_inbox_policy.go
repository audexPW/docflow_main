package recognize

// TypeInboxDocType returns the canonical business document type
// allowed for the five-folder XML export.
//
// Confidence, missing fields, internal approval and the availability
// of a 1C target object do not affect this decision.
func TypeInboxDocType(source string) (string, bool) {
	effective := EffectiveDocType(source)

	switch effective {
	case "act",
		"waybill",
		"contract",
		"power_of_attorney",
		"upd":
		return effective, true
	default:
		return "", false
	}
}
