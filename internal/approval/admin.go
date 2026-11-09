package approval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/iml885203/lodog/internal/casecfg"
	"github.com/iml885203/lodog/internal/redact"
)

// AdminHandler lists what is waiting, on its own listener. It cannot approve
// or deny: an endpoint an agent on the same machine can reach would let it
// approve its own calls, so verdicts come only from the approval mode's own
// channel (the user's prompt, the webhook's link).
//
//	GET  /approvals                 pending requests
func AdminHandler(store *Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /approvals", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		pending := store.Pending()
		for i := range pending {
			pending[i].Arguments = previewArgs(pending[i].Arguments)
			pending[i].Path = redact.MaskString(pending[i].Path)
		}
		_ = json.NewEncoder(w).Encode(pending)
	})
	return mux
}

// DecidePending gives every pending request a verdict: approve decides which
// to approve, the rest are denied. It is for a host that plays the operator
// in-process (the benchmark runner, with Approvals.InProcess), so a request
// whose risk is never_auto_approve is denied whatever approve says: only a
// person, through the approval mode's channel, can approve it.
func (a *Store) DecidePending(approve func(req Request) bool, by string, log *slog.Logger) ([]Request, error) {
	var decided []Request
	for _, req := range a.Pending() {
		d, err := a.Decide(req.ID, !req.NeverAutoApprove && approve(req), by)
		if err != nil {
			return decided, err
		}
		log.Info("approval decided", "id", d.ID, "status", d.Status, "by", d.DecidedBy, "tool", d.Tool)
		decided = append(decided, *d)
	}
	return decided, nil
}

// ChannelFor is the channel the config's approval mode asks people through:
// the webhook's link and page. Elicitation needs none, and returns nil. The
// webhook URL is a secret and comes from the environment; a missing one is an
// error at start, not at the first held call.
func ChannelFor(cfg casecfg.ApprovalsConfig, getenv func(string) string, log *slog.Logger) (Notifier, error) {
	if cfg.Resolved() != casecfg.ModeWebhook || cfg.Webhook == nil {
		return nil, nil //nolint:nilnil // elicitation has no channel: nil says so
	}
	wc := cfg.Webhook
	urlEnv := wc.URLEnv
	if urlEnv == "" {
		urlEnv = casecfg.DefaultWebhookURLEnv
	}
	notifier, err := NewWebhook(WebhookConfig{URL: getenv(urlEnv), BaseURL: wc.BaseURL, Listen: wc.Listen, Log: log})
	if err != nil {
		return nil, fmt.Errorf("approvals.webhook: %w (set %s in the environment)", err, urlEnv)
	}
	return notifier, nil
}

// PrintPending shows the calls the gateway at url is holding, indented, to
// out. url is the gateway's approvals endpoint (approvals.listen).
func PrintPending(ctx context.Context, out io.Writer, url string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("reach the approvals endpoint: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var pretty bytes.Buffer
	if json.Indent(&pretty, data, "", "  ") == nil {
		data = pretty.Bytes()
	}
	if _, err := out.Write(data); err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("approvals endpoint answered %d", resp.StatusCode)
	}
	return nil
}

// previewArgs is what the listing shows of a request's arguments: masked and
// clipped like the prompt a person approves from.
func previewArgs(args map[string]any) map[string]any {
	shown, _ := redact.Preview(args).(map[string]any)
	return shown
}
