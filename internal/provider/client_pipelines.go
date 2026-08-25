package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// ---------------------------------------------------------------------------
// Functions (VRL and JS transforms)
// ---------------------------------------------------------------------------

// FunctionAPI is the wire format of a transform function.
//
// The field names are camelCase because the server's Transform carries
// `rename_all = "camelCase"`. This is worth stating because the snake_case
// spellings do not error, they are silently ignored and the field falls back to
// its default: posting `trans_type: 1` for a JavaScript function stores a VRL
// function and then fails to compile the body as VRL.
//
// `transType` is an integer discriminant: 0 is VRL, 1 is JavaScript. The
// provider exposes it as `language` and translates.
type FunctionAPI struct {
	Name      string `json:"name"`
	Function  string `json:"function"`
	Params    string `json:"params"`
	NumArgs   int64  `json:"numArgs,omitempty"`
	TransType *int64 `json:"transType,omitempty"`
}

// FunctionListAPI wraps the functions list response.
type FunctionListAPI struct {
	List []FunctionAPI `json:"list"`
}

// functionTransTypes maps the provider's `language` onto the wire discriminant.
var functionTransTypes = map[string]int64{"vrl": 0, "js": 1}

// functionLanguage is the inverse, for reads.
func functionLanguage(transType *int64) string {
	if transType != nil && *transType == 1 {
		return "js"
	}
	return "vrl"
}

// CreateFunction creates a function.
//
// The server refuses to overwrite: a name that already exists comes back as
// `400 "Function already exist"` rather than being updated.
func (c *Client) CreateFunction(ctx context.Context, orgID string, req FunctionAPI) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/api/%s/functions", pathEscape(orgID)), req)
}

// UpdateFunction replaces an existing function.
func (c *Client) UpdateFunction(ctx context.Context, orgID, name string, req FunctionAPI) error {
	path := fmt.Sprintf("/api/%s/functions/%s", pathEscape(orgID), pathEscape(name))
	return c.do(ctx, http.MethodPut, path, req)
}

// ListFunctions returns every function in an organization.
func (c *Client) ListFunctions(ctx context.Context, orgID string) ([]FunctionAPI, error) {
	var out FunctionListAPI
	if err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/api/%s/functions", pathEscape(orgID)), nil, &out); err != nil {
		return nil, err
	}
	return out.List, nil
}

// GetFunction reads one function by name, returning (nil, nil) when absent.
//
// There is no single-function GET. `GET /functions/{name}` returns the
// function's pipeline dependencies, not the function, so reads go through the
// list endpoint.
func (c *Client) GetFunction(ctx context.Context, orgID, name string) (*FunctionAPI, error) {
	functions, err := c.ListFunctions(ctx, orgID)
	if err != nil {
		return nil, err
	}
	for i := range functions {
		if functions[i].Name == name {
			return &functions[i], nil
		}
	}
	return nil, nil
}

// FunctionDependencyAPI is one pipeline that uses a function.
type FunctionDependencyAPI struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// FunctionDependencyListAPI wraps the dependency response.
type FunctionDependencyListAPI struct {
	List []FunctionDependencyAPI `json:"list"`
}

// ListFunctionDependencies returns the pipelines that use a function. The
// server refuses to delete a function a pipeline still references, so this is
// what explains a blocked destroy.
func (c *Client) ListFunctionDependencies(ctx context.Context, orgID, name string) ([]FunctionDependencyAPI, error) {
	path := fmt.Sprintf("/api/%s/functions/%s", pathEscape(orgID), pathEscape(name))
	var out FunctionDependencyListAPI
	found, err := c.doJSONOptional(ctx, http.MethodGet, path, nil, &out)
	if err != nil || !found {
		return nil, err
	}
	return out.List, nil
}

// DeleteFunction removes a function.
func (c *Client) DeleteFunction(ctx context.Context, orgID, name string) error {
	path := fmt.Sprintf("/api/%s/functions/%s", pathEscape(orgID), pathEscape(name))
	return c.deleteIgnoreMissing(ctx, path)
}

// isFunctionAlreadyExists reports whether err is the server refusing to
// overwrite an existing function.
func isFunctionAlreadyExists(err error) bool {
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		return strings.Contains(apiErr.Body, "Function already exist")
	}
	return false
}

