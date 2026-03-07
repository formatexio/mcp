package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerResources(server *sdk.Server, client *Client) {
	server.AddResource(&sdk.Resource{
		URI:         "formatex://engines",
		Name:        "FormaTeX Engines",
		Description: "Available LaTeX compilation engines with descriptions and guidance on when to use each one.",
		MIMEType:    "application/json",
	}, handleEnginesResource(client))

	server.AddResource(&sdk.Resource{
		URI:         "formatex://usage",
		Name:        "FormaTeX Usage",
		Description: "Current month compilation usage statistics and plan limits for the configured API key.",
		MIMEType:    "application/json",
	}, handleUsageResource(client))
}

func handleEnginesResource(client *Client) sdk.ResourceHandler {
	return func(ctx context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
		engines, err := client.ListEngines(ctx)
		if err != nil {
			return nil, fmt.Errorf("fetch engines: %w", err)
		}

		engineDocs := []map[string]string{}
		descriptions := map[string]string{
			"pdflatex": "Standard engine. Fast, wide compatibility, best for most documents. Does not support system fonts or Unicode-heavy documents.",
			"xelatex":  "Modern engine with Unicode support and access to system fonts. Recommended for documents with non-Latin characters or advanced typography.",
			"lualatex": "Lua-based engine with full Unicode support and programmability. Slightly slower than XeLaTeX but more flexible.",
			"latexmk":  "Automation tool that runs the correct engine and number of passes automatically. Best for complex documents with bibliography and cross-references.",
		}

		for _, e := range engines {
			entry := map[string]string{"name": e}
			if desc, ok := descriptions[e]; ok {
				entry["description"] = desc
			}
			engineDocs = append(engineDocs, entry)
		}

		data, err := json.MarshalIndent(map[string]interface{}{
			"engines": engineDocs,
			"tip":     "Use compile_smart to auto-detect the best engine for your document.",
		}, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("marshal engines: %w", err)
		}

		return &sdk.ReadResourceResult{
			Contents: []*sdk.ResourceContents{{
				URI:      req.Params.URI,
				MIMEType: "application/json",
				Text:     string(data),
			}},
		}, nil
	}
}

func handleUsageResource(client *Client) sdk.ResourceHandler {
	return func(ctx context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
		usage, err := client.GetUsage(ctx)
		if err != nil {
			return nil, fmt.Errorf("fetch usage: %w", err)
		}

		data, err := json.MarshalIndent(usage, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("marshal usage: %w", err)
		}

		return &sdk.ReadResourceResult{
			Contents: []*sdk.ResourceContents{{
				URI:      req.Params.URI,
				MIMEType: "application/json",
				Text:     string(data),
			}},
		}, nil
	}
}
