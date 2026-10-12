package onec

import "sort"

// edTypeInboxQuality is emitted only for the new export profile.
// All values describe the recorded recognition result, not approval
// or confirmation by the receiving 1C system.
type edTypeInboxQuality struct {
	TypeConfidence float64 `xml:"typeConfidence,attr"`

	Faults            []string               `xml:"Faults>Fault,omitempty"`
	Rejected          []edTypeInboxNamedText `xml:"RejectedFields>Field,omitempty"`
	Review            []string               `xml:"ReviewFields>Field,omitempty"`
	ReviewNotes       []edTypeInboxNamedText `xml:"ReviewNotes>Field,omitempty"`
	Evidence          []edTypeInboxEvidence  `xml:"FieldEvidence>Field,omitempty"`
	IntegrationIssues []string               `xml:"IntegrationIssues>Issue,omitempty"`
}

type edTypeInboxNamedText struct {
	Name  string `xml:"name,attr"`
	Value string `xml:",chardata"`
}

type edTypeInboxEvidence struct {
	Name       string  `xml:"name,attr"`
	Confidence float64 `xml:"confidence,attr"`
	Source     string  `xml:"source,attr,omitempty"`
}

func buildTypeInboxQuality(p Payload) *edTypeInboxQuality {
	if !p.TypeInbox {
		return nil
	}

	result := &edTypeInboxQuality{
		TypeConfidence: p.DocTypeConfidence,
		Faults:         append([]string(nil), p.Faults...),
		Review:         append([]string(nil), p.Review...),
		IntegrationIssues: append(
			[]string(nil), p.IntegrationIssues...,
		),
	}

	rejectedKeys := make([]string, 0, len(p.Rejected))
	for key := range p.Rejected {
		rejectedKeys = append(rejectedKeys, key)
	}
	sort.Strings(rejectedKeys)
	for _, key := range rejectedKeys {
		result.Rejected = append(
			result.Rejected,
			edTypeInboxNamedText{
				Name:  key,
				Value: p.Rejected[key],
			},
		)
	}

	noteKeys := make([]string, 0, len(p.ReviewNotes))
	for key := range p.ReviewNotes {
		noteKeys = append(noteKeys, key)
	}
	sort.Strings(noteKeys)
	for _, key := range noteKeys {
		result.ReviewNotes = append(
			result.ReviewNotes,
			edTypeInboxNamedText{
				Name:  key,
				Value: p.ReviewNotes[key],
			},
		)
	}

	evidenceKeys := make([]string, 0, len(p.FieldEvidence))
	for key := range p.FieldEvidence {
		evidenceKeys = append(evidenceKeys, key)
	}
	sort.Strings(evidenceKeys)
	for _, key := range evidenceKeys {
		evidence := p.FieldEvidence[key]
		result.Evidence = append(
			result.Evidence,
			edTypeInboxEvidence{
				Name:       key,
				Confidence: evidence.Confidence,
				Source:     evidence.Source,
			},
		)
	}

	return result
}
