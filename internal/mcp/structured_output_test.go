package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// A tool handler whose output type is not `any` makes the SDK generate an output
// schema and always send the marshalled output as structuredContent. Every
// handler here used struct{}, so clients that prefer structuredContent over the
// text content (Claude Code, Claude desktop) showed "{}" for every tool and the
// real text -- the PDF URL, the usage, the error log -- was lost. These tests
// pin the fix: text content only, no structured output.

// fakeFormaTeXAPI answers the three endpoints the tools below call.
func fakeFormaTeXAPI(t *testing.T, status int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status >= 400 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"missing API key"}`))
			return
		}
		switch r.URL.Path {
		case "/api/v1/engines":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"engines":["pdflatex","xelatex"]}`))
		case "/api/v1/usage":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"plan":"pro","period":{"start":"2026-10-01","end":"2026-10-31"},"compilations":{"used":12,"limit":100,"overage":0},"breakdown":{"byEngine":{"pdflatex":12},"byStatus":{"success":12},"avgDurationMs":420}}`))
		case "/api/v1/compile":
			w.Header().Set("X-Job-ID", "job-123")
			w.Header().Set("X-Compile-Duration-Ms", "350")
			w.Header().Set("X-Signed-PDF-URL", "https://example.test/pdf/job-123")
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write([]byte("%PDF-1.5 fake"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func callText(t *testing.T, session *sdk.ClientSession, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	res, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	return res
}

func requireTextOnly(t *testing.T, res *sdk.CallToolResult, want ...string) string {
	t.Helper()
	if res.StructuredContent != nil {
		t.Errorf("StructuredContent = %#v, want nil: clients would show it instead of the text", res.StructuredContent)
	}
	if len(res.Content) != 1 {
		t.Fatalf("Content has %d items, want 1", len(res.Content))
	}
	text, ok := res.Content[0].(*sdk.TextContent)
	if !ok {
		t.Fatalf("Content[0] is %T, want *TextContent", res.Content[0])
	}
	for _, w := range want {
		if !strings.Contains(text.Text, w) {
			t.Errorf("text %q does not contain %q", text.Text, w)
		}
	}
	return text.Text
}

func TestToolsReturnTextNotEmptyStructuredContent(t *testing.T) {
	session := connectTo(t, fakeFormaTeXAPI(t, http.StatusOK))

	t.Run("list_engines", func(t *testing.T) {
		res := callText(t, session, "list_engines", nil)
		if res.IsError {
			t.Fatal("IsError set")
		}
		requireTextOnly(t, res, "Available LaTeX engines", "pdflatex", "xelatex")
	})

	t.Run("get_usage", func(t *testing.T) {
		res := callText(t, session, "get_usage", nil)
		if res.IsError {
			t.Fatal("IsError set")
		}
		requireTextOnly(t, res, "Plan: pro", "12 / 100 used", "88 remaining")
	})

	t.Run("compile_latex", func(t *testing.T) {
		res := callText(t, session, "compile_latex", map[string]any{"latex": `\documentclass{article}\begin{document}Hi\end{document}`, "engine": "pdflatex"})
		if res.IsError {
			t.Fatalf("IsError set: %+v", res.Content)
		}
		requireTextOnly(t, res, "Compilation successful", "job-123", "https://example.test/pdf/job-123")
	})
}

func TestFailuresStayTextWithIsError(t *testing.T) {
	session := connectTo(t, fakeFormaTeXAPI(t, http.StatusUnauthorized))

	res := callText(t, session, "list_engines", nil)
	if !res.IsError {
		t.Error("IsError not set for an API failure")
	}
	requireTextOnly(t, res, "Failed to list engines")
}

func TestToolsDeclareNoOutputSchema(t *testing.T) {
	// An output schema is what makes the SDK fill structuredContent.
	res, err := connect(t).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tool := range res.Tools {
		if tool.OutputSchema != nil {
			t.Errorf("%s declares an output schema; handlers must return `any` so only text content is sent", tool.Name)
		}
	}
}
