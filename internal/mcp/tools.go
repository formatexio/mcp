package mcp

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Input types for each tool. Struct tags generate the JSON schema automatically.

type CompileLatexInput struct {
	Latex   string `json:"latex"`
	Engine  string `json:"engine"`
	Runs    int    `json:"runs"`
	Timeout int    `json:"timeout"`
}

type CompileSmartInput struct {
	Latex   string `json:"latex"`
	Runs    int    `json:"runs"`
	Timeout int    `json:"timeout"`
}

type CheckSyntaxInput struct {
	Latex string `json:"latex"`
}

type ListEnginesInput struct{}

type GetUsageInput struct{}

type CompileAsyncInput struct {
	Latex   string `json:"latex"`
	Engine  string `json:"engine,omitempty"`
	Runs    int    `json:"runs,omitempty"`
	Timeout int    `json:"timeout,omitempty"`
}

type GetJobInput struct {
	JobID string `json:"jobId"`
}

type ListTemplatesInput struct{}

type CompileTemplateInput struct {
	TemplateID string            `json:"templateId"`
	Variables  map[string]string `json:"variables"`
}

func registerTools(server *sdk.Server, client *Client) {
	sdk.AddTool(server, &sdk.Tool{
		Name:        "compile_latex",
		Description: "Compile LaTeX source code to a PDF using the FormaTeX API. Returns the PDF as a base64-encoded string. Specify the engine explicitly (pdflatex for standard documents, xelatex/lualatex for Unicode/modern fonts, latexmk for complex documents).",
	}, handleCompileLatex(client))

	sdk.AddTool(server, &sdk.Tool{
		Name:        "compile_smart",
		Description: "Compile LaTeX with automatic engine detection and error fixing. Recommended for most use cases — the smart compiler detects Unicode requirements, applies common fixes, and retries with fallback engines. Returns the PDF as a base64-encoded string.",
	}, handleCompileSmart(client))

	sdk.AddTool(server, &sdk.Tool{
		Name:        "check_syntax",
		Description: "Validate LaTeX source code for syntax errors, forbidden commands, and structural issues WITHOUT compiling. Does not count against your monthly compilation quota. Returns validation results and engine recommendations.",
	}, handleCheckSyntax(client))

	sdk.AddTool(server, &sdk.Tool{
		Name:        "list_engines",
		Description: "List the available LaTeX compilation engines (pdflatex, xelatex, lualatex, latexmk) and when to use each one.",
	}, handleListEngines(client))

	sdk.AddTool(server, &sdk.Tool{
		Name:        "get_usage",
		Description: "Get the current month's compilation statistics and plan limits for the configured API key. Shows compilations used, monthly limit, and breakdown by engine and status.",
	}, handleGetUsage(client))

	sdk.AddTool(server, &sdk.Tool{
		Name:        "compile_async",
		Description: "Submit a LaTeX compilation job asynchronously and return a job ID immediately. Use get_job to poll the result. Recommended for large documents or batch workflows where you don't need to wait for completion.",
	}, handleCompileAsync(client))

	sdk.AddTool(server, &sdk.Tool{
		Name:        "get_job",
		Description: "Get the status of an async compilation job by ID. If the job is completed, automatically fetches and returns the PDF as base64. If failed, fetches and returns the error log. Use after compile_async.",
	}, handleGetJob(client))

	sdk.AddTool(server, &sdk.Tool{
		Name:        "list_templates",
		Description: "List your saved LaTeX templates. Returns template IDs, names, and descriptions. Templates are available on Pro plan and higher. Use the template ID with compile_template.",
	}, handleListTemplates(client))

	sdk.AddTool(server, &sdk.Tool{
		Name:        "compile_template",
		Description: "Compile a saved LaTeX template by ID with optional variable substitutions. Returns the PDF as a base64-encoded string. Templates are available on Pro plan and higher.",
	}, handleCompileTemplate(client))
}

