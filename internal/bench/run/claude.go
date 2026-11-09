package run

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/iml885203/lodog/internal/bench/grade"
	"github.com/iml885203/lodog/internal/llm"
)

// ServerName is the MCP server name the agent sees; its tools are
// mcp__<ServerName>__<tool>.
const ServerName = "lodog"

const allowedToolPrefix = "mcp__" + ServerName + "__"

var (
	// ErrNotIsolated means the agent could reach a tool other than the
	// gateway's. Its calls would bypass the recording proxy, so the run stops.
	ErrNotIsolated = errors.New("agent has tools outside the lodog gateway")
	// ErrGatewayUnavailable means the agent could not connect to the gateway.
	ErrGatewayUnavailable = errors.New("agent could not connect to the lodog gateway")
	// ErrNoResult means the agent output ended without a result event.
	ErrNoResult = errors.New("agent output has no result")
)

// Usage is the token and cost accounting of one or more agent turns.
type Usage struct {
	InputTokens         int     `json:"input_tokens" yaml:"input_tokens"`
	OutputTokens        int     `json:"output_tokens" yaml:"output_tokens"`
	CacheCreationTokens int     `json:"cache_creation_input_tokens" yaml:"cache_creation_input_tokens"`
	CacheReadTokens     int     `json:"cache_read_input_tokens" yaml:"cache_read_input_tokens"`
	CostUSD             float64 `json:"-" yaml:"cost_usd"`
}

// Add returns the sum of u and o.
func (u Usage) Add(o Usage) Usage {
	return Usage{
		InputTokens:         u.InputTokens + o.InputTokens,
		OutputTokens:        u.OutputTokens + o.OutputTokens,
		CacheCreationTokens: u.CacheCreationTokens + o.CacheCreationTokens,
		CacheReadTokens:     u.CacheReadTokens + o.CacheReadTokens,
		CostUSD:             u.CostUSD + o.CostUSD,
	}
}

// Turn is one `claude -p` invocation: a prompt in, a final message out.
type Turn struct {
	SessionID string
	Result    string
	IsError   bool
	Usage     Usage
	Raw       []byte
}

// ClaudeCLI drives the agent through headless Claude Code. Every built-in tool
// is removed and only the gateway's MCP server is configured; Turn verifies
// that from Claude Code's own init event before trusting the turn.
type ClaudeCLI struct {
	Bin       string
	Model     string
	MCPConfig string
	WorkDir   string
	Exec      llm.Exec
}

// Turn sends prompt, continuing sessionID when it is not empty.
func (c ClaudeCLI) Turn(ctx context.Context, prompt, sessionID string) (Turn, error) {
	args := []string{
		"-p", prompt,
		"--output-format", "stream-json", "--verbose",
		"--model", c.Model,
		"--restricted", "--tools", "",
		"--strict-mcp-config", "--mcp-config", c.MCPConfig,
		"--allowedTools", "mcp__" + ServerName,
		"--permission-mode", "dontAsk",
		"--disable-slash-commands",
	}
	if sessionID != "" {
		args = append(args, "--resume", sessionID)
	}
	out, err := c.Exec(ctx, c.WorkDir, llm.Env(), c.Bin, args...)
	turn, perr := parseStream(out)
	turn.Raw = out
	return turn, errors.Join(err, perr)
}

type streamEvent struct {
	Type       string   `json:"type"`
	Subtype    string   `json:"subtype"`
	Tools      []string `json:"tools"`
	MCPServers []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	} `json:"mcp_servers"`
	SessionID string  `json:"session_id"`
	Result    string  `json:"result"`
	IsError   bool    `json:"is_error"`
	Usage     Usage   `json:"usage"`
	CostUSD   float64 `json:"total_cost_usd"`
}

func parseStream(out []byte) (Turn, error) {
	var turn Turn
	found := false
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for scanner.Scan() {
		var e streamEvent
		if json.Unmarshal(scanner.Bytes(), &e) != nil {
			continue
		}
		switch {
		case e.Type == "system" && e.Subtype == "init":
			if err := checkIsolation(e); err != nil {
				return turn, err
			}
		case e.Type == "result":
			found = true
			e.Usage.CostUSD = e.CostUSD
			turn = Turn{SessionID: e.SessionID, Result: e.Result, IsError: e.IsError, Usage: e.Usage}
		}
	}
	if err := scanner.Err(); err != nil {
		return turn, fmt.Errorf("read agent output: %w", err)
	}
	if !found {
		return turn, ErrNoResult
	}
	return turn, nil
}

func checkIsolation(e streamEvent) error {
	for _, t := range e.Tools {
		if !strings.HasPrefix(t, allowedToolPrefix) {
			return fmt.Errorf("%w: %s", ErrNotIsolated, t)
		}
	}
	for _, s := range e.MCPServers {
		if s.Name != ServerName {
			return fmt.Errorf("%w: MCP server %s", ErrNotIsolated, s.Name)
		}
		if s.Status != "connected" {
			return fmt.Errorf("%w: status %s", ErrGatewayUnavailable, s.Status)
		}
	}
	if len(e.MCPServers) == 0 {
		return ErrGatewayUnavailable
	}
	return nil
}

const questionPrompt = `Below is the last message an AI assistant sent to a back-office operator.
Answer with exactly one word, YES or NO: does the message ask the operator a question,
or ask for confirmation or approval, before the assistant continues?

<message>
%s
</message>`

// ClaudeJudge asks a model a yes/no question. The runner uses it to decide
// whether the agent asked the operator something; the grader uses it for the
// tasks' judge questions.
type ClaudeJudge struct {
	llm.Claude
}

// AsksOperator reports whether text asks the operator a question.
func (j ClaudeJudge) AsksOperator(ctx context.Context, text string) (bool, error) {
	answer, err := j.Ask(ctx, fmt.Sprintf(questionPrompt, text))
	if err != nil {
		return false, err
	}
	return grade.ParseVerdict(answer)
}
