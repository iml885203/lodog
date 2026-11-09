package spec

import (
	"encoding/json"
	"fmt"
	"strings"
)

// enumNameKeys are the vendor extensions document generators use to name the
// values of an enum, in the order they are tried.
var enumNameKeys = []string{"x-enumNames", "x-enum-varnames"}

// maxListedEnum bounds how many values a description spells out. The schema's
// own enum keeps all of them.
const maxListedEnum = 50

// EnumList spells out the values of a schema's enum for a reader: "1 = Email,
// 2 = LiveChat" when the schema names its values (x-enumNames), "1, 2" when it
// does not. It is "" when the schema has no enum. Names that do not pair one
// to one with the values are ignored rather than guessed at.
func EnumList(schema map[string]any) string {
	values, _ := schema["enum"].([]any)
	if len(values) == 0 {
		return ""
	}
	names := enumNames(schema, len(values))
	parts := make([]string, 0, len(values))
	for i, v := range values {
		if i == maxListedEnum {
			parts = append(parts, fmt.Sprintf("... (%d more)", len(values)-maxListedEnum))
			break
		}
		part := enumValueText(v)
		if names != nil && names[i] != "" {
			part += " = " + names[i]
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ", ")
}

// EnumDescription is the sentence that tells an agent which values a field
// takes, or "" when the schema has no enum.
func EnumDescription(schema map[string]any) string {
	list := EnumList(schema)
	if list == "" {
		return ""
	}
	return "Allowed values: " + list + "."
}

func enumNames(schema map[string]any, n int) []string {
	for _, key := range enumNameKeys {
		raw, ok := schema[key].([]any)
		if !ok || len(raw) != n {
			continue
		}
		names := make([]string, n)
		for i, v := range raw {
			if s, ok := v.(string); ok {
				names[i] = s
			}
		}
		return names
	}
	return nil
}

func enumValueText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// describeEnums writes the allowed values into the description of every
// schema under node that has an enum, so a field that is only a code (1, 2, 3)
// tells the agent what each code means wherever the schema is read.
func describeEnums(node any) {
	switch v := node.(type) {
	case map[string]any:
		if sentence := EnumDescription(v); sentence != "" {
			d, _ := v["description"].(string)
			if !strings.Contains(d, sentence) {
				v["description"] = AppendSentence(d, sentence)
			}
		}
		for _, child := range v {
			describeEnums(child)
		}
	case []any:
		for _, child := range v {
			describeEnums(child)
		}
	}
}

// AppendSentence adds a sentence to a description, ending the description's
// own last sentence first when it has no full stop.
func AppendSentence(description, sentence string) string {
	d := strings.TrimSpace(description)
	if d == "" {
		return sentence
	}
	if !strings.HasSuffix(d, ".") && !strings.HasSuffix(d, "!") && !strings.HasSuffix(d, "?") && !strings.HasSuffix(d, "。") {
		d += "."
	}
	return d + " " + sentence
}