func handleCompileLatex(client *Client) func(context.Context, *sdk.CallToolRequest, CompileLatexInput) (*sdk.CallToolResult, struct{}, error) {
	return func(ctx context.Context, req *sdk.CallToolRequest, input CompileLatexInput) (*sdk.CallToolResult, struct{}, error) {
		result, err := client.Compile(ctx, CompileAPIRequest{
			LaTeX:   input.Latex,
			Engine:  input.Engine,
			Runs:    input.Runs,
			Timeout: input.Timeout,
		})
		if err != nil {
			return &sdk.CallToolResult{
				Content: []sdk.Content{
					&sdk.TextContent{Text: fmt.Sprintf("Compilation failed: %s", err.Error())},
				},
				IsError: true,
			}, struct{}{}, nil
		}

		pdfB64 := base64.StdEncoding.EncodeToString(result.PDFData)
		engine := result.EngineUsed
		if engine == "" {
			engine = input.Engine
			if engine == "" {
				engine = "pdflatex"
			}
		}

		meta := fmt.Sprintf("Compilation successful. Engine: %s, Duration: %s ms, Job ID: %s, PDF size: %d bytes",
			engine, result.DurationMs, result.JobID, len(result.PDFData))

		return &sdk.CallToolResult{
			Content: []sdk.Content{
				&sdk.TextContent{Text: meta},
				&sdk.TextContent{Text: "PDF_BASE64:" + pdfB64},
			},
		}, struct{}{}, nil
	}
}

func handleCompileSmart(client *Client) func(context.Context, *sdk.CallToolRequest, CompileSmartInput) (*sdk.CallToolResult, struct{}, error) {
	return func(ctx context.Context, req *sdk.CallToolRequest, input CompileSmartInput) (*sdk.CallToolResult, struct{}, error) {
		result, err := client.SmartCompile(ctx, SmartCompileAPIRequest{
			LaTeX:    input.Latex,
			Runs:     input.Runs,
			Timeout:  input.Timeout,
			AutoFix:  true,
			Fallback: true,
		})
		if err != nil {
			return &sdk.CallToolResult{
				Content: []sdk.Content{
					&sdk.TextContent{Text: fmt.Sprintf("Smart compilation failed: %s", err.Error())},
				},
				IsError: true,
			}, struct{}{}, nil
		}

		pdfB64 := base64.StdEncoding.EncodeToString(result.PDFData)

		parts := []string{
			fmt.Sprintf("Smart compilation successful. Duration: %s ms, Job ID: %s, PDF size: %d bytes", result.DurationMs, result.JobID, len(result.PDFData)),
		}
		if result.EngineUsed != "" {
			parts = append(parts, fmt.Sprintf("Engine used: %s", result.EngineUsed))
		}
		if result.AutoFixesCount != "" && result.AutoFixesCount != "0" {
			parts = append(parts, fmt.Sprintf("Auto-fixes applied: %s", result.AutoFixesCount))
		}

		return &sdk.CallToolResult{
			Content: []sdk.Content{
				&sdk.TextContent{Text: strings.Join(parts, " | ")},
				&sdk.TextContent{Text: "PDF_BASE64:" + pdfB64},
			},
		}, struct{}{}, nil
	}
}

