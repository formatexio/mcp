package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Limits the API applies to a template. The variable limits are enforced when a
// template is compiled (internal/template/engine.go); they are repeated here so
// a model learns about a bad schema when it saves the template and not on the
// first compile. A test in internal/template keeps the two in step.
const (
	MaxTemplateVariables     = 50
	MaxVariableKeyLength     = 50
	MaxVariableDefaultLength = 10000
	maxTemplateNameLength    = 200
)

var (
	// templateEngines and templateCategories are the values the dashboard offers.
	// The API stores any string, but the dashboard translates the category by
	// name and the compiler rejects an unknown engine, so anything else would
	// save fine and then misbehave.
	templateEngines    = []string{"pdflatex", "xelatex", "lualatex", "latexmk", "auto"}
	templateCategories = []string{"Professional", "Academic", "Business", "Presentation", "Science", "Other"}
	variableTypes      = []string{"string", "number", "date"}

	// placeholderKeyRegex and placeholderRegex mirror the ones in
	// internal/template/engine.go: a key is a plain identifier and a
	// placeholder is a key between double angle brackets.
	placeholderKeyRegex = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
	placeholderRegex    = regexp.MustCompile(`<<([a-zA-Z_][a-zA-Z0-9_]*)>>`)
)

// TemplateVariableInput defines one placeholder of a template.
type TemplateVariableInput struct {
	Key         string `json:"key" jsonschema:"Variable name, written <<key>> in the source. Letters, digits and underscores, starting with a letter or underscore, at most 50 characters"`
	Type        string `json:"type,omitempty" jsonschema:"string (the default), number or date. A date value is YYYY-MM-DD or the word today"`
	Required    bool   `json:"required,omitempty" jsonschema:"Whether compile_template fails when no value and no default is given. Defaults to false"`
	Default     string `json:"default,omitempty" jsonschema:"Value used when none is passed at compile time, at most 10000 characters. Inserted as given, so it must be valid LaTeX"`
	Description string `json:"description,omitempty" jsonschema:"What the value is for, shown to whoever fills the template in"`
}

type GetTemplateInput struct {
	TemplateID string `json:"templateId" jsonschema:"Template ID from list_templates"`
}

type CreateTemplateInput struct {
	Name        string                  `json:"name" jsonschema:"Template name, at most 200 characters"`
	Source      string                  `json:"source" jsonschema:"Complete LaTeX source, from \\documentclass to \\end{document}. Write <<variable_name>> wherever a value should be filled in later"`
	Description string                  `json:"description,omitempty" jsonschema:"What the template is for. Optional"`
	Category    string                  `json:"category,omitempty" jsonschema:"Professional (the default), Academic, Business, Presentation, Science or Other"`
	Engine      string                  `json:"engine,omitempty" jsonschema:"pdflatex (the default), xelatex, lualatex, latexmk or auto, which picks the engine from the content"`
	Schema      []TemplateVariableInput `json:"schema,omitempty" jsonschema:"Definitions of the <<variable>> placeholders used in the source, at most 50. Optional; a placeholder without a definition is only replaced when compile_template is given a value for it"`
}

// UpdateTemplateInput leaves every field a pointer or a slice so that omitting
// a field (nil) can be told apart from setting it to an empty value.
type UpdateTemplateInput struct {
	TemplateID  string                  `json:"templateId" jsonschema:"ID of the template to change, from list_templates"`
	Name        *string                 `json:"name,omitempty" jsonschema:"New name, at most 200 characters. Omit to keep the current one"`
	Source      *string                 `json:"source,omitempty" jsonschema:"New complete LaTeX source, replacing the whole previous source. Omit to keep the current one"`
	Description *string                 `json:"description,omitempty" jsonschema:"New description; an empty string clears it. Omit to keep the current one"`
	Category    *string                 `json:"category,omitempty" jsonschema:"Professional, Academic, Business, Presentation, Science or Other. Omit to keep the current one"`
	Engine      *string                 `json:"engine,omitempty" jsonschema:"pdflatex, xelatex, lualatex, latexmk or auto. Omit to keep the current one"`
	Schema      []TemplateVariableInput `json:"schema,omitempty" jsonschema:"New variable definitions, replacing all previous ones; an empty list removes every variable. Omit to keep the current ones"`
}

