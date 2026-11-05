package provider

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// A condition node's `conditions` document comes in two formats, and the
// server tells them apart by a `version` field that sits next to `conditions`
// in the node data rather than inside it (ConditionParams in OpenObserve's
// src/config/src/meta/pipeline/components.rs):
//
//	{"node_type": "condition", "conditions": <ConditionList>}                 v1
//	{"node_type": "condition", "version": 2, "conditions": <ConditionGroup>}  v2
//
// The v1 document is written as before: a bare list, or an `and`/`or`/`not`
// tree. A v2 document is written the way an alert's `query_condition.conditions`
// takes it, `{"version": 2, "conditions": {"filterType": "group", ...}}`, which
// is also the node data minus `node_type`. Keeping the version inside the
// document means one condition can be moved between an alert and a pipeline
// unchanged, the schema needs no attribute that only one node type could use,
// and nothing about a v1 node changes: its document has no `version` key and
// is sent byte for byte as before.
//
// On write the wrapper is split into the node's `version` and `conditions`
// fields; on read a v2 node is wrapped again before it is compared with the
// configuration.

// pipelineConditionV2 is the only explicit version the provider sends. v1 is
// the server's default when the field is absent, and the server never writes
// `version: 1` back, so an explicit 1 would read back as a different document.
const pipelineConditionV2 = 2

// splitPipelineConditions turns a configured `conditions` document into the
// node data's `version` and `conditions` fields. Anything that is not a
// version wrapper is a v1 document and is sent as written.
func splitPipelineConditions(doc string) (*int64, json.RawMessage) {
	raw := json.RawMessage(doc)
	var wrapper map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return nil, raw
	}
	versionRaw, ok := wrapper["version"]
	if !ok {
		return nil, raw
	}
	version, ok := conditionVersion(versionRaw)
	if !ok || version != pipelineConditionV2 {
		// ValidateConfig rejects anything else, but a document only known at
		// apply time skips that. Send it unchanged and let the server's parse
		// error stand: splitting a `version: 1` would be stored as v1, read
		// back without the wrapper, and fail the apply as inconsistent.
		return nil, raw
	}
	return &version, wrapper["conditions"]
}

// conditionVersion reads a `version` written as an integer literal. The
// server reads it with as_u64(), which treats `2.0` as no version at all, so
// a float is not a version here either, and nor is a string, which
// json.Number would otherwise accept.
func conditionVersion(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 || raw[0] == '"' {
		return 0, false
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, false
	}
	v, err := strconv.ParseInt(n.String(), 10, 64)
	return v, err == nil
}

// v2ConditionKeys and v2GroupKeys are the fields OpenObserve's
// ConditionItemCondition and ConditionItem::Group carry. The server drops any
// other key, so one written in the configuration (`ignoreCase` for
// `ignore_case`, say) would be lost and read back as a change.
var (
	v2ConditionKeys = map[string]bool{
		"filterType": true, "column": true, "operator": true, "value": true,
		"ignore_case": true, "logicalOperator": true,
	}
	v2GroupKeys = map[string]bool{"filterType": true, "logicalOperator": true, "conditions": true}
)

// joinPipelineConditions is the inverse of splitPipelineConditions for a read:
// a v2 node's group is wrapped back into the document the configuration uses.
func joinPipelineConditions(version *int64, conditions json.RawMessage) json.RawMessage {
	if version == nil || *version == 1 || len(conditions) == 0 {
		return conditions
	}
	wrapped, err := json.Marshal(struct {
		Version    int64           `json:"version"`
		Conditions json.RawMessage `json:"conditions"`
	}{*version, conditions})
	if err != nil {
		return conditions
	}
	return wrapped
}

// conditionItemOperators are the operator spellings OpenObserve's Operator
// enum accepts, symbolic, snake_case, and the PascalCase aliases.
var conditionItemOperators = func() map[string]bool {
	ops := map[string]bool{"=": true, "!=": true, ">": true, ">=": true, "<": true, "<=": true}
	for spelling := range canonicalConditionOperators {
		ops[spelling] = true
	}
	return ops
}()

