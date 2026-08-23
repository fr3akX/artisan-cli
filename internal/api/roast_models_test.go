package api

import (
	"encoding/json"
	"strings"
	"testing"
)

const (
	roastUUID       = "aaaaaaaaaaaa4aaa8aaaaaaaaaaaaaaa"
	commentUUID     = "bbbbbbbbbbbb4bbb8bbbbbbbbbbbbbbb"
	labelUUID       = "cccccccccccc4ccc8ccccccccccccccc"
	roastSHA256     = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	roastTimestamp  = "2026-08-23T12:34:56.123456+00:00"
	roastTimestamp2 = "2026-08-23T12:35:56.123456+00:00"
)

func validRoastLabelJSON() string {
	return `{"label_uuid":"` + labelUUID + `","name":"Review","color":"violet","archived":false}`
}

func validRoastRevisionJSON() string {
	return `{
		"revision_number":1,"sha256":"` + roastSHA256 + `","byte_size":1234,
		"parser_version":"artisan-4-v1","parse_state":"parsed","parse_diagnostic_code":null,
		"parse_diagnostic_message":null,"uploaded_at":"` + roastTimestamp + `",
		"metadata":{"nested":{"safe":true}},"reparse_recommended":false
	}`
}

func validRoastListItemJSON() string {
	return `{
		"roast_uuid":"` + roastUUID + `","state":"parsed","roast_at":"` + roastTimestamp + `",
		"title":"Review roast","batch_prefix":"B","batch_number":7,"batch_position":2,
		"operator":"Operator","machine":"Loring","machine_setup":"S70",
		"temperature_unit":"C","duration_seconds":720.5,"green_weight_kg":1.25,
		"roasted_weight_kg":1.05,"revision_count":1,"updated_at":"` + roastTimestamp2 + `",
		"labels":[` + validRoastLabelJSON() + `]
	}`
}

func validRoastDetailJSON() string {
	return strings.TrimSuffix(validRoastListItemJSON(), "}") + `,
		"current_metadata":{"notes":{"roast":"private"}},
		"current_revision":` + validRoastRevisionJSON() + `,
		"links":{"self":"/api/v1/roasts/` + roastUUID + `","chart":"/api/v1/roasts/` + roastUUID + `/chart","revisions":"/api/v1/roasts/` + roastUUID + `/revisions"}
	}`
}

func validDeletedCommentJSON() string {
	return `{
		"comment_uuid":"` + commentUUID + `","roast_uuid":"` + roastUUID + `",
		"author_nickname":"Member","body":null,"created_at":"` + roastTimestamp + `",
		"edited_at":null,"deleted_at":"` + roastTimestamp2 + `","is_deleted":true,
		"can_edit":false,"can_delete":false
	}`
}

func TestRoastModelsDecodeCompleteServerFixturesAndExactJSONNames(t *testing.T) {
	payload := strings.TrimSuffix(validRoastDetailJSON(), "}") + `,"future_field":{"ignored":true}}`
	var detail RoastDetail
	if err := decodeOneJSON([]byte(payload), &detail); err != nil {
		t.Fatalf("decodeOneJSON(detail) error = %v", err)
	}
	if detail.RoastUUID != roastUUID || detail.CurrentRevision == nil || detail.CurrentRevision.RevisionNumber != 1 || string(detail.CurrentMetadata) != `{"notes":{"roast":"private"}}` {
		t.Fatalf("detail = %#v", detail)
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("json.Marshal(detail) error = %v", err)
	}
	for _, name := range []string{`"current_metadata"`, `"current_revision"`, `"reparse_recommended"`} {
		if !strings.Contains(string(encoded), name) {
			t.Fatalf("encoded detail missing %s: %s", name, encoded)
		}
	}
	if strings.Contains(string(encoded), "future_field") {
		t.Fatalf("unknown field retained: %s", encoded)
	}

	var comment CommentView
	if err := decodeOneJSON([]byte(validDeletedCommentJSON()), &comment); err != nil {
		t.Fatalf("decodeOneJSON(comment) error = %v", err)
	}
	encoded, err = json.Marshal(comment)
	if err != nil {
		t.Fatalf("json.Marshal(comment) error = %v", err)
	}
	for _, name := range []string{`"can_edit"`, `"can_delete"`} {
		if !strings.Contains(string(encoded), name) {
			t.Fatalf("encoded comment missing %s: %s", name, encoded)
		}
	}
}