func handleCheckSyntax(client *Client) func(context.Context, *sdk.CallToolRequest, CheckSyntaxInput) (*sdk.CallToolResult, struct{}, error) {
	return func(ctx context.Context, req *sdk.CallToolRequest, input CheckSyntaxInput) (*sdk.CallToolResult, struct{}, error) {
		result, err := client.CheckSyntax(ctx, input.Latex)
		if err != nil {
			return &sdk.CallToolResult{
				Content: []sdk.Content{
					&sdk.TextContent{Text: fmt.Sprintf("Syntax check failed: %s", err.Error())},
				},
				IsError: true,
			}, struct{}{}, nil
		}

		var sb strings.Builder
		if result.Valid {
			sb.WriteString("LaTeX source is valid.\n")
		} else {
			sb.WriteString("LaTeX source has issues:\n")
			for _, e := range result.Errors {
				sb.WriteString("  - " + e + "\n")
			}
		}

		if result.Analysis != nil {
			sb.WriteString(fmt.Sprintf("\nAnalysis:\n  Recommended engine: %s (confidence: %.0f%%)\n",
				result.Analysis.RecommendedEngine, result.Analysis.Confidence*100))
			if result.Analysis.HasUnicode {
				sb.WriteString("  Unicode characters detected — use xelatex or lualatex\n")
			}
			if result.Analysis.HasLegacyEncoding {
				sb.WriteString("  Legacy encoding packages detected\n")
			}
		}

		if len(result.Suggestions) > 0 {
			sb.WriteString("\nSuggestions:\n")
			for _, s := range result.Suggestions {
				sb.WriteString("  - " + s + "\n")
			}
		}

		return &sdk.CallToolResult{
			Content: []sdk.Content{
				&sdk.TextContent{Text: sb.String()},
			},
		}, struct{}{}, nil
	}
}

func handleListEngines(client *Client) func(context.Context, *sdk.CallToolRequest, ListEnginesInput) (*sdk.CallToolResult, struct{}, error) {
	return func(ctx context.Context, req *sdk.CallToolRequest, input ListEnginesInput) (*sdk.CallToolResult, struct{}, error) {
		engines, err := client.ListEngines(ctx)
		if err != nil {
			return &sdk.CallToolResult{
				Content: []sdk.Content{
					&sdk.TextContent{Text: fmt.Sprintf("Failed to list engines: %s", err.Error())},
				},
				IsError: true,
			}, struct{}{}, nil
		}

		descriptions := map[string]string{
			"pdflatex": "Standard engine. Fast, wide compatibility, best for most documents. Does not support system fonts or Unicode-heavy documents.",
			"xelatex":  "Modern engine with Unicode support and access to system fonts. Recommended for documents with non-Latin characters or advanced typography.",
			"lualatex": "Lua-based engine with full Unicode support and programmability. Slightly slower than XeLaTeX but more flexible.",
			"latexmk":  "Automation tool that runs the correct engine and number of passes automatically. Best for complex documents with bibliography and cross-references.",
		}

		var sb strings.Builder
		sb.WriteString("Available LaTeX engines:\n\n")
		for _, e := range engines {
			if desc, ok := descriptions[e]; ok {
				sb.WriteString(fmt.Sprintf("  %s: %s\n\n", e, desc))
			} else {
				sb.WriteString(fmt.Sprintf("  %s\n\n", e))
			}
		}
		sb.WriteString("Tip: Use compile_smart to let the API choose the best engine automatically.")

		return &sdk.CallToolResult{
			Content: []sdk.Content{
				&sdk.TextContent{Text: sb.String()},
			},
		}, struct{}{}, nil
	}
}

func handleCompileAsync(client *Client) func(context.Context, *sdk.CallToolRequest, CompileAsyncInput) (*sdk.CallToolResult, struct{}, error) {
	return func(ctx context.Context, req *sdk.CallToolRequest, input CompileAsyncInput) (*sdk.CallToolResult, struct{}, error) {
		result, err := client.CompileAsync(ctx, CompileAPIRequest{
			LaTeX:   input.Latex,
			Engine:  input.Engine,
			Runs:    input.Runs,
			Timeout: input.Timeout,
		})
		if err != nil {
			return &sdk.CallToolResult{
				Content: []sdk.Content{
					&sdk.TextContent{Text: fmt.Sprintf("Async compilation failed: %s", err.Error())},
				},
				IsError: true,
			}, struct{}{}, nil
		}

		return &sdk.CallToolResult{
			Content: []sdk.Content{
				&sdk.TextContent{Text: fmt.Sprintf("Job submitted. ID: %s, Status: %s\nCall get_job with this ID to poll for the result.", result.JobID, result.Status)},
			},
		}, struct{}{}, nil
	}
}

