package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// recordedRequest is what the fake API saw for one call.
type recordedRequest struct {
	Method string
	Path   string
	Key    string
	Body   map[string]json.RawMessage
}

// fakeAPI answers every request with the given status and body and records
// what it received.
type fakeAPI struct {
	mu       sync.Mutex
	requests []recordedRequest
}

func (f *fakeAPI) serve(t *testing.T, status int, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		rec := recordedRequest{Method: r.Method, Path: r.URL.EscapedPath(), Key: r.Header.Get("X-API-Key")}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &rec.Body); err != nil {
				t.Errorf("request body is not a JSON object: %s", raw)
			}
		}
		f.mu.Lock()
		f.requests = append(f.requests, rec)
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func (f *fakeAPI) only(t *testing.T) recordedRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 1 {
		t.Fatalf("API received %d requests, want 1: %+v", len(f.requests), f.requests)
	}
	return f.requests[0]
}

func (f *fakeAPI) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func callTool(t *testing.T, session *sdk.ClientSession, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	res, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	return res
}

func resultText(t *testing.T, res *sdk.CallToolResult, i int) string {
	t.Helper()
	if i >= len(res.Content) {
		t.Fatalf("result has %d content blocks, want at least %d", len(res.Content), i+1)
	}
	tc, ok := res.Content[i].(*sdk.TextContent)
	if !ok {
		t.Fatalf("content block %d is %T, want text", i, res.Content[i])
	}
	return tc.Text
}

const savedTemplateJSON = `{"template":{
	"id":"11111111-2222-3333-4444-555555555555",
	"name":"Invoice",
	"description":"Monthly invoice",
	"category":"Business",
	"engine":"xelatex",
	"source":"\\documentclass{article}\n\\begin{document}\nHello <<client>>\n\\end{document}",
	"schema":[{"key":"client","type":"string","required":true,"default":"","description":"Who is billed"}],
	"updatedAt":"2026-10-10T12:00:00Z"
}}`

