package approval

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/casecfg"
)

func TestElicitationNeedsNoChannel(t *testing.T) {
	if n, err := ChannelFor(casecfg.ApprovalsConfig{}, func(string) string { return "" }, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil || n != nil {
		t.Fatalf("notifier %v, err %v: elicitation has no channel", n, err)
	}
}

func TestAMissingWebhookURLStopsTheGatewayAtStartAndNamesTheVariable(t *testing.T) {
	cfg := casecfg.ApprovalsConfig{Webhook: &casecfg.WebhookConfig{Listen: "127.0.0.1:1", BaseURL: "http://localhost:1"}}
	_, err := ChannelFor(cfg, func(string) string { return "" }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), casecfg.DefaultWebhookURLEnv) {
		t.Fatalf("err = %v, want one naming %s", err, casecfg.DefaultWebhookURLEnv)
	}
}
