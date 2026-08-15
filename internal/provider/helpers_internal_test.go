package provider

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestDiffStrings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		current    []string
		desired    []string
		wantAdd    []string
		wantRemove []string
	}{
		{
			name:    "no change",
			current: []string{"a", "b"},
			desired: []string{"a", "b"},
		},
		{
			name:    "additions only",
			current: []string{"a"},
			desired: []string{"a", "b", "c"},
			wantAdd: []string{"b", "c"},
		},
		{
			name:       "removals only",
			current:    []string{"a", "b", "c"},
			desired:    []string{"a"},
			wantRemove: []string{"b", "c"},
		},
		{
			name:       "replacement",
			current:    []string{"a"},
			desired:    []string{"b"},
			wantAdd:    []string{"b"},
			wantRemove: []string{"a"},
		},
		{
			name:       "empty desired clears everything",
			current:    []string{"a", "b"},
			desired:    nil,
			wantRemove: []string{"a", "b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			add, remove := diffStrings(tt.current, tt.desired)
			assertStrings(t, "add", add, tt.wantAdd)
			assertStrings(t, "remove", remove, tt.wantRemove)
		})
	}
}

func TestDiffPermissions(t *testing.T) {
	t.Parallel()

	current := []EntityAuthorization{
		{Object: "stream", Permission: "AllowList"},
		{Object: "dashboard", Permission: "AllowAll"},
	}
	desired := []EntityAuthorization{
		{Object: "stream", Permission: "AllowList"},
		{Object: "stream", Permission: "AllowGet"},
	}

	add, remove := diffPermissions(current, desired)

	if len(add) != 1 || add[0].Object != "stream" || add[0].Permission != "AllowGet" {
		t.Errorf("add = %+v, want a single stream/AllowGet grant", add)
	}
	if len(remove) != 1 || remove[0].Object != "dashboard" {
		t.Errorf("remove = %+v, want the dashboard grant", remove)
	}
}

func TestDiffPermissionsDistinguishesPermissionLevel(t *testing.T) {
	t.Parallel()

	// The same object at a different grant level is a different permission, so
	// the old level has to be revoked rather than silently left in place.
	current := []EntityAuthorization{{Object: "stream:logs", Permission: "AllowAll"}}
	desired := []EntityAuthorization{{Object: "stream:logs", Permission: "AllowGet"}}

	add, remove := diffPermissions(current, desired)

	if len(add) != 1 || add[0].Permission != "AllowGet" {
		t.Errorf("add = %+v, want AllowGet", add)
	}
	if len(remove) != 1 || remove[0].Permission != "AllowAll" {
		t.Errorf("remove = %+v, want AllowAll", remove)
	}
}

func TestJSONSubset(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want string
		got  string
		ok   bool
	}{
		{
			name: "server adds a field",
			want: `{"title":"CPU"}`,
			got:  `{"title":"CPU","dashboardId":"abc","created":"2026-01-01"}`,
			ok:   true,
		},
		{
			name: "server changed a managed field",
			want: `{"title":"CPU"}`,
			got:  `{"title":"Memory","dashboardId":"abc"}`,
			ok:   false,
		},
		{
			name: "nested additions are allowed",
			want: `{"panels":[{"id":"p1"}]}`,
			got:  `{"panels":[{"id":"p1","layout":{"w":6}}]}`,
			ok:   true,
		},
		{
			name: "a dropped panel is drift",
			want: `{"panels":[{"id":"p1"},{"id":"p2"}]}`,
			got:  `{"panels":[{"id":"p1"}]}`,
			ok:   false,
		},
		{
			name: "a missing key is drift",
			want: `{"title":"CPU","description":"x"}`,
			got:  `{"title":"CPU"}`,
			ok:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var want, got any
			if err := json.Unmarshal([]byte(tt.want), &want); err != nil {
				t.Fatalf("unmarshaling want: %v", err)
			}
			if err := json.Unmarshal([]byte(tt.got), &got); err != nil {
				t.Fatalf("unmarshaling got: %v", err)
			}
			if jsonSubset(want, got) != tt.ok {
				t.Errorf("jsonSubset() = %v, want %v", !tt.ok, tt.ok)
			}
		})
	}
}

