package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// staleUserServer answers the user list with role "editor" for the first
// staleReads reads and "user" after, the way an OpenObserve node does while
// its organization-user cache lags a role change.
func staleUserServer(t *testing.T, staleReads int64) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var reads atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		role := "user"
		if reads.Add(1) <= staleReads {
			role = "editor"
		}
		_ = json.NewEncoder(w).Encode(UserListAPI{Data: []UserAPI{{Email: "jane@example.com", Role: role}}})
	}))
	t.Cleanup(srv.Close)
	return srv, &reads
}

// plannedUser is a plan that changes only the role.
func plannedUser() UserResourceModel {
	return UserResourceModel{
		Email:     types.StringValue("jane@example.com"),
		Role:      types.StringValue("user"),
		FirstName: types.StringNull(),
		LastName:  types.StringNull(),
	}
}

func shortenUserReadBack(t *testing.T, timeout time.Duration) {
	t.Helper()
	oldTimeout, oldInterval := userReadBackTimeout, userReadBackInterval
	userReadBackTimeout, userReadBackInterval = timeout, time.Millisecond
	t.Cleanup(func() { userReadBackTimeout, userReadBackInterval = oldTimeout, oldInterval })
}

// Not parallel: these tests change the package-level read-back bounds.

func TestUserReadIntoWaitsForStaleRole(t *testing.T) {
	shortenUserReadBack(t, 5*time.Second)
	srv, reads := staleUserServer(t, 2)
	r := &UserResource{client: newClient(srv.URL, "u", "p", "default")}

	model := plannedUser()
	var diags diag.Diagnostics
	r.readInto(context.Background(), "default", "jane@example.com", &model, &diags)

	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got := model.Role.ValueString(); got != "user" {
		t.Fatalf("role: got %q, want the role written (user)", got)
	}
	if got := reads.Load(); got != 3 {
		t.Fatalf("reads: got %d, want 3 (two stale, one fresh)", got)
	}
}

func TestUserReadIntoGivesUpAndReturnsServerValue(t *testing.T) {
	shortenUserReadBack(t, 50*time.Millisecond)
	srv, reads := staleUserServer(t, 1<<62)
	r := &UserResource{client: newClient(srv.URL, "u", "p", "default")}

	model := plannedUser()
	var diags diag.Diagnostics
	r.readInto(context.Background(), "default", "jane@example.com", &model, &diags)

	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got := model.Role.ValueString(); got != "editor" {
		t.Fatalf("role: got %q, want the server's value (editor) once retries run out", got)
	}
	if reads.Load() < 2 {
		t.Fatalf("reads: got %d, want at least one retry", reads.Load())
	}
}

// A retry that fails after a successful read keeps that read rather than
// failing the apply.
func TestUserReadIntoKeepsLastReadWhenRetryFails(t *testing.T) {
	shortenUserReadBack(t, 5*time.Second)
	var reads atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if reads.Add(1) > 1 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(UserListAPI{Data: []UserAPI{{Email: "jane@example.com", Role: "editor"}}})
	}))
	t.Cleanup(srv.Close)
	r := &UserResource{client: newClient(srv.URL, "u", "p", "default")}

	model := plannedUser()
	var diags diag.Diagnostics
	r.readInto(context.Background(), "default", "jane@example.com", &model, &diags)

	if diags.HasError() {
		t.Fatalf("unexpected error: %v", diags)
	}
	if diags.WarningsCount() != 1 {
		t.Fatalf("warnings: got %d, want 1", diags.WarningsCount())
	}
	if got := model.Role.ValueString(); got != "editor" {
		t.Fatalf("role: got %q, want the earlier read (editor)", got)
	}
}

func TestUserMatchesPlanIgnoresUnset(t *testing.T) {
	t.Parallel()
	planned := plannedUser()
	got := planned
	got.FirstName = types.StringValue("Jane")
	if !userMatchesPlan(planned, got) {
		t.Fatal("values not set in the plan must not block a match")
	}
	got.Role = types.StringValue("editor")
	if userMatchesPlan(planned, got) {
		t.Fatal("a planned role that differs must not match")
	}
}
