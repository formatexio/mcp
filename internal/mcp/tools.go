package mcp

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Input types for each tool. Struct tags generate the JSON schema automatically.

// A field without omitempty is marked required in the generated JSON schema,
// so every optional parameter carries omitempty: a client that leaves it out
// must not fail validation. A zero value is omitted from the upstream request,
// which makes the API apply its own default.

type CompileLatexInput struct {
	Latex   string `json:"latex" jsonschema:"Complete LaTeX source code, from \\documentclass to \\end{document}"`
	Engine  string `json:"engine" jsonschema:"Engine to compile with: pdflatex, xelatex, lualatex or latexmk"`
	Runs    int    `json:"runs,omitempty" jsonschema:"Number of compilation passes, needed for references and tables of contents. Optional; limited by the plan"`
	Timeout int    `json:"timeout,omitempty" jsonschema:"Compile timeout in seconds. Optional; limited by the plan"`
}

type CompileSmartInput struct {
	Latex   string `json:"latex" jsonschema:"Complete LaTeX source code, from \\documentclass to \\end{document}"`
	Runs    int    `json:"runs,omitempty" jsonschema:"Number of compilation passes, needed for references and tables of contents. Optional; limited by the plan"`
	Timeout int    `json:"timeout,omitempty" jsonschema:"Compile timeout in seconds. Optional; limited by the plan"`
}

type CheckSyntaxInput struct {
	Latex string `json:"latex" jsonschema:"LaTeX source code to validate"`
}

type ListEnginesInput struct{}

type GetUsageInput struct{}

type CompileAsyncInput struct {
	Latex   string `json:"latex" jsonschema:"Complete LaTeX source code, from \\documentclass to \\end{document}"`
	Engine  string `json:"engine,omitempty" jsonschema:"Engine to compile with: pdflatex, xelatex, lualatex or latexmk. Optional"`
	Runs    int    `json:"runs,omitempty" jsonschema:"Number of compilation passes. Optional; limited by the plan"`
	Timeout int    `json:"timeout,omitempty" jsonschema:"Compile timeout in seconds. Optional; limited by the plan"`
}

type GetJobInput struct {
	JobID string `json:"jobId" jsonschema:"Job ID returned by compile_async"`
}

type ListTemplatesInput struct{}

type CompileTemplateInput struct {
	TemplateID string            `json:"templateId" jsonschema:"Template ID from list_templates"`
	Variables  map[string]string `json:"variables,omitempty" jsonschema:"Values for the template variables, keyed by variable name. Optional"`
}

type FetchPDFBytesInput struct {
	JobID string `json:"jobId" jsonschema:"Job ID of a finished compilation"`
}

// boolPtr returns a pointer to b. ToolAnnotations uses *bool for the hints
// whose protocol default is true, so an explicit false must be a non-nil pointer.
func boolPtr(b bool) *bool { return &b }

// readOnlyTool builds the definition of a tool that only reads from the
// caller's own FormaTeX account (or validates input) and changes nothing, so
// clients may auto-approve it. Its interaction domain is the FormaTeX service
// itself, which makes it a closed world.
//
// The title is set both on the tool and in the annotations: the former is the
// current location, the latter is what clients written against the 2025-03
// revision of the specification read.
func readOnlyTool(name, title, description string) *sdk.Tool {
	return &sdk.Tool{
		Name:        name,
		Title:       title,
		Description: description,
		Annotations: &sdk.ToolAnnotations{
			Title:         title,
			ReadOnlyHint:  true,
			OpenWorldHint: boolPtr(false),
		},
	}
}

// compileTool builds the definition of a tool that starts a compilation. Each
// call creates a new job that counts against the monthly quota, so it is
// neither read-only nor idempotent. It only adds data and never overwrites or
// deletes anything, hence DestructiveHint false. It sends arbitrary content to a
// remote compiler and returns a link to externally hosted output, hence
// OpenWorldHint true.
func compileTool(name, title, description string) *sdk.Tool {
	return &sdk.Tool{
		Name:        name,
		Title:       title,
		Description: description,
		Annotations: &sdk.ToolAnnotations{
			Title:           title,
			ReadOnlyHint:    false,
			DestructiveHint: boolPtr(false),
			IdempotentHint:  false,
			OpenWorldHint:   boolPtr(true),
		},
	}
}

