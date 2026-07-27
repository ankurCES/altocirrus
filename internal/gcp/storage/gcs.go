package gcpstorage

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/altocirrus/altocirrus/internal/config"
	"github.com/altocirrus/altocirrus/internal/server"
	"github.com/altocirrus/altocirrus/internal/storage"
)

// Namespace constants for the backing store.
const (
	nsBuckets = "gcp:storage:buckets"
	nsUploads = "gcp:storage:uploads"
)

func nsContent(bucket string) string  { return "gcp:storage:content:" + bucket }
func nsObjects(bucket string) string  { return "gcp:storage:objects:" + bucket }

// ---- Models ----------------------------------------------------------------

// Bucket represents a GCS bucket resource.
type Bucket struct {
	Kind          string `json:"kind"`
	ID            string `json:"id"`
	Name          string `json:"name"`
	ProjectNumber string `json:"projectNumber"`
	TimeCreated   string `json:"timeCreated"`
	Updated       string `json:"updated"`
	Location      string `json:"location"`
	StorageClass  string `json:"storageClass"`
}

// Object represents a GCS object resource.
type Object struct {
	Kind           string `json:"kind"`
	ID             string `json:"id"`
	Name           string `json:"name"`
	Bucket         string `json:"bucket"`
	Size           string `json:"size"`
	ContentType    string `json:"contentType"`
	TimeCreated    string `json:"timeCreated"`
	Updated        string `json:"updated"`
	Md5Hash        string `json:"md5Hash"`
	Crc32c         string `json:"crc32c"`
	Generation     string `json:"generation"`
	Metageneration string `json:"metageneration"`
}

// BucketList is the response envelope for listing buckets.
type BucketList struct {
	Kind  string   `json:"kind"`
	Items []Bucket `json:"items"`
}

// ObjectList is the response envelope for listing objects.
type ObjectList struct {
	Kind     string   `json:"kind"`
	Items    []Object `json:"items,omitempty"`
	Prefixes []string `json:"prefixes,omitempty"`
}

// uploadRecord tracks a pending resumable upload.
type uploadRecord struct {
	Bucket      string `json:"bucket"`
	Name        string `json:"name"`
	ContentType string `json:"contentType"`
}

// ---- RegisterRoutes --------------------------------------------------------

// RegisterRoutes adds GCS JSON API routes to mux.
func RegisterRoutes(mux *http.ServeMux, store storage.Store, cfg *config.Config) {
	h := &handler{store: store, cfg: cfg}

	// Bucket endpoints.
	mux.HandleFunc("/storage/v1/b", h.routeBuckets)
	mux.HandleFunc("/storage/v1/b/", h.routeBucketOrObject)

	// Upload endpoints (simple + resumable).
	mux.HandleFunc("/upload/storage/v1/b/", h.routeUpload)
}

// ---- handler ---------------------------------------------------------------

type handler struct {
	store storage.Store
	cfg   *config.Config
}

// ---- Bucket routing --------------------------------------------------------

// routeBuckets handles /storage/v1/b (no trailing slash — list & create).
func (h *handler) routeBuckets(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listBuckets(w, r)
	case http.MethodPost:
		h.createBucket(w, r)
	default:
		server.GCPError(w, http.StatusMethodNotAllowed, "method not allowed", "METHOD_NOT_ALLOWED")
	}
}

// routeBucketOrObject handles /storage/v1/b/{bucket}[/o[/{object...}]].
func (h *handler) routeBucketOrObject(w http.ResponseWriter, r *http.Request) {
	// Strip the known prefix to get: {bucket}[/o[/{object...}]]
	rest := strings.TrimPrefix(r.URL.Path, "/storage/v1/b/")
	if rest == "" {
		server.GCPError(w, http.StatusBadRequest, "bucket name required", "INVALID_ARGUMENT")
		return
	}

	// Split on "/o/" to separate bucket from object path.
	if idx := strings.Index(rest, "/o/"); idx >= 0 {
		bucket := rest[:idx]
		objectName := rest[idx+3:] // everything after "/o/"
		h.routeObject(w, r, bucket, objectName)
		return
	}

	// Check for /o with no object name (list objects).
	if idx := strings.Index(rest, "/o"); idx >= 0 {
		bucket := rest[:idx]
		suffix := rest[idx:]
		if suffix == "/o" || suffix == "/o/" {
			h.routeObjectList(w, r, bucket)
			return
		}
	}

	// No /o — this is a bucket-level operation.
	// The bucket name might have a trailing slash; strip it.
	bucket := strings.TrimSuffix(rest, "/")
	switch r.Method {
	case http.MethodGet:
		h.getBucket(w, r, bucket)
	case http.MethodDelete:
		h.deleteBucket(w, r, bucket)
	default:
		server.GCPError(w, http.StatusMethodNotAllowed, "method not allowed", "METHOD_NOT_ALLOWED")
	}
}

