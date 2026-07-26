package cosmosdb

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/altocirrus/altocirrus/internal/config"
	"github.com/altocirrus/altocirrus/internal/server"
	"github.com/altocirrus/altocirrus/internal/storage"
)

const (
	nsDBs   = "azure:cosmosdb:dbs"
	nsColls = "azure:cosmosdb:colls"
	nsDocs  = "azure:cosmosdb:docs"
)

// ---------------------------------------------------------------------------
// Models
// ---------------------------------------------------------------------------

type Database struct {
	ID    string `json:"id"`
	RID   string `json:"_rid"`
	TS    int64  `json:"_ts"`
	Self  string `json:"_self"`
	ETag  string `json:"_etag"`
	Colls string `json:"_colls,omitempty"`
	Users string `json:"_users,omitempty"`
}

type Container struct {
	ID           string        `json:"id"`
	RID          string        `json:"_rid"`
	TS           int64         `json:"_ts"`
	Self         string        `json:"_self"`
	ETag         string        `json:"_etag"`
	Docs         string        `json:"_docs,omitempty"`
	PartitionKey *PartitionKey `json:"partitionKey,omitempty"`
}

type PartitionKey struct {
	Paths   []string `json:"paths"`
	Kind    string   `json:"kind"`
	Version int      `json:"version,omitempty"`
}

// Document is stored as raw JSON with metadata fields injected.
type Document = map[string]any

// ---------------------------------------------------------------------------
// Request bodies
// ---------------------------------------------------------------------------

type createDBRequest struct {
	ID string `json:"id"`
}

type createContainerRequest struct {
	ID           string        `json:"id"`
	PartitionKey *PartitionKey `json:"partitionKey"`
}

type queryRequest struct {
	Query      string           `json:"query"`
	Parameters []queryParameter `json:"parameters"`
}

type queryParameter struct {
	Name  string `json:"name"`
	Value any    `json:"value"`
}

// ---------------------------------------------------------------------------
// RegisterRoutes
// ---------------------------------------------------------------------------

func RegisterRoutes(mux *http.ServeMux, store storage.Store, cfg *config.Config) {
	h := &handler{store: store, cfg: cfg}

	// Databases
	mux.HandleFunc("POST /dbs", h.withHeaders(h.createDB))
	mux.HandleFunc("GET /dbs", h.withHeaders(h.listDBs))
	mux.HandleFunc("GET /dbs/{dbId}", h.withHeaders(h.getDB))
	mux.HandleFunc("DELETE /dbs/{dbId}", h.withHeaders(h.deleteDB))

	// Containers
	mux.HandleFunc("POST /dbs/{dbId}/colls", h.withHeaders(h.createContainer))
	mux.HandleFunc("GET /dbs/{dbId}/colls", h.withHeaders(h.listContainers))
	mux.HandleFunc("GET /dbs/{dbId}/colls/{collId}", h.withHeaders(h.getContainer))
	mux.HandleFunc("DELETE /dbs/{dbId}/colls/{collId}", h.withHeaders(h.deleteContainer))

	// Documents
	mux.HandleFunc("POST /dbs/{dbId}/colls/{collId}/docs", h.withHeaders(h.createOrQueryDocs))
	mux.HandleFunc("GET /dbs/{dbId}/colls/{collId}/docs", h.withHeaders(h.listDocs))
	mux.HandleFunc("GET /dbs/{dbId}/colls/{collId}/docs/{docId}", h.withHeaders(h.getDoc))
	mux.HandleFunc("PUT /dbs/{dbId}/colls/{collId}/docs/{docId}", h.withHeaders(h.replaceDoc))
	mux.HandleFunc("PATCH /dbs/{dbId}/colls/{collId}/docs/{docId}", h.withHeaders(h.patchDoc))
	mux.HandleFunc("DELETE /dbs/{dbId}/colls/{collId}/docs/{docId}", h.withHeaders(h.deleteDoc))
}

// ---------------------------------------------------------------------------
// handler
// ---------------------------------------------------------------------------

