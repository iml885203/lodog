package workflow

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func taskWithInputs(props map[string]any, required ...string) Task {
	inputs := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		req := make([]any, len(required))
		for i, r := range required {
			req[i] = r
		}
		inputs["required"] = req
	}
	return Task{Workflow: Workflow{WorkflowID: "t", Inputs: inputs}}
}

func TestPrepareHoldsEachArgumentToItsSchema(t *testing.T) {
	task := taskWithInputs(map[string]any{
		"qty":    map[string]any{"type": "integer", "minimum": 1, "maximum": 10},
		"price":  map[string]any{"type": "number", "minimum": 0.5},
		"mode":   map[string]any{"type": "string", "enum": []any{"a", "b"}},
		"level":  map[string]any{"type": "integer", "enum": []any{1, 2, 3}},
		"flag":   map[string]any{"type": "boolean"},
		"tags":   map[string]any{"type": "array"},
		"filter": map[string]any{"type": "object"},
		"any":    map[string]any{},
		"code":   map[string]any{"enum": []any{"x", 7}},
	})
	for name, tc := range map[string]struct {
		args    map[string]any
		wantErr string // empty: accepted
	}{
		"integer in range":          {map[string]any{"qty": 5.0}, ""},
		"integer at the maximum":    {map[string]any{"qty": 10.0}, ""},
		"integer at the minimum":    {map[string]any{"qty": 1.0}, ""},
		"integer above maximum":     {map[string]any{"qty": 11.0}, "at most 10"},
		"integer below minimum":     {map[string]any{"qty": 0.0}, "at least 1"},
		"integer given a fraction":  {map[string]any{"qty": 2.5}, "whole number"},
		"integer given a string":    {map[string]any{"qty": "5"}, "whole number"},
		"number accepts a fraction": {map[string]any{"price": 0.75}, ""},
		"number below minimum":      {map[string]any{"price": 0.25}, "at least 0.5"},
		"number given a bool":       {map[string]any{"price": true}, "a number"},
		"string enum member":        {map[string]any{"mode": "a"}, ""},
		"string enum non-member":    {map[string]any{"mode": "c"}, "one of [a b]"},
		"string given a number":     {map[string]any{"mode": 1.0}, "a string"},
		"numeric enum member":       {map[string]any{"level": 2.0}, ""},
		"numeric enum non-member":   {map[string]any{"level": 4.0}, "one of [1 2 3]"},
		"mixed enum number":         {map[string]any{"code": 7.0}, ""},
		"mixed enum string":         {map[string]any{"code": "x"}, ""},
		"mixed enum miss":           {map[string]any{"code": "y"}, "one of"},
		"boolean":                   {map[string]any{"flag": true}, ""},
		"boolean given a string":    {map[string]any{"flag": "true"}, "true or false"},
		"array":                     {map[string]any{"tags": []any{"a"}}, ""},
		"array given an object":     {map[string]any{"tags": map[string]any{}}, "a list"},
		"object":                    {map[string]any{"filter": map[string]any{"k": 1.0}}, ""},
		"object given a list":       {map[string]any{"filter": []any{}}, "an object"},
		"untyped accepts anything":  {map[string]any{"any": []any{1.0}}, ""},
		"unknown argument":          {map[string]any{"nope": 1.0}, `"nope" is not an argument of t; it takes any, code, filter, flag, level, mode, price, qty, tags`},
		"null is left out":          {map[string]any{"qty": nil}, ""},
	} {
		_, err := task.Prepare(tc.args, time.Now())
		if tc.wantErr == "" {
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		var inputErr *InputError
		if !errors.As(err, &inputErr) || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: err = %v, want an InputError containing %q", name, err, tc.wantErr)
		}
	}
}

func TestPrepareNamesNoArgumentsWhenTheToolTakesNone(t *testing.T) {
	_, err := taskWithInputs(nil).Prepare(map[string]any{"x": 1.0}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "no arguments") {
		t.Fatalf("err = %v", err)
	}
}

func TestPrepareDropsAnAbsentOptionalAndFillsDefaults(t *testing.T) {
	task := taskWithInputs(map[string]any{
		"size": map[string]any{"type": "integer", "default": 20},
		"from": map[string]any{"type": "string", "format": "date", "x-default": "now-1d"},
		"note": map[string]any{"type": "string"},
	}, "note")
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	if _, err := task.Prepare(nil, now); err == nil || !strings.Contains(err.Error(), `"note" is required`) {
		t.Fatalf("err = %v, want the required argument named", err)
	}
	got, err := task.Prepare(map[string]any{"note": "n"}, now)
	if err != nil || got["size"] != 20 || got["from"] != "2026-10-15" {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestPrepareRefusesABadRelativeDefault(t *testing.T) {
	task := taskWithInputs(map[string]any{"from": map[string]any{"type": "string", "x-default": "yesterday"}})
	if _, err := task.Prepare(nil, time.Now()); err == nil {
		t.Fatal("x-default \"yesterday\" must be refused")
	}
}

func TestInputSchemaDescribesARelativeDefault(t *testing.T) {
	task := taskWithInputs(map[string]any{
		"a": map[string]any{"type": "string", "x-default": "now-30d", "description": "Start."},
		"b": map[string]any{"type": "string", "x-default": "now+2h"},
		"c": map[string]any{"type": "string", "x-default": "now"},
	}, "c")
	props := task.InputSchema()["properties"].(map[string]any)
	for name, want := range map[string]string{
		"a": "Start. Default: 30 days before the call.",
		"b": "Default: 2 hours after the call.",
		"c": "Default: the time of the call.",
	} {
		schema := props[name].(map[string]any)
		if schema["description"] != want || schema["x-default"] != nil {
			t.Errorf("%s = %v, want description %q and no x-default", name, schema, want)
		}
	}
	if req := task.InputSchema()["required"]; req == nil {
		t.Error("required is dropped from the schema")
	}
}