func registerTools(server *sdk.Server, client *Client) {
	sdk.AddTool(server, compileTool(
		"compile_latex",
		"Compile LaTeX to PDF",
		"Compile LaTeX source code to a PDF using the FormaTeX API. Returns a download URL for the PDF. Specify the engine explicitly (pdflatex for standard documents, xelatex/lualatex for Unicode/modern fonts, latexmk for complex documents).",
	), handleCompileLatex(client))

	sdk.AddTool(server, compileTool(
		"compile_smart",
		"Smart Compile LaTeX",
		"Compile LaTeX with automatic engine detection and error fixing. Recommended for most use cases — the smart compiler detects Unicode requirements, applies common fixes, and retries with fallback engines. Returns a download URL for the PDF.",
	), handleCompileSmart(client))

	sdk.AddTool(server, readOnlyTool(
		"check_syntax",
		"Check LaTeX Syntax",
		"Validate LaTeX source code for syntax errors, forbidden commands, and structural issues WITHOUT compiling. Does not count against your monthly compilation quota. Returns validation results and engine recommendations.",
	), handleCheckSyntax(client))

	sdk.AddTool(server, readOnlyTool(
		"list_engines",
		"List LaTeX Engines",
		"List the available LaTeX compilation engines (pdflatex, xelatex, lualatex, latexmk) and when to use each one.",
	), handleListEngines(client))

	sdk.AddTool(server, readOnlyTool(
		"get_usage",
		"Get Usage and Limits",
		"Get the current month's compilation statistics and plan limits for the configured API key. Shows compilations used, monthly limit, and breakdown by engine and status.",
	), handleGetUsage(client))

	sdk.AddTool(server, compileTool(
		"compile_async",
		"Compile LaTeX Asynchronously",
		"Submit a LaTeX compilation job asynchronously and return a job ID immediately. Use get_job to poll the result. Recommended for large documents or batch workflows where you don't need to wait for completion.",
	), handleCompileAsync(client))

	sdk.AddTool(server, readOnlyTool(
		"get_job",
		"Get Compilation Job",
		"Get the status of an async compilation job by ID. If completed, returns a download URL for the PDF (authenticated with your API key — use fetch_pdf_bytes if you need the raw bytes). If failed, returns the error log. Use after compile_async.",
	), handleGetJob(client))

	sdk.AddTool(server, readOnlyTool(
		"list_templates",
		"List Templates",
		"List your saved LaTeX templates. Returns template IDs, names, descriptions and variables. Templates are available on Pro plan and higher. Use the template ID with compile_template, get_template or update_template.",
	), handleListTemplates(client))

	sdk.AddTool(server, compileTool(
		"compile_template",
		"Compile Template",
		"Compile a saved LaTeX template by ID with optional variable substitutions. Returns a download URL for the PDF. Templates are available on Pro plan and higher.",
	), handleCompileTemplate(client))

	registerTemplateTools(server, client)

	sdk.AddTool(server, readOnlyTool(
		"fetch_pdf_bytes",
		"Fetch PDF Bytes",
		"Explicitly fetch a compiled PDF as raw bytes (base64). Only call this when you need to materialize the file — for example, to save it to disk, inspect its contents, or present it as a downloadable artifact. For most use cases the signed URL returned by compile_latex/compile_smart is sufficient and far cheaper in tokens.",
	), handleFetchPDFBytes(client))
}

func handleCompileLatex(client *Client) func(context.Context, *sdk.CallToolRequest, CompileLatexInput) (*sdk.CallToolResult, any, error) {
	return func(ctx context.Context, req *sdk.CallToolRequest, input CompileLatexInput) (*sdk.CallToolResult, any, error) {
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
			}, nil, nil
		}

		engine := result.EngineUsed
		if engine == "" {
			engine = input.Engine
			if engine == "" {
				engine = "pdflatex"
			}
		}

		pdfURL := result.SignedPDFURL
		if pdfURL == "" {
			pdfURL = client.BaseURL() + "/api/v1/compilations/" + result.JobID + "/pdf"
		}
		meta := fmt.Sprintf("Compilation successful. Engine: %s, Duration: %s ms, Job ID: %s, PDF size: %d bytes\nDownload PDF: %s",
			engine, result.DurationMs, result.JobID, len(result.PDFData), pdfURL)

		return &sdk.CallToolResult{
			Content: []sdk.Content{
				&sdk.TextContent{Text: meta},
			},
		}, nil, nil
	}
}