type handler struct {
	store storage.Store
	cfg   *config.Config
}

func (h *handler) withHeaders(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-ms-request-charge", "1")
		w.Header().Set("x-ms-session-token", "1:0")
		w.Header().Set("x-ms-activity-id", server.RequestID())
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		next(w, r)
	}
}

// ---------------------------------------------------------------------------
// Databases
// ---------------------------------------------------------------------------

func (h *handler) createDB(w http.ResponseWriter, r *http.Request) {
	var req createDBRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		server.AzureError(w, "BadRequest", "id is required", http.StatusBadRequest)
		return
	}
	if _, ok := h.store.Get(nsDBs, req.ID); ok {
		server.AzureError(w, "Conflict", fmt.Sprintf("database %s already exists", req.ID), http.StatusConflict)
		return
	}
	db := Database{
		ID:    req.ID,
		RID:   shortRID(),
		TS:    time.Now().Unix(),
		Self:  fmt.Sprintf("dbs/%s/", req.ID),
		ETag:  fmt.Sprintf(`"%s"`, shortRID()),
		Colls: fmt.Sprintf("dbs/%s/colls/", req.ID),
		Users: fmt.Sprintf("dbs/%s/users/", req.ID),
	}
	data, _ := json.Marshal(db)
	h.store.Put(nsDBs, req.ID, data)
	server.WriteJSON(w, http.StatusCreated, db)
}

func (h *handler) listDBs(w http.ResponseWriter, r *http.Request) {
	keys := h.store.List(nsDBs, "")
	sort.Strings(keys)
	dbs := make([]Database, 0, len(keys))
	for _, k := range keys {
		if d, ok := h.loadDB(k); ok {
			dbs = append(dbs, d)
		}
	}
	w.Header().Set("x-ms-item-count", fmt.Sprintf("%d", len(dbs)))
	server.WriteJSON(w, http.StatusOK, map[string]any{
		"_rid":       "",
		"_count":     len(dbs),
		"Databases":  dbs,
	})
}

func (h *handler) getDB(w http.ResponseWriter, r *http.Request) {
	dbId := r.PathValue("dbId")
	db, ok := h.loadDB(dbId)
	if !ok {
		server.AzureError(w, "NotFound", fmt.Sprintf("database %s not found", dbId), http.StatusNotFound)
		return
	}
	server.WriteJSON(w, http.StatusOK, db)
}