func TestCreateTemplate(t *testing.T) {
	var api fakeAPI
	session := connectTo(t, api.serve(t, http.StatusCreated, savedTemplateJSON))

	res := callTool(t, session, "create_template", map[string]any{
		"name":     "  Invoice ",
		"source":   `\documentclass{article}` + "\n" + `\begin{document}Hello <<client>>\end{document}`,
		"category": "business",
		"engine":   "XeLaTeX",
		"schema": []any{
			map[string]any{"key": "client", "required": true, "description": "Who is billed"},
		},
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(t, res, 0))
	}

	req := api.only(t)
	if req.Method != http.MethodPost || req.Path != "/api/v1/templates" {
		t.Errorf("request = %s %s, want POST /api/v1/templates", req.Method, req.Path)
	}
	if req.Key != "dummy" {
		t.Errorf("X-API-Key = %q, want the client key", req.Key)
	}
	for field, want := range map[string]string{
		"name":     `"Invoice"`,
		"category": `"Business"`,
		"engine":   `"xelatex"`,
		"schema":   `[{"key":"client","type":"string","required":true,"default":"","description":"Who is billed"}]`,
	} {
		if got := string(req.Body[field]); got != want {
			t.Errorf("body.%s = %s, want %s", field, got, want)
		}
	}
	if _, present := req.Body["description"]; present {
		t.Error("an empty description should not be sent")
	}

	text := resultText(t, res, 0)
	for _, want := range []string{"Template created.", "ID: 11111111-2222-3333-4444-555555555555", "client (string, required)", "compile_template"} {
		if !strings.Contains(text, want) {
			t.Errorf("result lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Notes:") {
		t.Errorf("a consistent template should have no notes:\n%s", text)
	}
}

// A template with no variables must still send "schema": [] and not null, and
// the API default applies to the omitted engine and category.
func TestCreateTemplateMinimal(t *testing.T) {
	var api fakeAPI
	session := connectTo(t, api.serve(t, http.StatusCreated, savedTemplateJSON))

	res := callTool(t, session, "create_template", map[string]any{
		"name":   "Plain",
		"source": `\documentclass{article}\begin{document}x\end{document}`,
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(t, res, 0))
	}
	req := api.only(t)
	if got := string(req.Body["schema"]); got != "[]" {
		t.Errorf("body.schema = %s, want []", got)
	}
	for _, field := range []string{"category", "engine", "description"} {
		if _, present := req.Body[field]; present {
			t.Errorf("body.%s should be omitted so the API default applies", field)
		}
	}
}

func TestCreateTemplateRejectsBadInputWithoutCallingAPI(t *testing.T) {
	tooMany := make([]any, MaxTemplateVariables+1)
	for i := range tooMany {
		tooMany[i] = map[string]any{"key": "v" + strings.Repeat("x", i)}
	}

	const src = `\documentclass{article}\begin{document}x\end{document}`
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"empty name", map[string]any{"name": "  ", "source": src}, "name must not be empty"},
		{"long name", map[string]any{"name": strings.Repeat("n", 201), "source": src}, "limit is 200"},
		{"empty source", map[string]any{"name": "T", "source": " \n"}, "source must not be empty"},
		{"unknown engine", map[string]any{"name": "T", "source": src, "engine": "tectonic"}, "tectonic"},
		{"unknown category", map[string]any{"name": "T", "source": src, "category": "Fun"}, `category "Fun"`},
		{"bad key", map[string]any{"name": "T", "source": src, "schema": []any{map[string]any{"key": "1st"}}}, "not start with a digit"},
		{"key with dash", map[string]any{"name": "T", "source": src, "schema": []any{map[string]any{"key": "first-name"}}}, "letters, digits and underscores"},
		{"empty key", map[string]any{"name": "T", "source": src, "schema": []any{map[string]any{"key": ""}}}, "has no key"},
		{"duplicate key", map[string]any{"name": "T", "source": src, "schema": []any{map[string]any{"key": "a"}, map[string]any{"key": "a"}}}, "defined twice"},
		{"unknown type", map[string]any{"name": "T", "source": src, "schema": []any{map[string]any{"key": "a", "type": "boolean"}}}, "boolean"},
		{"number default", map[string]any{"name": "T", "source": src, "schema": []any{map[string]any{"key": "a", "type": "number", "default": "many"}}}, "not a number"},
		{"date default", map[string]any{"name": "T", "source": src, "schema": []any{map[string]any{"key": "a", "type": "date", "default": "31/12/2026"}}}, "not a date"},
		{"too many variables", map[string]any{"name": "T", "source": src, "schema": tooMany}, "limit is 50"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var api fakeAPI
			session := connectTo(t, api.serve(t, http.StatusCreated, savedTemplateJSON))

			res := callTool(t, session, "create_template", tc.args)
			if !res.IsError {
				t.Fatalf("expected an error, got: %s", resultText(t, res, 0))
			}
			text := resultText(t, res, 0)
			if !strings.Contains(text, tc.want) {
				t.Errorf("error %q does not mention %q", text, tc.want)
			}
			if !strings.Contains(text, "nothing was saved") {
				t.Errorf("error should say nothing was saved: %q", text)
			}
			if n := api.count(); n != 0 {
				t.Errorf("API received %d requests, want 0", n)
			}
		})
	}
}

func TestCreateTemplateAcceptsValidDefaults(t *testing.T) {
	var api fakeAPI
	session := connectTo(t, api.serve(t, http.StatusCreated, savedTemplateJSON))

	res := callTool(t, session, "create_template", map[string]any{
		"name":   "T",
		"source": `\documentclass{article}\begin{document}<<n>> <<d>> <<e>>\end{document}`,
		"schema": []any{
			map[string]any{"key": "n", "type": "Number", "default": "3.5"},
			map[string]any{"key": "d", "type": "date", "default": "today"},
			map[string]any{"key": "e", "type": "date", "default": "2026-12-31"},
		},
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(t, res, 0))
	}
	if got := string(api.only(t).Body["schema"]); !strings.Contains(got, `"type":"number"`) {
		t.Errorf("type should be normalised to lower case: %s", got)
	}
}

