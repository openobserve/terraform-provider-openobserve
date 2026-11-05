package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// The server stores a destination as an alert destination unless create and
// update carry module=pipeline, and a pipeline's remote_stream node cannot send
// to an alert destination.
func TestPipelineDestinationWritesSendPipelineModule(t *testing.T) {
	var mu sync.Mutex
	got := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got[r.Method] = r.URL.Path + "?" + r.URL.RawQuery
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	c := newClient(srv.URL, "u", "p", "default")
	ctx := context.Background()
	dest := PipelineDestinationAPI{Name: "remote", URL: "https://receiver.example/ingest", Method: "post"}

	if err := c.CreatePipelineDestination(ctx, "default", dest); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := c.UpdatePipelineDestination(ctx, "default", "remote", dest); err != nil {
		t.Fatalf("update: %v", err)
	}

	want := map[string]string{
		http.MethodPost: "/api/default/alerts/destinations?module=pipeline",
		http.MethodPut:  "/api/default/alerts/destinations/remote?module=pipeline",
	}
	for method, w := range want {
		if got[method] != w {
			t.Errorf("%s: got %q, want %q", method, got[method], w)
		}
	}
}
