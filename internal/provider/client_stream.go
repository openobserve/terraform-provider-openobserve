package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// StreamPartitionAPI is a single partition key definition.
//
// `types` is an untyped value because OpenObserve serialises the partition type
// either as the string "value"/"prefix" or as the object {"hash": 16}.
type StreamPartitionAPI struct {
	Field    string `json:"field"`
	Types    any    `json:"types,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
}

// PartitionKeyList is a list of partition keys that tolerates both shapes the
// API uses: current builds return a JSON array, while older ones return a map
// keyed by partition level ("L0", "L1", …). Writes always send an array.
type PartitionKeyList []StreamPartitionAPI

// UnmarshalJSON decodes either the array or the level-keyed map form.
func (p *PartitionKeyList) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		*p = nil
		return nil
	}

	if trimmed[0] == '[' {
		var list []StreamPartitionAPI
		if err := json.Unmarshal(trimmed, &list); err != nil {
			return err
		}
		*p = list
		return nil
	}

	var byLevel map[string]StreamPartitionAPI
	if err := json.Unmarshal(trimmed, &byLevel); err != nil {
		return err
	}
	levels := make([]string, 0, len(byLevel))
	for level := range byLevel {
		levels = append(levels, level)
	}
	sort.Slice(levels, func(i, j int) bool { return partitionLevelLess(levels[i], levels[j]) })

	list := make([]StreamPartitionAPI, 0, len(levels))
	for _, level := range levels {
		list = append(list, byLevel[level])
	}
	*p = list
	return nil
}

// partitionLevelLess orders "L0" before "L10", which a plain string sort would
// get wrong once a stream has more than ten partition levels.
func partitionLevelLess(a, b string) bool {
	ai, aok := partitionLevelIndex(a)
	bi, bok := partitionLevelIndex(b)
	if aok && bok {
		return ai < bi
	}
	return a < b
}

func partitionLevelIndex(level string) (int, bool) {
	digits := strings.TrimPrefix(level, "L")
	if digits == level {
		return 0, false
	}
	n, err := strconv.Atoi(digits)
	if err != nil {
		return 0, false
	}
	return n, true
}

// DistinctFieldAPI is an entry of StreamSettingsAPI.DistinctValueFields.
type DistinctFieldAPI struct {
	Name    string `json:"name"`
	AddedTS int64  `json:"added_ts,omitempty"`
}

// DistinctFieldList tolerates both shapes the API uses for distinct value
// fields: a list of objects on current builds and a list of bare field names on
// older ones.
type DistinctFieldList []DistinctFieldAPI

// UnmarshalJSON decodes either the object or the plain-string element form.
func (d *DistinctFieldList) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		*d = nil
		return nil
	}

	var objects []DistinctFieldAPI
	if err := json.Unmarshal(trimmed, &objects); err == nil {
		*d = objects
		return nil
	}

	var names []string
	if err := json.Unmarshal(trimmed, &names); err != nil {
		return err
	}
	list := make([]DistinctFieldAPI, 0, len(names))
	for _, name := range names {
		list = append(list, DistinctFieldAPI{Name: name})
	}
	*d = list
	return nil
}

// StreamSettingsAPI is the settings object returned by the stream schema endpoint.
type StreamSettingsAPI struct {
	PartitionKeys               PartitionKeyList  `json:"partition_keys"`
	FullTextSearchKeys          []string          `json:"full_text_search_keys"`
	IndexFields                 []string          `json:"index_fields"`
	BloomFilterFields           []string          `json:"bloom_filter_fields"`
	DefinedSchemaFields         []string          `json:"defined_schema_fields"`
	DistinctValueFields         DistinctFieldList `json:"distinct_value_fields"`
	StorageType                 string            `json:"storage_type"`
	DataRetention               int64             `json:"data_retention"`
	FlattenLevel                *int64            `json:"flatten_level"`
	MaxQueryRange               int64             `json:"max_query_range"`
	StoreOriginalData           bool              `json:"store_original_data"`
	ApproxPartition             bool              `json:"approx_partition"`
	IndexOriginalData           bool              `json:"index_original_data"`
	IndexAllValues              bool              `json:"index_all_values"`
	EnableDistinctFields        bool              `json:"enable_distinct_fields"`
	EnableLogPatternsExtraction bool              `json:"enable_log_patterns_extraction"`
}

// StreamFieldAPI describes one column of a stream schema.
type StreamFieldAPI struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// StreamAPI is the response of GET /api/{org}/streams/{name}/schema.
type StreamAPI struct {
	Name        string            `json:"name"`
	StorageType string            `json:"storage_type"`
	StreamType  string            `json:"stream_type"`
	Settings    StreamSettingsAPI `json:"settings"`
	Schema      []StreamFieldAPI  `json:"schema"`
	TotalFields int               `json:"total_fields"`
	Stats       *StreamStatsAPI   `json:"stats,omitempty"`
}

// StreamStatsAPI holds per-stream ingestion statistics.
type StreamStatsAPI struct {
	DocNum         int64   `json:"doc_num"`
	FileNum        int64   `json:"file_num"`
	StorageSize    float64 `json:"storage_size"`
	CompressedSize float64 `json:"compressed_size"`
	IndexSize      float64 `json:"index_size"`
	DocTimeMin     int64   `json:"doc_time_min"`
	DocTimeMax     int64   `json:"doc_time_max"`
}

// StreamListAPI wraps GET /api/{org}/streams.
type StreamListAPI struct {
	List []StreamAPI `json:"list"`
}

// addRemove is the {add, remove} envelope the stream settings endpoint expects
// for every list-valued setting.
type addRemove[T any] struct {
	Add    []T `json:"add"`
	Remove []T `json:"remove"`
}

func newAddRemove[T any](add, remove []T) addRemove[T] {
	if add == nil {
		add = []T{}
	}
	if remove == nil {
		remove = []T{}
	}
	return addRemove[T]{Add: add, Remove: remove}
}

// UpdateStreamSettingsAPI is the body of PUT /api/{org}/streams/{name}/settings.
//
// List settings are expressed as deltas rather than absolute values, so callers
// must diff the desired state against the current state before calling.
type UpdateStreamSettingsAPI struct {
	PartitionKeys               addRemove[StreamPartitionAPI] `json:"partition_keys"`
	FullTextSearchKeys          addRemove[string]             `json:"full_text_search_keys"`
	IndexFields                 addRemove[string]             `json:"index_fields"`
	BloomFilterFields           addRemove[string]             `json:"bloom_filter_fields"`
	DefinedSchemaFields         addRemove[string]             `json:"defined_schema_fields"`
	DistinctValueFields         addRemove[string]             `json:"distinct_value_fields"`
	DataRetention               *int64                        `json:"data_retention,omitempty"`
	FlattenLevel                *int64                        `json:"flatten_level,omitempty"`
	MaxQueryRange               *int64                        `json:"max_query_range,omitempty"`
	StoreOriginalData           *bool                         `json:"store_original_data,omitempty"`
	ApproxPartition             *bool                         `json:"approx_partition,omitempty"`
	IndexOriginalData           *bool                         `json:"index_original_data,omitempty"`
	IndexAllValues              *bool                         `json:"index_all_values,omitempty"`
	EnableDistinctFields        *bool                         `json:"enable_distinct_fields,omitempty"`
	EnableLogPatternsExtraction *bool                         `json:"enable_log_patterns_extraction,omitempty"`
	StorageType                 *string                       `json:"storage_type,omitempty"`
}

// CreateStreamAPI is the body of POST /api/{org}/streams/{name}.
//
// Settings are a free-form object rather than StreamSettingsAPI because the
// create endpoint validates every key it receives, and sending zero values for
// enum-typed fields such as `storage_type` is rejected. The provider creates an
// empty stream here and applies settings through the settings endpoint, which
// takes deltas.
type CreateStreamAPI struct {
	Fields   []StreamFieldAPI `json:"fields"`
	Settings map[string]any   `json:"settings"`
}

// GetStream fetches a stream schema. It returns (nil, nil) when the stream does
// not exist.
func (c *Client) GetStream(ctx context.Context, orgID, streamType, name string) (*StreamAPI, error) {
	path := fmt.Sprintf("/api/%s/streams/%s/schema?type=%s", pathEscape(orgID), pathEscape(name), pathEscape(streamType))
	var out StreamAPI
	found, err := c.doJSONOptional(ctx, http.MethodGet, path, nil, &out)
	if err != nil {
		return nil, err
	}
	if !found || out.Name == "" {
		return nil, nil
	}
	return &out, nil
}

// ListStreams returns every stream of the given type, or all types when
// streamType is empty.
func (c *Client) ListStreams(ctx context.Context, orgID, streamType string) ([]StreamAPI, error) {
	path := fmt.Sprintf("/api/%s/streams", pathEscape(orgID))
	if streamType != "" {
		path += "?type=" + pathEscape(streamType)
	}
	var out StreamListAPI
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.List, nil
}

// CreateStream creates an empty stream. Streams that already exist (because
// data was ingested into them) are reported via isAlreadyExists.
func (c *Client) CreateStream(ctx context.Context, orgID, streamType, name string, req CreateStreamAPI) error {
	path := fmt.Sprintf("/api/%s/streams/%s?type=%s", pathEscape(orgID), pathEscape(name), pathEscape(streamType))
	if req.Fields == nil {
		req.Fields = []StreamFieldAPI{}
	}
	if req.Settings == nil {
		req.Settings = map[string]any{}
	}
	return c.do(ctx, http.MethodPost, path, req)
}

// UpdateStreamSettings applies a settings delta to an existing stream.
func (c *Client) UpdateStreamSettings(ctx context.Context, orgID, streamType, name string, req UpdateStreamSettingsAPI) error {
	path := fmt.Sprintf("/api/%s/streams/%s/settings?type=%s", pathEscape(orgID), pathEscape(name), pathEscape(streamType))
	return c.do(ctx, http.MethodPut, path, req)
}

// DeleteStream removes a stream and its data.
//
// Stream deletion is asynchronous: a stream already queued for deletion answers
// with "is being deleted" rather than 404. That is the outcome the caller asked
// for, so it counts as success and a retried destroy does not fail.
//
// The name is resolved against the server before deleting, because OpenObserve
// normalizes stream names on some endpoints and not others. Creating
// `live-test-stream` stores `live_test_stream`, and every read path
// (schema, settings, update) normalizes the name it is given, so the raw name
// keeps working. The delete handler does not normalize, so deleting by the raw
// name answers 404 for a stream that plainly exists.
//
// Tolerating that 404 as "already gone" is what made this silent: a destroy
// reported success and left the stream and its data in place. Resolving the
// name first means a 404 is only ever tolerated once a read has confirmed the
// stream really is absent. See openobserve/terraform-provider-openobserve#1.
func (c *Client) DeleteStream(ctx context.Context, orgID, streamType, name string) error {
	target := name
	switch stream, err := c.GetStream(ctx, orgID, streamType, name); {
	case err != nil:
		return err
	case stream == nil:
		// Genuinely absent through the normalizing read path, so there is
		// nothing to delete and a retried destroy stays successful.
		return nil
	case stream.Name != "":
		target = stream.Name
	}

	path := fmt.Sprintf("/api/%s/streams/%s?type=%s", pathEscape(orgID), pathEscape(target), pathEscape(streamType))
	err := c.deleteIgnoreMissing(ctx, path)
	if err != nil && isBeingDeleted(err) {
		return nil
	}
	return err
}

// isBeingDeleted reports whether err says the object is already on its way out.
func isBeingDeleted(err error) bool {
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		return strings.Contains(apiErr.Body, "is being deleted")
	}
	return false
}