func TestSavedTemplateNotes(t *testing.T) {
	var api fakeAPI
	body := `{"template":{"id":"abc","name":"T","category":"Other","engine":"pdflatex",
		"source":"Hello <<name>> and <<missing>>, again <<missing>> and <<also_missing>>",
		"schema":[{"key":"name","type":"string"},{"key":"unused","type":"string"},{"key":"spare","type":"string"}]}}`
	session := connectTo(t, api.serve(t, http.StatusCreated, body))

	res := callTool(t, session, "create_template", map[string]any{
		"name":   "T",
		"source": "Hello <<name>>",
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(t, res, 0))
	}
	text := resultText(t, res, 0)
	for _, want := range []string{
		`\documentclass`,
		"In the source but not in the schema: <<missing>>, <<also_missing>>.",
		"In the schema but not used in the source: unused, spare.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("notes lack %q:\n%s", want, text)
		}
	}
	if n := strings.Count(text, "<<missing>>"); n != 1 {
		t.Errorf("a repeated placeholder should be listed once, got %d", n)
	}
}

func TestUpdateTemplateSendsOnlyGivenFields(t *testing.T) {
	var api fakeAPI
	session := connectTo(t, api.serve(t, http.StatusOK, savedTemplateJSON))

	res := callTool(t, session, "update_template", map[string]any{
		"templateId": "11111111-2222-3333-4444-555555555555",
		"name":       "Invoice v2",
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(t, res, 0))
	}

	req := api.only(t)
	if req.Method != http.MethodPut || req.Path != "/api/v1/templates/user/11111111-2222-3333-4444-555555555555" {
		t.Errorf("request = %s %s", req.Method, req.Path)
	}
	if len(req.Body) != 1 || string(req.Body["name"]) != `"Invoice v2"` {
		t.Errorf("body = %v, want only name", req.Body)
	}
	if !strings.Contains(resultText(t, res, 0), "Template updated.") {
		t.Errorf("unexpected result: %s", resultText(t, res, 0))
	}
}

// Setting a field to nothing is different from leaving it out: an empty
// description and an empty schema must reach the API.
func TestUpdateTemplateAppliesEmptyValues(t *testing.T) {
	var api fakeAPI
	session := connectTo(t, api.serve(t, http.StatusOK, savedTemplateJSON))

	res := callTool(t, session, "update_template", map[string]any{
		"templateId":  "abc",
		"description": "",
		"schema":      []any{},
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(t, res, 0))
	}
	req := api.only(t)
	if got := string(req.Body["description"]); got != `""` {
		t.Errorf("body.description = %s, want an empty string", got)
	}
	if got := string(req.Body["schema"]); got != "[]" {
		t.Errorf("body.schema = %s, want []", got)
	}
	if len(req.Body) != 2 {
		t.Errorf("body = %v, want only description and schema", req.Body)
	}
}

func TestUpdateTemplateRejectsBadInputWithoutCallingAPI(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"no id", map[string]any{"templateId": " ", "name": "x"}, "templateId must not be empty"},
		{"nothing to change", map[string]any{"templateId": "abc"}, "Nothing to update"},
		{"empty name", map[string]any{"templateId": "abc", "name": ""}, "name must not be empty"},
		{"empty source", map[string]any{"templateId": "abc", "source": ""}, "source must not be empty"},
		{"bad engine", map[string]any{"templateId": "abc", "engine": "word"}, `engine "word"`},
		{"bad category", map[string]any{"templateId": "abc", "category": "x"}, `category "x"`},
		{"bad schema", map[string]any{"templateId": "abc", "schema": []any{map[string]any{"key": "a b"}}}, "letters, digits and underscores"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var api fakeAPI
			session := connectTo(t, api.serve(t, http.StatusOK, savedTemplateJSON))

			res := callTool(t, session, "update_template", tc.args)
			if !res.IsError {
				t.Fatalf("expected an error, got: %s", resultText(t, res, 0))
			}
			if text := resultText(t, res, 0); !strings.Contains(text, tc.want) {
				t.Errorf("error %q does not mention %q", text, tc.want)
			}
			if n := api.count(); n != 0 {
				t.Errorf("API received %d requests, want 0", n)
			}
		})
	}
}

func TestTemplateIDIsEscapedInThePath(t *testing.T) {
	var api fakeAPI
	session := connectTo(t, api.serve(t, http.StatusOK, savedTemplateJSON))

	callTool(t, session, "get_template", map[string]any{"templateId": "../usage?x=1"})
	if got := api.only(t).Path; got != "/api/v1/templates/user/..%2Fusage%3Fx=1" {
		t.Errorf("path = %q, the ID must stay one path segment", got)
	}
}

