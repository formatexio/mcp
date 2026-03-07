package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerPrompts(server *sdk.Server) {
	server.AddPrompt(&sdk.Prompt{
		Name:        "fix_latex",
		Description: "Guided LaTeX error diagnosis workflow. Takes the LaTeX source and compilation error log, returns structured instructions to fix the issues.",
		Arguments: []*sdk.PromptArgument{
			{Name: "latex_source", Description: "The LaTeX source code that failed to compile", Required: true},
			{Name: "error_log", Description: "The compilation error log output", Required: true},
			{Name: "engine", Description: "The LaTeX engine that was used (pdflatex, xelatex, lualatex, latexmk)", Required: false},
		},
	}, handleFixLatexPrompt)
}

func handleFixLatexPrompt(ctx context.Context, req *sdk.GetPromptRequest) (*sdk.GetPromptResult, error) {
	latexSource := req.Params.Arguments["latex_source"]
	errorLog := req.Params.Arguments["error_log"]
	engine := req.Params.Arguments["engine"]
	if engine == "" {
		engine = "pdflatex"
	}

	if latexSource == "" {
		return nil, fmt.Errorf("latex_source argument is required")
	}
	if errorLog == "" {
		return nil, fmt.Errorf("error_log argument is required")
	}

	message := buildFixLatexMessage(latexSource, errorLog, engine)

	return &sdk.GetPromptResult{
		Description: "LaTeX error diagnosis and fix instructions",
		Messages: []*sdk.PromptMessage{
			{Role: "user", Content: &sdk.TextContent{Text: message}},
		},
	}, nil
}

func buildFixLatexMessage(latexSource, errorLog, engine string) string {
	var sb strings.Builder

	sb.WriteString("I need help fixing a LaTeX compilation error. Please analyze the error and provide specific fixes.\n\n")

	sb.WriteString("## LaTeX Source\n```latex\n")
	sb.WriteString(latexSource)
	sb.WriteString("\n```\n\n")

	sb.WriteString(fmt.Sprintf("## Compilation Error (engine: %s)\n```\n", engine))
	sb.WriteString(errorLog)
	sb.WriteString("\n```\n\n")

	sb.WriteString("## Instructions\n\n")
	sb.WriteString("Please:\n")
	sb.WriteString("1. Identify the root cause of the compilation error from the log\n")
	sb.WriteString("2. Locate the specific line(s) in the source that cause the error\n")
	sb.WriteString("3. Provide the corrected LaTeX source with the fix applied\n")
	sb.WriteString("4. Explain what was wrong and why the fix works\n")
	sb.WriteString("5. If the engine choice contributed to the error, suggest the right engine\n\n")

	sb.WriteString("Common issues to check:\n")
	sb.WriteString("- Missing \\usepackage declarations\n")
	sb.WriteString("- Unicode characters requiring xelatex or lualatex\n")
	sb.WriteString("- Conflicting package options\n")
	sb.WriteString("- Unclosed environments or mismatched braces\n")
	sb.WriteString("- Missing \\begin{document} or \\end{document}\n")
	sb.WriteString("- Deprecated commands (use \\textbf instead of {\\bf}, etc.)\n\n")

	sb.WriteString("After providing the fix, compile the corrected source using the compile_smart tool to verify it works.")

	return sb.String()
}
