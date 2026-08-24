package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// configureResourceClient extracts the shared API client from provider data.
func configureResourceClient(req resource.ConfigureRequest, resp *resource.ConfigureResponse) *Client {
	if req.ProviderData == nil {
		return nil
	}
	client, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected provider data type",
			fmt.Sprintf("Expected *provider.Client, got %T. This is a bug in the provider.", req.ProviderData),
		)
		return nil
	}
	return client
}

// configureDataSourceClient extracts the shared API client from provider data.
func configureDataSourceClient(req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) *Client {
	if req.ProviderData == nil {
		return nil
	}
	client, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected provider data type",
			fmt.Sprintf("Expected *provider.Client, got %T. This is a bug in the provider.", req.ProviderData),
		)
		return nil
	}
	return client
}

// resolveOrg returns the resource-level org_id when set, otherwise the
// provider-level default.
func resolveOrg(client *Client, v types.String) string {
	if !v.IsNull() && !v.IsUnknown() && v.ValueString() != "" {
		return v.ValueString()
	}
	return client.DefaultOrgID()
}

// requireOrg resolves the organization and records an error when neither the
// resource nor the provider supplies one.
func requireOrg(client *Client, v types.String, diags *diag.Diagnostics) string {
	org := resolveOrg(client, v)
	if org == "" {
		diags.AddError(
			"Missing organization",
			"Set `org_id` on this resource or `org_id` on the provider block (or the OPENOBSERVE_ORG_ID environment variable).",
		)
	}
	return org
}

// ---------------------------------------------------------------------------
// Collection conversions
// ---------------------------------------------------------------------------

// stringsFromSet converts a Terraform set of strings to a sorted Go slice.
// A null or unknown set yields nil.
func stringsFromSet(ctx context.Context, v types.Set, diags *diag.Diagnostics) []string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	var out []string
	diags.Append(v.ElementsAs(ctx, &out, false)...)
	sort.Strings(out)
	return out
}

// stringsFromList converts a Terraform list of strings to a Go slice,
// preserving order. A null or unknown list yields nil.
func stringsFromList(ctx context.Context, v types.List, diags *diag.Diagnostics) []string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	var out []string
	diags.Append(v.ElementsAs(ctx, &out, false)...)
	return out
}

// setFromStrings converts a Go slice to a Terraform set of strings.
func setFromStrings(ctx context.Context, in []string, diags *diag.Diagnostics) types.Set {
	if in == nil {
		in = []string{}
	}
	v, d := types.SetValueFrom(ctx, types.StringType, in)
	diags.Append(d...)
	return v
}

// listFromStrings converts a Go slice to a Terraform list of strings.
func listFromStrings(ctx context.Context, in []string, diags *diag.Diagnostics) types.List {
	if in == nil {
		in = []string{}
	}
	v, d := types.ListValueFrom(ctx, types.StringType, in)
	diags.Append(d...)
	return v
}

// mapFromStrings converts a Go string map to a Terraform map of strings.
func mapFromStrings(ctx context.Context, in map[string]string, diags *diag.Diagnostics) types.Map {
	if len(in) == 0 {
		return types.MapNull(types.StringType)
	}
	v, d := types.MapValueFrom(ctx, types.StringType, in)
	diags.Append(d...)
	return v
}

// stringsFromMap converts a Terraform map of strings to a Go map.
func stringsFromMap(ctx context.Context, v types.Map, diags *diag.Diagnostics) map[string]string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	out := map[string]string{}
	diags.Append(v.ElementsAs(ctx, &out, false)...)
	return out
}

// diffStrings returns the elements to add and remove to turn current into
// desired. Both results are sorted for deterministic requests.
func diffStrings(current, desired []string) (add, remove []string) {
	currentSet := make(map[string]struct{}, len(current))
	for _, s := range current {
		currentSet[s] = struct{}{}
	}
	desiredSet := make(map[string]struct{}, len(desired))
	for _, s := range desired {
		desiredSet[s] = struct{}{}
	}
	for _, s := range desired {
		if _, ok := currentSet[s]; !ok {
			add = append(add, s)
		}
	}
	for _, s := range current {
		if _, ok := desiredSet[s]; !ok {
			remove = append(remove, s)
		}
	}
	sort.Strings(add)
	sort.Strings(remove)
	return add, remove
}