// templateWriteTool builds the definition of a tool that writes to the caller's
// saved templates. Both tools stay inside the FormaTeX account, so the world is
// closed. create_template adds a template and never touches an existing one
// (not destructive, not idempotent: a second call makes a second template).
// update_template replaces fields in place: repeating it is harmless
// (idempotent) but the previous value is gone (destructive), which is what
// makes clients ask before running it.
func templateWriteTool(name, title, description string, destructive, idempotent bool) *sdk.Tool {
	return &sdk.Tool{
		Name:        name,
		Title:       title,
		Description: description,
		Annotations: &sdk.ToolAnnotations{
			Title:           title,
			ReadOnlyHint:    false,
			DestructiveHint: boolPtr(destructive),
			IdempotentHint:  idempotent,
			OpenWorldHint:   boolPtr(false),
		},
	}
}

func registerTemplateTools(server *sdk.Server, client *Client) {
	sdk.AddTool(server, readOnlyTool(
		"get_template",
		"Get Template",
		"Get one saved template by ID, with its full LaTeX source and variable definitions. Call it before update_template to edit existing source, or to see what a template contains before compiling it. Templates are available on Pro plan and higher.",
	), handleGetTemplate(client))

	sdk.AddTool(server, templateWriteTool(
		"create_template",
		"Create Template",
		"Save a new reusable LaTeX template to your FormaTeX account. Write <<variable_name>> in the source wherever a value changes between documents and describe each variable in the schema; compile_template then fills them in. Values are inserted exactly as given with no LaTeX escaping, so they must already be valid LaTeX. Images and other asset files cannot be attached with this tool: add them in the dashboard. Available on Pro plan and higher, up to a per-plan number of templates. Returns the template ID to use with compile_template.",
		false, false,
	), handleCreateTemplate(client))

	sdk.AddTool(server, templateWriteTool(
		"update_template",
		"Update Template",
		"Change a saved template. Only the fields you pass are changed. source and schema replace the whole previous value, so call get_template first when you are editing existing source. This overwrites the saved version and the previous one cannot be recovered. Returns the saved template.",
		true, true,
	), handleUpdateTemplate(client))
}

// normalizeChoice returns the entry of allowed that matches s ignoring case, so
// "Academic" and "academic" are both accepted and stored the same way.
func normalizeChoice(field, s string, allowed []string) (string, error) {
	for _, a := range allowed {
		if strings.EqualFold(strings.TrimSpace(s), a) {
			return a, nil
		}
	}
	return "", fmt.Errorf("%s %q is not one of: %s", field, s, strings.Join(allowed, ", "))
}

func validateTemplateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("name must not be empty")
	}
	if n := utf8.RuneCountInString(name); n > maxTemplateNameLength {
		return "", fmt.Errorf("name is %d characters, the limit is %d", n, maxTemplateNameLength)
	}
	return name, nil
}

func validateTemplateSource(source string) error {
	if strings.TrimSpace(source) == "" {
		return errors.New("source must not be empty")
	}
	return nil
}

// buildSchema checks the variable definitions and converts them to the API
// shape. It returns a non-nil slice so an empty schema is sent as [].
func buildSchema(in []TemplateVariableInput) ([]TemplateVariable, error) {
	if len(in) > MaxTemplateVariables {
		return nil, fmt.Errorf("schema has %d variables, the limit is %d", len(in), MaxTemplateVariables)
	}

	out := make([]TemplateVariable, 0, len(in))
	seen := make(map[string]bool, len(in))
	for i, v := range in {
		where := fmt.Sprintf("schema[%d]", i)
		if v.Key == "" {
			return nil, fmt.Errorf("%s has no key", where)
		}
		where = fmt.Sprintf("schema[%d] (%q)", i, v.Key)
		if len(v.Key) > MaxVariableKeyLength {
			return nil, fmt.Errorf("%s: key is longer than %d characters", where, MaxVariableKeyLength)
		}
		if !placeholderKeyRegex.MatchString(v.Key) {
			return nil, fmt.Errorf("%s: key must be letters, digits and underscores and not start with a digit", where)
		}
		if seen[v.Key] {
			return nil, fmt.Errorf("%s: key is defined twice", where)
		}
		seen[v.Key] = true

		typ := "string"
		if v.Type != "" {
			var err error
			if typ, err = normalizeChoice("type", v.Type, variableTypes); err != nil {
				return nil, fmt.Errorf("%s: %w", where, err)
			}
		}
		if len(v.Default) > MaxVariableDefaultLength {
			return nil, fmt.Errorf("%s: default is longer than %d characters", where, MaxVariableDefaultLength)
		}
		if err := checkDefault(typ, v.Default); err != nil {
			return nil, fmt.Errorf("%s: %w", where, err)
		}

		out = append(out, TemplateVariable{
			Key:         v.Key,
			Type:        typ,
			Required:    v.Required,
			Default:     v.Default,
			Description: v.Description,
		})
	}
	return out, nil
}