// pipelineConditionsProblem explains why a condition node's document cannot
// work, or returns "" when it can. It catches what the server would reject,
// what would read back differently, and a v1 document under the v2 wrapper or
// the other way round. It does not try to be the server's whole grammar.
func pipelineConditionsProblem(doc string) string {
	var v any
	if err := json.Unmarshal([]byte(doc), &v); err != nil {
		return fmt.Sprintf("`conditions` is not valid JSON: %s.", err)
	}
	obj, isObject := v.(map[string]any)
	if !isObject {
		return "" // a v1 legacy list
	}

	versionValue, versioned := obj["version"]
	if !versioned {
		if _, group := obj["filterType"]; group {
			return "This has a `filterType`, so it is written in the v2 format, but it is not marked as v2. " +
				"The server would read it as v1: a group is rejected, and a single item is taken as a v1 " +
				"condition with its v2 keys dropped. Wrap it as " +
				"`{ version = 2, conditions = { filterType = \"group\", ... } }`."
		}
		return ""
	}

	var rawObj map[string]json.RawMessage
	_ = json.Unmarshal([]byte(doc), &rawObj)
	if n, ok := conditionVersion(rawObj["version"]); !ok || n != pipelineConditionV2 {
		return fmt.Sprintf("`version` is %s, and the only version a condition document can name is 2. "+
			"A v1 document is written without a wrapper: a list of conditions, or an `and`, `or` or `not` "+
			"tree.", compactJSON(versionValue))
	}
	for k := range obj {
		if k != "version" && k != "conditions" {
			return fmt.Sprintf("A v2 condition document holds only `version` and `conditions`, but it also has "+
				"`%s`. The server drops unknown keys, which would read back as a change.", k)
		}
	}

	group, ok := obj["conditions"].(map[string]any)
	if !ok || group["filterType"] != "group" {
		return "With `version = 2`, `conditions` must be a v2 group: " +
			"`{ filterType = \"group\", logicalOperator = \"AND\", conditions = [ ... ] }`. " +
			"This looks like a v1 document; drop the `version` wrapper to use it as v1, or rewrite it with " +
			"`filterType` items."
	}
	if items, isList := group["conditions"].([]any); isList && len(items) == 0 {
		return "A v2 condition group needs at least one condition. The server rejects an empty one."
	}
	if p := unknownKeyProblem(group, v2GroupKeys, "conditions"); p != "" {
		return p
	}
	return conditionGroupProblem(group, "conditions")
}

// conditionGroupProblem walks a v2 group and its items. at is the item's
// location in the document, for the message.
func conditionGroupProblem(group map[string]any, at string) string {
	if p := logicalOperatorProblem(group, at); p != "" {
		return p
	}
	items, ok := group["conditions"].([]any)
	if !ok {
		return fmt.Sprintf("`%s.conditions` must be a list of condition items.", at)
	}
	for i, item := range items {
		itemAt := fmt.Sprintf("%s.conditions[%d]", at, i)
		m, ok := item.(map[string]any)
		if !ok {
			return fmt.Sprintf("`%s` must be an object with a `filterType`.", itemAt)
		}
		switch m["filterType"] {
		case "group":
			if p := unknownKeyProblem(m, v2GroupKeys, itemAt); p != "" {
				return p
			}
			if p := conditionGroupProblem(m, itemAt); p != "" {
				return p
			}
		case "condition":
			if p := unknownKeyProblem(m, v2ConditionKeys, itemAt); p != "" {
				return p
			}
			if p := logicalOperatorProblem(m, itemAt); p != "" {
				return p
			}
			if _, ok := m["column"].(string); !ok {
				return fmt.Sprintf("`%s` needs a `column`.", itemAt)
			}
			// `value` has no default on the server, so even a unary operator
			// such as `is_null` needs one; "" will do.
			if _, ok := m["value"]; !ok {
				return fmt.Sprintf("`%s` needs a `value`, even for a unary operator such as `is_null`; "+
					"use `\"\"` there.", itemAt)
			}
			// An explicit null is not stored either, so it reads back missing.
			if ic, present := m["ignore_case"]; present {
				if _, isBool := ic.(bool); !isBool {
					return fmt.Sprintf("`%s.ignore_case` must be true or false, got %s.", itemAt, compactJSON(ic))
				}
			}
			op, _ := m["operator"].(string)
			if !conditionItemOperators[op] {
				return fmt.Sprintf("`%s.operator` is %s; it must be one of %s.",
					itemAt, compactJSON(m["operator"]), strings.Join(backtickedSortedKeys(conditionItemOperators), ", "))
			}
		default:
			return fmt.Sprintf("`%s.filterType` must be `condition` or `group`. v1 forms such as `and`, `or` "+
				"and `not` cannot be mixed into a v2 group.", itemAt)
		}
	}
	return ""
}

func unknownKeyProblem(m map[string]any, allowed map[string]bool, at string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !allowed[k] {
			return fmt.Sprintf("`%s` has a key `%s` that a v2 %s does not have; it must be one of %s. The server "+
				"drops unknown keys, which would read back as a change.",
				at, k, m["filterType"], strings.Join(backtickedSortedKeys(allowed), ", "))
		}
	}
	return ""
}

func logicalOperatorProblem(m map[string]any, at string) string {
	op, present := m["logicalOperator"]
	switch {
	case !present:
		return fmt.Sprintf("`%s.logicalOperator` is missing; it must be `AND` or `OR` (upper case).", at)
	case op == "AND", op == "OR":
		return ""
	}
	return fmt.Sprintf("`%s.logicalOperator` must be `AND` or `OR` (upper case), got %s.",
		at, compactJSON(m["logicalOperator"]))
}

func compactJSON(v any) string {
	out, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(out)
}

func backtickedSortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, "`"+k+"`")
	}
	sort.Strings(keys)
	return keys
}
