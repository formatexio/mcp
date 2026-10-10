package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Client is an HTTP client for the FormaTeX REST API.
type Client struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new FormaTeX API client.
// timeout is set to 5 minutes to accommodate long LaTeX compilations.
func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		apiKey:  apiKey,
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 5 * time.Minute,
		},
	}
}

// BaseURL returns the API base URL configured for this client.
func (c *Client) BaseURL() string { return c.baseURL }

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("X-API-Key", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "formatex-mcp/1.0")
}

// CompileError is a structured API error.
type CompileError struct {
	StatusCode int
	Message    string
}

func (e *CompileError) Error() string {
	return fmt.Sprintf("FormaTeX API error (status %d): %s", e.StatusCode, e.Message)
}

func readAPIError(body []byte, statusCode int) error {
	var resp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &resp); err == nil && resp.Error != "" {
		return &CompileError{StatusCode: statusCode, Message: resp.Error}
	}
	return &CompileError{StatusCode: statusCode, Message: string(body)}
}

// CompileAPIRequest is the request body for POST /compile.
type CompileAPIRequest struct {
	LaTeX   string `json:"latex"`
	Engine  string `json:"engine,omitempty"`
	Runs    int    `json:"runs,omitempty"`
	Timeout int    `json:"timeout,omitempty"`
}

// SmartCompileAPIRequest is the request body for POST /compile/smart.
type SmartCompileAPIRequest struct {
	LaTeX    string `json:"latex"`
	Engine   string `json:"engine,omitempty"`
	Runs     int    `json:"runs,omitempty"`
	Timeout  int    `json:"timeout,omitempty"`
	AutoFix  bool   `json:"autoFix,omitempty"`
	Fallback bool   `json:"fallback,omitempty"`
}

// CompileResponse holds the raw PDF bytes and response headers.
type CompileResponse struct {
	PDFData        []byte
	JobID          string
	DurationMs     string
	EngineUsed     string
	AutoFixesCount string
	SignedPDFURL   string // short-lived browser-clickable URL, set when server supports it
}

// SyntaxCheckResult is the response from POST /compile/check.
type SyntaxCheckResult struct {
	Valid       bool     `json:"valid"`
	Errors      []string `json:"errors"`
	Suggestions []string `json:"suggestions"`
	Analysis    *struct {
		RecommendedEngine string   `json:"recommendedEngine"`
		Confidence        float64  `json:"confidence"`
		HasUnicode        bool     `json:"hasUnicode"`
		UnicodeChars      []string `json:"unicodeChars"`
		HasFontSpec       bool     `json:"hasFontspec"`
		HasLegacyEncoding bool     `json:"hasLegacyEncoding"`
	} `json:"analysis,omitempty"`
}

// UsageResponse is the response from GET /usage.
type UsageResponse struct {
	Plan   string `json:"plan"`
	Period struct {
		Start string `json:"start"`
		End   string `json:"end"`
	} `json:"period"`
	Compilations struct {
		Used    int `json:"used"`
		Limit   int `json:"limit"`
		Overage int `json:"overage"`
	} `json:"compilations"`
	Breakdown struct {
		ByEngine      map[string]int `json:"byEngine"`
		ByStatus      map[string]int `json:"byStatus"`
		AvgDurationMs int            `json:"avgDurationMs"`
	} `json:"breakdown"`
}

// Compile sends a synchronous compilation request and returns the raw PDF bytes.
func (c *Client) Compile(ctx context.Context, req CompileAPIRequest) (*CompileResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/compile", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.setHeaders(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, readAPIError(respBody, resp.StatusCode)
	}

	return &CompileResponse{
		PDFData:      respBody,
		JobID:        resp.Header.Get("X-Job-ID"),
		DurationMs:   resp.Header.Get("X-Compile-Duration-Ms"),
		SignedPDFURL: resp.Header.Get("X-Signed-PDF-URL"),
	}, nil
}