// routeObjectList handles GET /storage/v1/b/{bucket}/o — list objects.
func (h *handler) routeObjectList(w http.ResponseWriter, r *http.Request, bucket string) {
	if r.Method != http.MethodGet {
		server.GCPError(w, http.StatusMethodNotAllowed, "method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	h.listObjects(w, r, bucket)
}

// routeObject handles GET and DELETE on a specific object.
func (h *handler) routeObject(w http.ResponseWriter, r *http.Request, bucket, objectName string) {
	switch r.Method {
	case http.MethodGet:
		h.getObject(w, r, bucket, objectName)
	case http.MethodDelete:
		h.deleteObject(w, r, bucket, objectName)
	default:
		server.GCPError(w, http.StatusMethodNotAllowed, "method not allowed", "METHOD_NOT_ALLOWED")
	}
}

// ---- Upload routing --------------------------------------------------------

// routeUpload handles /upload/storage/v1/b/{bucket}/o...
func (h *handler) routeUpload(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/upload/storage/v1/b/")
	if rest == "" {
		server.GCPError(w, http.StatusBadRequest, "bucket name required", "INVALID_ARGUMENT")
		return
	}

	// Expect: {bucket}/o or {bucket}/o?...
	idx := strings.Index(rest, "/o")
	if idx < 0 {
		server.GCPError(w, http.StatusBadRequest, "invalid upload path", "INVALID_ARGUMENT")
		return
	}
	bucket := rest[:idx]

	uploadType := r.URL.Query().Get("uploadType")

	switch {
	case r.Method == http.MethodPost && uploadType == "media":
		h.simpleUpload(w, r, bucket)
	case r.Method == http.MethodPost && uploadType == "resumable":
		h.initiateResumable(w, r, bucket)
	case r.Method == http.MethodPut && uploadType == "resumable":
		h.completeResumable(w, r, bucket)
	default:
		server.GCPError(w, http.StatusBadRequest, "unsupported upload type or method", "INVALID_ARGUMENT")
	}
}

// ---- Bucket handlers -------------------------------------------------------

func (h *handler) createBucket(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string `json:"name"`
		Location string `json:"location"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.GCPError(w, http.StatusBadRequest, "invalid JSON body", "INVALID_ARGUMENT")
		return
	}
	if body.Name == "" {
		server.GCPError(w, http.StatusBadRequest, "bucket name is required", "INVALID_ARGUMENT")
		return
	}

	// Check for duplicate.
	if _, ok := h.store.Get(nsBuckets, body.Name); ok {
		server.GCPError(w, http.StatusConflict, fmt.Sprintf("bucket %q already exists", body.Name), "ALREADY_EXISTS")
		return
	}

	location := body.Location
	if location == "" {
		location = "US"
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	b := Bucket{
		Kind:          "storage#bucket",
		ID:            body.Name,
		Name:          body.Name,
		ProjectNumber: h.cfg.GCP.ProjectNumber,
		TimeCreated:   now,
		Updated:       now,
		Location:      strings.ToUpper(location),
		StorageClass:  "STANDARD",
	}

	data, _ := json.Marshal(b)
	h.store.Put(nsBuckets, body.Name, data)

	server.WriteJSON(w, http.StatusOK, b)
}

func (h *handler) listBuckets(w http.ResponseWriter, r *http.Request) {
	keys := h.store.List(nsBuckets, "")
	sort.Strings(keys)

	buckets := make([]Bucket, 0, len(keys))
	for _, k := range keys {
		raw, ok := h.store.Get(nsBuckets, k)
		if !ok {
			continue
		}
		var b Bucket
		if err := json.Unmarshal(raw, &b); err != nil {
			continue
		}
		buckets = append(buckets, b)
	}

	server.WriteJSON(w, http.StatusOK, BucketList{
		Kind:  "storage#buckets",
		Items: buckets,
	})
}

func (h *handler) getBucket(w http.ResponseWriter, r *http.Request, name string) {
	raw, ok := h.store.Get(nsBuckets, name)
	if !ok {
		server.GCPError(w, http.StatusNotFound, fmt.Sprintf("bucket %q not found", name), "NOT_FOUND")
		return
	}
	var b Bucket
	if err := json.Unmarshal(raw, &b); err != nil {
		server.GCPError(w, http.StatusInternalServerError, "corrupt bucket metadata", "INTERNAL")
		return
	}
	server.WriteJSON(w, http.StatusOK, b)
}

func (h *handler) deleteBucket(w http.ResponseWriter, r *http.Request, name string) {
	if _, ok := h.store.Get(nsBuckets, name); !ok {
		server.GCPError(w, http.StatusNotFound, fmt.Sprintf("bucket %q not found", name), "NOT_FOUND")
		return
	}

	// Fail if bucket has objects.
	objects := h.store.List(nsObjects(name), "")
	if len(objects) > 0 {
		server.GCPError(w, http.StatusConflict, "bucket is not empty", "FAILED_PRECONDITION")
		return
	}

	h.store.Delete(nsBuckets, name)
	w.WriteHeader(http.StatusNoContent)
}

// ---- Object handlers -------------------------------------------------------

func (h *handler) simpleUpload(w http.ResponseWriter, r *http.Request, bucket string) {
	if _, ok := h.store.Get(nsBuckets, bucket); !ok {
		server.GCPError(w, http.StatusNotFound, fmt.Sprintf("bucket %q not found", bucket), "NOT_FOUND")
		return
	}

	name := r.URL.Query().Get("name")
	if name == "" {
		server.GCPError(w, http.StatusBadRequest, "object name is required (name query param)", "INVALID_ARGUMENT")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		server.GCPError(w, http.StatusInternalServerError, "failed to read request body", "INTERNAL")
		return
	}

	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	obj := h.storeObject(bucket, name, contentType, body)
	server.WriteJSON(w, http.StatusOK, obj)
}

func (h *handler) initiateResumable(w http.ResponseWriter, r *http.Request, bucket string) {
	if _, ok := h.store.Get(nsBuckets, bucket); !ok {
		server.GCPError(w, http.StatusNotFound, fmt.Sprintf("bucket %q not found", bucket), "NOT_FOUND")
		return
	}

	var body struct {
		Name        string `json:"name"`
		ContentType string `json:"contentType"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.GCPError(w, http.StatusBadRequest, "invalid JSON body", "INVALID_ARGUMENT")
		return
	}
	if body.Name == "" {
		server.GCPError(w, http.StatusBadRequest, "object name is required", "INVALID_ARGUMENT")
		return
	}
	if body.ContentType == "" {
		body.ContentType = "application/octet-stream"
	}

	uploadID := server.RequestID()
	rec := uploadRecord{
		Bucket:      bucket,
		Name:        body.Name,
		ContentType: body.ContentType,
	}
	data, _ := json.Marshal(rec)
	h.store.Put(nsUploads, uploadID, data)

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	location := fmt.Sprintf("%s://%s/upload/storage/v1/b/%s/o?uploadType=resumable&upload_id=%s", scheme, r.Host, bucket, uploadID)
	w.Header().Set("Location", location)
	w.WriteHeader(http.StatusOK)
}

func (h *handler) completeResumable(w http.ResponseWriter, r *http.Request, bucket string) {
	uploadID := r.URL.Query().Get("upload_id")
	if uploadID == "" {
		server.GCPError(w, http.StatusBadRequest, "upload_id is required", "INVALID_ARGUMENT")
		return
	}

	raw, ok := h.store.Get(nsUploads, uploadID)
	if !ok {
		server.GCPError(w, http.StatusNotFound, "upload session not found", "NOT_FOUND")
		return
	}
	var rec uploadRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		server.GCPError(w, http.StatusInternalServerError, "corrupt upload record", "INTERNAL")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		server.GCPError(w, http.StatusInternalServerError, "failed to read request body", "INTERNAL")
		return
	}

	obj := h.storeObject(rec.Bucket, rec.Name, rec.ContentType, body)

	// Clean up the upload record.
	h.store.Delete(nsUploads, uploadID)

	server.WriteJSON(w, http.StatusOK, obj)
}