func TestRoastModelsRejectMissingNullAndWrongRequiredFields(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		target  func() any
	}{
		{name: "list missing state", payload: removeRoastJSONField(t, validRoastListItemJSON(), "state"), target: func() any { return &RoastListItem{} }},
		{name: "list null uuid", payload: replaceRoastJSONField(t, validRoastListItemJSON(), "roast_uuid", nil), target: func() any { return &RoastListItem{} }},
		{name: "list wrong revision count", payload: replaceRoastJSONField(t, validRoastListItemJSON(), "revision_count", "1"), target: func() any { return &RoastListItem{} }},
		{name: "list wrong labels", payload: replaceRoastJSONField(t, validRoastListItemJSON(), "labels", true), target: func() any { return &RoastListItem{} }},
		{name: "detail missing metadata", payload: removeRoastJSONField(t, validRoastDetailJSON(), "current_metadata"), target: func() any { return &RoastDetail{} }},
		{name: "detail null metadata", payload: replaceRoastJSONField(t, validRoastDetailJSON(), "current_metadata", nil), target: func() any { return &RoastDetail{} }},
		{name: "detail null links", payload: replaceRoastJSONField(t, validRoastDetailJSON(), "links", nil), target: func() any { return &RoastDetail{} }},
		{name: "revision missing reparse", payload: removeRoastJSONField(t, validRoastRevisionJSON(), "reparse_recommended"), target: func() any { return &RoastRevision{} }},
		{name: "revision wrong reparse", payload: replaceRoastJSONField(t, validRoastRevisionJSON(), "reparse_recommended", 0), target: func() any { return &RoastRevision{} }},
		{name: "comment missing body", payload: removeRoastJSONField(t, validDeletedCommentJSON(), "body"), target: func() any { return &CommentView{} }},
		{name: "comment wrong permission", payload: replaceRoastJSONField(t, validDeletedCommentJSON(), "can_edit", "false"), target: func() any { return &CommentView{} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := json.Unmarshal([]byte(tt.payload), tt.target()); err == nil {
				t.Fatalf("accepted malformed payload: %s", tt.payload)
			}
		})
	}
}

