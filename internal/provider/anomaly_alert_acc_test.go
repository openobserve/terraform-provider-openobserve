package provider_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// Acceptance tests for openobserve_anomaly_alert. They run real plans and
// applies, so they need TF_ACC=1, an OpenObserve Enterprise server in
// OPENOBSERVE_ENDPOINT/_USERNAME/_PASSWORD/_ORG_ID, and a Terraform or
// OpenTofu binary (TF_ACC_TERRAFORM_PATH). OPENOBSERVE_ANOMALY_STREAM names a
// logs stream to read; the training test needs one with 21+ days of data.
// OPENOBSERVE_TEST_PREFIX overrides the "tf_acc" prefix of created objects.
//
// The training test waits for a real training run, so it also needs
// OPENOBSERVE_ANOMALY_TRAINING=1 and a longer timeout than `make testacc`:
//
//	TF_ACC=1 OPENOBSERVE_ANOMALY_TRAINING=1 OPENOBSERVE_ANOMALY_STREAM=app_logs \
//	  go test ./internal/provider/ -run TestAccAnomalyAlertTraining -v -timeout 20m

// listAnomalyIDs returns the HTTP status of the anomaly detection list and
// the IDs it holds, read straight from the API.
func listAnomalyIDs() (int, map[string]bool, error) {
	org := os.Getenv("OPENOBSERVE_ORG_ID")
	if org == "" {
		org = "default"
	}
	req, err := http.NewRequest(http.MethodGet,
		strings.TrimRight(os.Getenv("OPENOBSERVE_ENDPOINT"), "/")+"/api/"+org+"/anomaly_detection", nil)
	if err != nil {
		return 0, nil, err
	}
	req.SetBasicAuth(os.Getenv("OPENOBSERVE_USERNAME"), os.Getenv("OPENOBSERVE_PASSWORD"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close() //nolint:errcheck // response body close error is not actionable
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil, err
	}
	var rows []struct {
		AnomalyID string `json:"anomaly_id"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return resp.StatusCode, nil, err
	}
	ids := make(map[string]bool, len(rows))
	for _, row := range rows {
		ids[row.AnomalyID] = true
	}
	return resp.StatusCode, ids, nil
}

// checkAnomalyAlertsDestroyed fails when an alert from the final state is
// still listed, so a delete that reports success without deleting is caught.
func checkAnomalyAlertsDestroyed(s *terraform.State) error {
	status, ids, err := listAnomalyIDs()
	if err != nil || status != http.StatusOK {
		return fmt.Errorf("listing anomaly alerts after destroy: HTTP %d, %v", status, err)
	}
	for _, rs := range s.RootModule().Resources {
		if rs.Type != "openobserve_anomaly_alert" {
			continue
		}
		if id := rs.Primary.Attributes["anomaly_id"]; ids[id] {
			return fmt.Errorf("anomaly alert %s still exists after destroy", id)
		}
	}
	return nil
}

func anomalyAccPreCheck(t *testing.T) (prefix, stream string) {
	t.Helper()
	if os.Getenv("TF_ACC") == "" || os.Getenv("OPENOBSERVE_ENDPOINT") == "" {
		t.Skip("set TF_ACC=1 and OPENOBSERVE_ENDPOINT to run acceptance tests")
	}
	// An open-source server has no anomaly detection routes and answers 404.
	status, _, err := listAnomalyIDs()
	if err != nil {
		t.Fatalf("probing the anomaly detection API: %v", err)
	}
	if status == http.StatusNotFound {
		t.Skip("anomaly detection needs OpenObserve Enterprise; this server has no anomaly_detection API")
	}
	prefix = os.Getenv("OPENOBSERVE_TEST_PREFIX")
	if prefix == "" {
		prefix = "tf_acc"
	}
	return prefix, os.Getenv("OPENOBSERVE_ANOMALY_STREAM")
}

// anomalyAccStreamName is the stream the test alert reads.
func anomalyAccStreamName(stream, name string) string {
	if stream != "" {
		return stream
	}
	return name + "_stream"
}

// anomalyAccConfig renders a disabled anomaly alert, so it neither trains nor
// notifies, with the given extra attribute lines. With no stream configured it
// creates an empty one to read.
func anomalyAccConfig(prefix, stream, name, extra string) string {
	streamRef := fmt.Sprintf("%q", stream)
	streamBlock := ""
	if stream == "" {
		streamBlock = fmt.Sprintf(`
resource "openobserve_stream" "s" {
  name        = "%s_stream"
  stream_type = "logs"
}
`, name)
		streamRef = "openobserve_stream.s.name"
	}
	return fmt.Sprintf(`
provider "openobserve" {}
%s
resource "openobserve_anomaly_alert" "test" {
  name        = %q
  stream_name = %s

  detection_function       = "count"
  histogram_interval       = "1h"
  schedule_interval        = "1h"
  detection_window_seconds = 7200
  enabled                  = false
  alert_enabled            = false
%s
}
`, streamBlock, name, streamRef, extra)
}

func TestAccAnomalyAlert(t *testing.T) {
	prefix, stream := anomalyAccPreCheck(t)
	name := fmt.Sprintf("%s_anomaly_%d", prefix, time.Now().UnixNano())
	const addr = "openobserve_anomaly_alert.test"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkAnomalyAlertsDestroyed,
		Steps: []resource.TestStep{
			// Create with every band input.
			{
				Config: anomalyAccConfig(prefix, stream, name, `
  band_width               = 4
  alert_direction          = "above"
  alert_window_buckets     = 3
  alert_window_fire_pct    = 60
  alert_window_recover_pct = 40
  tags                     = ["tf_acc"]
  priority                 = 4`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "band_width", "4"),
					resource.TestCheckResourceAttr(addr, "alert_direction", "above"),
					resource.TestCheckResourceAttr(addr, "alert_window_buckets", "3"),
					resource.TestCheckResourceAttr(addr, "alert_window_fire_pct", "60"),
					resource.TestCheckResourceAttr(addr, "alert_window_recover_pct", "40"),
					resource.TestCheckResourceAttr(addr, "training_window_days", "28"),
					resource.TestCheckResourceAttrSet(addr, "anomaly_id"),
					resource.TestCheckResourceAttr(addr, "priority", "4"),
					resource.TestCheckResourceAttr(addr, "tags.#", "1"),
				),
			},
			// Update each input.
			{
				Config: anomalyAccConfig(prefix, stream, name, `
  band_width               = 5.5
  alert_direction          = "below"
  alert_window_buckets     = 2
  alert_window_fire_pct    = 80
  alert_window_recover_pct = 50
  training_window_days     = 30`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "band_width", "5.5"),
					resource.TestCheckResourceAttr(addr, "alert_direction", "below"),
					resource.TestCheckResourceAttr(addr, "alert_window_buckets", "2"),
					resource.TestCheckResourceAttr(addr, "alert_window_fire_pct", "80"),
					resource.TestCheckResourceAttr(addr, "alert_window_recover_pct", "50"),
					resource.TestCheckResourceAttr(addr, "training_window_days", "30"),
					resource.TestCheckNoResourceAttr(addr, "tags.#"),
					resource.TestCheckNoResourceAttr(addr, "priority"),
				),
			},
			// band_width and alert_budget_per_day conflict at plan time.
			{
				Config: anomalyAccConfig(prefix, stream, name, `
  band_width           = 5.5
  alert_budget_per_day = 5`),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?s)alert_budget_per_day.*band_width|band_width.*alert_budget_per_day`),
			},
			// recover above fire is rejected at plan time.
			{
				Config: anomalyAccConfig(prefix, stream, name, `
  alert_window_fire_pct    = 50
  alert_window_recover_pct = 70`),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`Recover percent above fire percent`),
			},
			// Removing the settings clears them back to the defaults, and the
			// budget replaces the fixed band.
			{
				Config: anomalyAccConfig(prefix, stream, name, `
  alert_budget_per_day = 5`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(addr, "band_width"),
					resource.TestCheckResourceAttr(addr, "alert_budget_per_day", "5"),
					resource.TestCheckResourceAttr(addr, "alert_direction", "both"),
					resource.TestCheckResourceAttr(addr, "alert_window_buckets", "1"),
					resource.TestCheckResourceAttr(addr, "alert_window_fire_pct", "100"),
					resource.TestCheckNoResourceAttr(addr, "alert_window_recover_pct"),
				),
			},
			// A filters-mode query, then a switch to custom SQL, which the
			// server answers by clearing the filters.
			{
				Config: anomalyAccConfig(prefix, stream, name, `
  filters = [{ field = "_timestamp", operator = "is_not_null", value = "" }]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "filters.#", "1"),
					resource.TestCheckResourceAttr(addr, "filters.0.operator", "is_not_null"),
					resource.TestCheckNoResourceAttr(addr, "custom_sql"),
				),
			},
			{
				Config: anomalyAccConfig(prefix, stream, name, fmt.Sprintf(`
  custom_sql = "SELECT histogram(_timestamp, '1h') AS _timestamp, count(*) AS value FROM \"%s\" GROUP BY _timestamp"`,
					anomalyAccStreamName(stream, name))),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(addr, "filters.#"),
					resource.TestCheckResourceAttrSet(addr, "custom_sql"),
				),
			},
			// Import reads back exactly what the configuration holds.
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					return s.RootModule().Resources[addr].Primary.ID, nil
				},
			},
		},
	})
}

// TestAccAnomalyAlertTraining creates an enabled alert, which starts
// training, waits for it, and checks the computed band attributes populate on
// refresh. It runs only with OPENOBSERVE_ANOMALY_TRAINING=1 and
// OPENOBSERVE_ANOMALY_STREAM naming a logs stream with 21+ days of data.
//
// The wait is bounded by the test binary's -timeout, less a margin for the
// destroy: a timeout kills the process, and the alert would then be left
// running on the server.
func TestAccAnomalyAlertTraining(t *testing.T) {
	prefix, stream := anomalyAccPreCheck(t)
	if os.Getenv("OPENOBSERVE_ANOMALY_TRAINING") != "1" || stream == "" {
		t.Skip("set OPENOBSERVE_ANOMALY_TRAINING=1 and OPENOBSERVE_ANOMALY_STREAM to a logs stream with 21+ days " +
			"of data to run the training test")
	}
	const cleanupMargin = 90 * time.Second
	waitUntil := time.Now().Add(10 * time.Minute)
	if deadline, ok := t.Deadline(); ok && deadline.Add(-cleanupMargin).Before(waitUntil) {
		waitUntil = deadline.Add(-cleanupMargin)
	}
	if time.Until(waitUntil) < time.Minute {
		t.Skip("too little of the test timeout is left to wait for training; run with -timeout 20m")
	}

	name := fmt.Sprintf("%s_anomaly_train_%d", prefix, time.Now().UnixNano())
	const addr = "openobserve_anomaly_alert.test"
	config := strings.Replace(anomalyAccConfig(prefix, stream, name, ""),
		"enabled                  = false\n", "enabled                  = true\n", 1)

	trained := func(s *terraform.State) error {
		attrs := s.RootModule().Resources[addr].Primary.Attributes
		if attrs["is_trained"] != "true" {
			return fmt.Errorf("not trained in the time the test timeout allows: status %q", attrs["status"])
		}
		return nil
	}

	// Each refresh step re-reads the alert. A step only waits while there is
	// time left, so once the budget is spent the remaining steps run straight
	// through to the final check and the destroy.
	const poll = 15 * time.Second
	done := false
	steps := []resource.TestStep{{Config: config}}
	for i := 0; i < 40; i++ {
		steps = append(steps, resource.TestStep{
			PreConfig: func() {
				if !done && time.Until(waitUntil) > poll {
					time.Sleep(poll)
				}
			},
			RefreshState: true,
			Check: func(s *terraform.State) error {
				if trained(s) != nil {
					return nil // keep waiting
				}
				done = true
				return resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "status", "ready"),
					resource.TestCheckResourceAttrSet(addr, "band_grouping"),
					resource.TestCheckResourceAttrSet(addr, "band_k"),
					resource.TestCheckResourceAttrSet(addr, "training_data_start_us"),
					resource.TestCheckResourceAttrSet(addr, "training_data_end_us"),
				)(s)
			},
		})
	}
	steps = append(steps, resource.TestStep{
		RefreshState: true,
		Check:        trained,
	})

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkAnomalyAlertsDestroyed,
		Steps:                    steps,
	})
}
