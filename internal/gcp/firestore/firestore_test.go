package firestore

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/altocirrus/altocirrus/internal/config"
	"github.com/altocirrus/altocirrus/internal/storage"
)

func setup() *http.ServeMux {
	store := storage.NewMemoryStore()
	cfg := config.Load()
	mux := http.NewServeMux()
	RegisterRoutes(mux, store, cfg)
	return mux
}

func doReq(mux http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

const basePath = "/v1/projects/test-project/databases/(default)/documents"

func strVal(s string) Value {
	return Value{StringValue: &s}
}

func intVal(n string) Value {
	return Value{IntegerValue: &n}
}

func boolVal(b bool) Value {
	return Value{BooleanValue: &b}
}

func TestDocumentCRUD(t *testing.T) {
	mux := setup()

	// Create
	w := doReq(mux, "POST", basePath+"/users?documentId=user1", createDocRequest{
		Fields: map[string]Value{"name": strVal("Alice"), "age": intVal("30")},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("create: want 200, got %d: %s", w.Code, w.Body.String())
	}
	var doc FirestoreDocument
	json.Unmarshal(w.Body.Bytes(), &doc)
	if doc.Name != "projects/test-project/databases/(default)/documents/users/user1" {
		t.Fatalf("name: got %s", doc.Name)
	}
	if doc.Fields["name"].StringValue == nil || *doc.Fields["name"].StringValue != "Alice" {
		t.Fatal("field name mismatch")
	}

	// Duplicate
	w = doReq(mux, "POST", basePath+"/users?documentId=user1", createDocRequest{
		Fields: map[string]Value{"name": strVal("Bob")},
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("dup: want 409, got %d", w.Code)
	}

	// Auto-ID
	w = doReq(mux, "POST", basePath+"/users", createDocRequest{
		Fields: map[string]Value{"name": strVal("NoID")},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("auto-id: want 200, got %d", w.Code)
	}

	// Get
	w = doReq(mux, "GET", basePath+"/users/user1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get: want 200, got %d", w.Code)
	}

	// Get missing
	w = doReq(mux, "GET", basePath+"/users/nope", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("get missing: want 404, got %d", w.Code)
	}

	// List
	w = doReq(mux, "GET", basePath+"/users", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list: want 200, got %d", w.Code)
	}
	var listResp map[string]any
	json.Unmarshal(w.Body.Bytes(), &listResp)
	docs := listResp["documents"].([]any)
	if len(docs) != 2 {
		t.Fatalf("list count: want 2, got %d", len(docs))
	}

	// Update (PATCH)
	w = doReq(mux, "PATCH", basePath+"/users/user1", createDocRequest{
		Fields: map[string]Value{"age": intVal("31"), "city": strVal("NYC")},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("update: want 200, got %d", w.Code)
	}
	json.Unmarshal(w.Body.Bytes(), &doc)
	if doc.Fields["name"].StringValue == nil || *doc.Fields["name"].StringValue != "Alice" {
		t.Fatal("update clobbered name")
	}
	if doc.Fields["city"].StringValue == nil || *doc.Fields["city"].StringValue != "NYC" {
		t.Fatal("update didn't add city")
	}

	// Delete
	w = doReq(mux, "DELETE", basePath+"/users/user1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("delete: want 200, got %d", w.Code)
	}

	// Get after delete
	w = doReq(mux, "GET", basePath+"/users/user1", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("get deleted: want 404, got %d", w.Code)
	}
}

func TestRunQuery(t *testing.T) {
	mux := setup()

	// Seed data
	doReq(mux, "POST", basePath+"/items?documentId=i1", createDocRequest{
		Fields: map[string]Value{"status": strVal("active"), "priority": intVal("1")},
	})
	doReq(mux, "POST", basePath+"/items?documentId=i2", createDocRequest{
		Fields: map[string]Value{"status": strVal("done"), "priority": intVal("2")},
	})
	doReq(mux, "POST", basePath+"/items?documentId=i3", createDocRequest{
		Fields: map[string]Value{"status": strVal("active"), "priority": intVal("3")},
	})

	// Simple field filter
	w := doReq(mux, "POST", basePath+":runQuery", runQueryRequest{
		StructuredQuery: &structuredQuery{
			From: []collectionSelector{{CollectionID: "items"}},
			Where: &queryFilter{
				FieldFilter: &fieldFilter{
					Field: fieldReference{FieldPath: "status"},
					Op:    "EQUAL",
					Value: strVal("active"),
				},
			},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("query: want 200, got %d: %s", w.Code, w.Body.String())
	}
	var results []map[string]any
	json.Unmarshal(w.Body.Bytes(), &results)
	if len(results) != 2 {
		t.Fatalf("query count: want 2, got %d", len(results))
	}

	// Composite AND filter
	w = doReq(mux, "POST", basePath+":runQuery", runQueryRequest{
		StructuredQuery: &structuredQuery{
			From: []collectionSelector{{CollectionID: "items"}},
			Where: &queryFilter{
				CompositeFilter: &compositeFilter{
					Op: "AND",
					Filters: []queryFilter{
						{FieldFilter: &fieldFilter{
							Field: fieldReference{FieldPath: "status"},
							Op:    "EQUAL",
							Value: strVal("active"),
						}},
						{FieldFilter: &fieldFilter{
							Field: fieldReference{FieldPath: "priority"},
							Op:    "EQUAL",
							Value: intVal("1"),
						}},
					},
				},
			},
		},
	})
	json.Unmarshal(w.Body.Bytes(), &results)
	if len(results) != 1 {
		t.Fatalf("composite query count: want 1, got %d", len(results))
	}

	// No filter (all docs)
	w = doReq(mux, "POST", basePath+":runQuery", runQueryRequest{
		StructuredQuery: &structuredQuery{
			From: []collectionSelector{{CollectionID: "items"}},
		},
	})
	json.Unmarshal(w.Body.Bytes(), &results)
	if len(results) != 3 {
		t.Fatalf("no-filter query: want 3, got %d", len(results))
	}
}

func TestBatchGet(t *testing.T) {
	mux := setup()

	doReq(mux, "POST", basePath+"/things?documentId=t1", createDocRequest{
		Fields: map[string]Value{"x": strVal("1")},
	})
	doReq(mux, "POST", basePath+"/things?documentId=t2", createDocRequest{
		Fields: map[string]Value{"x": strVal("2")},
	})

	w := doReq(mux, "POST", basePath+":batchGet", batchGetRequest{
		Documents: []string{
			"projects/test-project/databases/(default)/documents/things/t1",
			"projects/test-project/databases/(default)/documents/things/t2",
			"projects/test-project/databases/(default)/documents/things/t999",
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("batchGet: want 200, got %d: %s", w.Code, w.Body.String())
	}
	var results []map[string]any
	json.Unmarshal(w.Body.Bytes(), &results)
	if len(results) != 3 {
		t.Fatalf("batchGet count: want 3, got %d", len(results))
	}

	found := 0
	missing := 0
	for _, r := range results {
		if _, ok := r["found"]; ok {
			found++
		}
		if _, ok := r["missing"]; ok {
			missing++
		}
	}
	if found != 2 || missing != 1 {
		t.Fatalf("batchGet: want 2 found + 1 missing, got %d found + %d missing", found, missing)
	}
}

func TestValueTypes(t *testing.T) {
	mux := setup()

	dv := 3.14
	w := doReq(mux, "POST", basePath+"/typed?documentId=tv1", createDocRequest{
		Fields: map[string]Value{
			"str":  strVal("hello"),
			"num":  intVal("42"),
			"flag": boolVal(true),
			"pi":   {DoubleValue: &dv},
			"nil":  {NullValue: json.RawMessage("null")},
			"ts":   {TimestampValue: ptr("2024-01-01T00:00:00Z")},
			"list": {ArrayValue: &ArrayValue{Values: []Value{strVal("a"), strVal("b")}}},
			"obj":  {MapValue: &MapValue{Fields: map[string]Value{"nested": strVal("yes")}}},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("typed create: want 200, got %d: %s", w.Code, w.Body.String())
	}

	w = doReq(mux, "GET", basePath+"/typed/tv1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("typed get: want 200, got %d", w.Code)
	}

	var doc FirestoreDocument
	json.Unmarshal(w.Body.Bytes(), &doc)

	if doc.Fields["str"].StringValue == nil || *doc.Fields["str"].StringValue != "hello" {
		t.Fatal("stringValue roundtrip failed")
	}
	if doc.Fields["num"].IntegerValue == nil || *doc.Fields["num"].IntegerValue != "42" {
		t.Fatal("integerValue roundtrip failed")
	}
	if doc.Fields["flag"].BooleanValue == nil || *doc.Fields["flag"].BooleanValue != true {
		t.Fatal("booleanValue roundtrip failed")
	}
	if doc.Fields["pi"].DoubleValue == nil || *doc.Fields["pi"].DoubleValue != 3.14 {
		t.Fatal("doubleValue roundtrip failed")
	}
	if len(doc.Fields["nil"].NullValue) == 0 {
		t.Fatal("nullValue roundtrip failed")
	}
	if doc.Fields["list"].ArrayValue == nil || len(doc.Fields["list"].ArrayValue.Values) != 2 {
		t.Fatal("arrayValue roundtrip failed")
	}
	if doc.Fields["obj"].MapValue == nil {
		t.Fatal("mapValue roundtrip failed")
	}
}

func ptr(s string) *string { return &s }