func (h *handler) listObjects(w http.ResponseWriter, r *http.Request, bucket string) {
	if _, ok := h.store.Get(nsBuckets, bucket); !ok {
		server.GCPError(w, http.StatusNotFound, fmt.Sprintf("bucket %q not found", bucket), "NOT_FOUND")
		return
	}

	prefix := r.URL.Query().Get("prefix")
	delimiter := r.URL.Query().Get("delimiter")

	keys := h.store.List(nsObjects(bucket), prefix)
	sort.Strings(keys)

	var objects []Object
	prefixSet := make(map[string]struct{})

	for _, k := range keys {
		if delimiter != "" {
			// Check if the key (after the prefix) contains the delimiter.
			after := strings.TrimPrefix(k, prefix)
			if dIdx := strings.Index(after, delimiter); dIdx >= 0 {
				// This key falls under a common prefix.
				commonPrefix := prefix + after[:dIdx+len(delimiter)]
				prefixSet[commonPrefix] = struct{}{}
				continue
			}
		}

		raw, ok := h.store.Get(nsObjects(bucket), k)
		if !ok {
			continue
		}
		var obj Object
		if err := json.Unmarshal(raw, &obj); err != nil {
			continue
		}
		objects = append(objects, obj)
	}

	var prefixes []string
	for p := range prefixSet {
		prefixes = append(prefixes, p)
	}
	sort.Strings(prefixes)

	resp := ObjectList{
		Kind:     "storage#objects",
		Items:    objects,
		Prefixes: prefixes,
	}
	server.WriteJSON(w, http.StatusOK, resp)
}