func TestRoastModelsRejectInvalidIdentifiersEnumsNumbersAndTimestamps(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		target  func() any
	}{
		{name: "dashed response roast UUID", payload: strings.Replace(validRoastListItemJSON(), roastUUID, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", 1), target: func() any { return &RoastListItem{} }},
		{name: "uppercase response roast UUID", payload: strings.Replace(validRoastListItemJSON(), roastUUID, strings.ToUpper(roastUUID), 1), target: func() any { return &RoastListItem{} }},
		{name: "versionless response roast UUID", payload: strings.Replace(validRoastListItemJSON(), roastUUID, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 1), target: func() any { return &RoastListItem{} }},
		{name: "invalid roast state", payload: strings.Replace(validRoastListItemJSON(), `"state":"parsed"`, `"state":"deleted"`, 1), target: func() any { return &RoastListItem{} }},
		{name: "invalid temperature unit", payload: strings.Replace(validRoastListItemJSON(), `"temperature_unit":"C"`, `"temperature_unit":"K"`, 1), target: func() any { return &RoastListItem{} }},
		{name: "negative revision count", payload: strings.Replace(validRoastListItemJSON(), `"revision_count":1`, `"revision_count":-1`, 1), target: func() any { return &RoastListItem{} }},
		{name: "awaiting with revision count", payload: strings.Replace(validRoastListItemJSON(), `"state":"parsed"`, `"state":"awaiting_profile"`, 1), target: func() any { return &RoastListItem{} }},
		{name: "invalid list timestamp", payload: strings.Replace(validRoastListItemJSON(), roastTimestamp2, "2026-08-23 12:35:56", 1), target: func() any { return &RoastListItem{} }},
		{name: "invalid sha", payload: strings.Replace(validRoastRevisionJSON(), roastSHA256, strings.Repeat("g", 64), 1), target: func() any { return &RoastRevision{} }},
		{name: "uppercase sha", payload: strings.Replace(validRoastRevisionJSON(), roastSHA256, strings.ToUpper(roastSHA256), 1), target: func() any { return &RoastRevision{} }},
		{name: "zero revision number", payload: strings.Replace(validRoastRevisionJSON(), `"revision_number":1`, `"revision_number":0`, 1), target: func() any { return &RoastRevision{} }},
		{name: "oversized revision number", payload: strings.Replace(validRoastRevisionJSON(), `"revision_number":1`, `"revision_number":2147483648`, 1), target: func() any { return &RoastRevision{} }},
		{name: "zero byte size", payload: strings.Replace(validRoastRevisionJSON(), `"byte_size":1234`, `"byte_size":0`, 1), target: func() any { return &RoastRevision{} }},
		{name: "invalid parse state", payload: strings.Replace(validRoastRevisionJSON(), `"parse_state":"parsed"`, `"parse_state":"pending"`, 1), target: func() any { return &RoastRevision{} }},
		{name: "invalid revision timestamp", payload: strings.Replace(validRoastRevisionJSON(), roastTimestamp, "2026-08-23T12:34:56", 1), target: func() any { return &RoastRevision{} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := json.Unmarshal([]byte(tt.payload), tt.target()); err == nil {
				t.Fatalf("accepted malformed payload: %s", tt.payload)
			}
		})
	}
}

func TestRoastDetailRejectsStateCountAndCurrentRevisionIncoherence(t *testing.T) {
	awaiting := validRoastDetailJSON()
	awaiting = strings.Replace(awaiting, `"state":"parsed"`, `"state":"awaiting_profile"`, 1)
	awaiting = strings.Replace(awaiting, `"revision_count":1`, `"revision_count":0`, 1)
	awaiting = replaceRoastJSONField(t, awaiting, "current_revision", nil)
	if err := json.Unmarshal([]byte(awaiting), &RoastDetail{}); err != nil {
		t.Fatalf("valid awaiting detail rejected: %v", err)
	}
	for _, payload := range []string{
		strings.Replace(validRoastDetailJSON(), `"revision_count":1`, `"revision_count":2`, 1),
		strings.Replace(validRoastDetailJSON(), `"current_revision":`+validRoastRevisionJSON(), `"current_revision":null`, 1),
		strings.Replace(awaiting, `"revision_count":0`, `"revision_count":1`, 1),
		strings.Replace(awaiting, `"current_revision":null`, `"current_revision":`+validRoastRevisionJSON(), 1),
	} {
		if err := json.Unmarshal([]byte(payload), &RoastDetail{}); err == nil {
			t.Fatalf("accepted incoherent detail: %s", payload)
		}
	}
}

func TestRoastModelsRequireObjectMetadataAndLinksAndRejectNullArrayElements(t *testing.T) {
	for _, payload := range []string{
		replaceRoastJSONField(t, validRoastRevisionJSON(), "metadata", []any{}),
		replaceRoastJSONField(t, validRoastRevisionJSON(), "metadata", "object"),
		replaceRoastJSONField(t, validRoastDetailJSON(), "current_metadata", []any{}),
		replaceRoastJSONField(t, validRoastDetailJSON(), "links", []any{}),
		strings.Replace(validRoastListItemJSON(), `"labels":[`+validRoastLabelJSON()+`]`, `"labels":[null]`, 1),
	} {
		var target any = &RoastDetail{}
		if strings.Contains(payload, `"revision_number"`) && !strings.Contains(payload, `"roast_uuid"`) {
			target = &RoastRevision{}
		} else if strings.Contains(payload, `"labels":[null]`) {
			target = &RoastListItem{}
		}
		if err := json.Unmarshal([]byte(payload), target); err == nil {
			t.Fatalf("accepted malformed object/array: %s", payload)
		}
	}
	for _, page := range []any{&RoastPage{}, &RoastRevisionPage{}, &CommentPage{}} {
		if err := json.Unmarshal([]byte(`{"items":[null],"next_cursor":null}`), page); err == nil {
			t.Fatalf("accepted null page element for %T", page)
		}
	}
}

func TestCommentModelRejectsIdentityDeletionAndPermissionIncoherence(t *testing.T) {
	active := strings.Replace(validDeletedCommentJSON(), `"body":null`, `"body":"Evidence"`, 1)
	active = strings.Replace(active, `"deleted_at":"`+roastTimestamp2+`"`, `"deleted_at":null`, 1)
	active = strings.Replace(active, `"is_deleted":true`, `"is_deleted":false`, 1)
	if err := json.Unmarshal([]byte(active), &CommentView{}); err != nil {
		t.Fatalf("valid active comment rejected: %v", err)
	}
	for _, payload := range []string{
		strings.Replace(validDeletedCommentJSON(), `"body":null`, `"body":"deleted text"`, 1),
		strings.Replace(validDeletedCommentJSON(), `"deleted_at":"`+roastTimestamp2+`"`, `"deleted_at":null`, 1),
		strings.Replace(active, `"body":"Evidence"`, `"body":null`, 1),
		strings.Replace(active, `"can_edit":false,"can_delete":false`, `"can_edit":true,"can_delete":false`, 1),
		strings.Replace(active, `"can_edit":false,"can_delete":false`, `"can_edit":false,"can_delete":true`, 1),
		strings.Replace(validDeletedCommentJSON(), `"can_edit":false`, `"can_edit":true`, 1),
	} {
		var comment CommentView
		if err := json.Unmarshal([]byte(payload), &comment); err == nil {
			t.Fatalf("accepted incoherent comment: %s", payload)
		}
	}
}

func TestRoastAndCommentModelsRejectMultipleDocumentsAndInvalidSurrogates(t *testing.T) {
	for _, test := range []struct {
		payload string
		target  any
	}{
		{payload: validRoastDetailJSON() + `{}`, target: &RoastDetail{}},
		{payload: validDeletedCommentJSON() + `{}`, target: &CommentView{}},
		{payload: strings.Replace(validRoastListItemJSON(), "Review roast", `bad\ud800`, 1), target: &RoastListItem{}},
		{payload: strings.Replace(validDeletedCommentJSON(), "Member", `bad\udc00`, 1), target: &CommentView{}},
	} {
		if err := decodeOneJSON([]byte(test.payload), test.target); err == nil {
			t.Fatalf("accepted malformed JSON: %s", test.payload)
		}
	}
}

func removeRoastJSONField(t *testing.T, payload, field string) string {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal([]byte(payload), &object); err != nil {
		t.Fatalf("fixture decode: %v", err)
	}
	if _, ok := object[field]; !ok {
		t.Fatalf("fixture missing field %q", field)
	}
	delete(object, field)
	encoded, err := json.Marshal(object)
	if err != nil {
		t.Fatalf("fixture encode: %v", err)
	}
	return string(encoded)
}

func replaceRoastJSONField(t *testing.T, payload, field string, value any) string {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal([]byte(payload), &object); err != nil {
		t.Fatalf("fixture decode: %v", err)
	}
	if _, ok := object[field]; !ok {
		t.Fatalf("fixture missing field %q", field)
	}
	object[field] = value
	encoded, err := json.Marshal(object)
	if err != nil {
		t.Fatalf("fixture encode: %v", err)
	}
	return string(encoded)
}