// ---------------------------------------------------------------------------
// Pipeline destinations
// ---------------------------------------------------------------------------
//
// A pipeline destination is the same object as an alert destination, stored
// through the same endpoints. The server decides which it is from one field: a
// body carrying a template becomes an alert destination, a body without one
// becomes a pipeline destination that alerts cannot use.
//
// They are separate resources here because that single field changes what the
// object is for, and because the rest of the alert destination surface (emails,
// SNS, templates) does not apply.

// PipelineDestinationAPI is the wire format of a pipeline destination.
type PipelineDestinationAPI struct {
	Name                string            `json:"name"`
	URL                 string            `json:"url"`
	Method              string            `json:"method"`
	SkipTLSVerify       bool              `json:"skip_tls_verify"`
	Headers             map[string]string `json:"headers,omitempty"`
	OutputFormat        json.RawMessage   `json:"output_format,omitempty"`
	DestinationTypeName *string           `json:"destination_type_name,omitempty"`
	Metadata            map[string]string `json:"metadata,omitempty"`
	// Emails is always sent, and always empty. The server's request model
	// declares it non-optional, and a pipeline destination has no recipients.
	Emails []string `json:"emails"`
}

// CreatePipelineDestination creates a pipeline destination.
func (c *Client) CreatePipelineDestination(ctx context.Context, orgID string, req PipelineDestinationAPI) error {
	if req.Emails == nil {
		req.Emails = []string{}
	}
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/api/%s/alerts/destinations", pathEscape(orgID)), req)
}

// UpdatePipelineDestination replaces an existing pipeline destination.
func (c *Client) UpdatePipelineDestination(ctx context.Context, orgID, name string, req PipelineDestinationAPI) error {
	if req.Emails == nil {
		req.Emails = []string{}
	}
	path := fmt.Sprintf("/api/%s/alerts/destinations/%s", pathEscape(orgID), pathEscape(name))
	return c.do(ctx, http.MethodPut, path, req)
}

// GetPipelineDestination reads a pipeline destination, returning (nil, nil)
// when absent.
func (c *Client) GetPipelineDestination(ctx context.Context, orgID, name string) (*PipelineDestinationAPI, error) {
	path := fmt.Sprintf("/api/%s/alerts/destinations/%s", pathEscape(orgID), pathEscape(name))
	var out PipelineDestinationAPI
	found, err := c.doJSONOptional(ctx, http.MethodGet, path, nil, &out)
	if err != nil {
		return nil, err
	}
	if !found || out.Name == "" {
		return nil, nil
	}
	return &out, nil
}

// DeletePipelineDestination removes a pipeline destination.
func (c *Client) DeletePipelineDestination(ctx context.Context, orgID, name string) error {
	path := fmt.Sprintf("/api/%s/alerts/destinations/%s", pathEscape(orgID), pathEscape(name))
	return c.deleteIgnoreMissing(ctx, path)
}

// ---------------------------------------------------------------------------
// Pipelines
// ---------------------------------------------------------------------------

// PipelinePositionAPI is a node's position in the visual editor. It has no
// effect on behaviour, but the server requires it.
type PipelinePositionAPI struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// PipelineNodeDataAPI is the tagged payload of a node. `node_type` selects
// which of the remaining fields apply.
type PipelineNodeDataAPI struct {
	NodeType string `json:"node_type"`

	// stream and remote_stream
	OrgID      string `json:"org_id,omitempty"`
	StreamName string `json:"stream_name,omitempty"`
	StreamType string `json:"stream_type,omitempty"`

	// remote_stream
	DestinationName string `json:"destination_name,omitempty"`

	// function
	Name         string `json:"name,omitempty"`
	AfterFlatten *bool  `json:"after_flatten,omitempty"`
	NumArgs      int64  `json:"num_args,omitempty"`

	// condition
	Conditions json.RawMessage `json:"conditions,omitempty"`
}

// PipelineNodeAPI is one node of the pipeline graph.
type PipelineNodeAPI struct {
	ID       string              `json:"id"`
	Data     PipelineNodeDataAPI `json:"data"`
	Position PipelinePositionAPI `json:"position"`
	IOType   string              `json:"io_type"`
}

// PipelineEdgeAPI connects two nodes.
type PipelineEdgeAPI struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Target string `json:"target"`
}

