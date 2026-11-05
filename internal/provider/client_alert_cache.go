package provider

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

// alertListTTL bounds how long one provider process reuses an alert folder
// listing or a folder's alert list. A plan refreshes every alert within a few
// seconds, so this is enough for N alerts to list each folder once, while a
// long-lived process still sees changes made elsewhere.
const alertListTTL = 30 * time.Second

// alertListCache holds the listings the folder lookup reads, so that
// refreshing many alerts in one run does not list the same folders once per
// alert. Concurrent misses for the same key share one request. Of the failed
// requests only a 401 or 403 is kept. Writes through this client drop the
// org's entries, so a run never reads stale data about its own changes.
type alertListCache struct {
	mu      sync.Mutex
	entries map[alertListKey]*alertListEntry
	now     func() time.Time
}

type alertListKey struct {
	org string
	// folder is the folder whose alerts were listed, "" for the org-wide
	// list. Unused for the folder listing.
	folder  string
	folders bool
}

type alertListEntry struct {
	ready   chan struct{}
	expires time.Time
	alerts  []AlertListItemAPI
	folders []FolderAPI
	err     error
	// callerDone is set when the context of the caller that ran the fetch
	// ended, so the failure is that caller's, not the server's.
	callerDone bool
}

func (c *alertListCache) clock() time.Time {
	c.mu.Lock()
	now := c.now
	c.mu.Unlock()
	if now != nil {
		return now()
	}
	return time.Now()
}

// get returns the entry for key, running fetch when there is no fresh one.
// Callers must not modify the returned slices.
func (c *alertListCache) get(ctx context.Context, key alertListKey, fetch func(*alertListEntry)) (*alertListEntry, error) {
	c.mu.Lock()
	if c.entries == nil {
		c.entries = map[alertListKey]*alertListEntry{}
	}
	if e, ok := c.entries[key]; ok {
		c.mu.Unlock()
		select {
		case <-e.ready:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if e.callerDone && ctx.Err() == nil {
			// The request ran on another caller's context, which ended. It
			// is not cached, so asking again starts a new one.
			return c.get(ctx, key, fetch)
		}
		if c.clock().Before(e.expires) {
			// Everyone waiting on a failed request shares its failure. A
			// transient one is already dropped, so the next call asks again.
			return e, e.err
		}
		c.mu.Lock()
		if c.entries[key] == e {
			delete(c.entries, key)
		}
		c.mu.Unlock()
		return c.get(ctx, key, fetch)
	}
	e := &alertListEntry{ready: make(chan struct{})}
	c.entries[key] = e
	c.mu.Unlock()

	completed := false
	defer func() {
		// Deferred so waiters are released even if fetch panics.
		if !completed {
			e.err = errFetchIncomplete
		}
		e.callerDone = e.err != nil && ctx.Err() != nil
		if e.err != nil && !isPermissionDenied(e.err) {
			// Only a refusal is kept: it will not change within the run, and
			// under RBAC the org-wide list may be refused for every alert
			// refreshed. Anything else is asked again by the next call. A
			// refusal outlives a grant made by the same apply for up to the
			// TTL; role and group writes do not drop it.
			c.mu.Lock()
			if c.entries[key] == e {
				delete(c.entries, key)
			}
			c.mu.Unlock()
		}
		close(e.ready)
	}()
	fetch(e)
	completed = true
	e.expires = c.clock().Add(alertListTTL)
	return e, e.err
}

// errFetchIncomplete marks an entry whose fetch never returned.
var errFetchIncomplete = errors.New("alert list request did not complete")

// isPermissionDenied reports whether err is an HTTP 401 or 403.
func isPermissionDenied(err error) bool {
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden
	}
	return false
}

// invalidate drops every listing of an organization.
func (c *alertListCache) invalidate(org string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.entries {
		if key.org == org {
			delete(c.entries, key)
		}
	}
}

// cachedAlertList is ListAlerts through the lookup cache.
func (c *Client) cachedAlertList(ctx context.Context, orgID, folderID string) ([]AlertListItemAPI, error) {
	e, err := c.alertLists.get(ctx, alertListKey{org: orgID, folder: folderID}, func(e *alertListEntry) {
		e.alerts, e.err = c.ListAlerts(ctx, orgID, folderID)
	})
	if err != nil {
		return nil, err
	}
	return e.alerts, nil
}

// cachedAlertFolders is ListFolders(alerts) through the lookup cache.
func (c *Client) cachedAlertFolders(ctx context.Context, orgID string) ([]FolderAPI, error) {
	e, err := c.alertLists.get(ctx, alertListKey{org: orgID, folders: true}, func(e *alertListEntry) {
		e.folders, e.err = c.ListFolders(ctx, orgID, "alerts")
	})
	if err != nil {
		return nil, err
	}
	return e.folders, nil
}
