package approval

import (
	"context"
	"log/slog"
	"maps"
	"sync"
	"time"

	"github.com/iml885203/lodog/internal/redact"
)

// Notice is what a person is told about one held call: everything they need
// to decide, in the words of the case's overlay and workflows.
type Notice struct {
	ID         string
	OnBehalfOf string
	Actor      string
	// What is the task or operation in plain words (workflow or overlay summary).
	What   string
	Method string
	// Target is the concrete path the call would hit.
	Target string
	Risk   string
	// Reason is the agent's own account of why it wants this. It is
	// untrusted text: a notifier shows it as quoted data, never as markup.
	Reason    string
	Arguments map[string]any
	ExpiresAt time.Time
}

// NoticeState is where a notified approval stands, for updating the message.
type NoticeState string

// The states a notice moves through after it is sent.
const (
	NoticeApproved NoticeState = "approved"
	NoticeDenied   NoticeState = "denied"
	NoticeUsed     NoticeState = "used"
	NoticeExpired  NoticeState = "expired"
)

// Verdict is a verdict a person gave through a notification channel. The
// channel has itself checked that this person may decide the request (the
// link that reached them was the credential); By is the name it records.
type Verdict struct {
	ID      string
	Approve bool
	By      string
}

// Answer tells the person who answered what came of it.
type Answer struct {
	Accepted bool
	Message  string
}

// Notifier is a channel that asks people to approve held calls and carries
// their answer back. Only someone the channel reached can answer, so nothing
// an agent can reach decides an approval.
type Notifier interface {
	// Notify sends the request and returns a reference Update can use.
	Notify(ctx context.Context, n Notice) (ref string, err error)
	// Update rewrites the sent message to show state. by is the deciding
	// identity for approved and denied, empty otherwise.
	Update(ctx context.Context, ref string, n Notice, state NoticeState, by string) error
	// Listen delivers verdicts to handle until ctx ends.
	Listen(ctx context.Context, handle func(ctx context.Context, d Verdict) Answer) error
}

// notifyTimeout bounds one call to a channel, so a slow channel cannot hold
// an agent's tool call open.
const notifyTimeout = 10 * time.Second

// Hub connects held calls to the notification channel: it announces
// each new request, applies the verdicts the channel delivers, and keeps what
// was sent true as the approval moves on.
type Hub struct {
	approvals *Store
	log       *slog.Logger
	// After schedules the expiry update; tests replace it.
	After func(d time.Duration, f func())

	mu       sync.Mutex
	notifier Notifier
	sent     map[string]sentNotice
}

type sentNotice struct {
	ref    string
	notice Notice
}

// NewHub connects store to its notification channel.
func NewHub(a *Store, log *slog.Logger) *Hub {
	h := &Hub{
		approvals: a, log: log, sent: map[string]sentNotice{},
		After: func(d time.Duration, f func()) { time.AfterFunc(d, f) },
	}
	a.observe = h.settled
	return h
}

// Use installs the channel.
func (h *Hub) Use(n Notifier) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notifier = n
}

// Channel is the installed notifier, nil when there is none.
func (h *Hub) Channel() Notifier {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.notifier
}

// Configured reports whether a channel is installed.
func (h *Hub) Configured() bool { return h != nil && h.Channel() != nil }

// Announce sends a freshly held request to the channel and reports whether a
// person has been told. A failure is logged and the call stays held.
func (h *Hub) Announce(ctx context.Context, req *Request, what, reason string) bool {
	if h == nil {
		return false
	}
	n := h.Channel()
	if n == nil {
		return false
	}
	notice := NoticeFor(req, what, reason)
	sendCtx, cancel := context.WithTimeout(ctx, notifyTimeout)
	defer cancel()
	ref, err := n.Notify(sendCtx, notice)
	if err != nil {
		h.log.Error("approval notification was not sent: nobody has been asked, and nothing else can approve this call; the agent has to request approval again", "id", req.ID, "error", err.Error())
		return false
	}
	h.mu.Lock()
	h.sent[req.ID] = sentNotice{ref: ref, notice: notice}
	h.mu.Unlock()
	later := context.WithoutCancel(ctx)
	h.After(TTL, func() { h.expired(later, req.ID) })
	return true
}

// Handle applies a verdict from the channel, which has already checked who may
// give it.
func (h *Hub) Handle(_ context.Context, d Verdict) Answer {
	req, err := h.approvals.Decide(d.ID, d.Approve, d.By)
	if err != nil {
		return Answer{Message: "這筆核准已經不能再決定：" + err.Error()}
	}
	h.log.Info("approval decided", "id", req.ID, "status", req.Status, "by", req.DecidedBy, "tool", req.Tool)
	return Answer{Accepted: true}
}

// settled follows an approval's state into the sent message.
func (h *Hub) settled(req Request) {
	switch req.Status {
	case Approved:
		h.update(context.Background(), req.ID, NoticeApproved, req.DecidedBy)
	case Denied:
		h.update(context.Background(), req.ID, NoticeDenied, req.DecidedBy)
	case Used:
		h.update(context.Background(), req.ID, NoticeUsed, "")
	case Pending, InFlight:
	}
}

func (h *Hub) expired(ctx context.Context, id string) {
	if h.approvals.status(id) == Pending {
		h.update(ctx, id, NoticeExpired, "")
	}
}

func (h *Hub) update(ctx context.Context, id string, state NoticeState, by string) {
	n := h.Channel()
	h.mu.Lock()
	s, ok := h.sent[id]
	h.mu.Unlock()
	if n == nil || !ok {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, notifyTimeout)
	defer cancel()
	if err := n.Update(ctx, s.ref, s.notice, state, by); err != nil {
		h.log.Warn("approval message could not be updated", "id", id, "state", state, "error", err.Error())
	}
}

// NoticeFor is what a person is told about a held request. what is the task
// or operation in plain words; reason is the agent's own account.
func NoticeFor(req *Request, what, reason string) Notice {
	return Notice{
		ID: req.ID, OnBehalfOf: req.OnBehalfOf, Actor: req.Actor, What: what, Method: req.Method, Target: redact.MaskString(req.Path),
		Risk: req.Risk, Reason: reason, Arguments: maps.Clone(req.Arguments), ExpiresAt: req.CreatedAt.Add(TTL),
	}
}