// PipelineSourceAPI is the pipeline's trigger. `source_type` is `realtime`,
// where the pipeline runs as data arrives into a stream, or `scheduled`, where
// it runs a query on a cadence.
type PipelineSourceAPI struct {
	SourceType string `json:"source_type"`

	// realtime
	OrgID      string `json:"org_id,omitempty"`
	StreamName string `json:"stream_name,omitempty"`
	StreamType string `json:"stream_type,omitempty"`

	// scheduled: passed through as written, because a derived stream carries
	// the whole query and trigger surface of an alert.
	QueryCondition   json.RawMessage `json:"query_condition,omitempty"`
	TriggerCondition json.RawMessage `json:"trigger_condition,omitempty"`
}

// PipelineAPI is the wire format of a pipeline.
type PipelineAPI struct {
	ID          string            `json:"pipeline_id,omitempty"`
	Version     int64             `json:"version,omitempty"`
	Enabled     bool              `json:"enabled"`
	Org         string            `json:"org,omitempty"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Source      PipelineSourceAPI `json:"source"`
	Nodes       []PipelineNodeAPI `json:"nodes"`
	Edges       []PipelineEdgeAPI `json:"edges"`
}

// PipelineListAPI wraps the pipelines list response.
type PipelineListAPI struct {
	List []PipelineAPI `json:"list"`
}

// CreatePipeline creates a pipeline. The server assigns the ID, so the caller
// reads it back by name.
//
// Note the name is normalized: the server trims it and lowercases it, so a
// pipeline created as `My Pipeline` is stored as `my pipeline`.
func (c *Client) CreatePipeline(ctx context.Context, orgID string, req PipelineAPI) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/api/%s/pipelines", pathEscape(orgID)), req)
}

// UpdatePipeline replaces an existing pipeline.
//
// The update endpoint is a PUT on the collection rather than on the individual
// pipeline, with the ID carried in the body.
func (c *Client) UpdatePipeline(ctx context.Context, orgID string, req PipelineAPI) error {
	return c.do(ctx, http.MethodPut, fmt.Sprintf("/api/%s/pipelines", pathEscape(orgID)), req)
}

// GetPipeline reads a pipeline by ID, returning (nil, nil) when absent.
func (c *Client) GetPipeline(ctx context.Context, orgID, pipelineID string) (*PipelineAPI, error) {
	path := fmt.Sprintf("/api/%s/pipelines/%s", pathEscape(orgID), pathEscape(pipelineID))
	var out PipelineAPI
	found, err := c.doJSONOptional(ctx, http.MethodGet, path, nil, &out)
	if err != nil {
		return nil, err
	}
	if !found || out.Name == "" {
		return nil, nil
	}
	return &out, nil
}

// ListPipelines returns every pipeline in an organization.
func (c *Client) ListPipelines(ctx context.Context, orgID string) ([]PipelineAPI, error) {
	var out PipelineListAPI
	if err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/api/%s/pipelines", pathEscape(orgID)), nil, &out); err != nil {
		return nil, err
	}
	return out.List, nil
}

// FindPipelineByName resolves a pipeline name to its entry. The server
// lowercases names on save, so the comparison is case-insensitive.
func (c *Client) FindPipelineByName(ctx context.Context, orgID, name string) (*PipelineAPI, error) {
	pipelines, err := c.ListPipelines(ctx, orgID)
	if err != nil {
		return nil, err
	}
	want := strings.ToLower(strings.TrimSpace(name))
	for i := range pipelines {
		if strings.ToLower(pipelines[i].Name) == want {
			return &pipelines[i], nil
		}
	}
	return nil, nil
}

// SetPipelineEnabled pauses or resumes a pipeline. Enabling is its own
// endpoint rather than a field on the update body.
func (c *Client) SetPipelineEnabled(ctx context.Context, orgID, pipelineID string, enabled bool) error {
	path := fmt.Sprintf("/api/%s/pipelines/%s/enable?value=%t", pathEscape(orgID), pathEscape(pipelineID), enabled)
	return c.do(ctx, http.MethodPut, path, nil)
}

// DeletePipeline removes a pipeline.
func (c *Client) DeletePipeline(ctx context.Context, orgID, pipelineID string) error {
	path := fmt.Sprintf("/api/%s/pipelines/%s", pathEscape(orgID), pathEscape(pipelineID))
	return c.deleteIgnoreMissing(ctx, path)
}
