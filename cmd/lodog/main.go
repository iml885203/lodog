// Command lodog turns a legacy API's OpenAPI document into an agent-friendly
// MCP gateway, and scores how agent-friendly the result is.
package main

import (
	"os"

	"github.com/iml885203/lodog/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
