package llm

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Decision struct {
	Action  string `json:"action"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

func (d Decision) Validate() error {
	if d.Action == "skip" {
		if d.Message != "" {
			return fmt.Errorf("skip requires an empty message")
		}
		return nil
	}
	if d.Action != "send" {
		return fmt.Errorf("action must be send or skip")
	}
	if !utf8.ValidString(d.Message) || strings.TrimSpace(d.Message) == "" || utf8.RuneCountInString(d.Message) > 200 {
		return fmt.Errorf("send requires a nonempty message of at most 200 Unicode characters")
	}
	for _, r := range d.Message {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return fmt.Errorf("send requires one line without control characters")
		}
	}
	return nil
}

func ParseDecision(raw string) (Decision, error) {
	var d Decision
	if !utf8.ValidString(raw) {
		return d, fmt.Errorf("invalid UTF-8")
	}
	// Token decoding rejects duplicate keys as well as unknown/missing fields.
	decoder := json.NewDecoder(strings.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return d, fmt.Errorf("decision must be a JSON object")
	}
	seen := map[string]bool{}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return d, err
		}
		name, ok := key.(string)
		if !ok || seen[name] {
			return d, fmt.Errorf("invalid or duplicate field %v", key)
		}
		seen[name] = true
		var value any
		if err := decoder.Decode(&value); err != nil {
			return d, err
		}
		text, ok := value.(string)
		if !ok {
			return d, fmt.Errorf("%s must be a string", name)
		}
		switch name {
		case "action":
			d.Action = text
		case "reason":
			d.Reason = text
		case "message":
			d.Message = text
		default:
			return d, fmt.Errorf("unknown field %s", name)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return d, err
	}
	if decoder.InputOffset() < int64(len(raw)) && strings.TrimSpace(raw[decoder.InputOffset():]) != "" {
		return d, fmt.Errorf("trailing content")
	}
	if len(seen) != 3 {
		return d, fmt.Errorf("action, reason and message are required")
	}
	return d, d.Validate()
}
