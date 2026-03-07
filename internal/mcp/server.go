package mcp

import (
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewMCPServer creates and configures the FormaTeX MCP server.
// It registers all tools, resources, and prompts backed by the FormaTeX REST API.
func NewMCPServer(client *Client) *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{
		Name:    "formatex-mcp",
		Version: "1.0.0",
	}, nil)

	registerTools(server, client)
	registerResources(server, client)
	registerPrompts(server)

	return server
}