// SmartCompile sends a smart compilation request and returns the raw PDF bytes.
func (c *Client) SmartCompile(ctx context.Context, req SmartCompileAPIRequest) (*CompileResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/compile/smart", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.setHeaders(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, readAPIError(respBody, resp.StatusCode)
	}

	return &CompileResponse{
		PDFData:        respBody,
		JobID:          resp.Header.Get("X-Job-ID"),
		DurationMs:     resp.Header.Get("X-Compile-Duration-Ms"),
		EngineUsed:     resp.Header.Get("X-Engine-Used"),
		AutoFixesCount: resp.Header.Get("X-Auto-Fixes-Applied"),
		SignedPDFURL:   resp.Header.Get("X-Signed-PDF-URL"),
	}, nil
}

// ListEngines fetches available LaTeX engines from GET /engines.
func (c *Client) ListEngines(ctx context.Context) ([]string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v1/engines", nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return nil, readAPIError(body, resp.StatusCode)
	}

	var result struct {
		Engines []string `json:"engines"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return result.Engines, nil
}

// CheckSyntax validates LaTeX source without compiling via POST /compile/check.
func (c *Client) CheckSyntax(ctx context.Context, latex string) (*SyntaxCheckResult, error) {
	body, err := json.Marshal(map[string]string{"latex": latex})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/compile/check", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.setHeaders(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, readAPIError(respBody, resp.StatusCode)
	}

	var result SyntaxCheckResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &result, nil
}

// AsyncCompileResponse is the response from POST /compile/async.
type AsyncCompileResponse struct {
	JobID  string `json:"jobId"`
	Status string `json:"status"`
}

// JobStatusResponse is the response from GET /jobs/:id.
type JobStatusResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Result *struct {
		PDFSize    int    `json:"pdfSize"`
		DurationMs int    `json:"durationMs"`
		Engine     string `json:"engine"`
	} `json:"result,omitempty"`
	Error        string `json:"error,omitempty"`
	SignedPDFURL string `json:"signed_pdf_url,omitempty"` // present when completed and server supports signed URLs
}

// TemplateVariable describes a single variable in a template schema.
type TemplateVariable struct {
	Key         string `json:"key"`
	Type        string `json:"type"`
	Required    bool   `json:"required"`
	Default     string `json:"default"`
	Description string `json:"description"`
}

// UserTemplate represents a single user template returned by GET /templates.
// Source is only worth reading from the single-template endpoints; the list
// endpoint returns it too, but list_templates does not print it.
type UserTemplate struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Category    string             `json:"category"`
	Engine      string             `json:"engine"`
	Source      string             `json:"source"`
	Schema      []TemplateVariable `json:"schema"`
	UpdatedAt   string             `json:"updatedAt"`
}

// CreateTemplateAPIRequest is the request body for POST /templates.
type CreateTemplateAPIRequest struct {
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	Category    string             `json:"category,omitempty"`
	Engine      string             `json:"engine,omitempty"`
	Source      string             `json:"source"`
	Schema      []TemplateVariable `json:"schema"`
}

// UpdateTemplateAPIRequest is the request body for PUT /templates/user/:id.
// Every field is a pointer so that an omitted field is left alone by the API
// while an empty one (an empty description, an empty schema) is applied.
type UpdateTemplateAPIRequest struct {
	Name        *string             `json:"name,omitempty"`
	Description *string             `json:"description,omitempty"`
	Category    *string             `json:"category,omitempty"`
	Engine      *string             `json:"engine,omitempty"`
	Source      *string             `json:"source,omitempty"`
	Schema      *[]TemplateVariable `json:"schema,omitempty"`
}

// ListTemplatesResponse is the response from GET /templates.
type ListTemplatesResponse struct {
	Templates []UserTemplate `json:"templates"`
	Message   string         `json:"message,omitempty"`
}

// CompileTemplateResponse holds the PDF and metadata from POST /templates/user/:id/compile.
type CompileTemplateResponse struct {
	PDFData    []byte
	JobID      string
	DurationMs string
}

// CompileAsync submits an async compilation job via POST /compile/async.
func (c *Client) CompileAsync(ctx context.Context, req CompileAPIRequest) (*AsyncCompileResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/compile/async", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.setHeaders(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, readAPIError(respBody, resp.StatusCode)
	}

	var result AsyncCompileResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &result, nil
}

// GetJobStatus fetches the status of an async job via GET /jobs/:id.
func (c *Client) GetJobStatus(ctx context.Context, jobID string) (*JobStatusResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v1/jobs/"+jobID, nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, readAPIError(respBody, resp.StatusCode)
	}

	var result JobStatusResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &result, nil
}

// GetJobPDF fetches the compiled PDF for a completed job via GET /jobs/:id/pdf.
func (c *Client) GetJobPDF(ctx context.Context, jobID string) ([]byte, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v1/jobs/"+jobID+"/pdf", nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, readAPIError(body, resp.StatusCode)
	}
	return body, nil
}

// GetJobLog fetches the compilation log for a job via GET /jobs/:id/log.
func (c *Client) GetJobLog(ctx context.Context, jobID string) (string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v1/jobs/"+jobID+"/log", nil)
	if err != nil {
		return "", err
	}
	c.setHeaders(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return "", readAPIError(body, resp.StatusCode)
	}
	return string(body), nil
}

// ListTemplates fetches the user's templates via GET /templates.
func (c *Client) ListTemplates(ctx context.Context) (*ListTemplatesResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v1/templates", nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, readAPIError(respBody, resp.StatusCode)
	}

	var result ListTemplatesResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &result, nil
}

// templateRequest sends one request to a single-template endpoint and decodes
// the {"template": {...}} envelope those endpoints share. body may be nil.
func (c *Client) templateRequest(ctx context.Context, method, path string, body any) (*UserTemplate, error) {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		reader = bytes.NewReader(payload)
	}

	httpReq, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	c.setHeaders(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, readAPIError(respBody, resp.StatusCode)
	}

	var result struct {
		Template UserTemplate `json:"template"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &result.Template, nil
}