func handleCompileSmart(client *Client) func(context.Context, *sdk.CallToolRequest, CompileSmartInput) (*sdk.CallToolResult, any, error) {
	return func(ctx context.Context, req *sdk.CallToolRequest, input CompileSmartInput) (*sdk.CallToolResult, any, error) {
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
			}, nil, nil
		}

		parts := []string{
			fmt.Sprintf("Smart compilation successful. Duration: %s ms, Job ID: %s, PDF size: %d bytes", result.DurationMs, result.JobID, len(result.PDFData)),
		}
		if result.EngineUsed != "" {
			parts = append(parts, fmt.Sprintf("Engine used: %s", result.EngineUsed))
		}
		if result.AutoFixesCount != "" && result.AutoFixesCount != "0" {
			parts = append(parts, fmt.Sprintf("Auto-fixes applied: %s", result.AutoFixesCount))
		}
		pdfURL := result.SignedPDFURL
		if pdfURL == "" {
			pdfURL = client.BaseURL() + "/api/v1/compilations/" + result.JobID + "/pdf"
		}
		parts = append(parts, fmt.Sprintf("Download PDF: %s", pdfURL))

		return &sdk.CallToolResult{
			Content: []sdk.Content{
				&sdk.TextContent{Text: strings.Join(parts, " | ")},
			},
		}, nil, nil
	}
}

func handleCheckSyntax(client *Client) func(context.Context, *sdk.CallToolRequest, CheckSyntaxInput) (*sdk.CallToolResult, any, error) {
	return func(ctx context.Context, req *sdk.CallToolRequest, input CheckSyntaxInput) (*sdk.CallToolResult, any, error) {
		result, err := client.CheckSyntax(ctx, input.Latex)
		if err != nil {
			return &sdk.CallToolResult{
				Content: []sdk.Content{
					&sdk.TextContent{Text: fmt.Sprintf("Syntax check failed: %s", err.Error())},
				},
				IsError: true,
			}, nil, nil
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
		}, nil, nil
	}
}

func handleListEngines(client *Client) func(context.Context, *sdk.CallToolRequest, ListEnginesInput) (*sdk.CallToolResult, any, error) {
	return func(ctx context.Context, req *sdk.CallToolRequest, input ListEnginesInput) (*sdk.CallToolResult, any, error) {
		engines, err := client.ListEngines(ctx)
		if err != nil {
			return &sdk.CallToolResult{
				Content: []sdk.Content{
					&sdk.TextContent{Text: fmt.Sprintf("Failed to list engines: %s", err.Error())},
				},
				IsError: true,
			}, nil, nil
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
		}, nil, nil
	}
}

func handleCompileAsync(client *Client) func(context.Context, *sdk.CallToolRequest, CompileAsyncInput) (*sdk.CallToolResult, any, error) {
	return func(ctx context.Context, req *sdk.CallToolRequest, input CompileAsyncInput) (*sdk.CallToolResult, any, error) {
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
			}, nil, nil
		}

		return &sdk.CallToolResult{
			Content: []sdk.Content{
				&sdk.TextContent{Text: fmt.Sprintf("Job submitted. ID: %s, Status: %s\nCall get_job with this ID to poll for the result.", result.JobID, result.Status)},
			},
		}, nil, nil
	}
}

func handleGetJob(client *Client) func(context.Context, *sdk.CallToolRequest, GetJobInput) (*sdk.CallToolResult, any, error) {
	return func(ctx context.Context, req *sdk.CallToolRequest, input GetJobInput) (*sdk.CallToolResult, any, error) {
		job, err := client.GetJobStatus(ctx, input.JobID)
		if err != nil {
			return &sdk.CallToolResult{
				Content: []sdk.Content{
					&sdk.TextContent{Text: fmt.Sprintf("Failed to get job status: %s", err.Error())},
				},
				IsError: true,
			}, nil, nil
		}

		switch job.Status {
		case "completed":
			pdfURL := job.SignedPDFURL
			if pdfURL == "" {
				pdfURL = client.BaseURL() + "/api/v1/jobs/" + input.JobID + "/pdf"
			}
			meta := fmt.Sprintf("Job %s completed.\nDownload PDF: %s", input.JobID, pdfURL)
			return &sdk.CallToolResult{
				Content: []sdk.Content{
					&sdk.TextContent{Text: meta},
				},
			}, nil, nil

		case "failed":
			log, err := client.GetJobLog(ctx, input.JobID)
			if err != nil {
				log = fmt.Sprintf("(error log unavailable — it may have expired. Call compile_latex again to retry the compilation.)")
			}
			return &sdk.CallToolResult{
				Content: []sdk.Content{
					&sdk.TextContent{Text: fmt.Sprintf("Job %s failed.\n\nError log:\n%s", input.JobID, log)},
				},
				IsError: true,
			}, nil, nil

		default:
			return &sdk.CallToolResult{
				Content: []sdk.Content{
					&sdk.TextContent{Text: fmt.Sprintf("Job %s status: %s. Call get_job again to check for completion.", input.JobID, job.Status)},
				},
			}, nil, nil
		}
	}
}