func TestGetTemplateReturnsSourceAsItsOwnBlock(t *testing.T) {
	var api fakeAPI
	session := connectTo(t, api.serve(t, http.StatusOK, savedTemplateJSON))

	res := callTool(t, session, "get_template", map[string]any{"templateId": "abc"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", resultText(t, res, 0))
	}
	if req := api.only(t); req.Method != http.MethodGet || req.Body != nil {
		t.Errorf("request = %+v, want a GET without a body", req)
	}
	if len(res.Content) != 2 {
		t.Fatalf("got %d content blocks, want a summary and the source", len(res.Content))
	}
	if !strings.Contains(resultText(t, res, 0), "client (string, required)") {
		t.Errorf("summary lacks the variables: %s", resultText(t, res, 0))
	}
	want := "\\documentclass{article}\n\\begin{document}\nHello <<client>>\n\\end{document}"
	if got := resultText(t, res, 1); got != want {
		t.Errorf("source block = %q, want the template source untouched", got)
	}
}

func TestTemplateToolErrorsPointToTheNextStep(t *testing.T) {
	cases := []struct {
		name   string
		tool   string
		args   map[string]any
		status int
		body   string
		want   []string
	}{
		{
			name: "free plan", tool: "create_template", status: http.StatusForbidden,
			args: map[string]any{"name": "T", "source": "x"},
			body: `{"error":"user templates are not available on your current plan","plan":"free"}`,
			want: []string{"not available on your current plan", "dashboard/billing"},
		},
		{
			name: "limit reached", tool: "create_template", status: http.StatusForbidden,
			args: map[string]any{"name": "T", "source": "x"},
			body: `{"error":"template limit reached","plan":"pro","used":3,"limit":3}`,
			want: []string{"template limit reached", "dashboard/billing"},
		},
		{
			name: "unknown template", tool: "update_template", status: http.StatusNotFound,
			args: map[string]any{"templateId": "nope", "name": "x"},
			body: `{"error":"template not found"}`,
			want: []string{"template not found", "list_templates"},
		},
		{
			name: "rate limited", tool: "get_template", status: http.StatusTooManyRequests,
			args: map[string]any{"templateId": "abc"},
			body: `{"error":"rate limit exceeded"}`,
			want: []string{"status 429", "rate limit exceeded"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var api fakeAPI
			session := connectTo(t, api.serve(t, tc.status, tc.body))

			res := callTool(t, session, tc.tool, tc.args)
			if !res.IsError {
				t.Fatalf("expected an error, got: %s", resultText(t, res, 0))
			}
			text := resultText(t, res, 0)
			for _, want := range tc.want {
				if !strings.Contains(text, want) {
					t.Errorf("error %q does not mention %q", text, want)
				}
			}
		})
	}
}

// list_templates now shares its formatter with the write tools; this pins the
// text it produced before so the refactor is visibly behaviour-preserving.
func TestListTemplatesOutput(t *testing.T) {
	var api fakeAPI
	body := `{"templates":[
		{"id":"a1","name":"Invoice","description":"Monthly","category":"Business","engine":"pdflatex",
		 "schema":[{"key":"client","type":"string","required":true,"default":"ACME","description":"Who"},
		           {"key":"total","type":"number","required":false}]},
		{"id":"b2","name":"Plain","category":"Other","engine":"auto","schema":[]}
	]}`
	session := connectTo(t, api.serve(t, http.StatusOK, body))

	res := callTool(t, session, "list_templates", nil)
	want := "Found 2 template(s):\n\n" +
		"ID: a1\n  Name: Invoice\n  Category: Business\n  Engine: pdflatex\n  Description: Monthly\n" +
		"  Variables:\n    - client (string, required), default: \"ACME\" — Who\n    - total (number, optional)\n\n" +
		"ID: b2\n  Name: Plain\n  Category: Other\n  Engine: auto\n  Variables: none\n\n" +
		"Use compile_template with a template ID to compile it, get_template to read one's source, or update_template to change it."
	if got := resultText(t, res, 0); got != want {
		t.Errorf("list_templates output changed:\n got: %q\nwant: %q", got, want)
	}
}