// GetTemplate fetches one template, source included, via GET /templates/user/:id.
func (c *Client) GetTemplate(ctx context.Context, templateID string) (*UserTemplate, error) {
	return c.templateRequest(ctx, http.MethodGet, "/api/v1/templates/user/"+url.PathEscape(templateID), nil)
}

// CreateTemplate saves a new template via POST /templates.
func (c *Client) CreateTemplate(ctx context.Context, req CreateTemplateAPIRequest) (*UserTemplate, error) {
	return c.templateRequest(ctx, http.MethodPost, "/api/v1/templates", req)
}

// UpdateTemplate changes the given fields of a template via PUT /templates/user/:id.
func (c *Client) UpdateTemplate(ctx context.Context, templateID string, req UpdateTemplateAPIRequest) (*UserTemplate, error) {
	return c.templateRequest(ctx, http.MethodPut, "/api/v1/templates/user/"+url.PathEscape(templateID), req)
}

// CompileTemplate compiles a user template via POST /templates/user/:id/compile.
func (c *Client) CompileTemplate(ctx context.Context, templateID string, variables map[string]string) (*CompileTemplateResponse, error) {
	body, err := json.Marshal(map[string]interface{}{"variables": variables})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/templates/user/"+templateID+"/compile", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.setHeaders(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, readAPIError(respBody, resp.StatusCode)
	}

	return &CompileTemplateResponse{
		PDFData:    respBody,
		JobID:      resp.Header.Get("X-Job-ID"),
		DurationMs: resp.Header.Get("X-Compile-Duration-Ms"),
	}, nil
}

// GetUsage fetches current month usage stats via GET /usage.
func (c *Client) GetUsage(ctx context.Context) (*UsageResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v1/usage", nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return nil, readAPIError(body, resp.StatusCode)
	}

	var result UsageResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &result, nil
}
