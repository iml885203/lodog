package spec

import "testing"

func TestEnumNamesThatDoNotPairWithTheValuesAreNotGuessedAt(t *testing.T) {
	got := EnumList(map[string]any{"enum": []any{1.0, 2.0, 3.0}, "x-enumNames": []any{"A", "B"}})
	if got != "1, 2, 3" {
		t.Fatalf("list = %q", got)
	}
	if got := EnumList(map[string]any{"enum": []any{1.0, 2.0}, "x-enum-varnames": []any{"A", "B"}}); got != "1 = A, 2 = B" {
		t.Fatalf("x-enum-varnames list = %q", got)
	}
}