func TestReconcileJSONKeepsConfiguredDocument(t *testing.T) {
	t.Parallel()

	var diags diag.Diagnostics
	configured := types.StringValue(`{"title":"CPU","version":5}`)
	server := json.RawMessage(`{"title":"CPU","version":5,"dashboardId":"abc"}`)

	got := reconcileJSON(configured, server, &diags)

	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %+v", diags)
	}
	// Server-added fields must not surface as drift.
	if got.ValueString() != `{"title":"CPU","version":5}` {
		t.Errorf("reconcileJSON() = %q, want the configured document back", got.ValueString())
	}
}

func TestReconcileJSONReportsRealDrift(t *testing.T) {
	t.Parallel()

	var diags diag.Diagnostics
	configured := types.StringValue(`{"title":"CPU"}`)
	server := json.RawMessage(`{"title":"Memory","dashboardId":"abc"}`)

	got := reconcileJSON(configured, server, &diags)

	if got.ValueString() == `{"title":"CPU"}` {
		t.Error("a changed title must be reported as drift, not masked")
	}
	if !contains(got.ValueString(), "Memory") {
		t.Errorf("reconcileJSON() = %q, want the server document", got.ValueString())
	}
}

func TestNormalizeJSONIsStable(t *testing.T) {
	t.Parallel()

	a, err := normalizeJSON([]byte(`{"b":2,"a":1}`))
	if err != nil {
		t.Fatalf("normalizeJSON: %v", err)
	}
	b, err := normalizeJSON([]byte("{\n  \"a\": 1,\n  \"b\": 2\n}"))
	if err != nil {
		t.Fatalf("normalizeJSON: %v", err)
	}
	if a != b {
		t.Errorf("documents differing only in key order and whitespace normalized differently: %q vs %q", a, b)
	}
}

func TestEncodeConditionValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "integer becomes a JSON number", in: "100", want: "100"},
		{name: "float becomes a JSON number", in: "1.5", want: "1.5"},
		{name: "negative number stays numeric", in: "-3", want: "-3"},
		{name: "text becomes a JSON string", in: "error", want: `"error"`},
		{name: "text needing escapes is quoted", in: `a"b`, want: `"a\"b"`},
		{name: "empty becomes an empty string", in: "", want: `""`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var diags diag.Diagnostics
			got := string(encodeConditionValue(tt.in, &diags))
			if got != tt.want {
				t.Errorf("encodeConditionValue(%q) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

func TestConditionValueRoundTrips(t *testing.T) {
	t.Parallel()

	for _, in := range []string{"100", "1.5", "error", "a b c"} {
		var diags diag.Diagnostics
		encoded := encodeConditionValue(in, &diags)
		if got := decodeConditionValue(encoded).ValueString(); got != in {
			t.Errorf("round trip of %q produced %q", in, got)
		}
	}
}

func TestPartitionTypeConversion(t *testing.T) {
	t.Parallel()

	kind, buckets := partitionTypeFromAPI("value")
	if kind != "value" || !buckets.IsNull() {
		t.Errorf("value partition decoded as (%q, %v)", kind, buckets)
	}

	kind, buckets = partitionTypeFromAPI(map[string]any{"hash": float64(32)})
	if kind != "hash" || buckets.ValueInt64() != 32 {
		t.Errorf("hash partition decoded as (%q, %v)", kind, buckets)
	}

	encoded := partitionTypeToAPI("hash", types.Int64Value(32))
	m, ok := encoded.(map[string]any)
	if !ok || m["hash"] != int64(32) {
		t.Errorf("hash partition encoded as %#v, want {\"hash\": 32}", encoded)
	}

	if got := partitionTypeToAPI("value", types.Int64Null()); got != "value" {
		t.Errorf("value partition encoded as %#v, want the bare string", got)
	}
}

func TestSamePartitionType(t *testing.T) {
	t.Parallel()

	if !samePartitionType("value", "value") {
		t.Error("identical value partitions compared unequal")
	}
	if samePartitionType("value", "prefix") {
		t.Error("value and prefix partitions compared equal")
	}
	if samePartitionType("value", map[string]any{"hash": float64(16)}) {
		t.Error("value and hash partitions compared equal")
	}
	if !samePartitionType(map[string]any{"hash": float64(16)}, map[string]any{"hash": float64(16)}) {
		t.Error("identical hash partitions compared unequal")
	}
	if samePartitionType(map[string]any{"hash": float64(16)}, map[string]any{"hash": float64(32)}) {
		t.Error("hash partitions with different bucket counts compared equal")
	}
}

func TestDashboardBodySelectsVersionedSlot(t *testing.T) {
	t.Parallel()

	d := &DashboardAPI{
		V5:      json.RawMessage(`{"title":"five"}`),
		Version: 5,
	}
	if got := string(d.Body()); got != `{"title":"five"}` {
		t.Errorf("Body() = %s, want the v5 document", got)
	}
	if d.Title() != "five" {
		t.Errorf("Title() = %q, want %q", d.Title(), "five")
	}

	// An unrecognised version must still find the populated slot rather than
	// returning nothing, so a newer server does not break reads.
	unknown := &DashboardAPI{
		V8:      json.RawMessage(`{"title":"eight","dashboardId":"abc"}`),
		Version: 99,
	}
	if unknown.DashboardID() != "abc" {
		t.Errorf("DashboardID() = %q, want %q", unknown.DashboardID(), "abc")
	}
}

func TestIsAlreadyExists(t *testing.T) {
	t.Parallel()

	if !isAlreadyExists(&apiError{StatusCode: 400, Body: `{"message":"stream already exists"}`}) {
		t.Error("a 400 reporting an existing object should be recognised")
	}
	if !isAlreadyExists(&apiError{StatusCode: 409, Body: "conflict"}) {
		t.Error("a 409 should be recognised")
	}
	if isAlreadyExists(&apiError{StatusCode: 400, Body: "invalid email"}) {
		t.Error("an unrelated 400 must not be treated as a conflict")
	}
	if isAlreadyExists(&apiError{StatusCode: 500, Body: "already exists"}) {
		t.Error("a 500 must not be treated as a conflict")
	}
}

func TestIsNotFound(t *testing.T) {
	t.Parallel()

	if !isNotFound(&apiError{StatusCode: 404}) {
		t.Error("a 404 should be recognised")
	}
	if isNotFound(&apiError{StatusCode: 500}) {
		t.Error("a 500 must not be treated as not-found")
	}
}

func TestResolveOrgPrefersResourceValue(t *testing.T) {
	t.Parallel()

	c := newClient("http://localhost:5080", "u", "p", "default")

	if got := resolveOrg(c, types.StringValue("team-a")); got != "team-a" {
		t.Errorf("resolveOrg() = %q, want the resource value", got)
	}
	if got := resolveOrg(c, types.StringNull()); got != "default" {
		t.Errorf("resolveOrg() = %q, want the provider default", got)
	}
	if got := resolveOrg(c, types.StringUnknown()); got != "default" {
		t.Errorf("resolveOrg() with an unknown value = %q, want the provider default", got)
	}
}

func TestWithDashboardID(t *testing.T) {
	t.Parallel()

	out, err := withDashboardID(json.RawMessage(`{"title":"CPU"}`), "abc123")
	if err != nil {
		t.Fatalf("withDashboardID: %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("unmarshaling result: %v", err)
	}
	if doc["dashboardId"] != "abc123" {
		t.Errorf("dashboardId = %v, want abc123", doc["dashboardId"])
	}
	if doc["title"] != "CPU" {
		t.Error("withDashboardID must leave the rest of the document alone")
	}

	if _, err := withDashboardID(json.RawMessage(`["not an object"]`), "abc"); err == nil {
		t.Error("a non-object document should be rejected")
	}
}

func assertStrings(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s = %v, want %v", label, got, want)
		return
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("%s = %v, want %v", label, got, want)
			return
		}
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func TestIsBeingDeleted(t *testing.T) {
	t.Parallel()

	// Stream deletion is asynchronous, so a retried destroy sees this instead
	// of a 404 and must still succeed.
	if !isBeingDeleted(&apiError{StatusCode: 400, Body: `{"code":400,"message":"stream [logs] is being deleted"}`}) {
		t.Error("an in-flight deletion should be recognised")
	}
	if isBeingDeleted(&apiError{StatusCode: 400, Body: "some other problem"}) {
		t.Error("an unrelated 400 must not be treated as an in-flight deletion")
	}
}

func TestExpandPermissionObject(t *testing.T) {
	t.Parallel()

	// A bare resource type means "every entity of this type", which OpenObserve
	// stores against an organization-scoped wildcard.
	if got := expandPermissionObject("stream", "myorg"); got != "stream:_all_myorg" {
		t.Errorf("expandPermissionObject(\"stream\") = %q, want %q", got, "stream:_all_myorg")
	}
	// An object that already names an entity is passed through untouched.
	if got := expandPermissionObject("stream:payments", "myorg"); got != "stream:payments" {
		t.Errorf("expandPermissionObject(\"stream:payments\") = %q, want it unchanged", got)
	}
	// So is an explicitly written wildcard.
	if got := expandPermissionObject("stream:_all_myorg", "myorg"); got != "stream:_all_myorg" {
		t.Errorf("expandPermissionObject on an explicit wildcard = %q, want it unchanged", got)
	}
}

func TestReconcilePermissionsKeepsShorthand(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	var diags diag.Diagnostics

	configured := permissionSet(t, &diags, [][2]string{
		{"stream", "AllowList"},
		{"dashboard", "AllowGet"},
	})
	// The server only ever reports the expanded form.
	server := []EntityAuthorization{
		{Object: "stream:_all_myorg", Permission: "AllowList"},
		{Object: "dashboard:_all_myorg", Permission: "AllowGet"},
	}

	got := reconcilePermissions(ctx, "myorg", configured, server, &diags)

	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %+v", diags)
	}
	if !got.Equal(configured) {
		t.Errorf("shorthand was rewritten to %v; it should survive a read unchanged", got)
	}
}

func TestReconcilePermissionsReportsRealDrift(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	var diags diag.Diagnostics

	configured := permissionSet(t, &diags, [][2]string{{"stream", "AllowList"}})
	// Someone granted more than the configuration asks for.
	server := []EntityAuthorization{
		{Object: "stream:_all_myorg", Permission: "AllowList"},
		{Object: "stream:_all_myorg", Permission: "AllowDelete"},
	}

	got := reconcilePermissions(ctx, "myorg", configured, server, &diags)

	if got.Equal(configured) {
		t.Error("an extra grant on the server must be reported as drift, not masked")
	}
}

// permissionSet builds a permissions set value from {object, permission} pairs.
func permissionSet(t *testing.T, diags *diag.Diagnostics, pairs [][2]string) types.Set {
	t.Helper()

	values := make([]attr.Value, 0, len(pairs))
	for _, p := range pairs {
		values = append(values, objectValue(rolePermissionAttrTypes, map[string]attr.Value{
			"object":     types.StringValue(p[0]),
			"permission": types.StringValue(p[1]),
		}, diags))
	}
	set, d := types.SetValue(types.ObjectType{AttrTypes: rolePermissionAttrTypes}, values)
	diags.Append(d...)
	if diags.HasError() {
		t.Fatalf("building permission set: %+v", diags)
	}
	return set
}
