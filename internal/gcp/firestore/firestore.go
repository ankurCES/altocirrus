package firestore

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/altocirrus/altocirrus/internal/config"
	"github.com/altocirrus/altocirrus/internal/server"
	"github.com/altocirrus/altocirrus/internal/storage"
)

const nsDocs = "gcp:firestore:docs"

// ---------------------------------------------------------------------------
// Models — Firestore REST v1 wire format
// ---------------------------------------------------------------------------

// Value represents a Firestore typed value.
type Value struct {
	// ponytail: json.RawMessage survives null roundtrip; *interface{} doesn't
	NullValue      json.RawMessage    `json:"nullValue,omitempty"`
	BooleanValue   *bool              `json:"booleanValue,omitempty"`
	IntegerValue   *string            `json:"integerValue,omitempty"`
	DoubleValue    *float64           `json:"doubleValue,omitempty"`
	TimestampValue *string            `json:"timestampValue,omitempty"`
	StringValue    *string            `json:"stringValue,omitempty"`
	MapValue       *MapValue          `json:"mapValue,omitempty"`
	ArrayValue     *ArrayValue        `json:"arrayValue,omitempty"`
}

type MapValue struct {
	Fields map[string]Value `json:"fields"`
}

type ArrayValue struct {
	Values []Value `json:"values"`
}

// FirestoreDocument is the REST response for a document.
type FirestoreDocument struct {
	Name       string           `json:"name"`
	Fields     map[string]Value `json:"fields"`
	CreateTime string           `json:"createTime"`
	UpdateTime string           `json:"updateTime"`
}

// ---------------------------------------------------------------------------
// Request bodies
// ---------------------------------------------------------------------------

type createDocRequest struct {
	Fields map[string]Value `json:"fields"`
}

type runQueryRequest struct {
	StructuredQuery *structuredQuery `json:"structuredQuery"`
}

type structuredQuery struct {
	From  []collectionSelector `json:"from"`
	Where *queryFilter         `json:"where,omitempty"`
}

type collectionSelector struct {
	CollectionID string `json:"collectionId"`
}

type queryFilter struct {
	FieldFilter    *fieldFilter    `json:"fieldFilter,omitempty"`
	CompositeFilter *compositeFilter `json:"compositeFilter,omitempty"`
}

type fieldFilter struct {
	Field fieldReference `json:"field"`
	Op    string         `json:"op"`
	Value Value          `json:"value"`
}

type compositeFilter struct {
	Op      string        `json:"op"`
	Filters []queryFilter `json:"filters"`
}

type fieldReference struct {
	FieldPath string `json:"fieldPath"`
}

type batchGetRequest struct {
	Documents []string `json:"documents"`
}

// ---------------------------------------------------------------------------
// RegisterRoutes
// ---------------------------------------------------------------------------

func RegisterRoutes(mux *http.ServeMux, store storage.Store, cfg *config.Config) {
	h := &handler{store: store, cfg: cfg}

	// Document CRUD
	mux.HandleFunc("POST /v1/projects/{project}/databases/{db}/documents/{collection}", h.createDoc)
	mux.HandleFunc("GET /v1/projects/{project}/databases/{db}/documents/{collection}/{docId}", h.getDoc)
	mux.HandleFunc("GET /v1/projects/{project}/databases/{db}/documents/{collection}", h.listDocs)
	mux.HandleFunc("PATCH /v1/projects/{project}/databases/{db}/documents/{collection}/{docId}", h.updateDoc)
	mux.HandleFunc("DELETE /v1/projects/{project}/databases/{db}/documents/{collection}/{docId}", h.deleteDoc)

	// Query + batch
	mux.HandleFunc("POST /v1/projects/{project}/databases/{db}/documents:runQuery", h.runQuery)
	mux.HandleFunc("POST /v1/projects/{project}/databases/{db}/documents:batchGet", h.batchGet)
}

// ---------------------------------------------------------------------------
// handler
// ---------------------------------------------------------------------------

type handler struct {
	store storage.Store
	cfg   *config.Config
}

