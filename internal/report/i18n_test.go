package report

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestBothLanguagesHaveExactlyTheSameKeys(t *testing.T) {
	z, e := Keys("zh"), Keys("en")
	have := map[string]bool{}
	for _, k := range e {
		have[k] = true
	}
	for _, k := range z {
		if !have[k] {
			t.Errorf("key %q is missing in English", k)
		}
		delete(have, k)
	}
	for k := range have {
		t.Errorf("key %q is missing in Chinese", k)
	}
}

var placeholder = regexp.MustCompile(`\{\d+\}`)

func TestASentenceTakesTheSameArgumentsInBothLanguages(t *testing.T) {
	for _, k := range Keys("zh") {
		a, b := placeholder.FindAllString(zh[k], -1), placeholder.FindAllString(en[k], -1)
		if !sameSet(a, b) {
			t.Errorf("%s: Chinese uses %v, English %v", k, a, b)
		}
	}
}

func sameSet(a, b []string) bool {
	m := map[string]bool{}
	for _, x := range a {
		m[x] = true
	}
	n := map[string]bool{}
	for _, x := range b {
		n[x] = true
	}
	if len(m) != len(n) {
		return false
	}
	for x := range m {
		if !n[x] {
			return false
		}
	}
	return true
}

func TestNoSentenceIsLeftUntranslated(t *testing.T) {
	cjk := regexp.MustCompile(`[\x{4e00}-\x{9fff}]`)
	for k, v := range en {
		if cjk.MatchString(v) {
			t.Errorf("English %s still has Chinese: %q", k, v)
		}
	}
	for k, v := range zh {
		if strings.TrimSpace(v) == "" || strings.TrimSpace(en[k]) == "" {
			t.Errorf("%s has an empty text", k)
		}
	}
}

// usedKeys are the dictionary keys the code and the template name.
func usedKeys(t *testing.T) map[string]bool {
	t.Helper()
	used := map[string]bool{}
	files := []string{"report.go", "story.go", "owner.go", "panel.go", "drift.go", "html.go"}
	patterns := []*regexp.Regexp{regexp.MustCompile(`\bM\("([a-zA-Z0-9._]+)"`), regexp.MustCompile(`"@([a-z.]+)"`), regexp.MustCompile(`"((?:cat|ring|hero|foot|goal|issue|how|badge|sum|note|q|g|noreplay|nm|text|change|judge)\.[a-zA-Z0-9._]+)"`)}
	for _, f := range files {
		b, err := os.ReadFile(f) //nolint:gosec // G304: reads this package's own source files
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range patterns {
			for _, m := range p.FindAllStringSubmatch(string(b), -1) {
				used[m[1]] = true
			}
		}
	}
	tmpl, err := os.ReadFile("report.html.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []*regexp.Regexp{regexp.MustCompile(`\(?\bmt? "([a-zA-Z0-9._]+)"`), regexp.MustCompile(`\bma "[a-z-]+" "([a-zA-Z0-9._]+)"`), regexp.MustCompile(`\bk: "([a-zA-Z0-9._]+)"`)} {
		for _, m := range p.FindAllStringSubmatch(string(tmpl), -1) {
			used[m[1]] = true
		}
	}
	// a key that ends in a dot is the start of one built from a level or an id
	for k := range used {
		if strings.HasSuffix(k, ".") {
			delete(used, k)
		}
	}
	return used
}

func TestEveryKeyTheCodeNamesIsInTheDictionary(t *testing.T) {
	for k := range usedKeys(t) {
		if _, ok := zh[k]; !ok {
			t.Errorf("the code names key %q, the dictionary has none", k)
		}
	}
}

func TestEveryKeyOfTheDictionaryIsUsed(t *testing.T) {
	used := usedKeys(t)
	// keys built from a prefix and a level or check id
	dynamic := regexp.MustCompile(`^(level|tag|change|check)\.`)
	for k := range zh {
		if !used[k] && !dynamic.MatchString(k) && k != "sep" && k != "title" {
			t.Errorf("dictionary key %q is never used: delete it", k)
		}
	}
}

func TestAMsgRendersWithItsArgumentsAndLevelNames(t *testing.T) {
	m := M("sum.read", LevelArg("L0"), LevelArg("L1"))
	if got := m.Zh(); got != "同一題，「原樣接上」答錯，「補上說明」答對。" {
		t.Errorf("zh %q", got)
	}
	if got := m.En(); got != "Same task: wrong at “As is”, right at “Described”." {
		t.Errorf("en %q", got)
	}
	if got := M("noreplay.need", "L3", "#L0,L1").En(); !strings.Contains(got, "Levels with a run: L0, L1.") {
		t.Errorf("a list argument joins with the language's separator: %q", got)
	}
	if got := M("noreplay.need", "L3", "#L0,L1").Zh(); !strings.Contains(got, "L0、L1") {
		t.Errorf("zh list %q", got)
	}
	if got := Raw("an agent said `x`").Text("en"); got != "an agent said `x`" {
		t.Errorf("a raw text is shown as it is: %q", got)
	}
	if got := M("no.such.key").En(); got != "no.such.key" {
		t.Errorf("a missing key shows as itself: %q", got)
	}
}

func TestThePageCarriesBothDictionariesAndMarksEverySentenceWithItsKey(t *testing.T) {
	page := render(t, build2(t, "testdata/preview/shop", "testdata/preview/scenarios/full"))
	mustContain(t, page, `id="i18n"`, `data-lang="en"`, `data-k="tabs.now"`, `data-ia-aria-label="tabs.label"`, `"level.L3":"Guarded"`, `"level.L3":"加上把關"`)
	if strings.Contains(page, `data-k="no.such`) {
		t.Error("a sentence names a key that does not exist")
	}
	// the data of the case is not a key: a task's wording stays as it is
	if strings.Contains(page, `data-k="Give player 1`) {
		t.Error("a task's own words must not be treated as a key")
	}
}

func TestEveryKeyThePageNamesIsInBothDictionaries(t *testing.T) {
	for _, scen := range [][2]string{{"full", "shop"}, {"all-capped", "shop"}, {"incomplete", "shop"}, {"none", "shop"}, {"l0-l3", "shop-l0-l3"}} {
		page := render(t, build2(t, "testdata/preview/"+scen[1], "testdata/preview/scenarios/"+scen[0]))
		for _, m := range regexp.MustCompile(`data-(?:k|ia-[a-z-]+)="([a-zA-Z0-9._]+)"`).FindAllStringSubmatch(page, -1) {
			if _, ok := zh[m[1]]; !ok {
				t.Errorf("%s: the page names key %q, the dictionary has none", scen[0], m[1])
			}
		}
	}
}
