// Command formatex-mcp is the FormaTeX MCP server: it exposes the FormaTeX
// LaTeX compilation API to MCP clients over stdio, or over Streamable HTTP
// with --http.
package main

import "github.com/formatexio/mcp/internal/app"

func main() { app.Main() }
