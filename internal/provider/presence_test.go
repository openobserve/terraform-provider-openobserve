package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Regression cover for openobserve/terraform-provider-openobserve#3.
//
// A `dynamic` block over a SingleNestedBlock that produced no iterations does
// not arrive as a null object. Terraform hands the provider a present object
// whose attributes are unknown, which is indistinguishable from a block the
// author wrote but whose values have not resolved. Validation must treat that
// as undecided rather than as configured, or it reports indicators nobody set.

var testSingleQueryAttrs = map[string]attr.Type{
	"stream":      types.StringType,
	"stream_type": types.StringType,
	"scope":       types.StringType,
	"good_expr":   types.StringType,
}

func TestValuePresenceClassifiesBlocks(t *testing.T) {
	objType := types.ObjectType{AttrTypes: testSingleQueryAttrs}

	allUnknown := types.ObjectValueMust(testSingleQueryAttrs, map[string]attr.Value{
		"stream":      types.StringUnknown(),
		"stream_type": types.StringUnknown(),
		"scope":       types.StringUnknown(),
		"good_expr":   types.StringUnknown(),
	})
	allNull := types.ObjectValueMust(testSingleQueryAttrs, map[string]attr.Value{
		"stream":      types.StringNull(),
		"stream_type": types.StringNull(),
		"scope":       types.StringNull(),
		"good_expr":   types.StringNull(),
	})
	oneSet := types.ObjectValueMust(testSingleQueryAttrs, map[string]attr.Value{
		"stream":      types.StringValue("app_logs"),
		"stream_type": types.StringNull(),
		"scope":       types.StringNull(),
		"good_expr":   types.StringNull(),
	})
	mixed := types.ObjectValueMust(testSingleQueryAttrs, map[string]attr.Value{
		"stream":      types.StringUnknown(),
		"stream_type": types.StringValue("logs"),
		"scope":       types.StringNull(),
		"good_expr":   types.StringNull(),
	})

	cases := []struct {
		name string
		in   attr.Value
		want blockPresence
	}{
		{"null object is absent", types.ObjectNull(testSingleQueryAttrs), blockAbsent},
		{"unknown object is undecided", types.ObjectUnknown(testSingleQueryAttrs), blockIndeterminate},
		{"object of nulls is absent", allNull, blockAbsent},
		{"object of unknowns is undecided", allUnknown, blockIndeterminate},
		{"one known attribute is configured", oneSet, blockConfigured},
		{"a known attribute wins over an unknown sibling", mixed, blockConfigured},
		{"null string is absent", types.StringNull(), blockAbsent},
		{"unknown string is undecided", types.StringUnknown(), blockIndeterminate},
		{"set string is configured", types.StringValue("x"), blockConfigured},
		{"empty list is absent", types.ListValueMust(objType, []attr.Value{}), blockAbsent},
		{"non-empty list is configured", types.ListValueMust(objType, []attr.Value{oneSet}), blockConfigured},
		{"null list is absent", types.ListNull(objType), blockAbsent},
		{"unknown list is undecided", types.ListUnknown(objType), blockIndeterminate},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := valuePresence(tc.in); got != tc.want {
				t.Errorf("valuePresence = %v, want %v", got, tc.want)
			}
		})
	}
}

// A nested block that only contains other empty blocks is still absent. This is
// the count_sli shape: an object whose single_query, dual_query and promql are
// all null.
func TestValuePresenceDescendsIntoNestedBlocks(t *testing.T) {
	countAttrs := map[string]attr.Type{
		"single_query": types.ObjectType{AttrTypes: testSingleQueryAttrs},
	}

	empty := types.ObjectValueMust(countAttrs, map[string]attr.Value{
		"single_query": types.ObjectNull(testSingleQueryAttrs),
	})
	if got := valuePresence(empty); got != blockAbsent {
		t.Errorf("a block of empty blocks = %v, want blockAbsent", got)
	}

	unknownInner := types.ObjectValueMust(countAttrs, map[string]attr.Value{
		"single_query": types.ObjectValueMust(testSingleQueryAttrs, map[string]attr.Value{
			"stream":      types.StringUnknown(),
			"stream_type": types.StringUnknown(),
			"scope":       types.StringUnknown(),
			"good_expr":   types.StringUnknown(),
		}),
	})
	if got := valuePresence(unknownInner); got != blockIndeterminate {
		t.Errorf("a block whose inner block is all unknown = %v, want blockIndeterminate", got)
	}

	populated := types.ObjectValueMust(countAttrs, map[string]attr.Value{
		"single_query": types.ObjectValueMust(testSingleQueryAttrs, map[string]attr.Value{
			"stream":      types.StringValue("app_logs"),
			"stream_type": types.StringValue("logs"),
			"scope":       types.StringNull(),
			"good_expr":   types.StringValue("status < 500"),
		}),
	})
	if got := valuePresence(populated); got != blockConfigured {
		t.Errorf("a populated nested block = %v, want blockConfigured", got)
	}
}

func TestKnownAndSet(t *testing.T) {
	cases := []struct {
		name string
		in   attr.Value
		want bool
	}{
		{"value", types.StringValue("x"), true},
		{"null", types.StringNull(), false},
		// The one that matters: validation must not fire on an unresolved
		// value, or a variable-driven configuration errors for no reason.
		{"unknown", types.StringUnknown(), false},
		{"zero is still set", types.Int64Value(0), true},
		{"false is still set", types.BoolValue(false), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := knownAndSet(tc.in); got != tc.want {
				t.Errorf("knownAndSet = %v, want %v", got, tc.want)
			}
		})
	}
}
