package mcp

import (
	"context"
	"os"
	"regexp"
	"slices"
	"sort"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// wantTools lists every tool the server must register, with whether it is
// read-only. Adding or removing a tool means updating this list, smithery.yaml
// and the documentation together.
var wantTools = map[string]bool{
	"compile_latex":    false,
	"compile_smart":    false,
	"compile_async":    false,
	"compile_template": false,
	"check_syntax":     true,
	"list_engines":     true,
	"get_usage":        true,
	"get_job":          true,
	"list_templates":   true,
	"get_template":     true,
	"create_template":  false,
	"update_template":  false,
	"fetch_pdf_bytes":  true,
}

// wantWrites describes the annotations of the tools that change data. Compile
// tools only add jobs and reach a remote compiler, so they are an open world;
// template tools only touch the caller's own account, so theirs is closed.
// update_template replaces content in place: idempotent, but destructive.
var wantWrites = map[string]struct{ destructive, idempotent, openWorld bool }{
	"compile_latex":    {false, false, true},
	"compile_smart":    {false, false, true},
	"compile_async":    {false, false, true},
	"compile_template": {false, false, true},
	"create_template":  {false, false, false},
	"update_template":  {true, true, false},
}

// connect starts the server on an in-memory transport and returns a session.
// The API client points at a closed port: listing tools never calls the API.
func connect(t *testing.T) *sdk.ClientSession {
	t.Helper()
	return connectTo(t, "http://127.0.0.1:1/")
}

// connectTo is connect with the API client pointed at baseURL.
func connectTo(t *testing.T, baseURL string) *sdk.ClientSession {
	t.Helper()
	ctx := context.Background()

	server := NewMCPServer(NewClient(baseURL, "dummy"))
	serverTransport, clientTransport := sdk.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestServerVersion(t *testing.T) {
	if Version != "1.1.1" {
		t.Errorf("Version = %q, want 1.1.1", Version)
	}
	info := connect(t).InitializeResult().ServerInfo
	if info == nil || info.Version != Version || info.Name != "formatex-mcp" {
		t.Errorf("ServerInfo = %+v, want formatex-mcp %s", info, Version)
	}
}

func TestToolsHaveTitlesAndAnnotations(t *testing.T) {
	res, err := connect(t).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	var got []string
	for _, tool := range res.Tools {
		got = append(got, tool.Name)
	}
	var want []string
	for name := range wantTools {
		want = append(want, name)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Fatalf("registered tools = %v, want %v", got, want)
	}

	for _, tool := range res.Tools {
		t.Run(tool.Name, func(t *testing.T) {
			if tool.Title == "" {
				t.Error("missing Title")
			}
			if tool.Description == "" {
				t.Error("missing Description")
			}
			if tool.InputSchema == nil {
				t.Error("missing InputSchema")
			}
			a := tool.Annotations
			if a == nil {
				t.Fatal("missing Annotations")
			}
			if a.Title != tool.Title {
				t.Errorf("Annotations.Title = %q, want %q", a.Title, tool.Title)
			}
			if a.ReadOnlyHint != wantTools[tool.Name] {
				t.Errorf("ReadOnlyHint = %v, want %v", a.ReadOnlyHint, wantTools[tool.Name])
			}
			if a.OpenWorldHint == nil {
				t.Fatal("OpenWorldHint must be explicit, the protocol default is true")
			}
			if a.ReadOnlyHint {
				if *a.OpenWorldHint {
					t.Error("a read-only tool only reads the FormaTeX account: OpenWorldHint must be false")
				}
				return
			}
			want, ok := wantWrites[tool.Name]
			if !ok {
				t.Fatalf("%s writes data but has no entry in wantWrites", tool.Name)
			}
			if a.DestructiveHint == nil {
				t.Fatal("DestructiveHint must be explicit, the protocol default is true")
			}
			if *a.DestructiveHint != want.destructive {
				t.Errorf("DestructiveHint = %v, want %v", *a.DestructiveHint, want.destructive)
			}
			if a.IdempotentHint != want.idempotent {
				t.Errorf("IdempotentHint = %v, want %v", a.IdempotentHint, want.idempotent)
			}
			if *a.OpenWorldHint != want.openWorld {
				t.Errorf("OpenWorldHint = %v, want %v", *a.OpenWorldHint, want.openWorld)
			}
		})
	}
}

// wantRequired is the exact set of parameters a client must send. Optional
// parameters must stay out of this set: a client that omits them would
// otherwise fail schema validation before the tool runs.
var wantRequired = map[string][]string{
	"compile_latex":    {"engine", "latex"},
	"compile_smart":    {"latex"},
	"compile_async":    {"latex"},
	"compile_template": {"templateId"},
	"get_template":     {"templateId"},
	"create_template":  {"name", "source"},
	"update_template":  {"templateId"},
	"check_syntax":     {"latex"},
	"get_job":          {"jobId"},
	"fetch_pdf_bytes":  {"jobId"},
	"list_engines":     nil,
	"get_usage":        nil,
	"list_templates":   nil,
}

func TestRequiredParameters(t *testing.T) {
	res, err := connect(t).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tool := range res.Tools {
		t.Run(tool.Name, func(t *testing.T) {
			schema, ok := tool.InputSchema.(map[string]any)
			if !ok {
				t.Fatalf("InputSchema is %T, want an object", tool.InputSchema)
			}
			var got []string
			if req, ok := schema["required"].([]any); ok {
				for _, r := range req {
					got = append(got, r.(string))
				}
			}
			sort.Strings(got)
			want := append([]string(nil), wantRequired[tool.Name]...)
			sort.Strings(want)
			if !slices.Equal(got, want) {
				t.Errorf("required = %v, want %v", got, want)
			}

			// Every property should tell a model what to put in it.
			props, _ := schema["properties"].(map[string]any)
			for name, raw := range props {
				prop, _ := raw.(map[string]any)
				if desc, _ := prop["description"].(string); desc == "" {
					t.Errorf("property %q has no description", name)
				}
			}
		})
	}
}

// TestSmithery keeps smithery.yaml in step with the server: same version and,
// when the file lists them, the same tool names. The file is parsed with
// regular expressions to avoid a YAML dependency. The standalone repository
// ships Smithery's own startCommand layout, which has no tools section, so the
// tool check applies only where one exists.
func TestSmithery(t *testing.T) {
	data, err := os.ReadFile("../../smithery.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)

	m := regexp.MustCompile(`(?m)^version:\s*"([^"]+)"\s*$`).FindStringSubmatch(text)
	if m == nil || m[1] != Version {
		t.Errorf("smithery.yaml version = %v, want %q", m, Version)
	}

	section := regexp.MustCompile(`(?ms)^tools:\n(.*?)^\S`).FindStringSubmatch(text)
	if section == nil {
		return
	}
	var got []string
	for _, n := range regexp.MustCompile(`(?m)^\s+- name:\s*(\S+)\s*$`).FindAllStringSubmatch(section[1], -1) {
		got = append(got, n[1])
	}
	var want []string
	for name := range wantTools {
		want = append(want, name)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Errorf("smithery.yaml tools = %v, want %v", got, want)
	}
}