func (h *handler) getObject(w http.ResponseWriter, r *http.Request, bucket, objectName string) {
	if _, ok := h.store.Get(nsBuckets, bucket); !ok {
		server.GCPError(w, http.StatusNotFound, fmt.Sprintf("bucket %q not found", bucket), "NOT_FOUND")
		return
	}

	raw, ok := h.store.Get(nsObjects(bucket), objectName)
	if !ok {
		server.GCPError(w, http.StatusNotFound, fmt.Sprintf("object %q not found in bucket %q", objectName, bucket), "NOT_FOUND")
		return
	}

	var obj Object
	if err := json.Unmarshal(raw, &obj); err != nil {
		server.GCPError(w, http.StatusInternalServerError, "corrupt object metadata", "INTERNAL")
		return
	}

	// alt=media means download the content.
	if r.URL.Query().Get("alt") == "media" {
		content, ok := h.store.Get(nsContent(bucket), objectName)
		if !ok {
			server.GCPError(w, http.StatusNotFound, "object content not found", "NOT_FOUND")
			return
		}
		w.Header().Set("Content-Type", obj.ContentType)
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		w.WriteHeader(http.StatusOK)
		w.Write(content)
		return
	}

	// Return metadata.
	server.WriteJSON(w, http.StatusOK, obj)
}

func (h *handler) deleteObject(w http.ResponseWriter, r *http.Request, bucket, objectName string) {
	if _, ok := h.store.Get(nsBuckets, bucket); !ok {
		server.GCPError(w, http.StatusNotFound, fmt.Sprintf("bucket %q not found", bucket), "NOT_FOUND")
		return
	}

	if !h.store.Delete(nsObjects(bucket), objectName) {
		server.GCPError(w, http.StatusNotFound, fmt.Sprintf("object %q not found in bucket %q", objectName, bucket), "NOT_FOUND")
		return
	}

	h.store.Delete(nsContent(bucket), objectName)
	w.WriteHeader(http.StatusNoContent)
}

// ---- helpers ---------------------------------------------------------------

// storeObject persists object content and metadata, returning the Object model.
func (h *handler) storeObject(bucket, name, contentType string, content []byte) Object {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	gen := strconv.FormatInt(time.Now().UnixNano(), 10)

	md5sum := md5.Sum(content)
	md5b64 := base64.StdEncoding.EncodeToString(md5sum[:])

	crc := crc32.ChecksumIEEE(content)
	// GCS encodes crc32c as base64 of the big-endian 4-byte value.
	crcBytes := []byte{byte(crc >> 24), byte(crc >> 16), byte(crc >> 8), byte(crc)}
	crcB64 := base64.StdEncoding.EncodeToString(crcBytes)

	obj := Object{
		Kind:           "storage#object",
		ID:             fmt.Sprintf("%s/%s/%s", bucket, name, gen),
		Name:           name,
		Bucket:         bucket,
		Size:           strconv.Itoa(len(content)),
		ContentType:    contentType,
		TimeCreated:    now,
		Updated:        now,
		Md5Hash:        md5b64,
		Crc32c:         crcB64,
		Generation:     gen,
		Metageneration: "1",
	}

	data, _ := json.Marshal(obj)
	h.store.Put(nsObjects(bucket), name, data)
	h.store.Put(nsContent(bucket), name, content)

	return obj
}
