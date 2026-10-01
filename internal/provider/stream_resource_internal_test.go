package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// staleStreamServer answers the stream schema endpoint with
// enable_distinct_fields=false for the first staleReads reads and true after,
// the way an OpenObserve node does while its settings cache lags a write.
func staleStreamServer(t *testing.T, staleReads int64) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var reads atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := reads.Add(1)
		_ = json.NewEncoder(w).Encode(StreamAPI{
			Name:       "app",
			StreamType: "logs",
			Settings:   StreamSettingsAPI{EnableDistinctFields: n > staleReads},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &reads
}

// plannedStream is a plan that sets only enable_distinct_fields, leaving every
// other optional+computed setting unknown as Terraform does.
func plannedStream() StreamResourceModel {
	return StreamResourceModel{
		DataRetention:               types.Int64Unknown(),
		MaxQueryRange:               types.Int64Unknown(),
		FlattenLevel:                types.Int64Unknown(),
		StoreOriginalData:           types.BoolUnknown(),
		ApproxPartition:             types.BoolUnknown(),
		IndexOriginalData:           types.BoolUnknown(),
		IndexAllValues:              types.BoolUnknown(),
		EnableDistinctFields:        types.BoolValue(true),
		EnableLogPatternsExtraction: types.BoolUnknown(),
		FullTextSearchKeys:          types.SetUnknown(types.StringType),
		IndexFields:                 types.SetUnknown(types.StringType),
		BloomFilterFields:           types.SetUnknown(types.StringType),
		DefinedSchemaFields:         types.SetUnknown(types.StringType),
		DistinctValueFields:         types.SetUnknown(types.StringType),
		PartitionKeys:               types.ListUnknown(types.ObjectType{AttrTypes: streamPartitionKeyAttrTypes}),
		StorageType:                 types.StringUnknown(),
	}
}

func shortenStreamReadBack(t *testing.T, timeout time.Duration) {
	t.Helper()
	oldTimeout, oldInterval := streamReadBackTimeout, streamReadBackInterval
	streamReadBackTimeout, streamReadBackInterval = timeout, time.Millisecond
	t.Cleanup(func() { streamReadBackTimeout, streamReadBackInterval = oldTimeout, oldInterval })
}

// Not parallel: these tests change the package-level read-back bounds.

func TestStreamReadIntoWaitsForStaleSettings(t *testing.T) {
	shortenStreamReadBack(t, 5*time.Second)
	srv, reads := staleStreamServer(t, 2)
	r := &StreamResource{client: newClient(srv.URL, "u", "p", "default")}

	model := plannedStream()
	var diags diag.Diagnostics
	r.readInto(context.Background(), "default", "logs", "app", &model, &diags)

	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if !model.EnableDistinctFields.ValueBool() {
		t.Fatal("enable_distinct_fields: got false, want the value written (true)")
	}
	if got := reads.Load(); got != 3 {
		t.Fatalf("reads: got %d, want 3 (two stale, one fresh)", got)
	}
}

func TestStreamReadIntoGivesUpAndReturnsServerValue(t *testing.T) {
	shortenStreamReadBack(t, 50*time.Millisecond)
	srv, reads := staleStreamServer(t, 1<<62)
	r := &StreamResource{client: newClient(srv.URL, "u", "p", "default")}

	model := plannedStream()
	var diags diag.Diagnostics
	r.readInto(context.Background(), "default", "logs", "app", &model, &diags)

	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if model.EnableDistinctFields.ValueBool() {
		t.Fatal("enable_distinct_fields: got true, want the server's value (false) once retries run out")
	}
	if reads.Load() < 2 {
		t.Fatalf("reads: got %d, want at least one retry", reads.Load())
	}
}

func TestStreamSettingsMatchPlanIgnoresUnknown(t *testing.T) {
	t.Parallel()
	planned := plannedStream()
	got := planned
	got.EnableDistinctFields = types.BoolValue(true)
	got.DataRetention = types.Int64Value(30)
	if !streamSettingsMatchPlan(planned, got) {
		t.Fatal("values unknown at plan time must not block a match")
	}
	got.EnableDistinctFields = types.BoolValue(false)
	if streamSettingsMatchPlan(planned, got) {
		t.Fatal("a known planned value that differs must not match")
	}
}

// An unset partition key type is unknown inside an otherwise known list; it
// must not hold up the read-back.
func TestKnownValuesMatchPartitionKeyWithUnknownType(t *testing.T) {
	t.Parallel()
	objType := types.ObjectType{AttrTypes: streamPartitionKeyAttrTypes}
	key := func(kind attr.Value) attr.Value {
		return types.ObjectValueMust(streamPartitionKeyAttrTypes, map[string]attr.Value{
			"field":        types.StringValue("region"),
			"type":         kind,
			"hash_buckets": types.Int64Null(),
			"disabled":     types.BoolUnknown(),
		})
	}
	planned := types.ListValueMust(objType, []attr.Value{key(types.StringUnknown())})
	got := types.ListValueMust(objType, []attr.Value{types.ObjectValueMust(streamPartitionKeyAttrTypes, map[string]attr.Value{
		"field":        types.StringValue("region"),
		"type":         types.StringValue("value"),
		"hash_buckets": types.Int64Null(),
		"disabled":     types.BoolValue(false),
	})})
	if !knownValuesMatch(planned, got) {
		t.Fatal("an unknown nested type must match whatever the server chose")
	}

	other := types.ListValueMust(objType, []attr.Value{types.ObjectValueMust(streamPartitionKeyAttrTypes, map[string]attr.Value{
		"field":        types.StringValue("zone"),
		"type":         types.StringValue("value"),
		"hash_buckets": types.Int64Null(),
		"disabled":     types.BoolValue(false),
	})})
	if knownValuesMatch(planned, other) {
		t.Fatal("a different known field must not match")
	}
	if knownValuesMatch(planned, types.ListValueMust(objType, nil)) {
		t.Fatal("a missing element must not match")
	}
}

// A configured set that the server returns as a superset (OpenObserve derives
// some list settings from others) must converge, not wait out the timeout.
func TestStreamReadIntoConvergesOnServerSuperset(t *testing.T) {
	shortenStreamReadBack(t, 5*time.Second)
	var reads atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reads.Add(1)
		_ = json.NewEncoder(w).Encode(StreamAPI{
			Name:       "app",
			StreamType: "logs",
			Settings: StreamSettingsAPI{
				EnableDistinctFields: true,
				FullTextSearchKeys:   []string{"body", "log", "message"},
			},
		})
	}))
	t.Cleanup(srv.Close)
	r := &StreamResource{client: newClient(srv.URL, "u", "p", "default")}

	model := plannedStream()
	model.FullTextSearchKeys = types.SetValueMust(types.StringType, []attr.Value{types.StringValue("log")})
	var diags diag.Diagnostics
	r.readInto(context.Background(), "default", "logs", "app", &model, &diags)

	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got := reads.Load(); got != 1 {
		t.Fatalf("reads: got %d, want 1", got)
	}
}

// A retry that fails after a successful read keeps that read rather than
// failing the apply.
func TestStreamReadIntoKeepsLastReadWhenRetryFails(t *testing.T) {
	shortenStreamReadBack(t, 5*time.Second)
	var reads atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if reads.Add(1) > 1 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(StreamAPI{Name: "app", StreamType: "logs"})
	}))
	t.Cleanup(srv.Close)
	r := &StreamResource{client: newClient(srv.URL, "u", "p", "default")}

	model := plannedStream()
	var diags diag.Diagnostics
	r.readInto(context.Background(), "default", "logs", "app", &model, &diags)

	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if model.EnableDistinctFields.IsUnknown() || model.EnableDistinctFields.ValueBool() {
		t.Fatal("enable_distinct_fields: want the first read's value (false)")
	}
	if diags.WarningsCount() != 1 {
		t.Fatalf("warnings: got %d, want 1 explaining the failed retry", diags.WarningsCount())
	}
	if got := reads.Load(); got != 2 {
		t.Fatalf("reads: got %d, want 2", got)
	}
}