// ---------------------------------------------------------------------------
// Optional scalars
// ---------------------------------------------------------------------------

func optString(v types.String) *string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	s := v.ValueString()
	return &s
}

func optInt64(v types.Int64) *int64 {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	i := v.ValueInt64()
	return &i
}

func optBool(v types.Bool) *bool {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	b := v.ValueBool()
	return &b
}

func optFloat64(v types.Float64) *float64 {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	f := v.ValueFloat64()
	return &f
}

func stringFromPtr(p *string) types.String {
	if p == nil {
		return types.StringNull()
	}
	return types.StringValue(*p)
}

func int64FromPtr(p *int64) types.Int64 {
	if p == nil {
		return types.Int64Null()
	}
	return types.Int64Value(*p)
}

func boolFromPtr(p *bool) types.Bool {
	if p == nil {
		return types.BoolNull()
	}
	return types.BoolValue(*p)
}

func float64FromPtr(p *float64) types.Float64 {
	if p == nil {
		return types.Float64Null()
	}
	return types.Float64Value(*p)
}

// ---------------------------------------------------------------------------
// JSON attributes
// ---------------------------------------------------------------------------

// normalizeJSON re-marshals a JSON document with sorted keys so that
// semantically identical documents compare equal in Terraform state.
func normalizeJSON(raw []byte) (string, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", err
	}
	out, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// jsonStringValue converts a raw JSON document to a normalized Terraform string.
func jsonStringValue(raw json.RawMessage, diags *diag.Diagnostics) types.String {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return types.StringNull()
	}
	s, err := normalizeJSON(raw)
	if err != nil {
		diags.AddError("Invalid JSON returned by OpenObserve", err.Error())
		return types.StringNull()
	}
	return types.StringValue(s)
}

// rawJSONFromString parses a Terraform string attribute into raw JSON.
func rawJSONFromString(v types.String, attrName string, diags *diag.Diagnostics) json.RawMessage {
	if v.IsNull() || v.IsUnknown() || v.ValueString() == "" {
		return nil
	}
	raw := json.RawMessage(v.ValueString())
	if !json.Valid(raw) {
		diags.AddError(
			fmt.Sprintf("Invalid JSON in %s", attrName),
			fmt.Sprintf("The value of %s is not valid JSON.", attrName),
		)
		return nil
	}
	return raw
}

// objectValue builds an object value and appends any conversion diagnostics.
func objectValue(attrTypes map[string]attr.Type, values map[string]attr.Value, diags *diag.Diagnostics) types.Object {
	v, d := types.ObjectValue(attrTypes, values)
	diags.Append(d...)
	return v
}

// jsonSubset reports whether every key and value present in want also appears,
// with the same value, in got.
//
// It is how a JSON document supplied by the user is compared against the
// document the server stores: OpenObserve enriches documents with fields it
// manages itself (identifiers, timestamps), and those additions are not drift.
func jsonSubset(want, got any) bool {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return false
		}
		for k, wv := range w {
			gv, present := g[k]
			if !present || !jsonSubset(wv, gv) {
				return false
			}
		}
		return true
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range w {
			if !jsonSubset(w[i], g[i]) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(want, got)
	}
}

// reconcileJSON decides what a JSON attribute should hold after a read.
//
// When the server's document still contains everything the configured document
// asked for, the configured value is kept so server-managed additions do not
// surface as a permanent diff. Otherwise the server's document wins and
// Terraform reports the drift.
func reconcileJSON(configured types.String, serverBody json.RawMessage, diags *diag.Diagnostics) types.String {
	serverValue := jsonStringValue(serverBody, diags)
	if configured.IsNull() || configured.IsUnknown() || configured.ValueString() == "" {
		return serverValue
	}

	var want, got any
	if err := json.Unmarshal([]byte(configured.ValueString()), &want); err != nil {
		return serverValue
	}
	if err := json.Unmarshal(serverBody, &got); err != nil {
		return serverValue
	}
	if jsonSubset(want, got) {
		normalized, err := normalizeJSON([]byte(configured.ValueString()))
		if err != nil {
			return serverValue
		}
		return types.StringValue(normalized)
	}
	return serverValue
}

