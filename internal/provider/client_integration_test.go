package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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
