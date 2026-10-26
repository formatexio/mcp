package mcp

import (
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Version is the FormaTeX MCP server version. It is reported to clients in the
// initialize handshake, by the HTTP /health endpoint and by the --version flag.
const Version = "1.1.1"

// NewMCPServer creates and configures the FormaTeX MCP server.
// It registers all tools, resources, and prompts backed by the FormaTeX REST API.
func NewMCPServer(client *Client) *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{
		Name:    "formatex-mcp",
		Version: Version,
	}, nil)

	registerTools(server, client)
	registerResources(server, client)
	registerPrompts(server)

	return server
}