// reconcileStringSet decides what an Optional+Computed set attribute should
// hold after a write or a read.
//
// OpenObserve derives some list settings from others (a bloom filter field is
// also registered as a secondary index field, for example), so the server's
// list can be a strict superset of what was configured. Reporting those
// additions would produce a diff the user cannot resolve, so a superset keeps
// the configured value and anything else is treated as real drift.
func reconcileStringSet(ctx context.Context, configured types.Set, server []string, diags *diag.Diagnostics) types.Set {
	if configured.IsNull() || configured.IsUnknown() {
		return setFromStrings(ctx, server, diags)
	}

	want := stringsFromSet(ctx, configured, diags)
	if diags.HasError() {
		return setFromStrings(ctx, server, diags)
	}

	have := make(map[string]struct{}, len(server))
	for _, s := range server {
		have[s] = struct{}{}
	}
	for _, s := range want {
		if _, ok := have[s]; !ok {
			return setFromStrings(ctx, server, diags)
		}
	}
	return configured
}

// boolPreserveNull maps a server boolean back onto an optional attribute
// without inventing a value the configuration never set.
//
// Terraform requires the value applied to an Optional attribute to match the
// configuration exactly, so writing `false` where the user wrote nothing is an
// inconsistent-result error. A server `false` against an unset attribute is
// therefore left null; anything else is reported, so real drift still shows.
func boolPreserveNull(configured types.Bool, server bool) types.Bool {
	if !server && configured.IsNull() {
		return types.BoolNull()
	}
	return types.BoolValue(server)
}

// ---------------------------------------------------------------------------
// Configuration presence, for ValidateConfig
// ---------------------------------------------------------------------------

// Validation must never fire on an unknown value.
//
// Unknown means "not determined yet", so a value that is merely unresolved
// during plan is indistinguishable from one the author never wrote. Erroring on
// it produces a diagnostic the user cannot act on, because the configuration is
// correct and only the ordering made the value unavailable.
//
// This bites hardest on a nested block driven by a `dynamic` block inside a
// module. Terraform materializes a SingleNestedBlock whose `dynamic` produced no
// iterations as a present object whose attributes are unknown, rather than as a
// null object, so a bare `!= nil` on the model pointer reports a block the
// author never wrote. That is the shape of openobserve/terraform-provider-openobserve#3.

// blockPresence is what a nested block's configuration can tell us.
type blockPresence int

const (
	// blockAbsent means nothing inside the block was written.
	blockAbsent blockPresence = iota
	// blockConfigured means at least one value inside it is known and set.
	blockConfigured
	// blockIndeterminate means the block carries unknowns, so whether the
	// author wrote it cannot be decided yet. Skip the check.
	blockIndeterminate
)

// knownAndSet reports whether a scalar was written and has already resolved.
// Use this rather than !IsNull() anywhere a diagnostic depends on the answer.
func knownAndSet(v attr.Value) bool {
	return v != nil && !v.IsNull() && !v.IsUnknown()
}

// valuePresence classifies a configuration value, descending into nested
// objects so that a block containing only unknown leaves is reported as
// indeterminate rather than as configured.
func valuePresence(v attr.Value) blockPresence {
	if v == nil || v.IsNull() {
		return blockAbsent
	}
	if v.IsUnknown() {
		return blockIndeterminate
	}

	switch t := v.(type) {
	case types.Object:
		result := blockAbsent
		for _, av := range t.Attributes() {
			switch valuePresence(av) {
			case blockConfigured:
				return blockConfigured
			case blockIndeterminate:
				result = blockIndeterminate
			}
		}
		return result
	case types.List:
		if len(t.Elements()) == 0 {
			return blockAbsent
		}
		return blockConfigured
	case types.Set:
		if len(t.Elements()) == 0 {
			return blockAbsent
		}
		return blockConfigured
	case types.Map:
		if len(t.Elements()) == 0 {
			return blockAbsent
		}
		return blockConfigured
	default:
		return blockConfigured
	}
}

// nestedBlockPresence classifies the nested block at the given path, reading it
// from the raw configuration rather than from a decoded model pointer. The
// pointer cannot express the difference between an absent block and one whose
// contents are not yet known.
func nestedBlockPresence(ctx context.Context, cfg tfsdk.Config, p path.Path) blockPresence {
	var obj types.Object
	if diags := cfg.GetAttribute(ctx, p, &obj); diags.HasError() {
		// An unreadable block is not something to raise a validation error
		// about; leave it to apply, which sees concrete values.
		return blockIndeterminate
	}
	return valuePresence(obj)
}
