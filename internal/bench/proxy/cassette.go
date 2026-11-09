package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Recorded is one answer of the system, kept so a later run can be replayed
// without it. Cookies keep only their names: the session a sign-in issued is
// never written down, and a replay hands out a placeholder value instead.
type Recorded struct {
	Key         string   `json:"key"`
	Status      int      `json:"status"`
	ContentType string   `json:"content_type,omitempty"`
	Cookies     []string `json:"cookies,omitempty"`
	Body        string   `json:"body,omitempty"`
}

// replayCookie is the value of every cookie a replayed answer sets.
const replayCookie = "lodog-replay"

// maxRecordedBody bounds one recorded answer; a longer one is not recorded,
// so a replay misses it instead of serving half a document.
const maxRecordedBody = 8 << 20

// recordingKey identifies a call across runs: method, path and query, with
// the gateway's credential parameters dropped and time values replaced,
// because a relative window ("the last 30 days") differs on every run.
func recordingKey(method, path string, query url.Values, secret []string) string {
	q := url.Values{}
	for name, values := range query {
		if containsFold(secret, name) {
			continue
		}
		for _, v := range values {
			if isTime(v) {
				v = "<time>"
			}
			q.Add(name, v)
		}
	}
	return strings.ToUpper(method) + " /" + strings.ToLower(strings.Trim(path, "/")) + "?" + q.Encode()
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

var timeLayouts = []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02"}

func isTime(v string) bool {
	for _, layout := range timeLayouts {
		if _, err := time.Parse(layout, v); err == nil {
			return true
		}
	}
	return false
}

// Recorder appends answers to a recording file. Safe for concurrent use.
type Recorder struct {
	mu sync.Mutex
	f  *os.File
}

// NewRecorder appends to path, creating it.
func NewRecorder(path string) (*Recorder, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create recording dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open recording %s: %w", path, err)
	}
	return &Recorder{f: f}, nil
}

// Add writes one answer.
func (r *Recorder) Add(rec Recorded) error {
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode recorded answer: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write recorded answer: %w", err)
	}
	return nil
}

// Close closes the recording file.
func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Close()
}

// Recording is a loaded recording; the last answer for a key wins.
type Recording map[string]Recorded

// LoadRecording reads a file a Recorder wrote.
func LoadRecording(path string) (Recording, error) {
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("read recording: %w", err)
	}
	out := Recording{}
	scanner := bufio.NewScanner(bytes.NewReader(b))
	scanner.Buffer(make([]byte, 0, 1<<20), 2*maxRecordedBody)
	for n := 1; scanner.Scan(); n++ {
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		var rec Recorded
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", path, n, err)
		}
		out[rec.Key] = rec
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read recording %s: %w", path, err)
	}
	if len(out) == 0 {
		return nil, errors.New("recording " + path + " holds no answers")
	}
	return out, nil
}

func (rec Recorded) write(w http.ResponseWriter) {
	if rec.ContentType != "" {
		w.Header().Set("Content-Type", rec.ContentType)
	}
	for _, name := range rec.Cookies {
		http.SetCookie(w, &http.Cookie{Name: name, Value: replayCookie, Path: "/"}) //nolint:gosec // G124: a placeholder for a replayed sign-in, not a credential; Secure would hide it from a plain-HTTP gateway
	}
	w.WriteHeader(rec.Status)
	_, _ = w.Write([]byte(rec.Body))
}

func cookieNames(h http.Header) []string {
	var names []string
	for _, c := range (&http.Response{Header: h}).Cookies() {
		names = append(names, c.Name)
	}
	return names
}