func docName(project, db, collection, docId string) string {
	return fmt.Sprintf("projects/%s/databases/%s/documents/%s/%s", project, db, collection, docId)
}

func storageKey(project, db, collection, docId string) string {
	return fmt.Sprintf("%s/%s/%s/%s", project, db, collection, docId)
}

func collectionPrefix(project, db, collection string) string {
	return fmt.Sprintf("%s/%s/%s/", project, db, collection)
}

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

// ---------------------------------------------------------------------------
// Create
// ---------------------------------------------------------------------------

func (h *handler) createDoc(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	db := r.PathValue("db")
	collection := r.PathValue("collection")

	docId := r.URL.Query().Get("documentId")
	if docId == "" {
		docId = server.RequestID()
	}

	var req createDocRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		server.GCPError(w, http.StatusBadRequest, "invalid request body", "INVALID_ARGUMENT")
		return
	}

	key := storageKey(project, db, collection, docId)
	if _, ok := h.store.Get(nsDocs, key); ok {
		server.GCPError(w, http.StatusConflict, fmt.Sprintf("document %s already exists", docId), "ALREADY_EXISTS")
		return
	}

	now := nowRFC3339()
	doc := FirestoreDocument{
		Name:       docName(project, db, collection, docId),
		Fields:     req.Fields,
		CreateTime: now,
		UpdateTime: now,
	}

	data, _ := json.Marshal(doc)
	h.store.Put(nsDocs, key, data)
	server.WriteJSON(w, http.StatusOK, doc)
}

// ---------------------------------------------------------------------------
// Get
// ---------------------------------------------------------------------------

