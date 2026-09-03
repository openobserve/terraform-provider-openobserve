package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// Integration tests exercise the client against a live OpenObserve instance.
// They are skipped unless OPENOBSERVE_ENDPOINT is set, so `go test ./...` stays
// hermetic:
//
//	docker run -d --name o2 -p 5080:5080 \
//	  -e ZO_ROOT_USER_EMAIL=root@example.com \
//	  -e ZO_ROOT_USER_PASSWORD='Complexpass#123' \
//	  openobserve/openobserve:latest
//
//	OPENOBSERVE_ENDPOINT=http://localhost:5080 \
//	OPENOBSERVE_USERNAME=root@example.com \
//	OPENOBSERVE_PASSWORD='Complexpass#123' \
//	OPENOBSERVE_ORG_ID=default \
//	  go test ./internal/provider/ -run TestIntegration -v
//
// Every test cleans up after itself, which is also how the delete paths get
// covered.
func integrationClient(t *testing.T) *Client {
	t.Helper()

	endpoint := os.Getenv("OPENOBSERVE_ENDPOINT")
	if endpoint == "" {
		t.Skip("set OPENOBSERVE_ENDPOINT to run integration tests")
	}
	org := os.Getenv("OPENOBSERVE_ORG_ID")
	if org == "" {
		org = "default"
	}
	return newClient(
		endpoint,
		os.Getenv("OPENOBSERVE_USERNAME"),
		os.Getenv("OPENOBSERVE_PASSWORD"),
		org,
	)
}