func handleListTemplates(client *Client) func(context.Context, *sdk.CallToolRequest, ListTemplatesInput) (*sdk.CallToolResult, any, error) {
	return func(ctx context.Context, req *sdk.CallToolRequest, input ListTemplatesInput) (*sdk.CallToolResult, any, error) {
		result, err := client.ListTemplates(ctx)
		if err != nil {
			return &sdk.CallToolResult{
				Content: []sdk.Content{
					&sdk.TextContent{Text: fmt.Sprintf("Failed to list templates: %s", err.Error())},
				},
				IsError: true,
			}, nil, nil
		}

		if result.Message != "" && len(result.Templates) == 0 {
			return &sdk.CallToolResult{
				Content: []sdk.Content{
					&sdk.TextContent{Text: result.Message},
				},
			}, nil, nil
		}

		if len(result.Templates) == 0 {
			return &sdk.CallToolResult{
				Content: []sdk.Content{
					&sdk.TextContent{Text: "No templates found. Create one with create_template, or in the dashboard at https://formatex.io/dashboard/templates"},
				},
			}, nil, nil
		}

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Found %d template(s):\n\n", len(result.Templates)))
		for _, t := range result.Templates {
			writeTemplate(&sb, t)
			sb.WriteString("\n")
		}
		sb.WriteString("Use compile_template with a template ID to compile it, get_template to read one's source, or update_template to change it.")

		return &sdk.CallToolResult{
			Content: []sdk.Content{
				&sdk.TextContent{Text: sb.String()},
			},
		}, nil, nil
	}
}

func handleCompileTemplate(client *Client) func(context.Context, *sdk.CallToolRequest, CompileTemplateInput) (*sdk.CallToolResult, any, error) {
	return func(ctx context.Context, req *sdk.CallToolRequest, input CompileTemplateInput) (*sdk.CallToolResult, any, error) {
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
			}, nil, nil
		}

		pdfURL := client.BaseURL() + "/api/v1/compilations/" + result.JobID + "/pdf"
		meta := fmt.Sprintf("Template compiled successfully. Duration: %s ms, Job ID: %s, PDF size: %d bytes\nDownload PDF: %s",
			result.DurationMs, result.JobID, len(result.PDFData), pdfURL)

		return &sdk.CallToolResult{
			Content: []sdk.Content{
				&sdk.TextContent{Text: meta},
			},
		}, nil, nil
	}
}

func handleFetchPDFBytes(client *Client) func(context.Context, *sdk.CallToolRequest, FetchPDFBytesInput) (*sdk.CallToolResult, any, error) {
	return func(ctx context.Context, req *sdk.CallToolRequest, input FetchPDFBytesInput) (*sdk.CallToolResult, any, error) {
		pdfBytes, err := client.GetJobPDF(ctx, input.JobID)
		if err != nil {
			return &sdk.CallToolResult{
				Content: []sdk.Content{
					&sdk.TextContent{Text: fmt.Sprintf("Failed to fetch PDF: %s", err.Error())},
				},
				IsError: true,
			}, nil, nil
		}

		pdfB64 := base64.StdEncoding.EncodeToString(pdfBytes)
		return &sdk.CallToolResult{
			Content: []sdk.Content{
				&sdk.TextContent{Text: fmt.Sprintf("PDF fetched. Size: %d bytes. Job ID: %s", len(pdfBytes), input.JobID)},
				&sdk.TextContent{Text: "PDF_BASE64:" + pdfB64},
			},
		}, nil, nil
	}
}

func handleGetUsage(client *Client) func(context.Context, *sdk.CallToolRequest, GetUsageInput) (*sdk.CallToolResult, any, error) {
	return func(ctx context.Context, req *sdk.CallToolRequest, input GetUsageInput) (*sdk.CallToolResult, any, error) {
		usage, err := client.GetUsage(ctx)
		if err != nil {
			return &sdk.CallToolResult{
				Content: []sdk.Content{
					&sdk.TextContent{Text: fmt.Sprintf("Failed to get usage: %s", err.Error())},
				},
				IsError: true,
			}, nil, nil
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
		}, nil, nil
	}
}