// checkDefault applies the same type rules to a default that the API applies to
// a value passed at compile time.
func checkDefault(typ, def string) error {
	if def == "" {
		return nil
	}
	switch typ {
	case "number":
		if _, err := strconv.ParseFloat(def, 64); err != nil {
			return fmt.Errorf("default %q is not a number", def)
		}
	case "date":
		if def != "today" {
			if _, err := time.Parse("2006-01-02", def); err != nil {
				return fmt.Errorf("default %q is not a date (YYYY-MM-DD) or \"today\"", def)
			}
		}
	}
	return nil
}

// templateNotes lists problems that do not stop a template from being saved but
// that the author would want to know about: placeholders nobody defined, and
// definitions nothing uses. It reads the saved template, so it also covers an
// update that changed only one of source and schema. Each kind is one note,
// however many names it covers.
func templateNotes(t *UserTemplate) []string {
	var notes []string

	if !strings.Contains(t.Source, `\documentclass`) {
		notes = append(notes, `The source has no \documentclass, so it cannot be compiled as it is.`)
	}

	defined := make(map[string]bool, len(t.Schema))
	for _, v := range t.Schema {
		defined[v.Key] = true
	}

	used := make(map[string]bool)
	var undefined []string
	for _, m := range placeholderRegex.FindAllStringSubmatch(t.Source, -1) {
		key := m[1]
		if used[key] {
			continue
		}
		used[key] = true
		if !defined[key] {
			undefined = append(undefined, "<<"+key+">>")
		}
	}
	if len(undefined) > 0 {
		notes = append(notes, fmt.Sprintf("In the source but not in the schema: %s. They are only replaced when compile_template is given a value for them and otherwise stay in the document as literal text.", strings.Join(undefined, ", ")))
	}

	var unused []string
	for _, v := range t.Schema {
		if !used[v.Key] {
			unused = append(unused, v.Key)
		}
	}
	if len(unused) > 0 {
		notes = append(notes, fmt.Sprintf("In the schema but not used in the source: %s.", strings.Join(unused, ", ")))
	}
	return notes
}

// writeTemplate writes the block list_templates prints for one template and the
// write tools reuse to show what was saved.
func writeTemplate(sb *strings.Builder, t UserTemplate) {
	sb.WriteString(fmt.Sprintf("ID: %s\n  Name: %s\n  Category: %s\n  Engine: %s\n", t.ID, t.Name, t.Category, t.Engine))
	if t.Description != "" {
		sb.WriteString(fmt.Sprintf("  Description: %s\n", t.Description))
	}
	if len(t.Schema) == 0 {
		sb.WriteString("  Variables: none\n")
		return
	}
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
}

func templateText(text string, isError bool) *sdk.CallToolResult {
	return &sdk.CallToolResult{
		Content: []sdk.Content{&sdk.TextContent{Text: text}},
		IsError: isError,
	}
}

// templateFailure turns an API error into a tool error, adding the next step
// for the two failures a model cannot work out from the status alone.
func templateFailure(action string, err error) *sdk.CallToolResult {
	msg := fmt.Sprintf("%s: %s", action, err.Error())
	var apiErr *CompileError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusForbidden:
			msg += "\nTemplates need the Pro plan or higher and each plan has a limit on how many you can keep. Plans: https://formatex.io/dashboard/billing"
		case http.StatusNotFound:
			msg += "\nCall list_templates to see the IDs of your templates."
		}
	}
	return templateText(msg, true)
}

func templateInvalid(err error) *sdk.CallToolResult {
	return templateText("Invalid template, nothing was saved: "+err.Error(), true)
}

func templateSaved(verb string, t *UserTemplate) *sdk.CallToolResult {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Template %s.\n", verb))
	writeTemplate(&sb, *t)
	if notes := templateNotes(t); len(notes) > 0 {
		sb.WriteString("\nNotes:\n")
		for _, n := range notes {
			sb.WriteString("  - " + n + "\n")
		}
	}
	sb.WriteString(fmt.Sprintf("\nCompile it with compile_template using templateId %s, or change it with update_template.", t.ID))
	return templateText(sb.String(), false)
}

