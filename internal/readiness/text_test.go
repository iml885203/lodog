package readiness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestEveryTextKeyIsDefinedAndUsed keeps Texts and the code in step; the
// report's English dictionary is kept in step with Texts by its own tests.
func TestEveryTextKeyIsDefinedAndUsed(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	used := map[string]bool{}
	key := regexp.MustCompile(`"(rd\.[a-z0-9_.]+)"`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "texts.go" {
			continue
		}
		b, err := os.ReadFile(f) //nolint:gosec // G304: reads this package's own source files
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range key.FindAllStringSubmatch(string(b), -1) {
			used[m[1]] = true
		}
	}
	for k := range used {
		if _, ok := Texts[k]; !ok {
			t.Errorf("the code names %q, Texts has none", k)
		}
	}
	for k := range Texts {
		if !used[k] {
			t.Errorf("Texts has %q, the code never names it: delete it", k)
		}
	}
}

func TestATextPrintsAndMarshalsAsItsChineseSentence(t *testing.T) {
	x := say("rd.missing.line", "GET /a/{id}", joined("rd.join.semi", []Text{say("rd.missing.write", "POST /a"), say("rd.missing.list", "GET /a")}))
	want := "GET /a/{id}：POST /a 寫完讀不回；找一筆只能翻分頁列表 GET /a"
	if got := x.String(); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
	b, err := json.Marshal(Check{Name: say("rd.check.paging"), Detail: x, Examples: []Text{Lit("GET /a"), say("rd.more", 3)}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"name":"分頁一致"`) || !strings.Contains(string(b), `"detail":"`+want+`"`) || !strings.Contains(string(b), `"examples":["GET /a","…還有 3 個"]`) {
		t.Errorf("JSON keeps the Chinese strings: %s", b)
	}
	var back Check
	if err := json.Unmarshal(b, &back); err != nil || back.Detail.String() != want {
		t.Errorf("read back %q, %v", back.Detail, err)
	}
	if got := bullets([]Text{Lit("GET /a"), say("rd.more", 2)}).String(); got != "- GET /a\n- …還有 2 個，一併處理" {
		t.Errorf("bullets %q", got)
	}
}
