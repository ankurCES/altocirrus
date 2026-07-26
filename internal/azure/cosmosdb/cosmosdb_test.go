package cosmosdb

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/altocirrus/altocirrus/internal/config"
	"github.com/altocirrus/altocirrus/internal/storage"
)

func setup() (*http.ServeMux, storage.Store) {
	store := storage.NewMemoryStore()
	cfg := config.Load()
	mux := http.NewServeMux()
	RegisterRoutes(mux, store, cfg)
	return mux, store
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

func doQuery(mux http.Handler, path string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(body)
	req := httptest.NewRequest("POST", path, &buf)
	req.Header.Set("Content-Type", "application/query+json")
	req.Header.Set("x-ms-documentdb-isquery", "true")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func TestDatabaseCRUD(t *testing.T) {
	mux, _ := setup()

	// Create
	w := doReq(mux, "POST", "/dbs", map[string]string{"id": "testdb"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create db: want 201, got %d: %s", w.Code, w.Body.String())
	}
	var db Database
	json.Unmarshal(w.Body.Bytes(), &db)
	if db.ID != "testdb" {
		t.Fatalf("db id: want testdb, got %s", db.ID)
	}

	// Duplicate
	w = doReq(mux, "POST", "/dbs", map[string]string{"id": "testdb"})
	if w.Code != http.StatusConflict {
		t.Fatalf("dup db: want 409, got %d", w.Code)
	}

	// Get
	w = doReq(mux, "GET", "/dbs/testdb", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get db: want 200, got %d", w.Code)
	}

	// List
	doReq(mux, "POST", "/dbs", map[string]string{"id": "testdb2"})
	w = doReq(mux, "GET", "/dbs", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list dbs: want 200, got %d", w.Code)
	}
	var listResp map[string]any
	json.Unmarshal(w.Body.Bytes(), &listResp)
	count := listResp["_count"].(float64)
	if count != 2 {
		t.Fatalf("list count: want 2, got %.0f", count)
	}

	// Delete
	w = doReq(mux, "DELETE", "/dbs/testdb", nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete db: want 204, got %d", w.Code)
	}

	// Get after delete
	w = doReq(mux, "GET", "/dbs/testdb", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("get deleted db: want 404, got %d", w.Code)
	}
}

func TestContainerCRUD(t *testing.T) {
	mux, _ := setup()
	doReq(mux, "POST", "/dbs", map[string]string{"id": "mydb"})

	// Create container
	w := doReq(mux, "POST", "/dbs/mydb/colls", map[string]any{
		"id":           "mycoll",
		"partitionKey": map[string]any{"paths": []string{"/category"}, "kind": "Hash"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create coll: want 201, got %d: %s", w.Code, w.Body.String())
	}

	// Duplicate
	w = doReq(mux, "POST", "/dbs/mydb/colls", map[string]any{"id": "mycoll"})
	if w.Code != http.StatusConflict {
		t.Fatalf("dup coll: want 409, got %d", w.Code)
	}

	// Get
	w = doReq(mux, "GET", "/dbs/mydb/colls/mycoll", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get coll: want 200, got %d", w.Code)
	}

	// List
	w = doReq(mux, "GET", "/dbs/mydb/colls", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list colls: want 200, got %d", w.Code)
	}

	// Delete
	w = doReq(mux, "DELETE", "/dbs/mydb/colls/mycoll", nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete coll: want 204, got %d", w.Code)
	}

	// Container on nonexistent db
	w = doReq(mux, "POST", "/dbs/nope/colls", map[string]any{"id": "x"})
	if w.Code != http.StatusNotFound {
		t.Fatalf("coll on missing db: want 404, got %d", w.Code)
	}
}

func TestDocumentCRUD(t *testing.T) {
	mux, _ := setup()
	doReq(mux, "POST", "/dbs", map[string]string{"id": "db1"})
	doReq(mux, "POST", "/dbs/db1/colls", map[string]any{"id": "coll1"})

	// Create doc
	w := doReq(mux, "POST", "/dbs/db1/colls/coll1/docs", map[string]any{
		"id": "doc1", "name": "Alice", "age": 30,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create doc: want 201, got %d: %s", w.Code, w.Body.String())
	}
	var doc map[string]any
	json.Unmarshal(w.Body.Bytes(), &doc)
	if doc["_rid"] == nil || doc["_ts"] == nil || doc["_etag"] == nil {
		t.Fatal("doc missing metadata fields")
	}

	// Duplicate
	w = doReq(mux, "POST", "/dbs/db1/colls/coll1/docs", map[string]any{"id": "doc1"})
	if w.Code != http.StatusConflict {
		t.Fatalf("dup doc: want 409, got %d", w.Code)
	}

	// Auto-ID
	w = doReq(mux, "POST", "/dbs/db1/colls/coll1/docs", map[string]any{"name": "NoID"})
	if w.Code != http.StatusCreated {
		t.Fatalf("auto-id doc: want 201, got %d", w.Code)
	}

	// Get
	w = doReq(mux, "GET", "/dbs/db1/colls/coll1/docs/doc1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get doc: want 200, got %d", w.Code)
	}

	// List
	w = doReq(mux, "GET", "/dbs/db1/colls/coll1/docs", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list docs: want 200, got %d", w.Code)
	}

	// Replace
	w = doReq(mux, "PUT", "/dbs/db1/colls/coll1/docs/doc1", map[string]any{
		"name": "Bob", "age": 25,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("replace doc: want 200, got %d", w.Code)
	}
	json.Unmarshal(w.Body.Bytes(), &doc)
	if doc["name"] != "Bob" {
		t.Fatalf("replace name: want Bob, got %v", doc["name"])
	}

	// Patch
	w = doReq(mux, "PATCH", "/dbs/db1/colls/coll1/docs/doc1", map[string]any{
		"age": 26,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("patch doc: want 200, got %d", w.Code)
	}
	json.Unmarshal(w.Body.Bytes(), &doc)
	if doc["name"] != "Bob" {
		t.Fatal("patch clobbered name")
	}
	if doc["age"].(float64) != 26 {
		t.Fatalf("patch age: want 26, got %v", doc["age"])
	}

	// Delete
	w = doReq(mux, "DELETE", "/dbs/db1/colls/coll1/docs/doc1", nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete doc: want 204, got %d", w.Code)
	}

	// Get after delete
	w = doReq(mux, "GET", "/dbs/db1/colls/coll1/docs/doc1", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("get deleted doc: want 404, got %d", w.Code)
	}
}

func TestQueryDocs(t *testing.T) {
	mux, _ := setup()
	doReq(mux, "POST", "/dbs", map[string]string{"id": "qdb"})
	doReq(mux, "POST", "/dbs/qdb/colls", map[string]any{"id": "qcoll"})

	doReq(mux, "POST", "/dbs/qdb/colls/qcoll/docs", map[string]any{"id": "1", "status": "active", "val": 10})
	doReq(mux, "POST", "/dbs/qdb/colls/qcoll/docs", map[string]any{"id": "2", "status": "inactive", "val": 20})
	doReq(mux, "POST", "/dbs/qdb/colls/qcoll/docs", map[string]any{"id": "3", "status": "active", "val": 30})

	// SELECT * FROM c (all docs)
	w := doQuery(mux, "/dbs/qdb/colls/qcoll/docs", queryRequest{
		Query: "SELECT * FROM c",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("query all: want 200, got %d", w.Code)
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["_count"].(float64) != 3 {
		t.Fatalf("query all count: want 3, got %.0f", resp["_count"].(float64))
	}

	// WHERE with parameter
	w = doQuery(mux, "/dbs/qdb/colls/qcoll/docs", queryRequest{
		Query:      `SELECT * FROM c WHERE c.status = @s`,
		Parameters: []queryParameter{{Name: "@s", Value: "active"}},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("query param: want 200, got %d", w.Code)
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["_count"].(float64) != 2 {
		t.Fatalf("query param count: want 2, got %.0f", resp["_count"].(float64))
	}

	// WHERE with literal
	w = doQuery(mux, "/dbs/qdb/colls/qcoll/docs", queryRequest{
		Query: `SELECT * FROM c WHERE c.status = "inactive"`,
	})
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["_count"].(float64) != 1 {
		t.Fatalf("query literal count: want 1, got %.0f", resp["_count"].(float64))
	}
}

func TestResponseHeaders(t *testing.T) {
	mux, _ := setup()
	w := doReq(mux, "POST", "/dbs", map[string]string{"id": "hdb"})
	if w.Header().Get("x-ms-request-charge") == "" {
		t.Fatal("missing x-ms-request-charge header")
	}
	if w.Header().Get("x-ms-session-token") == "" {
		t.Fatal("missing x-ms-session-token header")
	}
	if w.Header().Get("x-ms-activity-id") == "" {
		t.Fatal("missing x-ms-activity-id header")
	}
}

func TestCascadeDelete(t *testing.T) {
	mux, store := setup()
	doReq(mux, "POST", "/dbs", map[string]string{"id": "cdb"})
	doReq(mux, "POST", "/dbs/cdb/colls", map[string]any{"id": "cc"})
	doReq(mux, "POST", "/dbs/cdb/colls/cc/docs", map[string]any{"id": "d1"})

	// Delete DB cascades containers + docs
	doReq(mux, "DELETE", "/dbs/cdb", nil)

	if keys := store.List("azure:cosmosdb:colls", "cdb/"); len(keys) != 0 {
		t.Fatalf("cascade: containers not deleted: %v", keys)
	}
	if keys := store.List("azure:cosmosdb:docs", "cdb/"); len(keys) != 0 {
		t.Fatalf("cascade: docs not deleted: %v", keys)
	}
}