func handleGetJob(client *Client) func(context.Context, *sdk.CallToolRequest, GetJobInput) (*sdk.CallToolResult, struct{}, error) {
	return func(ctx context.Context, req *sdk.CallToolRequest, input GetJobInput) (*sdk.CallToolResult, struct{}, error) {
		job, err := client.GetJobStatus(ctx, input.JobID)
		if err != nil {
			return &sdk.CallToolResult{
				Content: []sdk.Content{
					&sdk.TextContent{Text: fmt.Sprintf("Failed to get job status: %s", err.Error())},
				},
				IsError: true,
			}, struct{}{}, nil
		}

		switch job.Status {
		case "completed":
			pdfBytes, err := client.GetJobPDF(ctx, input.JobID)
			if err != nil {
				return &sdk.CallToolResult{
					Content: []sdk.Content{
						&sdk.TextContent{Text: fmt.Sprintf("Job completed but failed to fetch PDF: %s", err.Error())},
					},
					IsError: true,
				}, struct{}{}, nil
			}
			pdfB64 := base64.StdEncoding.EncodeToString(pdfBytes)
			meta := fmt.Sprintf("Job %s completed. PDF size: %d bytes.", input.JobID, len(pdfBytes))
			return &sdk.CallToolResult{
				Content: []sdk.Content{
					&sdk.TextContent{Text: meta},
					&sdk.TextContent{Text: "PDF_BASE64:" + pdfB64},
				},
			}, struct{}{}, nil

		case "failed":
			log, err := client.GetJobLog(ctx, input.JobID)
			if err != nil {
				log = fmt.Sprintf("(could not fetch error log: %s)", err.Error())
			}
			return &sdk.CallToolResult{
				Content: []sdk.Content{
					&sdk.TextContent{Text: fmt.Sprintf("Job %s failed.\n\nError log:\n%s", input.JobID, log)},
				},
				IsError: true,
			}, struct{}{}, nil

		default:
			return &sdk.CallToolResult{
				Content: []sdk.Content{
					&sdk.TextContent{Text: fmt.Sprintf("Job %s status: %s. Call get_job again to check for completion.", input.JobID, job.Status)},
				},
			}, struct{}{}, nil
		}
	}
}

func handleListTemplates(client *Client) func(context.Context, *sdk.CallToolRequest, ListTemplatesInput) (*sdk.CallToolResult, struct{}, error) {
	return func(ctx context.Context, req *sdk.CallToolRequest, input ListTemplatesInput) (*sdk.CallToolResult, struct{}, error) {
		result, err := client.ListTemplates(ctx)
		if err != nil {
			return &sdk.CallToolResult{
				Content: []sdk.Content{
					&sdk.TextContent{Text: fmt.Sprintf("Failed to list templates: %s", err.Error())},
				},
				IsError: true,
			}, struct{}{}, nil
		}

		if result.Message != "" && len(result.Templates) == 0 {
			return &sdk.CallToolResult{
				Content: []sdk.Content{
					&sdk.TextContent{Text: result.Message},
				},
			}, struct{}{}, nil
		}

		if len(result.Templates) == 0 {
			return &sdk.CallToolResult{
				Content: []sdk.Content{
					&sdk.TextContent{Text: "No templates found. Create templates at https://formatex.io/dashboard/templates"},
				},
			}, struct{}{}, nil
		}

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Found %d template(s):\n\n", len(result.Templates)))
		for _, t := range result.Templates {
			sb.WriteString(fmt.Sprintf("ID: %s\n  Name: %s\n  Category: %s\n  Engine: %s\n", t.ID, t.Name, t.Category, t.Engine))
			if t.Description != "" {
				sb.WriteString(fmt.Sprintf("  Description: %s\n", t.Description))
			}
			if len(t.Schema) > 0 {
				sb.WriteString("  Variables:\n")
				for _, v := range t.Schema {
					req := "optional"
					if v.Required {
						req = "required"
					}
					line := fmt.Sprintf("    - %s (%s, %s)", v.Key, v.Type, req)
					if v.Default != "" {
						line += fmt.Sprintf(", default: %q", v.Default)
					}
					if v.Description != "" {
						line += fmt.Sprintf(" — %s", v.Description)
					}
					sb.WriteString(line + "\n")
				}
			} else {
				sb.WriteString("  Variables: none\n")
			}
			sb.WriteString("\n")
		}
		sb.WriteString("Use compile_template with a template ID to compile it.")

		return &sdk.CallToolResult{
			Content: []sdk.Content{
				&sdk.TextContent{Text: sb.String()},
			},
		}, struct{}{}, nil
	}
}

