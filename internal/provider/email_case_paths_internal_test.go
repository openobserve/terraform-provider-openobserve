package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Account paths carry the lowercase email, the identity the server stores, so
// a role grant made through the path reaches the account.
func TestAccountPathsUseLowercaseEmail(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"code":200}`))
	}))
	t.Cleanup(srv.Close)
	c := newClient(srv.URL, "u", "p", "default")
	ctx := context.Background()
	_ = c.UpdateUser(ctx, "default", "Jane.Doe@Example.com", UpdateUserAPI{})
	_ = c.DeleteUser(ctx, "default", "Jane.Doe@Example.com")
	_ = c.UpdateServiceAccount(ctx, "default", "CI@Example.com", ServiceAccountAPI{})
	_, _ = c.GetUserRoles(ctx, "default", "Jane.Doe@Example.com")
	for _, p := range paths {
		if p != strings.ToLower(p) {
			t.Errorf("path %q is not lowercase", p)
		}
	}
	if len(paths) != 4 {
		t.Errorf("calls: %v", paths)
	}
}

func TestMembersToModel(t *testing.T) {
	t.Parallel()
	set := func(v ...string) types.Set { return stringSet(v...) }
	elems := func(s types.Set) []string {
		var out []string
		for _, e := range s.Elements() {
			out = append(out, e.(types.String).ValueString())
		}
		sort.Strings(out)
		return out
	}
	cases := []struct {
		name   string
		prior  types.Set
		server []string
		want   []string
	}{
		{"configured spelling kept", set("Jane@Example.com"), []string{"jane@example.com"}, []string{"Jane@Example.com"}},
		{"import", types.SetNull(types.StringType), []string{"jane@example.com"}, []string{"jane@example.com"}},
		{"added elsewhere", set("Jane@Example.com"), []string{"jane@example.com", "bob@example.com"},
			[]string{"Jane@Example.com", "bob@example.com"}},
		// A grant an earlier release made under the configured spelling: shown
		// in lowercase so that the plan repairs it.
		{"legacy mixed-case grant", set("Jane@Example.com"), []string{"Jane@Example.com"}, []string{"jane@example.com"}},
		{"legacy and repaired", set("Jane@Example.com"), []string{"Jane@Example.com", "jane@example.com"},
			[]string{"Jane@Example.com", "jane@example.com"}},
		{"legacy, state already lowercase", set("jane@example.com"), []string{"Jane@Example.com"}, []string{"jane@example.com"}},
	}
	for _, tc := range cases {
		var d diag.Diagnostics
		got := elems(membersToModel(context.Background(), tc.prior, tc.server, &d))
		sort.Strings(tc.want)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestLowerEmails(t *testing.T) {
	t.Parallel()
	if got := lowerEmails([]string{"A@B.com", "c@d.com"}); got[0] != "a@b.com" || got[1] != "c@d.com" {
		t.Errorf("got %v", got)
	}
	if lowerEmails(nil) != nil {
		t.Error("nil must stay nil")
	}
}

// Refreshing the result again gives the same answer, whatever state holds.
func TestMembersToModelIsStable(t *testing.T) {
	t.Parallel()
	for _, server := range [][]string{
		{"Jane@Example.com"},
		{"Jane@Example.com", "jane@example.com"},
		{"jane@example.com", "bob@example.com"},
	} {
		var d diag.Diagnostics
		state := stringSet("Jane@Example.com")
		first := membersToModel(context.Background(), state, server, &d)
		for i := 0; i < 4; i++ {
			next := membersToModel(context.Background(), first, server, &d)
			if !next.Equal(first) {
				t.Errorf("server %v: refresh %d gave %v after %v", server, i, next, first)
			}
			first = next
		}
	}
}