func handleGetTemplate(client *Client) func(context.Context, *sdk.CallToolRequest, GetTemplateInput) (*sdk.CallToolResult, any, error) {
	return func(ctx context.Context, req *sdk.CallToolRequest, input GetTemplateInput) (*sdk.CallToolResult, any, error) {
		if strings.TrimSpace(input.TemplateID) == "" {
			return templateText("templateId must not be empty. Call list_templates to see your template IDs.", true), nil, nil
		}

		t, err := client.GetTemplate(ctx, input.TemplateID)
		if err != nil {
			return templateFailure("Failed to get template", err), nil, nil
		}

		var sb strings.Builder
		writeTemplate(&sb, *t)
		if t.UpdatedAt != "" {
			sb.WriteString(fmt.Sprintf("  Updated: %s\n", t.UpdatedAt))
		}
		sb.WriteString("\nThe LaTeX source follows as the next content block.")

		// The source goes in its own block, unwrapped: LaTeX can contain any
		// quoting or fencing a wrapper would use, and a model that edits it
		// should be able to copy the block as it is.
		return &sdk.CallToolResult{
			Content: []sdk.Content{
				&sdk.TextContent{Text: sb.String()},
				&sdk.TextContent{Text: t.Source},
			},
		}, nil, nil
	}
}

func handleCreateTemplate(client *Client) func(context.Context, *sdk.CallToolRequest, CreateTemplateInput) (*sdk.CallToolResult, any, error) {
	return func(ctx context.Context, req *sdk.CallToolRequest, input CreateTemplateInput) (*sdk.CallToolResult, any, error) {
		name, err := validateTemplateName(input.Name)
		if err != nil {
			return templateInvalid(err), nil, nil
		}
		if err := validateTemplateSource(input.Source); err != nil {
			return templateInvalid(err), nil, nil
		}

		body := CreateTemplateAPIRequest{
			Name:        name,
			Description: input.Description,
			Source:      input.Source,
		}
		if input.Category != "" {
			if body.Category, err = normalizeChoice("category", input.Category, templateCategories); err != nil {
				return templateInvalid(err), nil, nil
			}
		}
		if input.Engine != "" {
			if body.Engine, err = normalizeChoice("engine", input.Engine, templateEngines); err != nil {
				return templateInvalid(err), nil, nil
			}
		}
		if body.Schema, err = buildSchema(input.Schema); err != nil {
			return templateInvalid(err), nil, nil
		}

		t, err := client.CreateTemplate(ctx, body)
		if err != nil {
			return templateFailure("Failed to create template", err), nil, nil
		}
		return templateSaved("created", t), nil, nil
	}
}

func handleUpdateTemplate(client *Client) func(context.Context, *sdk.CallToolRequest, UpdateTemplateInput) (*sdk.CallToolResult, any, error) {
	return func(ctx context.Context, req *sdk.CallToolRequest, input UpdateTemplateInput) (*sdk.CallToolResult, any, error) {
		if strings.TrimSpace(input.TemplateID) == "" {
			return templateText("templateId must not be empty. Call list_templates to see your template IDs.", true), nil, nil
		}

		var body UpdateTemplateAPIRequest

		if input.Name != nil {
			name, err := validateTemplateName(*input.Name)
			if err != nil {
				return templateInvalid(err), nil, nil
			}
			body.Name = &name
		}
		if input.Source != nil {
			if err := validateTemplateSource(*input.Source); err != nil {
				return templateInvalid(err), nil, nil
			}
			body.Source = input.Source
		}
		body.Description = input.Description
		if input.Category != nil {
			category, err := normalizeChoice("category", *input.Category, templateCategories)
			if err != nil {
				return templateInvalid(err), nil, nil
			}
			body.Category = &category
		}
		if input.Engine != nil {
			engine, err := normalizeChoice("engine", *input.Engine, templateEngines)
			if err != nil {
				return templateInvalid(err), nil, nil
			}
			body.Engine = &engine
		}
		if input.Schema != nil {
			schema, err := buildSchema(input.Schema)
			if err != nil {
				return templateInvalid(err), nil, nil
			}
			body.Schema = &schema
		}

		if body == (UpdateTemplateAPIRequest{}) {
			return templateText("Nothing to update: pass at least one of name, source, description, category, engine or schema.", true), nil, nil
		}

		t, err := client.UpdateTemplate(ctx, input.TemplateID, body)
		if err != nil {
			return templateFailure("Failed to update template", err), nil, nil
		}
		return templateSaved("updated", t), nil, nil
	}
}
