package approval

import (
	"fmt"
	"strings"
)

// escape makes text safe inside Slack mrkdwn: an agent's words cannot become
// a mention, a link or markup that looks like part of the gateway's message.
func escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "`", "'", "*", "∗", "_", "ˍ", "~", "∼").Replace(s)
}

// factBlocks is the part of a request every Slack message carries: the
// one-line summary and the blocks saying who wants what, where, how risky,
// and why.
func factBlocks(n Notice) (string, []map[string]any) {
	who := escape(n.OnBehalfOf)
	if who == "" {
		who = escape(n.Actor)
	}
	headline := fmt.Sprintf("*%s*（%s）的 agent 想要：*%s*", who, escape(n.Actor), escape(orDefault(n.What, n.Method+" "+n.Target)))
	lines := []string{
		headline,
		fmt.Sprintf("• 對象：`%s %s`", escape(n.Method), escape(n.Target)),
		fmt.Sprintf("• 風險：*%s*", escape(n.Risk)),
	}
	if n.Reason != "" {
		lines = append(lines, "• agent 自述的理由（不是 gateway 查證過的）：\n> "+escape(n.Reason))
	}
	summary := "需要核准：" + orDefault(n.What, n.Method+" "+n.Target)
	return summary, []map[string]any{
		section(":lock: *需要核准*  `" + n.ID + "`"),
		section(strings.Join(lines, "\n")),
	}
}

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func section(text string) map[string]any {
	return map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": text}}
}
