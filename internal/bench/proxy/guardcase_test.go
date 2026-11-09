package proxy

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/iml885203/lodog/internal/bench/record"
)

type nullSink struct{}

func (nullSink) Append(record.Call) error { return nil }

func TestAReadOnlyProxyOnTheBackofficeCaseSendsNoneOfItsDangerousCalls(t *testing.T) {
	g, err := GuardFromCase("../../../cases/backoffice", "spec/policy.auto.yaml", nil)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var seen []string
	system := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, r.Method+" "+r.URL.Path)
	}))
	t.Cleanup(system.Close)
	target, err := url.Parse(system.URL)
	if err != nil {
		t.Fatal(err)
	}
	px := httptest.NewServer(New(target, nil, nullSink{}, slog.New(slog.DiscardHandler)).ReadOnly(g))
	t.Cleanup(px.Close)

	sent := []string{"POST /api/operator/login", "GET /api/customer/searchPlayerProfile", "GET /api/customer/910002", "GET /api/payment/transactions"}
	held := []string{
		"GET /api/customer/910004/resetPassword", "GET /api/images/TestDelete",
		"POST /api/customer/910002/edit", "POST /api/promotion/manualBonus/issue",
		"POST /api/payment/transaction/withdraw/reject", "DELETE /api/customer/crmlogs/1",
	}
	for _, c := range append(append([]string{}, sent...), held...) {
		method, path, _ := strings.Cut(c, " ")
		req, err := http.NewRequestWithContext(t.Context(), method, px.URL+path, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(seen, sent) {
		t.Fatalf("Backoffice saw %v, want only %v", seen, sent)
	}
}

func TestAGuardNeedsACase(t *testing.T) {
	if _, err := GuardFromCase("", "spec/policy.auto.yaml", nil); !errors.Is(err, ErrBadOptions) {
		t.Fatalf("err = %v, want ErrBadOptions", err)
	}
}