func (h *handler) getDoc(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	db := r.PathValue("db")
	collection := r.PathValue("collection")
	docId := r.PathValue("docId")
	key := storageKey(project, db, collection, docId)

	raw, ok := h.store.Get(nsDocs, key)
	if !ok {
		server.GCPError(w, http.StatusNotFound, fmt.Sprintf("document %s not found", docId), "NOT_FOUND")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write(raw)
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

func (h *handler) listDocs(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	db := r.PathValue("db")
	collection := r.PathValue("collection")
	prefix := collectionPrefix(project, db, collection)

	keys := h.store.List(nsDocs, prefix)
	sort.Strings(keys)

	docs := make([]FirestoreDocument, 0, len(keys))
	for _, k := range keys {
		raw, ok := h.store.Get(nsDocs, k)
		if !ok {
			continue
		}
		var doc FirestoreDocument
		if json.Unmarshal(raw, &doc) == nil {
			docs = append(docs, doc)
		}
	}

	server.WriteJSON(w, http.StatusOK, map[string]any{"documents": docs})
}

// ---------------------------------------------------------------------------
// Update (PATCH)
// ---------------------------------------------------------------------------

func (h *handler) updateDoc(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	db := r.PathValue("db")
	collection := r.PathValue("collection")
	docId := r.PathValue("docId")
	key := storageKey(project, db, collection, docId)

	raw, ok := h.store.Get(nsDocs, key)
	if !ok {
		server.GCPError(w, http.StatusNotFound, fmt.Sprintf("document %s not found", docId), "NOT_FOUND")
		return
	}

	var existing FirestoreDocument
	json.Unmarshal(raw, &existing)

	var patch createDocRequest
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		server.GCPError(w, http.StatusBadRequest, "invalid request body", "INVALID_ARGUMENT")
		return
	}

	// Merge fields. updateMask handling: if query param present, only update listed fields.
	mask := r.URL.Query().Get("updateMask.fieldPaths")
	if mask != "" {
		fields := strings.Split(mask, ",")
		for _, f := range fields {
			f = strings.TrimSpace(f)
			if v, ok := patch.Fields[f]; ok {
				existing.Fields[f] = v
			}
		}
	} else {
		for k, v := range patch.Fields {
			existing.Fields[k] = v
		}
	}

	existing.UpdateTime = nowRFC3339()
	data, _ := json.Marshal(existing)
	h.store.Put(nsDocs, key, data)
	server.WriteJSON(w, http.StatusOK, existing)
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

func (h *handler) deleteDoc(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	db := r.PathValue("db")
	collection := r.PathValue("collection")
	docId := r.PathValue("docId")
	key := storageKey(project, db, collection, docId)

	if !h.store.Delete(nsDocs, key) {
		server.GCPError(w, http.StatusNotFound, fmt.Sprintf("document %s not found", docId), "NOT_FOUND")
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{})
}

// ---------------------------------------------------------------------------
// runQuery — ponytail: supports EQUAL field filters + AND composites.
// Add inequality/orderBy when needed.
// ---------------------------------------------------------------------------

func (h *handler) runQuery(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	db := r.PathValue("db")

	var req runQueryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.StructuredQuery == nil {
		server.GCPError(w, http.StatusBadRequest, "invalid query", "INVALID_ARGUMENT")
		return
	}

	if len(req.StructuredQuery.From) == 0 {
		server.GCPError(w, http.StatusBadRequest, "from is required", "INVALID_ARGUMENT")
		return
	}

	collection := req.StructuredQuery.From[0].CollectionID
	prefix := collectionPrefix(project, db, collection)
	keys := h.store.List(nsDocs, prefix)
	sort.Strings(keys)

	docs := make([]FirestoreDocument, 0, len(keys))
	for _, k := range keys {
		raw, ok := h.store.Get(nsDocs, k)
		if !ok {
			continue
		}
		var doc FirestoreDocument
		if json.Unmarshal(raw, &doc) == nil {
			docs = append(docs, doc)
		}
	}

	if req.StructuredQuery.Where != nil {
		docs = applyFilter(docs, req.StructuredQuery.Where)
	}

	results := make([]map[string]any, 0, len(docs))
	for _, doc := range docs {
		results = append(results, map[string]any{"document": doc, "readTime": nowRFC3339()})
	}
	server.WriteJSON(w, http.StatusOK, results)
}

func applyFilter(docs []FirestoreDocument, f *queryFilter) []FirestoreDocument {
	if f.FieldFilter != nil {
		return applyFieldFilter(docs, f.FieldFilter)
	}
	if f.CompositeFilter != nil && strings.EqualFold(f.CompositeFilter.Op, "AND") {
		for _, sub := range f.CompositeFilter.Filters {
			docs = applyFilter(docs, &sub)
		}
	}
	return docs
}

func applyFieldFilter(docs []FirestoreDocument, ff *fieldFilter) []FirestoreDocument {
	if !strings.EqualFold(ff.Op, "EQUAL") {
		return docs
	}
	field := ff.Field.FieldPath
	result := make([]FirestoreDocument, 0)
	for _, doc := range docs {
		if v, ok := doc.Fields[field]; ok && valuesEqual(v, ff.Value) {
			result = append(result, doc)
		}
	}
	return result
}

func valuesEqual(a, b Value) bool {
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	return string(aj) == string(bj)
}

// ---------------------------------------------------------------------------
// batchGet
// ---------------------------------------------------------------------------

func (h *handler) batchGet(w http.ResponseWriter, r *http.Request) {
	var req batchGetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		server.GCPError(w, http.StatusBadRequest, "invalid request body", "INVALID_ARGUMENT")
		return
	}

	results := make([]map[string]any, 0, len(req.Documents))
	for _, name := range req.Documents {
		key := nameToKey(name)
		raw, ok := h.store.Get(nsDocs, key)
		if !ok {
			results = append(results, map[string]any{"missing": name, "readTime": nowRFC3339()})
			continue
		}
		var doc FirestoreDocument
		json.Unmarshal(raw, &doc)
		results = append(results, map[string]any{"found": doc, "readTime": nowRFC3339()})
	}
	server.WriteJSON(w, http.StatusOK, results)
}

// nameToKey converts "projects/p/databases/d/documents/coll/docId" → "p/d/coll/docId"
func nameToKey(name string) string {
	parts := strings.Split(name, "/")
	// Expected: projects/P/databases/D/documents/COLL/DOCID
	if len(parts) >= 7 {
		return parts[1] + "/" + parts[3] + "/" + parts[5] + "/" + parts[6]
	}
	return name
}