func (h *handler) deleteDB(w http.ResponseWriter, r *http.Request) {
	dbId := r.PathValue("dbId")
	if !h.store.Delete(nsDBs, dbId) {
		server.AzureError(w, "NotFound", fmt.Sprintf("database %s not found", dbId), http.StatusNotFound)
		return
	}
	// cascade: delete containers + docs
	for _, k := range h.store.List(nsColls, dbId+"/") {
		h.store.Delete(nsColls, k)
	}
	for _, k := range h.store.List(nsDocs, dbId+"/") {
		h.store.Delete(nsDocs, k)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Containers
// ---------------------------------------------------------------------------

func (h *handler) createContainer(w http.ResponseWriter, r *http.Request) {
	dbId := r.PathValue("dbId")
	if _, ok := h.loadDB(dbId); !ok {
		server.AzureError(w, "NotFound", fmt.Sprintf("database %s not found", dbId), http.StatusNotFound)
		return
	}
	var req createContainerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		server.AzureError(w, "BadRequest", "id is required", http.StatusBadRequest)
		return
	}
	collKey := dbId + "/" + req.ID
	if _, ok := h.store.Get(nsColls, collKey); ok {
		server.AzureError(w, "Conflict", fmt.Sprintf("container %s already exists", req.ID), http.StatusConflict)
		return
	}
	if req.PartitionKey == nil {
		req.PartitionKey = &PartitionKey{Paths: []string{"/id"}, Kind: "Hash", Version: 2}
	}
	coll := Container{
		ID:           req.ID,
		RID:          shortRID(),
		TS:           time.Now().Unix(),
		Self:         fmt.Sprintf("dbs/%s/colls/%s/", dbId, req.ID),
		ETag:         fmt.Sprintf(`"%s"`, shortRID()),
		Docs:         fmt.Sprintf("dbs/%s/colls/%s/docs/", dbId, req.ID),
		PartitionKey: req.PartitionKey,
	}
	data, _ := json.Marshal(coll)
	h.store.Put(nsColls, collKey, data)
	server.WriteJSON(w, http.StatusCreated, coll)
}

func (h *handler) listContainers(w http.ResponseWriter, r *http.Request) {
	dbId := r.PathValue("dbId")
	if _, ok := h.loadDB(dbId); !ok {
		server.AzureError(w, "NotFound", fmt.Sprintf("database %s not found", dbId), http.StatusNotFound)
		return
	}
	keys := h.store.List(nsColls, dbId+"/")
	sort.Strings(keys)
	colls := make([]Container, 0, len(keys))
	for _, k := range keys {
		if c, ok := h.loadContainer(k); ok {
			colls = append(colls, c)
		}
	}
	w.Header().Set("x-ms-item-count", fmt.Sprintf("%d", len(colls)))
	server.WriteJSON(w, http.StatusOK, map[string]any{
		"_rid":                "",
		"_count":              len(colls),
		"DocumentCollections": colls,
	})
}

func (h *handler) getContainer(w http.ResponseWriter, r *http.Request) {
	dbId := r.PathValue("dbId")
	collId := r.PathValue("collId")
	coll, ok := h.loadContainer(dbId + "/" + collId)
	if !ok {
		server.AzureError(w, "NotFound", fmt.Sprintf("container %s not found", collId), http.StatusNotFound)
		return
	}
	server.WriteJSON(w, http.StatusOK, coll)
}

func (h *handler) deleteContainer(w http.ResponseWriter, r *http.Request) {
	dbId := r.PathValue("dbId")
	collId := r.PathValue("collId")
	collKey := dbId + "/" + collId
	if !h.store.Delete(nsColls, collKey) {
		server.AzureError(w, "NotFound", fmt.Sprintf("container %s not found", collId), http.StatusNotFound)
		return
	}
	for _, k := range h.store.List(nsDocs, collKey+"/") {
		h.store.Delete(nsDocs, k)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Documents
// ---------------------------------------------------------------------------

func (h *handler) createOrQueryDocs(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("x-ms-documentdb-isquery") == "true" {
		h.queryDocs(w, r)
		return
	}
	h.createDoc(w, r)
}

func (h *handler) createDoc(w http.ResponseWriter, r *http.Request) {
	dbId := r.PathValue("dbId")
	collId := r.PathValue("collId")
	collKey := dbId + "/" + collId
	if _, ok := h.store.Get(nsColls, collKey); !ok {
		server.AzureError(w, "NotFound", fmt.Sprintf("container %s not found", collId), http.StatusNotFound)
		return
	}

	var doc Document
	if err := json.NewDecoder(r.Body).Decode(&doc); err != nil {
		server.AzureError(w, "BadRequest", "invalid JSON body", http.StatusBadRequest)
		return
	}

	id, _ := doc["id"].(string)
	if id == "" {
		id = shortRID()
		doc["id"] = id
	}
	docKey := collKey + "/" + id
	if _, ok := h.store.Get(nsDocs, docKey); ok {
		server.AzureError(w, "Conflict", fmt.Sprintf("document %s already exists", id), http.StatusConflict)
		return
	}

	now := time.Now().Unix()
	rid := shortRID()
	doc["_rid"] = rid
	doc["_ts"] = now
	doc["_self"] = fmt.Sprintf("dbs/%s/colls/%s/docs/%s/", dbId, collId, id)
	doc["_etag"] = fmt.Sprintf(`"%s"`, shortRID())
	doc["_attachments"] = "attachments/"

	data, _ := json.Marshal(doc)
	h.store.Put(nsDocs, docKey, data)
	server.WriteJSON(w, http.StatusCreated, doc)
}

func (h *handler) getDoc(w http.ResponseWriter, r *http.Request) {
	dbId := r.PathValue("dbId")
	collId := r.PathValue("collId")
	docId := r.PathValue("docId")
	docKey := dbId + "/" + collId + "/" + docId

	raw, ok := h.store.Get(nsDocs, docKey)
	if !ok {
		server.AzureError(w, "NotFound", fmt.Sprintf("document %s not found", docId), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write(raw)
}

func (h *handler) listDocs(w http.ResponseWriter, r *http.Request) {
	dbId := r.PathValue("dbId")
	collId := r.PathValue("collId")
	prefix := dbId + "/" + collId + "/"
	keys := h.store.List(nsDocs, prefix)
	sort.Strings(keys)

	docs := make([]Document, 0, len(keys))
	for _, k := range keys {
		raw, ok := h.store.Get(nsDocs, k)
		if !ok {
			continue
		}
		var doc Document
		if json.Unmarshal(raw, &doc) == nil {
			docs = append(docs, doc)
		}
	}

	w.Header().Set("x-ms-item-count", fmt.Sprintf("%d", len(docs)))
	server.WriteJSON(w, http.StatusOK, map[string]any{
		"_rid":      "",
		"_count":    len(docs),
		"Documents": docs,
	})
}

func (h *handler) replaceDoc(w http.ResponseWriter, r *http.Request) {
	dbId := r.PathValue("dbId")
	collId := r.PathValue("collId")
	docId := r.PathValue("docId")
	docKey := dbId + "/" + collId + "/" + docId

	if _, ok := h.store.Get(nsDocs, docKey); !ok {
		server.AzureError(w, "NotFound", fmt.Sprintf("document %s not found", docId), http.StatusNotFound)
		return
	}

	var doc Document
	if err := json.NewDecoder(r.Body).Decode(&doc); err != nil {
		server.AzureError(w, "BadRequest", "invalid JSON body", http.StatusBadRequest)
		return
	}
	doc["id"] = docId
	now := time.Now().Unix()
	doc["_rid"] = shortRID()
	doc["_ts"] = now
	doc["_self"] = fmt.Sprintf("dbs/%s/colls/%s/docs/%s/", dbId, collId, docId)
	doc["_etag"] = fmt.Sprintf(`"%s"`, shortRID())
	doc["_attachments"] = "attachments/"

	data, _ := json.Marshal(doc)
	h.store.Put(nsDocs, docKey, data)
	server.WriteJSON(w, http.StatusOK, doc)
}

func (h *handler) patchDoc(w http.ResponseWriter, r *http.Request) {
	dbId := r.PathValue("dbId")
	collId := r.PathValue("collId")
	docId := r.PathValue("docId")
	docKey := dbId + "/" + collId + "/" + docId

	raw, ok := h.store.Get(nsDocs, docKey)
	if !ok {
		server.AzureError(w, "NotFound", fmt.Sprintf("document %s not found", docId), http.StatusNotFound)
		return
	}
	var existing Document
	json.Unmarshal(raw, &existing)

	var patch Document
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		server.AzureError(w, "BadRequest", "invalid JSON body", http.StatusBadRequest)
		return
	}

	for k, v := range patch {
		if strings.HasPrefix(k, "_") {
			continue
		}
		existing[k] = v
	}

	now := time.Now().Unix()
	existing["_ts"] = now
	existing["_etag"] = fmt.Sprintf(`"%s"`, shortRID())

	data, _ := json.Marshal(existing)
	h.store.Put(nsDocs, docKey, data)
	server.WriteJSON(w, http.StatusOK, existing)
}

func (h *handler) deleteDoc(w http.ResponseWriter, r *http.Request) {
	dbId := r.PathValue("dbId")
	collId := r.PathValue("collId")
	docId := r.PathValue("docId")
	docKey := dbId + "/" + collId + "/" + docId

	if !h.store.Delete(nsDocs, docKey) {
		server.AzureError(w, "NotFound", fmt.Sprintf("document %s not found", docId), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Query — ponytail: regex parser, supports SELECT * FROM c WHERE c.x = val
// Upgrade to real parser if complex queries needed.
// ---------------------------------------------------------------------------

// queryDocs handles POST with x-ms-documentdb-isquery=true
func (h *handler) queryDocs(w http.ResponseWriter, r *http.Request) {
	dbId := r.PathValue("dbId")
	collId := r.PathValue("collId")
	prefix := dbId + "/" + collId + "/"

	var req queryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		server.AzureError(w, "BadRequest", "invalid query body", http.StatusBadRequest)
		return
	}

	keys := h.store.List(nsDocs, prefix)
	sort.Strings(keys)
	allDocs := make([]Document, 0, len(keys))
	for _, k := range keys {
		raw, ok := h.store.Get(nsDocs, k)
		if !ok {
			continue
		}
		var doc Document
		if json.Unmarshal(raw, &doc) == nil {
			allDocs = append(allDocs, doc)
		}
	}

	filters := parseWhereFilters(req.Query, req.Parameters)
	results := filterDocs(allDocs, filters)

	w.Header().Set("x-ms-item-count", fmt.Sprintf("%d", len(results)))
	server.WriteJSON(w, http.StatusOK, map[string]any{
		"_rid":      "",
		"_count":    len(results),
		"Documents": results,
	})
}

type filter struct {
	field string
	value any
}

// ponytail: regex-based WHERE parser. Handles:
//   c.field = @param, c.field = "string", c.field = number
// Upgrade to real SQL parser if needed.
var whereRe = regexp.MustCompile(`(?i)c\.(\w+)\s*=\s*(@\w+|"[^"]*"|\d+(?:\.\d+)?)`)

func parseWhereFilters(query string, params []queryParameter) []filter {
	paramMap := make(map[string]any, len(params))
	for _, p := range params {
		paramMap[p.Name] = p.Value
	}

	matches := whereRe.FindAllStringSubmatch(query, -1)
	filters := make([]filter, 0, len(matches))
	for _, m := range matches {
		field := m[1]
		valStr := m[2]
		var val any
		if strings.HasPrefix(valStr, "@") {
			val = paramMap[valStr]
		} else if strings.HasPrefix(valStr, `"`) {
			val = strings.Trim(valStr, `"`)
		} else {
			var n json.Number
			n = json.Number(valStr)
			if f, err := n.Float64(); err == nil {
				val = f
			} else {
				val = valStr
			}
		}
		filters = append(filters, filter{field: field, value: val})
	}
	return filters
}

func filterDocs(docs []Document, filters []filter) []Document {
	if len(filters) == 0 {
		return docs
	}
	result := make([]Document, 0)
	for _, doc := range docs {
		if matchesAll(doc, filters) {
			result = append(result, doc)
		}
	}
	return result
}

func matchesAll(doc Document, filters []filter) bool {
	for _, f := range filters {
		v, ok := doc[f.field]
		if !ok {
			return false
		}
		if !valuesEqual(v, f.value) {
			return false
		}
	}
	return true
}

func valuesEqual(a, b any) bool {
	// ponytail: JSON numbers come as float64 from Unmarshal.
	// Compare numerically when both are numeric.
	af, aNum := toFloat(a)
	bf, bNum := toFloat(b)
	if aNum && bNum {
		return af == bf
	}
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (h *handler) loadDB(id string) (Database, bool) {
	raw, ok := h.store.Get(nsDBs, id)
	if !ok {
		return Database{}, false
	}
	var db Database
	if err := json.Unmarshal(raw, &db); err != nil {
		return Database{}, false
	}
	return db, true
}

func (h *handler) loadContainer(key string) (Container, bool) {
	raw, ok := h.store.Get(nsColls, key)
	if !ok {
		return Container{}, false
	}
	var c Container
	if err := json.Unmarshal(raw, &c); err != nil {
		return Container{}, false
	}
	return c, true
}

func shortRID() string {
	var b [8]byte
	rand.Read(b[:])
	return fmt.Sprintf("%x", b)
}
