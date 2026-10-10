// Command mcp is the FormaTeX MCP server. It exists so that
// "go install github.com/formatexio/mcp@latest" keeps working; the documented
// install path is "go install github.com/formatexio/mcp/cmd/formatex-mcp@latest",
// which installs the same program under the name formatex-mcp.
package main

import "github.com/formatexio/mcp/internal/app"

func main() { app.Main() }
