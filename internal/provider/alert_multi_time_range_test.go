package provider

import (
	"context"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
)

// Regression cover for openobserve/terraform-provider-openobserve#2.
//
// multi_time_range must be a framework list rather than a Go slice. A slice
// cannot hold an unknown value, and Terraform hands the provider an unknown
// list whenever a `dynamic "multi_time_range"` block's for_each comes from a
// variable, which fails config decode before ValidateConfig ever runs:
//
//	Received unknown value, however the target type cannot handle unknown
//	values. Target Type: []provider.AlertTimeOffsetModel
func TestAlertMultiTimeRangeCanHoldUnknown(t *testing.T) {
	field, ok := reflect.TypeOf(AlertQueryConditionModel{}).FieldByName("MultiTimeRange")
	if !ok {
		t.Fatal("AlertQueryConditionModel has no MultiTimeRange field")
	}
	if field.Type.Kind() == reflect.Slice {
		t.Fatalf("MultiTimeRange is %s: a bare slice cannot represent an unknown value, so a "+
			"variable-driven dynamic block fails config decode. Use types.List.", field.Type)
	}
	if got, want := field.Type.String(), "basetypes.ListValue"; got != want {
		t.Errorf("MultiTimeRange is %s, want %s", got, want)
	}
}

// The server reporting no offsets must produce a null list, not an empty one.
// An empty list differs from the null Terraform holds for a block that was
// never written, and would show as drift on every plan.
func TestMultiTimeRangeAbsentIsNull(t *testing.T) {
	var diags diag.Diagnostics

	got := multiTimeRangeToList(context.Background(), nil, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if !got.IsNull() {
		t.Errorf("no offsets produced %v, want a null list", got)
	}

	got = multiTimeRangeToList(context.Background(), []AlertCompareHistoricData{{Offset: "1d"}}, &diags)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got.IsNull() || len(got.Elements()) != 1 {
		t.Errorf("one offset produced %v, want a one-element list", got)
	}
}