// uniqueName keeps parallel runs and repeated runs from colliding.
func uniqueName(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

func TestIntegrationStreamLifecycle(t *testing.T) {
	c := integrationClient(t)
	ctx := context.Background()
	org := c.DefaultOrgID()
	name := uniqueName("tf_it_stream")

	if err := c.CreateStream(ctx, org, "logs", name, CreateStreamAPI{}); err != nil {
		t.Fatalf("CreateStream: %v", err)
	}
	t.Cleanup(func() {
		if err := c.DeleteStream(ctx, org, "logs", name); err != nil {
			t.Errorf("DeleteStream: %v", err)
		}
	})

	got, err := c.GetStream(ctx, org, "logs", name)
	if err != nil {
		t.Fatalf("GetStream: %v", err)
	}
	if got == nil {
		t.Fatal("GetStream returned nothing for a stream that was just created")
	}

	update := UpdateStreamSettingsAPI{
		PartitionKeys:       newAddRemove([]StreamPartitionAPI{{Field: "service", Types: "value"}}, nil),
		FullTextSearchKeys:  newAddRemove([]string{"message"}, nil),
		IndexFields:         newAddRemove([]string{"level"}, nil),
		BloomFilterFields:   newAddRemove[string](nil, nil),
		DefinedSchemaFields: newAddRemove[string](nil, nil),
		DistinctValueFields: newAddRemove[string](nil, nil),
		DataRetention:       ptr(int64(45)),
	}
	if err := c.UpdateStreamSettings(ctx, org, "logs", name, update); err != nil {
		t.Fatalf("UpdateStreamSettings: %v", err)
	}

	got, err = c.GetStream(ctx, org, "logs", name)
	if err != nil {
		t.Fatalf("GetStream after update: %v", err)
	}
	if got.Settings.DataRetention != 45 {
		t.Errorf("data_retention = %d, want 45", got.Settings.DataRetention)
	}
	if len(got.Settings.FullTextSearchKeys) != 1 || got.Settings.FullTextSearchKeys[0] != "message" {
		t.Errorf("full_text_search_keys = %v, want [message]", got.Settings.FullTextSearchKeys)
	}
	// The read side may send partition keys as a map; decoding must produce the
	// key regardless.
	if len(got.Settings.PartitionKeys) != 1 || got.Settings.PartitionKeys[0].Field != "service" {
		t.Errorf("partition_keys = %+v, want a single key on `service`", got.Settings.PartitionKeys)
	}

	streams, err := c.ListStreams(ctx, org, "logs")
	if err != nil {
		t.Fatalf("ListStreams: %v", err)
	}
	if !containsStream(streams, name) {
		t.Errorf("ListStreams did not include %q", name)
	}
}

func TestIntegrationFolderAndDashboardLifecycle(t *testing.T) {
	c := integrationClient(t)
	ctx := context.Background()
	org := c.DefaultOrgID()

	folder, err := c.CreateFolder(ctx, org, "dashboards", FolderAPI{
		Name:        uniqueName("tf_it_folder"),
		Description: "integration test",
	})
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	t.Cleanup(func() {
		if err := c.DeleteFolder(ctx, org, "dashboards", folder.FolderID); err != nil {
			t.Errorf("DeleteFolder: %v", err)
		}
	})
	if folder.FolderID == "" {
		t.Fatal("CreateFolder did not return a folder ID")
	}

	title := uniqueName("tf_it_dashboard")
	body, err := json.Marshal(map[string]any{
		"version":     5,
		"title":       title,
		"description": "integration test",
		"role":        "",
		"owner":       "",
		"tabs":        []any{map[string]any{"tabId": "default", "name": "Default", "panels": []any{}}},
	})
	if err != nil {
		t.Fatalf("marshaling dashboard: %v", err)
	}

	created, err := c.CreateDashboard(ctx, org, folder.FolderID, body)
	if err != nil {
		t.Fatalf("CreateDashboard: %v", err)
	}
	dashboardID := created.DashboardID()
	if dashboardID == "" {
		t.Fatal("CreateDashboard did not return a dashboard ID")
	}
	t.Cleanup(func() {
		if err := c.DeleteDashboard(ctx, org, dashboardID, folder.FolderID); err != nil {
			t.Errorf("DeleteDashboard: %v", err)
		}
	})

	// The folder a dashboard lives in is only discoverable by searching, which
	// is what import relies on.
	foundFolder, err := c.FindDashboardFolder(ctx, org, dashboardID)
	if err != nil {
		t.Fatalf("FindDashboardFolder: %v", err)
	}
	if foundFolder != folder.FolderID {
		t.Errorf("FindDashboardFolder = %q, want %q", foundFolder, folder.FolderID)
	}

	got, err := c.GetDashboard(ctx, org, dashboardID)
	if err != nil {
		t.Fatalf("GetDashboard: %v", err)
	}
	if got == nil || got.Title() != title {
		t.Fatalf("GetDashboard returned %+v, want title %q", got, title)
	}

	// Updating requires the current hash, and the document must keep its ID.
	updatedBody, err := withDashboardID(json.RawMessage(`{"version":5,"title":"`+title+`-v2","description":"updated","role":"","owner":"","tabs":[{"tabId":"default","name":"Default","panels":[]}]}`), dashboardID)
	if err != nil {
		t.Fatalf("withDashboardID: %v", err)
	}
	updated, err := c.UpdateDashboard(ctx, org, dashboardID, folder.FolderID, got.Hash, updatedBody)
	if err != nil {
		t.Fatalf("UpdateDashboard: %v", err)
	}
	if updated.Title() != title+"-v2" {
		t.Errorf("title after update = %q, want %q", updated.Title(), title+"-v2")
	}
	if updated.DashboardID() != dashboardID {
		t.Errorf("update created a new dashboard: ID went from %q to %q", dashboardID, updated.DashboardID())
	}
}

func TestIntegrationUserLifecycle(t *testing.T) {
	c := integrationClient(t)
	ctx := context.Background()
	org := c.DefaultOrgID()
	email := uniqueName("tf_it_user") + "@example.com"

	err := c.CreateUser(ctx, org, CreateUserAPI{
		Email:     email,
		FirstName: "Integration",
		LastName:  "Test",
		Password:  "Complexpass#123",
		Role:      "admin",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	t.Cleanup(func() {
		if err := c.DeleteUser(ctx, org, email); err != nil {
			t.Errorf("DeleteUser: %v", err)
		}
	})

	user, err := c.GetUser(ctx, org, email)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if user == nil {
		t.Fatal("GetUser returned nothing for a user that was just created")
	}
	if user.FirstName != "Integration" {
		t.Errorf("first_name = %q, want %q", user.FirstName, "Integration")
	}

	lastName := "Updated"
	if err := c.UpdateUser(ctx, org, email, UpdateUserAPI{LastName: &lastName}); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	user, err = c.GetUser(ctx, org, email)
	if err != nil {
		t.Fatalf("GetUser after update: %v", err)
	}
	if user.LastName != lastName {
		t.Errorf("last_name = %q, want %q", user.LastName, lastName)
	}
}

func TestIntegrationServiceAccountLifecycle(t *testing.T) {
	c := integrationClient(t)
	ctx := context.Background()
	org := c.DefaultOrgID()
	email := uniqueName("tf_it_svc") + "@example.com"

	created, err := c.CreateServiceAccount(ctx, org, ServiceAccountAPI{
		Email:     email,
		FirstName: "Integration",
		LastName:  "Service",
	})
	if err != nil {
		t.Fatalf("CreateServiceAccount: %v", err)
	}
	t.Cleanup(func() {
		if err := c.DeleteServiceAccount(ctx, org, email); err != nil {
			t.Errorf("DeleteServiceAccount: %v", err)
		}
	})
	if created.Token == "" {
		t.Error("CreateServiceAccount did not return an API token; the resource has nothing to store")
	}

	rotated, err := c.RotateServiceAccountToken(ctx, org, email)
	if err != nil {
		t.Fatalf("RotateServiceAccountToken: %v", err)
	}
	if rotated.Token == "" {
		t.Error("rotation returned an empty token")
	}
	if rotated.Token == created.Token {
		t.Error("rotation returned the same token, so the old one was never invalidated")
	}

	accounts, err := c.ListServiceAccounts(ctx, org)
	if err != nil {
		t.Fatalf("ListServiceAccounts: %v", err)
	}
	found := false
	for _, a := range accounts {
		if a.Email == email {
			found = true
		}
	}
	if !found {
		t.Errorf("ListServiceAccounts did not include %q", email)
	}
}

func TestIntegrationAlertLifecycle(t *testing.T) {
	c := integrationClient(t)
	ctx := context.Background()
	org := c.DefaultOrgID()

	streamName := uniqueName("tf_it_alert_stream")
	if err := c.CreateStream(ctx, org, "logs", streamName, CreateStreamAPI{}); err != nil {
		t.Fatalf("CreateStream: %v", err)
	}
	t.Cleanup(func() { _ = c.DeleteStream(ctx, org, "logs", streamName) })

	templateName := uniqueName("tf_it_template")
	err := c.CreateAlertTemplate(ctx, org, AlertTemplateAPI{
		Name:         templateName,
		Body:         `{"text":"{alert_name}"}`,
		TemplateType: "http",
	})
	if err != nil {
		t.Fatalf("CreateAlertTemplate: %v", err)
	}
	t.Cleanup(func() {
		if err := c.DeleteAlertTemplate(ctx, org, templateName); err != nil {
			t.Errorf("DeleteAlertTemplate: %v", err)
		}
	})

	destName := uniqueName("tf_it_dest")
	err = c.CreateAlertDestination(ctx, org, AlertDestinationAPI{
		Name:            destName,
		DestinationType: "http",
		URL:             "https://example.com/hook",
		Method:          "post",
		Template:        &templateName,
		Emails:          []string{},
	})
	if err != nil {
		t.Fatalf("CreateAlertDestination: %v", err)
	}
	t.Cleanup(func() {
		if err := c.DeleteAlertDestination(ctx, org, destName); err != nil {
			t.Errorf("DeleteAlertDestination: %v", err)
		}
	})

	dest, err := c.GetAlertDestination(ctx, org, destName)
	if err != nil {
		t.Fatalf("GetAlertDestination: %v", err)
	}
	if dest == nil || dest.Template == nil || *dest.Template != templateName {
		t.Fatalf("destination = %+v, want template %q", dest, templateName)
	}

	alertName := uniqueName("tf-it-alert")
	sql := fmt.Sprintf("SELECT count(*) AS total FROM %q WHERE level = 'error'", streamName)
	alertID, err := c.CreateAlert(ctx, org, "default", AlertAPI{
		Name:         alertName,
		StreamType:   "logs",
		StreamName:   streamName,
		Destinations: []string{destName},
		Enabled:      true,
		QueryCondition: AlertQueryConditionAPI{
			QueryType: "sql",
			SQL:       &sql,
		},
		TriggerCondition: AlertTriggerConditionAPI{
			Period:        15,
			Operator:      ">=",
			Threshold:     100,
			Frequency:     5,
			FrequencyType: "minutes",
			Silence:       60,
			AlignTime:     true,
		},
	})
	if err != nil {
		t.Fatalf("CreateAlert: %v", err)
	}
	if alertID == "" {
		t.Fatal("CreateAlert did not return an alert ID")
	}
	t.Cleanup(func() {
		if err := c.DeleteAlert(ctx, org, alertID); err != nil {
			t.Errorf("DeleteAlert: %v", err)
		}
	})

	alert, err := c.GetAlert(ctx, org, alertID)
	if err != nil {
		t.Fatalf("GetAlert: %v", err)
	}
	if alert == nil {
		t.Fatal("GetAlert returned nothing for an alert that was just created")
	}
	if alert.TriggerCondition.Threshold != 100 {
		t.Errorf("threshold = %d, want 100", alert.TriggerCondition.Threshold)
	}
	if alert.QueryCondition.QueryType != "sql" {
		t.Errorf("query type = %q, want sql", alert.QueryCondition.QueryType)
	}

	alert.TriggerCondition.Threshold = 250
	if err := c.UpdateAlert(ctx, org, alertID, *alert); err != nil {
		t.Fatalf("UpdateAlert: %v", err)
	}
	alert, err = c.GetAlert(ctx, org, alertID)
	if err != nil {
		t.Fatalf("GetAlert after update: %v", err)
	}
	if alert.TriggerCondition.Threshold != 250 {
		t.Errorf("threshold after update = %d, want 250", alert.TriggerCondition.Threshold)
	}

	found, err := c.FindAlertByName(ctx, org, "", alertName)
	if err != nil {
		t.Fatalf("FindAlertByName: %v", err)
	}
	if found == nil || found.AlertID != alertID {
		t.Errorf("FindAlertByName = %+v, want alert ID %q", found, alertID)
	}
}

func TestIntegrationOrganizationsAreReadable(t *testing.T) {
	c := integrationClient(t)
	ctx := context.Background()

	orgs, err := c.ListOrganizations(ctx)
	if err != nil {
		t.Fatalf("ListOrganizations: %v", err)
	}
	if len(orgs) == 0 {
		t.Fatal("ListOrganizations returned nothing; at least the default organization should exist")
	}

	org, err := c.GetOrganization(ctx, c.DefaultOrgID())
	if err != nil {
		t.Fatalf("GetOrganization: %v", err)
	}
	if org == nil {
		t.Fatalf("GetOrganization(%q) returned nothing", c.DefaultOrgID())
	}
}

func TestIntegrationUserRolesAreReadable(t *testing.T) {
	c := integrationClient(t)
	ctx := context.Background()

	roles, err := c.ListUserRoles(ctx, c.DefaultOrgID())
	if err != nil {
		t.Fatalf("ListUserRoles: %v", err)
	}
	if len(roles) == 0 {
		t.Fatal("ListUserRoles returned nothing; at least one built-in role should exist")
	}
	for _, r := range roles {
		if r.Value == "" {
			t.Errorf("role %+v has no value, so it cannot be used for openobserve_user.role", r)
		}
	}
}

// TestIntegrationDeleteIsIdempotent covers the delete paths returning success
// for objects that are already gone, which is what makes a Terraform destroy
// safe to retry.
func TestIntegrationDeleteIsIdempotent(t *testing.T) {
	c := integrationClient(t)
	ctx := context.Background()
	org := c.DefaultOrgID()
	missing := uniqueName("tf_it_missing")

	if err := c.DeleteStream(ctx, org, "logs", missing); err != nil {
		t.Errorf("DeleteStream on a missing stream: %v", err)
	}
	if err := c.DeleteAlertTemplate(ctx, org, missing); err != nil {
		t.Errorf("DeleteAlertTemplate on a missing template: %v", err)
	}
	if err := c.DeleteAlertDestination(ctx, org, missing); err != nil {
		t.Errorf("DeleteAlertDestination on a missing destination: %v", err)
	}
	if err := c.DeleteUser(ctx, org, missing+"@example.com"); err != nil {
		t.Errorf("DeleteUser on a missing user: %v", err)
	}
}

func containsStream(streams []StreamAPI, name string) bool {
	for _, s := range streams {
		if s.Name == name {
			return true
		}
	}
	return false
}

func ptr[T any](v T) *T { return &v }

func TestIntegrationSloLifecycle(t *testing.T) {
	c := integrationClient(t)
	ctx := context.Background()
	org := c.DefaultOrgID()

	streamName := uniqueName("tf_it_slo_stream")
	if err := c.CreateStream(ctx, org, "logs", streamName, CreateStreamAPI{}); err != nil {
		t.Fatalf("CreateStream: %v", err)
	}
	t.Cleanup(func() { _ = c.DeleteStream(ctx, org, "logs", streamName) })

	name := uniqueName("tf_it_slo")
	config, err := json.Marshal(map[string]any{
		"source": map[string]any{
			"mode": "single_query",
			"query": SloCountSingleQueryAPI{
				Stream:     streamName,
				StreamType: "logs",
				GoodExpr:   "code < 500",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshalling count source: %v", err)
	}

	sloID, err := c.CreateSlo(ctx, org, SloAPI{
		Name:        name,
		Description: "integration test",
		SliType:     "count",
		Config:      config,
		WindowSecs:  2592000,
		SliceSecs:   300,
		Target:      99.9,
		Enabled:     true,
	})
	if err != nil {
		t.Fatalf("CreateSlo: %v", err)
	}
	if sloID == "" {
		t.Fatal("CreateSlo did not return an SLO ID")
	}
	t.Cleanup(func() {
		if err := c.DeleteSlo(ctx, org, sloID); err != nil {
			t.Errorf("DeleteSlo: %v", err)
		}
	})

	slo, err := c.GetSlo(ctx, org, sloID)
	if err != nil {
		t.Fatalf("GetSlo: %v", err)
	}
	if slo == nil {
		t.Fatal("GetSlo returned nothing for an SLO that was just created")
	}
	if slo.SliType != "count" {
		t.Errorf("sli_type = %q, want count", slo.SliType)
	}
	if slo.Target != 99.9 {
		t.Errorf("target = %v, want 99.9", slo.Target)
	}
	// The adjacently tagged count source has to survive the round trip, since
	// that nesting is the easiest part of this shape to get wrong.
	var wrapper struct {
		Source struct {
			Mode  string                 `json:"mode"`
			Query SloCountSingleQueryAPI `json:"query"`
		} `json:"source"`
	}
	if err := json.Unmarshal(slo.Config, &wrapper); err != nil {
		t.Fatalf("decoding count source: %v (raw: %s)", err, slo.Config)
	}
	if wrapper.Source.Mode != "single_query" || wrapper.Source.Query.GoodExpr != "code < 500" {
		t.Errorf("count source round-tripped as %+v", wrapper.Source)
	}

	slo.Target = 99.5
	if err := c.UpdateSlo(ctx, org, sloID, *slo); err != nil {
		t.Fatalf("UpdateSlo: %v", err)
	}
	slo, err = c.GetSlo(ctx, org, sloID)
	if err != nil {
		t.Fatalf("GetSlo after update: %v", err)
	}
	if slo.Target != 99.5 {
		t.Errorf("target after update = %v, want 99.5", slo.Target)
	}

	// Enablement is a separate endpoint rather than a field on the update body.
	if err := c.SetSloEnabled(ctx, org, sloID, false); err != nil {
		t.Fatalf("SetSloEnabled: %v", err)
	}
	slo, err = c.GetSlo(ctx, org, sloID)
	if err != nil {
		t.Fatalf("GetSlo after pause: %v", err)
	}
	if slo.Enabled {
		t.Error("SLO still reports enabled after being paused")
	}

	found, err := c.FindSloByName(ctx, org, "", name)
	if err != nil {
		t.Fatalf("FindSloByName: %v", err)
	}
	if found == nil || found.ID != sloID {
		t.Errorf("FindSloByName = %+v, want SLO ID %q", found, sloID)
	}
}

func TestIntegrationSloTimeSliceAndGrouping(t *testing.T) {
	c := integrationClient(t)
	ctx := context.Background()
	org := c.DefaultOrgID()

	streamName := uniqueName("tf_it_slo_ts_stream")
	if err := c.CreateStream(ctx, org, "logs", streamName, CreateStreamAPI{}); err != nil {
		t.Fatalf("CreateStream: %v", err)
	}
	t.Cleanup(func() { _ = c.DeleteStream(ctx, org, "logs", streamName) })

	config, err := json.Marshal(SloTimeSliceConfigAPI{
		Stream:        streamName,
		StreamType:    "logs",
		QueryLanguage: "sql",
		Query:         fmt.Sprintf("SELECT count(_timestamp) AS zo_slo_value FROM %q", streamName),
		Comparator:    ">",
		Threshold:     0,
	})
	if err != nil {
		t.Fatalf("marshalling time-slice config: %v", err)
	}

	sloID, err := c.CreateSlo(ctx, org, SloAPI{
		Name:       uniqueName("tf_it_slo_ts"),
		SliType:    "time_slice",
		Config:     config,
		GroupBy:    []string{"service"},
		WindowSecs: 604800,
		SliceSecs:  300,
		Target:     99,
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("CreateSlo (time_slice): %v", err)
	}
	t.Cleanup(func() { _ = c.DeleteSlo(ctx, org, sloID) })

	slo, err := c.GetSlo(ctx, org, sloID)
	if err != nil {
		t.Fatalf("GetSlo: %v", err)
	}
	if slo == nil {
		t.Fatal("GetSlo returned nothing")
	}
	if len(slo.GroupBy) != 1 || slo.GroupBy[0] != "service" {
		t.Errorf("group_by = %v, want [service]", slo.GroupBy)
	}
	var ts SloTimeSliceConfigAPI
	if err := json.Unmarshal(slo.Config, &ts); err != nil {
		t.Fatalf("decoding time-slice config: %v (raw: %s)", err, slo.Config)
	}
	if ts.Comparator != ">" || ts.QueryLanguage != "sql" {
		t.Errorf("time-slice config round-tripped as %+v", ts)
	}
}

// integrationDestination creates a template and destination for tests that need
// somewhere to send to. OpenObserve rejects an alert that has neither a
// destination nor a workflow.
func integrationDestination(t *testing.T, c *Client, ctx context.Context, org string) string {
	t.Helper()

	templateName := uniqueName("tf_it_tmpl")
	if err := c.CreateAlertTemplate(ctx, org, AlertTemplateAPI{
		Name:         templateName,
		Body:         `{"text":"{alert_name}"}`,
		TemplateType: "http",
	}); err != nil {
		t.Fatalf("CreateAlertTemplate: %v", err)
	}
	t.Cleanup(func() { _ = c.DeleteAlertTemplate(ctx, org, templateName) })

	destName := uniqueName("tf_it_dest")
	if err := c.CreateAlertDestination(ctx, org, AlertDestinationAPI{
		Name:            destName,
		DestinationType: "http",
		URL:             "https://example.com/hook",
		Method:          "post",
		Template:        &templateName,
		Emails:          []string{},
	}); err != nil {
		t.Fatalf("CreateAlertDestination: %v", err)
	}
	t.Cleanup(func() { _ = c.DeleteAlertDestination(ctx, org, destName) })

	return destName
}

// TestIntegrationAlertVariants covers the query families and the simple vs
// multi-alert distinction, which are the parts of the alert schema most likely
// to drift from the server.
func TestIntegrationAlertVariants(t *testing.T) {
	c := integrationClient(t)
	ctx := context.Background()
	org := c.DefaultOrgID()

	streamName := uniqueName("tf_it_alert_variants")
	if err := c.CreateStream(ctx, org, "logs", streamName, CreateStreamAPI{}); err != nil {
		t.Fatalf("CreateStream: %v", err)
	}
	t.Cleanup(func() { _ = c.DeleteStream(ctx, org, "logs", streamName) })

	destName := integrationDestination(t, c, ctx, org)
	warning := 50.0
	sql := fmt.Sprintf("SELECT count(_timestamp) AS total FROM %q", streamName)
	promql := "sum by (pod) (rate(errors_total[5m]))"

	cases := []struct {
		name  string
		query AlertQueryConditionAPI
		check func(t *testing.T, got *AlertQueryConditionAPI)
	}{
		{
			name:  "sql",
			query: AlertQueryConditionAPI{QueryType: "sql", SQL: &sql},
			check: func(t *testing.T, got *AlertQueryConditionAPI) {
				if got.SQL == nil || *got.SQL != sql {
					t.Errorf("sql = %v, want it preserved", got.SQL)
				}
			},
		},
		{
			name: "aggregation multi alert",
			query: AlertQueryConditionAPI{
				QueryType: "custom",
				Aggregation: &AlertAggregationAPI{
					GroupBy:      []string{"service"},
					Function:     "p95",
					MultiAlert:   true,
					WarningValue: &warning,
					Having: AlertConditionAPI{
						Column:   "duration_ms",
						Operator: ">",
						Value:    json.RawMessage("100"),
					},
				},
			},
			check: func(t *testing.T, got *AlertQueryConditionAPI) {
				if got.Aggregation == nil {
					t.Fatal("aggregation was dropped")
				}
				if !got.Aggregation.MultiAlert {
					t.Error("multi_alert did not survive the round trip")
				}
				if got.Aggregation.WarningValue == nil {
					t.Error("warning_value did not survive the round trip")
				}
			},
		},
		{
			name: "promql multi alert",
			query: AlertQueryConditionAPI{
				QueryType:        "promql",
				PromQL:           &promql,
				PromQLMultiAlert: true,
				PromQLCondition: &AlertConditionAPI{
					Column:   "value",
					Operator: ">",
					Value:    json.RawMessage("10"),
				},
			},
			check: func(t *testing.T, got *AlertQueryConditionAPI) {
				if !got.PromQLMultiAlert {
					t.Error("promql_multi_alert did not survive the round trip")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			alertID, err := c.CreateAlert(ctx, org, "default", AlertAPI{
				Name:           uniqueName("tf-it-variant"),
				StreamType:     "logs",
				StreamName:     streamName,
				Destinations:   []string{destName},
				Enabled:        true,
				QueryCondition: tc.query,
				TriggerCondition: AlertTriggerConditionAPI{
					Period:        10,
					Operator:      ">=",
					Threshold:     1,
					Frequency:     5,
					FrequencyType: "minutes",
					AlignTime:     true,
				},
			})
			if err != nil {
				t.Fatalf("CreateAlert: %v", err)
			}
			t.Cleanup(func() { _ = c.DeleteAlert(ctx, org, alertID) })

			got, err := c.GetAlert(ctx, org, alertID)
			if err != nil {
				t.Fatalf("GetAlert: %v", err)
			}
			if got == nil {
				t.Fatal("GetAlert returned nothing")
			}
			if got.QueryCondition.QueryType != tc.query.QueryType {
				t.Errorf("query type = %q, want %q", got.QueryCondition.QueryType, tc.query.QueryType)
			}
			tc.check(t, &got.QueryCondition)
		})
	}
}

// TestIntegrationSloAlert covers an alert that reads a precomputed objective
// rather than running its own query.
func TestIntegrationSloAlert(t *testing.T) {
	c := integrationClient(t)
	ctx := context.Background()
	org := c.DefaultOrgID()

	streamName := uniqueName("tf_it_slo_alert_stream")
	if err := c.CreateStream(ctx, org, "logs", streamName, CreateStreamAPI{}); err != nil {
		t.Fatalf("CreateStream: %v", err)
	}
	t.Cleanup(func() { _ = c.DeleteStream(ctx, org, "logs", streamName) })

	config, err := json.Marshal(map[string]any{
		"source": map[string]any{
			"mode": "single_query",
			"query": SloCountSingleQueryAPI{
				Stream:     streamName,
				StreamType: "logs",
				GoodExpr:   "code < 500",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshalling count source: %v", err)
	}
	sloID, err := c.CreateSlo(ctx, org, SloAPI{
		Name:       uniqueName("tf_it_slo_for_alert"),
		SliType:    "count",
		Config:     config,
		WindowSecs: 2592000,
		SliceSecs:  300,
		Target:     99.9,
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("CreateSlo: %v", err)
	}
	t.Cleanup(func() { _ = c.DeleteSlo(ctx, org, sloID) })

	warning := 75.0
	destName := integrationDestination(t, c, ctx, org)
	alertID, err := c.CreateAlert(ctx, org, "default", AlertAPI{
		Name:         uniqueName("tf-it-slo-alert"),
		StreamType:   "logs",
		StreamName:   streamName,
		Destinations: []string{destName},
		Enabled:      true,
		QueryCondition: AlertQueryConditionAPI{
			QueryType: "slo",
			SloCondition: &AlertSloConditionAPI{
				SloID:    sloID,
				Kind:     "error_budget",
				Operator: ">",
				Critical: 90,
				Warning:  &warning,
			},
		},
		// No count gate: an SLO alert is thresholded by its slo_condition, and
		// the server rejects a non-default trigger threshold rather than
		// ignoring it. The operator still has to be a valid variant.
		TriggerCondition: AlertTriggerConditionAPI{
			Period:        5,
			Operator:      "=",
			Frequency:     5,
			FrequencyType: "minutes",
			AlignTime:     true,
		},
	})
	if err != nil {
		t.Fatalf("CreateAlert (slo): %v", err)
	}
	t.Cleanup(func() { _ = c.DeleteAlert(ctx, org, alertID) })

	got, err := c.GetAlert(ctx, org, alertID)
	if err != nil {
		t.Fatalf("GetAlert: %v", err)
	}
	if got == nil {
		t.Fatal("GetAlert returned nothing")
	}
	if got.QueryCondition.SloCondition == nil {
		t.Fatal("slo_condition was dropped")
	}
	if got.QueryCondition.SloCondition.SloID != sloID {
		t.Errorf("slo_id = %q, want %q", got.QueryCondition.SloCondition.SloID, sloID)
	}
	if got.QueryCondition.SloCondition.Kind != "error_budget" {
		t.Errorf("kind = %q, want error_budget", got.QueryCondition.SloCondition.Kind)
	}
	if got.QueryCondition.SloCondition.Warning == nil {
		t.Error("warning threshold did not survive the round trip")
	}
}

// TestIntegrationCompositeAlertLifecycle covers the composite path end to end,
// including the two behaviours that only show up against a live server: the
// expression is stored canonicalized rather than as sent, and a child cannot be
// deleted while a composite still references it.
func TestIntegrationCompositeAlertLifecycle(t *testing.T) {
	c := integrationClient(t)
	ctx := context.Background()
	org := c.DefaultOrgID()

	streamName := uniqueName("tf_it_comp_stream")
	if err := c.CreateStream(ctx, org, "logs", streamName, CreateStreamAPI{}); err != nil {
		t.Fatalf("CreateStream: %v", err)
	}
	t.Cleanup(func() { _ = c.DeleteStream(ctx, org, "logs", streamName) })

	templateName := uniqueName("tf_it_comp_template")
	err := c.CreateAlertTemplate(ctx, org, AlertTemplateAPI{
		Name:         templateName,
		Body:         `{"text":"{alert_name}"}`,
		TemplateType: "http",
	})
	if err != nil {
		t.Fatalf("CreateAlertTemplate: %v", err)
	}
	t.Cleanup(func() { _ = c.DeleteAlertTemplate(ctx, org, templateName) })

	destName := uniqueName("tf_it_comp_dest")
	err = c.CreateAlertDestination(ctx, org, AlertDestinationAPI{
		Name:            destName,
		DestinationType: "http",
		URL:             "https://example.com/hook",
		Method:          "post",
		Template:        &templateName,
		Emails:          []string{},
	})
	if err != nil {
		t.Fatalf("CreateAlertDestination: %v", err)
	}
	t.Cleanup(func() { _ = c.DeleteAlertDestination(ctx, org, destName) })

	// A composite requires its folder to exist; unlike an alert it will not
	// create the default one on demand.
	folder, err := c.CreateFolder(ctx, org, "alerts", FolderAPI{Name: uniqueName("tf_it_comp_folder")})
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	folderID := folder.FolderID
	t.Cleanup(func() { _ = c.DeleteFolder(ctx, org, "alerts", folderID) })

	newChild := func(suffix, predicate string) string {
		t.Helper()
		sql := fmt.Sprintf("SELECT count(*) AS total FROM %q WHERE %s", streamName, predicate)
		id, err := c.CreateAlert(ctx, org, folderID, AlertAPI{
			Name:         uniqueName("tf-it-comp-" + suffix),
			StreamType:   "logs",
			StreamName:   streamName,
			Destinations: []string{destName},
			Enabled:      true,
			QueryCondition: AlertQueryConditionAPI{
				QueryType: "sql",
				SQL:       &sql,
			},
			TriggerCondition: AlertTriggerConditionAPI{
				Period: 10, Operator: ">=", Threshold: 1,
				Frequency: 5, FrequencyType: "minutes", AlignTime: true,
			},
		})
		if err != nil {
			t.Fatalf("CreateAlert(%s): %v", suffix, err)
		}
		return id
	}

	childA := newChild("a", "level = 'error'")
	childB := newChild("b", "duration_ms > 1000")

	// Written without the outer parentheses the server canonicalizes to.
	expression := fmt.Sprintf("{%s} && {%s}", childA, childB)
	compositeName := uniqueName("tf-it-composite")
	compositeID, err := c.CreateCompositeAlert(ctx, org, folderID, CompositeAlertWriteAPI{
		CompositeCondition: CompositeConditionAPI{
			Expression:            expression,
			WarningCountsAsFiring: true,
			StaleChildPolicy:      "treat_as_false",
		},
		Name:             compositeName,
		Description:      "integration test composite",
		Enabled:          true,
		Destinations:     []string{destName},
		TriggerCondition: CompositeTriggerAPI{Silence: 30},
	})
	if err != nil {
		t.Fatalf("CreateCompositeAlert: %v", err)
	}
	if compositeID == "" {
		t.Fatal("CreateCompositeAlert did not return an alert ID")
	}
	// Registered before the children so it is torn down first; the children
	// cannot be deleted while it references them.
	t.Cleanup(func() {
		if err := c.DeleteAlert(ctx, org, childB); err != nil {
			t.Errorf("DeleteAlert(childB): %v", err)
		}
	})
	t.Cleanup(func() {
		if err := c.DeleteAlert(ctx, org, childA); err != nil {
			t.Errorf("DeleteAlert(childA): %v", err)
		}
	})
	t.Cleanup(func() {
		if err := c.DeleteAlert(ctx, org, compositeID); err != nil {
			t.Errorf("DeleteAlert(composite): %v", err)
		}
	})

	composite, err := c.GetCompositeAlert(ctx, org, compositeID)
	if err != nil {
		t.Fatalf("GetCompositeAlert: %v", err)
	}
	if composite == nil {
		t.Fatal("GetCompositeAlert returned nothing for a composite that was just created")
	}
	if composite.AlertType != "composite" {
		t.Errorf("alert_type = %q, want composite", composite.AlertType)
	}
	if composite.TriggerCondition.Silence != 30 {
		t.Errorf("silence = %d, want 30", composite.TriggerCondition.Silence)
	}
	if composite.CompositeCondition.StaleChildPolicy != "treat_as_false" {
		t.Errorf("stale_child_policy = %q, want treat_as_false", composite.CompositeCondition.StaleChildPolicy)
	}
	if len(composite.Children) != 2 {
		t.Errorf("got %d children, want 2", len(composite.Children))
	}

	// The server stores its own spelling. The provider relies on comparing the
	// two as expressions, so assert both halves of that.
	if composite.CompositeCondition.Expression == expression {
		t.Error("expected the server to canonicalize the expression it was sent")
	}
	if !compositeExpressionsEquivalent(expression, composite.CompositeCondition.Expression) {
		t.Errorf("stored expression %q is not equivalent to %q",
			composite.CompositeCondition.Expression, expression)
	}

	// A child cannot be deleted out from under a composite that names it.
	err = c.DeleteAlert(ctx, org, childA)
	if err == nil {
		t.Error("deleting a referenced child succeeded; expected the server to refuse it")
	} else if detail := compositeErrorDetail(err); !strings.Contains(detail, "Destroy the composite first") {
		t.Errorf("child_referenced error was not annotated: %s", detail)
	}

	refs, err := c.ListCompositeReferences(ctx, org, childA)
	if err != nil {
		t.Fatalf("ListCompositeReferences: %v", err)
	}
	if refs == nil || len(refs.References) != 1 || refs.References[0].AlertID != compositeID {
		t.Errorf("references = %+v, want exactly the composite %q", refs, compositeID)
	}

	// Update: swap the operator and disable it.
	updated := fmt.Sprintf("{%s} || {%s}", childA, childB)
	err = c.UpdateCompositeAlert(ctx, org, compositeID, CompositeAlertWriteAPI{
		CompositeCondition: CompositeConditionAPI{
			Expression:            updated,
			WarningCountsAsFiring: false,
			StaleChildPolicy:      "use_last_state",
		},
		Name:             compositeName,
		Description:      "integration test composite",
		Enabled:          false,
		Destinations:     []string{destName},
		TriggerCondition: CompositeTriggerAPI{Silence: 45},
	})
	if err != nil {
		t.Fatalf("UpdateCompositeAlert: %v", err)
	}

	composite, err = c.GetCompositeAlert(ctx, org, compositeID)
	if err != nil {
		t.Fatalf("GetCompositeAlert after update: %v", err)
	}
	if composite.Enabled {
		t.Error("composite is still enabled after being updated to disabled")
	}
	if composite.TriggerCondition.Silence != 45 {
		t.Errorf("silence after update = %d, want 45", composite.TriggerCondition.Silence)
	}
	if !compositeExpressionsEquivalent(updated, composite.CompositeCondition.Expression) {
		t.Errorf("expression after update = %q, want an equivalent of %q",
			composite.CompositeCondition.Expression, updated)
	}
	if composite.CompositeCondition.WarningCountsAsFiring {
		t.Error("warning_counts_as_firing is still true after being updated to false")
	}

	// The validate endpoint reports the canonical form without persisting.
	validation, err := c.ValidateCompositeExpression(ctx, org, CompositeConditionAPI{
		Expression:            expression,
		WarningCountsAsFiring: true,
		StaleChildPolicy:      "use_last_state",
	}, compositeID)
	if err != nil {
		t.Fatalf("ValidateCompositeExpression: %v", err)
	}
	if !validation.Valid {
		t.Error("a valid expression was reported invalid")
	}
	if validation.CanonicalExpression == nil {
		t.Fatal("validate returned no canonical expression")
	}
	if got, want := *validation.CanonicalExpression, canonicalCompositeExpressionOf(t, expression); got != want {
		t.Errorf("server canonical form = %q, provider computed %q; the provider's parser has drifted", got, want)
	}

	// Reading a composite through the ordinary alert accessor should not
	// silently produce a half-populated alert.
	if _, err := c.GetCompositeAlert(ctx, org, childA); err == nil {
		t.Error("GetCompositeAlert on an ordinary alert should report the type mismatch")
	}
}

// canonicalCompositeExpressionOf is the provider's own canonical form, used to
// assert it still matches the server's.
func canonicalCompositeExpressionOf(t *testing.T, expression string) string {
	t.Helper()
	canonical, _, err := validateCompositeExpression(expression)
	if err != nil {
		t.Fatalf("validateCompositeExpression(%q): %v", expression, err)
	}
	return canonical
}

// TestIntegrationStreamNameNormalizationDelete is regression cover for
// openobserve/terraform-provider-openobserve#1.
//
// OpenObserve normalizes stream names on create, schema, settings and update,
// replacing every character outside [a-zA-Z0-9_:] with an underscore. Its
// delete handler does not normalize. So a stream created as `tf-it-x` is stored
// as `tf_it_x`, reads back fine under either spelling, and answers 404 when
// deleted under the name the caller used.
//
// The provider tolerated that 404 as "already gone", which turned a failed
// delete into a destroy that reported success and left the stream and its data
// in place. This asserts the stream is really gone afterwards, because the
// delete call returning nil is exactly what the bug already did.
func TestIntegrationStreamNameNormalizationDelete(t *testing.T) {
	c := integrationClient(t)
	ctx := context.Background()
	org := c.DefaultOrgID()

	// Hyphens are the common case: legal in HCL, normalized by the server.
	requested := strings.ReplaceAll(uniqueName("tf-it-normalized"), "_", "-")
	if !strings.Contains(requested, "-") {
		t.Fatalf("test name %q needs a character the server normalizes", requested)
	}

	if err := c.CreateStream(ctx, org, "logs", requested, CreateStreamAPI{}); err != nil {
		t.Fatalf("CreateStream: %v", err)
	}

	stream, err := c.GetStream(ctx, org, "logs", requested)
	if err != nil {
		t.Fatalf("GetStream: %v", err)
	}
	if stream == nil {
		t.Fatal("GetStream returned nothing for a stream that was just created")
	}
	stored := stream.Name
	t.Cleanup(func() { _ = c.DeleteStream(ctx, org, "logs", stored) })

	if stored == requested {
		t.Skipf("this server did not normalize %q, so there is nothing to regress against "+
			"(ZO_SKIP_FORMATTING_STREAM_NAME may be set)", requested)
	}

	if err := c.DeleteStream(ctx, org, "logs", requested); err != nil {
		t.Fatalf("DeleteStream by the requested name: %v", err)
	}

	// The delete reporting success is not enough: that is what the bug did.
	gone, err := c.GetStream(ctx, org, "logs", stored)
	if err != nil {
		t.Fatalf("GetStream after delete: %v", err)
	}
	if gone != nil {
		t.Errorf("stream %q (requested as %q) still exists after a delete that reported success",
			stored, requested)
	}

	// A repeated delete stays successful, so a retried destroy does not fail.
	if err := c.DeleteStream(ctx, org, "logs", requested); err != nil {
		t.Errorf("second DeleteStream: %v", err)
	}
}

// TestIntegrationPipelineLifecycle covers functions, pipeline destinations and
// pipelines together, because the interesting behaviour is in how they depend
// on each other rather than in any one of them.
func TestIntegrationPipelineLifecycle(t *testing.T) {
	c := integrationClient(t)
	ctx := context.Background()
	org := c.DefaultOrgID()

	srcStream := uniqueName("tf_it_pipe_src")
	dstStream := uniqueName("tf_it_pipe_dst")
	for _, s := range []string{srcStream, dstStream} {
		if err := c.CreateStream(ctx, org, "logs", s, CreateStreamAPI{}); err != nil {
			t.Fatalf("CreateStream(%s): %v", s, err)
		}
		t.Cleanup(func() { _ = c.DeleteStream(ctx, org, "logs", s) })
	}

	// --- function ---------------------------------------------------------
	vrl := int64(0)
	fnName := uniqueName("tf_it_fn")
	body := `.processed = true`
	if err := c.CreateFunction(ctx, org, FunctionAPI{
		Name: fnName, Function: body, Params: "row", TransType: &vrl,
	}); err != nil {
		t.Fatalf("CreateFunction: %v", err)
	}

	fn, err := c.GetFunction(ctx, org, fnName)
	if err != nil {
		t.Fatalf("GetFunction: %v", err)
	}
	if fn == nil {
		t.Fatal("GetFunction returned nothing for a function that was just created")
	}
	if functionLanguage(fn.TransType) != "vrl" {
		t.Errorf("language = %q, want vrl", functionLanguage(fn.TransType))
	}
	// A VRL program's value is its last expression, so the server appends a
	// trailing `.` to make the transform return the record.
	if fn.Function == body {
		t.Error("expected the server to append a trailing return expression to the VRL body")
	}
	if !vrlBodiesEquivalent(body, fn.Function) {
		t.Errorf("stored body %q is not equivalent to %q", fn.Function, body)
	}

	// Creating the same name again is refused rather than overwriting.
	err = c.CreateFunction(ctx, org, FunctionAPI{Name: fnName, Function: body, Params: "row", TransType: &vrl})
	if !isFunctionAlreadyExists(err) {
		t.Errorf("re-creating a function gave %v, want the already-exists refusal", err)
	}

	// --- pipeline destination --------------------------------------------
	destName := uniqueName("tf_it_pipe_dest")
	if err := c.CreatePipelineDestination(ctx, org, PipelineDestinationAPI{
		Name: destName, URL: "https://example.com/sink", Method: "post",
	}); err != nil {
		t.Fatalf("CreatePipelineDestination: %v", err)
	}
	dest, err := c.GetPipelineDestination(ctx, org, destName)
	if err != nil {
		t.Fatalf("GetPipelineDestination: %v", err)
	}
	if dest == nil || dest.URL != "https://example.com/sink" {
		t.Fatalf("destination = %+v, want the configured URL", dest)
	}

	// --- pipeline ---------------------------------------------------------
	after := true
	pipelineName := uniqueName("tf_it_pipeline")
	err = c.CreatePipeline(ctx, org, PipelineAPI{
		Name:    pipelineName,
		Enabled: true,
		Source: PipelineSourceAPI{
			SourceType: "realtime", OrgID: org, StreamName: srcStream, StreamType: "logs",
		},
		Nodes: []PipelineNodeAPI{
			{ID: "in", IOType: "input", Position: PipelinePositionAPI{},
				Data: PipelineNodeDataAPI{NodeType: "stream", OrgID: org, StreamName: srcStream, StreamType: "logs"}},
			{ID: "fn", IOType: "default", Position: PipelinePositionAPI{X: 250},
				Data: PipelineNodeDataAPI{NodeType: "function", Name: fnName, AfterFlatten: &after}},
			{ID: "out", IOType: "output", Position: PipelinePositionAPI{X: 500},
				Data: PipelineNodeDataAPI{NodeType: "stream", OrgID: org, StreamName: dstStream, StreamType: "logs"}},
		},
		Edges: []PipelineEdgeAPI{
			{ID: "ein-fn", Source: "in", Target: "fn"},
			{ID: "efn-out", Source: "fn", Target: "out"},
		},
	})
	if err != nil {
		t.Fatalf("CreatePipeline: %v", err)
	}

	found, err := c.FindPipelineByName(ctx, org, pipelineName)
	if err != nil {
		t.Fatalf("FindPipelineByName: %v", err)
	}
	if found == nil {
		t.Fatal("FindPipelineByName returned nothing for a pipeline that was just created")
	}
	pipelineID := found.ID

	// Registered before the function and destination so the pipeline is torn
	// down first: neither can be deleted while it references them.
	t.Cleanup(func() { _ = c.DeleteFunction(ctx, org, fnName) })
	t.Cleanup(func() { _ = c.DeletePipelineDestination(ctx, org, destName) })
	t.Cleanup(func() {
		if err := c.DeletePipeline(ctx, org, pipelineID); err != nil {
			t.Errorf("DeletePipeline: %v", err)
		}
	})

	pipeline, err := c.GetPipeline(ctx, org, pipelineID)
	if err != nil {
		t.Fatalf("GetPipeline: %v", err)
	}
	if pipeline == nil {
		t.Fatal("GetPipeline returned nothing")
	}
	if len(pipeline.Nodes) != 3 || len(pipeline.Edges) != 2 {
		t.Errorf("graph has %d nodes and %d edges, want 3 and 2", len(pipeline.Nodes), len(pipeline.Edges))
	}

	// This is the dependency the whole design turns on: a function in use
	// cannot be deleted, so Terraform has to destroy the pipeline first.
	err = c.DeleteFunction(ctx, org, fnName)
	if err == nil {
		t.Error("deleting a function a pipeline uses succeeded; expected the server to refuse it")
	} else if detail := pipelineErrorDetail(err); !strings.Contains(detail, "Destroy the pipeline first") {
		t.Errorf("in-use function error was not annotated: %s", detail)
	}

	deps, err := c.ListFunctionDependencies(ctx, org, fnName)
	if err != nil {
		t.Fatalf("ListFunctionDependencies: %v", err)
	}
	if len(deps) != 1 || deps[0].ID != pipelineID {
		t.Errorf("dependencies = %+v, want exactly the pipeline %q", deps, pipelineID)
	}

	// The same holds for a destination, through a different code path.
	shipStream := uniqueName("tf_it_pipe_ship")
	if err := c.CreateStream(ctx, org, "logs", shipStream, CreateStreamAPI{}); err != nil {
		t.Fatalf("CreateStream(ship): %v", err)
	}
	t.Cleanup(func() { _ = c.DeleteStream(ctx, org, "logs", shipStream) })

	shipName := uniqueName("tf_it_ship")
	err = c.CreatePipeline(ctx, org, PipelineAPI{
		Name:    shipName,
		Enabled: true,
		Source: PipelineSourceAPI{
			SourceType: "realtime", OrgID: org, StreamName: shipStream, StreamType: "logs",
		},
		Nodes: []PipelineNodeAPI{
			{ID: "in", IOType: "input", Position: PipelinePositionAPI{},
				Data: PipelineNodeDataAPI{NodeType: "stream", OrgID: org, StreamName: shipStream, StreamType: "logs"}},
			{ID: "out", IOType: "output", Position: PipelinePositionAPI{X: 250},
				Data: PipelineNodeDataAPI{NodeType: "remote_stream", OrgID: org, DestinationName: destName}},
		},
		Edges: []PipelineEdgeAPI{{ID: "ein-out", Source: "in", Target: "out"}},
	})
	if err != nil {
		t.Fatalf("CreatePipeline(ship): %v", err)
	}
	shipFound, err := c.FindPipelineByName(ctx, org, shipName)
	if err != nil || shipFound == nil {
		t.Fatalf("FindPipelineByName(ship): %v", err)
	}
	t.Cleanup(func() { _ = c.DeletePipeline(ctx, org, shipFound.ID) })

	err = c.DeletePipelineDestination(ctx, org, destName)
	if err == nil {
		t.Error("deleting a destination a pipeline uses succeeded; expected the server to refuse it")
	} else if detail := pipelineErrorDetail(err); !strings.Contains(detail, "Destroy the pipeline first") {
		t.Errorf("in-use destination error was not annotated: %s", detail)
	}

	// A stream can source only one realtime pipeline.
	err = c.CreatePipeline(ctx, org, PipelineAPI{
		Name:    uniqueName("tf_it_dup"),
		Enabled: true,
		Source: PipelineSourceAPI{
			SourceType: "realtime", OrgID: org, StreamName: srcStream, StreamType: "logs",
		},
		Nodes: []PipelineNodeAPI{
			{ID: "in", IOType: "input", Position: PipelinePositionAPI{},
				Data: PipelineNodeDataAPI{NodeType: "stream", OrgID: org, StreamName: srcStream, StreamType: "logs"}},
			{ID: "out", IOType: "output", Position: PipelinePositionAPI{X: 250},
				Data: PipelineNodeDataAPI{NodeType: "stream", OrgID: org, StreamName: dstStream, StreamType: "logs"}},
		},
		Edges: []PipelineEdgeAPI{{ID: "ein-out", Source: "in", Target: "out"}},
	})
	if err == nil {
		t.Error("a second realtime pipeline on the same source stream succeeded; expected a refusal")
	} else if detail := pipelineErrorDetail(err); !strings.Contains(detail, "only one realtime pipeline") {
		t.Errorf("duplicate-source error was not annotated: %s", detail)
	}

	// Pausing is its own endpoint rather than a field on the update body.
	if err := c.SetPipelineEnabled(ctx, org, pipelineID, false); err != nil {
		t.Fatalf("SetPipelineEnabled: %v", err)
	}
	paused, err := c.GetPipeline(ctx, org, pipelineID)
	if err != nil {
		t.Fatalf("GetPipeline after disable: %v", err)
	}
	if paused.Enabled {
		t.Error("pipeline is still enabled after being paused")
	}
}

// TestIntegrationFunctionLanguageCasing guards the casing trap in the function
// wire format. Transform is serialized with rename_all = "camelCase", so the
// snake_case spellings are not rejected, they are silently ignored and the
// field falls back to its default. Posting `trans_type: 1` for a JavaScript
// function stores a VRL function and then fails to compile the body as VRL.
func TestIntegrationFunctionLanguageCasing(t *testing.T) {
	c := integrationClient(t)
	ctx := context.Background()
	org := c.DefaultOrgID()

	js := int64(1)
	name := uniqueName("tf_it_js")
	if err := c.CreateFunction(ctx, org, FunctionAPI{
		Name:      name,
		Function:  "function process(row){ row.seen = true; return row }",
		Params:    "row",
		TransType: &js,
	}); err != nil {
		t.Fatalf("CreateFunction(js): %v", err)
	}
	t.Cleanup(func() { _ = c.DeleteFunction(ctx, org, name) })

	fn, err := c.GetFunction(ctx, org, name)
	if err != nil {
		t.Fatalf("GetFunction: %v", err)
	}
	if fn == nil {
		t.Fatal("GetFunction returned nothing")
	}
	if got := functionLanguage(fn.TransType); got != "js" {
		t.Errorf("language = %q, want js. The transType field is being dropped on the wire, which "+
			"silently stores a VRL function instead.", got)
	}
}

// TestIntegrationIngestionTokenLifecycle covers the token family, including the
// part that shapes the resource: there is no delete endpoint, so a destroy has
// to disable rather than remove.
func TestIntegrationIngestionTokenLifecycle(t *testing.T) {
	c := integrationClient(t)
	ctx := context.Background()
	org := c.DefaultOrgID()

	name := uniqueName("tf_it_token")
	desc := "integration test"
	created, err := c.CreateIngestionToken(ctx, org, CreateIngestionTokenAPI{Name: name, Description: &desc})
	if err != nil {
		t.Fatalf("CreateIngestionToken: %v", err)
	}
	t.Cleanup(func() { _ = c.SetIngestionTokenEnabled(ctx, org, name, false) })

	if created.Token == "" {
		t.Error("create returned no token value; the secret is only available here and in the listing")
	}
	if !created.Enabled {
		t.Error("a new token should be enabled")
	}

	got, err := c.GetIngestionToken(ctx, org, name)
	if err != nil {
		t.Fatalf("GetIngestionToken: %v", err)
	}
	if got == nil {
		t.Fatal("GetIngestionToken returned nothing for a token that was just created")
	}
	if got.Token != created.Token {
		t.Error("the listing reports a different token value than create did")
	}
	if got.Description != desc {
		t.Errorf("description = %q, want %q", got.Description, desc)
	}

	// Disabling is the only mutation, and the closest thing to deletion the
	// API offers.
	if err := c.SetIngestionTokenEnabled(ctx, org, name, false); err != nil {
		t.Fatalf("SetIngestionTokenEnabled(false): %v", err)
	}
	got, err = c.GetIngestionToken(ctx, org, name)
	if err != nil {
		t.Fatalf("GetIngestionToken after disable: %v", err)
	}
	if got.Enabled {
		t.Error("token is still enabled after being disabled")
	}

	// A token that does not exist reads as absent rather than erroring.
	missing, err := c.GetIngestionToken(ctx, org, uniqueName("tf_it_absent"))
	if err != nil {
		t.Fatalf("GetIngestionToken for an absent name: %v", err)
	}
	if missing != nil {
		t.Error("an absent token should read as nil")
	}
}

// TestIntegrationAlertPendingPeriod covers the field added in OpenObserve's
// pending-period change, including the one combination the server refuses.
func TestIntegrationAlertPendingPeriod(t *testing.T) {
	c := integrationClient(t)
	ctx := context.Background()
	org := c.DefaultOrgID()

	stream := uniqueName("tf_it_pp_stream")
	if err := c.CreateStream(ctx, org, "logs", stream, CreateStreamAPI{}); err != nil {
		t.Fatalf("CreateStream: %v", err)
	}
	t.Cleanup(func() { _ = c.DeleteStream(ctx, org, "logs", stream) })

	tmpl := uniqueName("tf_it_pp_tmpl")
	if err := c.CreateAlertTemplate(ctx, org, AlertTemplateAPI{
		Name: tmpl, Body: `{"text":"{alert_name}"}`, TemplateType: "http",
	}); err != nil {
		t.Fatalf("CreateAlertTemplate: %v", err)
	}
	t.Cleanup(func() { _ = c.DeleteAlertTemplate(ctx, org, tmpl) })

	dest := uniqueName("tf_it_pp_dest")
	if err := c.CreateAlertDestination(ctx, org, AlertDestinationAPI{
		Name: dest, DestinationType: "http", URL: "https://example.com/hook",
		Method: "post", Template: &tmpl, Emails: []string{},
	}); err != nil {
		t.Fatalf("CreateAlertDestination: %v", err)
	}
	t.Cleanup(func() { _ = c.DeleteAlertDestination(ctx, org, dest) })

	sql := fmt.Sprintf("SELECT count(*) AS total FROM %q", stream)
	base := AlertAPI{
		StreamType: "logs", StreamName: stream, Destinations: []string{dest}, Enabled: true,
		QueryCondition: AlertQueryConditionAPI{QueryType: "sql", SQL: &sql},
		TriggerCondition: AlertTriggerConditionAPI{
			Period: 10, Operator: ">=", Threshold: 1, Frequency: 5,
			FrequencyType: "minutes", AlignTime: true,
		},
	}

	withPending := base
	withPending.Name = uniqueName("tf-it-pending")
	withPending.PendingPeriodSec = 120
	alertID, err := c.CreateAlert(ctx, org, "default", withPending)
	if err != nil {
		t.Fatalf("CreateAlert with a pending period: %v", err)
	}
	t.Cleanup(func() { _ = c.DeleteAlert(ctx, org, alertID) })

	alert, err := c.GetAlert(ctx, org, alertID)
	if err != nil {
		t.Fatalf("GetAlert: %v", err)
	}
	if alert.PendingPeriodSec != 120 {
		t.Errorf("pending_period_sec = %d, want 120", alert.PendingPeriodSec)
	}

	// A real-time alert has no schedule to wait across, and the server says so
	// rather than ignoring the field.
	realtime := base
	realtime.Name = uniqueName("tf-it-pending-rt")
	realtime.IsRealTime = true
	realtime.PendingPeriodSec = 60
	realtime.QueryCondition = AlertQueryConditionAPI{
		QueryType:  "custom",
		Conditions: []byte(`{"and":[{"column":"level","operator":"=","value":"error","ignore_case":false}]}`),
	}
	if _, err := c.CreateAlert(ctx, org, "default", realtime); err == nil {
		t.Error("a real-time alert with a pending period was accepted; expected a refusal")
	} else if !strings.Contains(err.Error(), "pending period") {
		t.Errorf("unexpected error for a real-time pending period: %v", err)
	}
}

// TestIntegrationAlertUnaryOperators pins the operator spelling the v2 API
// accepts.
//
// Its request model declares the word-shaped operators with no serde rename,
// unlike the internal storage model which uses snake_case with PascalCase
// aliases. Sending `is_not_empty` is rejected outright, so the provider must
// offer the PascalCase forms and only those.
func TestIntegrationAlertUnaryOperators(t *testing.T) {
	c := integrationClient(t)
	ctx := context.Background()
	org := c.DefaultOrgID()

	stream := uniqueName("tf_it_unary_stream")
	if err := c.CreateStream(ctx, org, "metrics", stream, CreateStreamAPI{}); err != nil {
		t.Fatalf("CreateStream: %v", err)
	}
	t.Cleanup(func() { _ = c.DeleteStream(ctx, org, "metrics", stream) })

	tmpl := uniqueName("tf_it_unary_tmpl")
	if err := c.CreateAlertTemplate(ctx, org, AlertTemplateAPI{
		Name: tmpl, Body: `{"text":"{alert_name}"}`, TemplateType: "http",
	}); err != nil {
		t.Fatalf("CreateAlertTemplate: %v", err)
	}
	t.Cleanup(func() { _ = c.DeleteAlertTemplate(ctx, org, tmpl) })

	dest := uniqueName("tf_it_unary_dest")
	if err := c.CreateAlertDestination(ctx, org, AlertDestinationAPI{
		Name: dest, DestinationType: "http", URL: "https://example.com/hook",
		Method: "post", Template: &tmpl, Emails: []string{},
	}); err != nil {
		t.Fatalf("CreateAlertDestination: %v", err)
	}
	t.Cleanup(func() { _ = c.DeleteAlertDestination(ctx, org, dest) })

	promql := "up"
	newAlert := func(operator string) AlertAPI {
		return AlertAPI{
			Name: uniqueName("tf-it-unary"), StreamType: "metrics", StreamName: stream,
			Destinations: []string{dest}, Enabled: true,
			QueryCondition: AlertQueryConditionAPI{
				QueryType: "promql", PromQL: &promql,
				PromQLCondition: &AlertConditionAPI{
					Column: "value", Operator: operator, Value: []byte(`""`),
				},
			},
			TriggerCondition: AlertTriggerConditionAPI{
				Period: 5, Operator: ">=", Threshold: 1, Frequency: 5,
				FrequencyType: "minutes", AlignTime: true,
			},
		}
	}

	for _, op := range []string{"IsNull", "IsNotNull", "IsEmpty", "IsNotEmpty"} {
		id, err := c.CreateAlert(ctx, org, "default", newAlert(op))
		if err != nil {
			t.Errorf("operator %q was rejected: %v", op, err)
			continue
		}
		t.Cleanup(func() { _ = c.DeleteAlert(ctx, org, id) })
	}

	// The snake_case spelling belongs to the storage model, not the wire.
	if _, err := c.CreateAlert(ctx, org, "default", newAlert("is_not_empty")); err == nil {
		t.Error("snake_case `is_not_empty` was accepted; the provider's operator list assumes it is not, " +
			"so if this now works the list should offer both spellings")
	}
}

// TestIntegrationSyntheticLifecycle covers synthetic checks. It skips when the
// feature is switched off, since the routes are not registered at all then.
func TestIntegrationSyntheticLifecycle(t *testing.T) {
	c := integrationClient(t)
	ctx := context.Background()
	org := c.DefaultOrgID()

	catalog, err := c.ListSyntheticLocations(ctx, org)
	if err != nil {
		if isSyntheticsDisabled(err) {
			t.Skip("synthetics are disabled on this server; set ZO_SYNTHETICS_ENABLED=true to run this")
		}
		t.Fatalf("ListSyntheticLocations: %v", err)
	}
	if len(catalog.Locations) == 0 {
		t.Skip("no synthetic locations registered on this deployment, so no check can be created")
	}
	if len(catalog.Devices) == 0 {
		t.Error("the locations endpoint reported no devices; a browser check has nothing to run against")
	}

	locations := make([]string, 0, 1)
	locations = append(locations, catalog.Locations[0].ID)

	name := uniqueName("tf_it_synthetic")
	id, err := c.CreateSynthetic(ctx, org, "default", SyntheticAPI{
		Name:         name,
		Description:  "integration test",
		CheckType:    "http",
		Target:       "https://example.com",
		Config:       []byte("{}"),
		Frequency:    SyntheticFrequencyAPI{FrequencyType: "minutes", Interval: 5},
		Locations:    locations,
		Enabled:      true,
		AlertIfFails: 1,
	})
	if err != nil {
		t.Fatalf("CreateSynthetic: %v", err)
	}
	if id == "" {
		t.Fatal("CreateSynthetic returned no ID; the create response carries the check itself, not an envelope")
	}
	t.Cleanup(func() { _ = c.DeleteSynthetic(ctx, org, id) })

	check, err := c.GetSynthetic(ctx, org, id)
	if err != nil {
		t.Fatalf("GetSynthetic: %v", err)
	}
	if check == nil {
		t.Fatal("GetSynthetic returned nothing for a check that was just created")
	}
	if check.CheckType != "http" || check.Target != "https://example.com" {
		t.Errorf("check = %+v, want the configured type and target", check)
	}

	// The update body has to carry the folder. Without it the server fails a
	// foreign key with no hint that a folder was what it wanted.
	check.Description = "updated"
	if err := c.UpdateSynthetic(ctx, org, id, *check); err != nil {
		t.Fatalf("UpdateSynthetic: %v", err)
	}
	updated, err := c.GetSynthetic(ctx, org, id)
	if err != nil {
		t.Fatalf("GetSynthetic after update: %v", err)
	}
	if updated.Description != "updated" {
		t.Errorf("description = %q, want updated", updated.Description)
	}

	// Enabling is its own endpoint rather than a field on the update body.
	if err := c.SetSyntheticEnabled(ctx, org, id, false); err != nil {
		t.Fatalf("SetSyntheticEnabled: %v", err)
	}
	paused, err := c.GetSynthetic(ctx, org, id)
	if err != nil {
		t.Fatalf("GetSynthetic after disable: %v", err)
	}
	if paused.Enabled {
		t.Error("check is still enabled after being paused")
	}

	found, err := c.FindSyntheticByName(ctx, org, name)
	if err != nil {
		t.Fatalf("FindSyntheticByName: %v", err)
	}
	if found == nil || found.ID != id {
		t.Errorf("FindSyntheticByName = %+v, want the check %q", found, id)
	}
}
