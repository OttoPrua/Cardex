package model

import (
	"bytes"
	"encoding/json"
	"strings"
)

// legacyOwnerQuestionToken is the open-question type spelling used before owner.
const legacyOwnerQuestionToken = "otto"

// OpenQuestionList is the JSON form of design-node open questions.
// A legacy array of strings decodes each string as text with type decision and status open.
type OpenQuestionList []OpenQuestion

// UnmarshalJSON accepts an array of strings, an array of objects, or a mix of both.
func (l *OpenQuestionList) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		*l = nil
		return nil
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	out := make(OpenQuestionList, 0, len(raw))
	for _, item := range raw {
		q, err := decodeOpenQuestion(item)
		if err != nil {
			return err
		}
		out = append(out, q)
	}
	*l = out
	return nil
}

// ParseOpenQuestions decodes stored JSON, including a legacy string array.
func ParseOpenQuestions(data []byte) (OpenQuestionList, error) {
	var l OpenQuestionList
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, err
	}
	return l, nil
}

// ContainsLegacyOpenQuestion reports whether any array element is a JSON string.
func ContainsLegacyOpenQuestion(data []byte) bool {
	var raw []json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(data), &raw); err != nil {
		return false
	}
	for _, item := range raw {
		item = bytes.TrimSpace(item)
		if len(item) > 0 && item[0] == '"' {
			return true
		}
	}
	return false
}

// NormalizeOpenQuestions fills empty type and status. Recognizable values that
// differ only by case or surrounding space become the canonical token. An
// unrecognized token keeps the question and uses decision / open. A nil list
// stays nil. CitedPaths nil becomes an empty slice.
func NormalizeOpenQuestions(in OpenQuestionList) (OpenQuestionList, error) {
	if in == nil {
		return nil, nil
	}
	out := make(OpenQuestionList, len(in))
	for i, q := range in {
		nq, err := normalizeOpenQuestion(q)
		if err != nil {
			return nil, err
		}
		out[i] = nq
	}
	return out, nil
}

func decodeOpenQuestion(item json.RawMessage) (OpenQuestion, error) {
	item = bytes.TrimSpace(item)
	if len(item) > 0 && item[0] == '"' {
		var text string
		if err := json.Unmarshal(item, &text); err != nil {
			return OpenQuestion{}, err
		}
		return legacyOpenQuestion(text), nil
	}
	var q OpenQuestion
	if err := json.Unmarshal(item, &q); err != nil {
		return OpenQuestion{}, err
	}
	return normalizeOpenQuestion(q)
}

func legacyOpenQuestion(text string) OpenQuestion {
	q, _ := normalizeOpenQuestion(OpenQuestion{Text: text, Type: OQDecision, Status: OQOpen})
	return q
}

// OpenQuestionTagNames is the tag vocabulary, longest-prefix first.
// The legacy personal-name token is accepted and normalized to owner.
func OpenQuestionTagNames() []string {
	return []string{"fact web", "fact", "experiment", "decision", string(OQOwner), legacyOwnerQuestionToken}
}

// CanonicalOpenQuestionType trims and lowercases a type. The fact-web tag and the
// legacy personal-name token normalize to fact and owner. Empty and unknown become decision.
func CanonicalOpenQuestionType(raw OpenQuestionType) OpenQuestionType {
	s := strings.ToLower(strings.TrimSpace(string(raw)))
	switch s {
	case "fact web":
		s = string(OQFact)
	case legacyOwnerQuestionToken:
		s = string(OQOwner)
	}
	return OpenQuestionType(canonicalEnum(s, func(v string) bool {
		return OpenQuestionType(v).Valid()
	}, string(OQDecision)))
}

// RewriteStoredOwnerQuestionTypes rewrites a stored open-question JSON array so the
// legacy personal-name type token becomes owner. Every other value is left as stored.
// A second call changes nothing.
func RewriteStoredOwnerQuestionTypes(data []byte) ([]byte, bool, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return data, false, nil
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, false, err
	}
	changed := false
	out := make([]json.RawMessage, len(raw))
	for i, item := range raw {
		item = bytes.TrimSpace(item)
		if len(item) == 0 || item[0] != '{' {
			out[i] = item
			continue
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(item, &obj); err != nil {
			return nil, false, err
		}
		typeRaw, ok := obj["type"]
		if !ok {
			out[i] = item
			continue
		}
		var typ string
		if err := json.Unmarshal(bytes.TrimSpace(typeRaw), &typ); err != nil {
			out[i] = item
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(typ), legacyOwnerQuestionToken) {
			out[i] = item
			continue
		}
		repl, err := json.Marshal(string(OQOwner))
		if err != nil {
			return nil, false, err
		}
		obj["type"] = repl
		next, err := json.Marshal(obj)
		if err != nil {
			return nil, false, err
		}
		out[i] = next
		changed = true
	}
	if !changed {
		return data, false, nil
	}
	next, err := json.Marshal(out)
	if err != nil {
		return nil, false, err
	}
	return next, true, nil
}

// CanonicalOpenQuestionStatus trims and lowercases a status. Empty and unknown become open.
func CanonicalOpenQuestionStatus(raw OpenQuestionStatus) OpenQuestionStatus {
	return OpenQuestionStatus(canonicalEnum(string(raw), func(s string) bool {
		return OpenQuestionStatus(s).Valid()
	}, string(OQOpen)))
}

func canonicalEnum(raw string, valid func(string) bool, fallback string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s != "" && valid(s) {
		return s
	}
	return fallback
}

func normalizeOpenQuestion(q OpenQuestion) (OpenQuestion, error) {
	q.Type = CanonicalOpenQuestionType(q.Type)
	q.Status = CanonicalOpenQuestionStatus(q.Status)
	if q.CitedPaths == nil {
		q.CitedPaths = []string{}
	}
	return q, nil
}
