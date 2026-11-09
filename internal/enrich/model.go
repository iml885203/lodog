package enrich

import (
	"context"

	"github.com/iml885203/lodog/internal/llm"
)

// Claude is the Model lodog uses: the shared headless Claude asker, given
// read-only access to the legacy source when there is one.
type Claude struct{ llm.Claude }

// Complete answers one prompt.
func (c Claude) Complete(ctx context.Context, prompt, sourceDir string) (string, error) {
	asker := c.Claude
	asker.ReadDir = sourceDir
	return asker.Ask(ctx, prompt)
}