func handleCompileTemplate(client *Client) func(context.Context, *sdk.CallToolRequest, CompileTemplateInput) (*sdk.CallToolResult, struct{}, error) {
	return func(ctx context.Context, req *sdk.CallToolRequest, input CompileTemplateInput) (*sdk.CallToolResult, struct{}, error) {
		if input.Variables == nil {
			input.Variables = map[string]string{}
		}
		result, err := client.CompileTemplate(ctx, input.TemplateID, input.Variables)
		if err != nil {
			return &sdk.CallToolResult{
				Content: []sdk.Content{
					&sdk.TextContent{Text: fmt.Sprintf("Template compilation failed: %s", err.Error())},
				},
				IsError: true,
			}, struct{}{}, nil
		}

		pdfB64 := base64.StdEncoding.EncodeToString(result.PDFData)
		meta := fmt.Sprintf("Template compiled successfully. Duration: %s ms, Job ID: %s, PDF size: %d bytes",
			result.DurationMs, result.JobID, len(result.PDFData))

		return &sdk.CallToolResult{
			Content: []sdk.Content{
				&sdk.TextContent{Text: meta},
				&sdk.TextContent{Text: "PDF_BASE64:" + pdfB64},
			},
		}, struct{}{}, nil
	}
}

func handleGetUsage(client *Client) func(context.Context, *sdk.CallToolRequest, GetUsageInput) (*sdk.CallToolResult, struct{}, error) {
	return func(ctx context.Context, req *sdk.CallToolRequest, input GetUsageInput) (*sdk.CallToolResult, struct{}, error) {
		usage, err := client.GetUsage(ctx)
		if err != nil {
			return &sdk.CallToolResult{
				Content: []sdk.Content{
					&sdk.TextContent{Text: fmt.Sprintf("Failed to get usage: %s", err.Error())},
				},
				IsError: true,
			}, struct{}{}, nil
		}

		remaining := usage.Compilations.Limit - usage.Compilations.Used
		if remaining < 0 {
			remaining = 0
		}

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Plan: %s\n", usage.Plan))
		sb.WriteString(fmt.Sprintf("Period: %s to %s\n\n", usage.Period.Start, usage.Period.End))
		sb.WriteString(fmt.Sprintf("Compilations: %d / %d used (%d remaining)\n",
			usage.Compilations.Used, usage.Compilations.Limit, remaining))

		if usage.Compilations.Overage > 0 {
			sb.WriteString(fmt.Sprintf("Overage: %d compilations over limit\n", usage.Compilations.Overage))
		}

		if len(usage.Breakdown.ByEngine) > 0 {
			sb.WriteString("\nBreakdown by engine:\n")
			for engine, count := range usage.Breakdown.ByEngine {
				sb.WriteString(fmt.Sprintf("  %s: %d\n", engine, count))
			}
		}

		if len(usage.Breakdown.ByStatus) > 0 {
			sb.WriteString("\nBreakdown by status:\n")
			for status, count := range usage.Breakdown.ByStatus {
				sb.WriteString(fmt.Sprintf("  %s: %d\n", status, count))
			}
		}

		if usage.Breakdown.AvgDurationMs > 0 {
			sb.WriteString(fmt.Sprintf("\nAverage compilation time: %d ms\n", usage.Breakdown.AvgDurationMs))
		}

		return &sdk.CallToolResult{
			Content: []sdk.Content{
				&sdk.TextContent{Text: sb.String()},
			},
		}, struct{}{}, nil
	}
}
