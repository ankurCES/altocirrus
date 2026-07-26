package blobstorage

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
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

const (
	accountName        = "devstoreaccount1"
	containerNamespace = "azure:blob:containers"
)

func contentNamespace(container string) string {
	return fmt.Sprintf("azure:blob:content:%s", container)
}

func metaNamespace(container string) string {
	return fmt.Sprintf("azure:blob:meta:%s", container)
}

// ---------------------------------------------------------------------------
// XML response models
// ---------------------------------------------------------------------------

// EnumerationResults is the top-level XML element for list responses.
type EnumerationResults struct {
	XMLName    xml.Name    `xml:"EnumerationResults"`
	Containers *Containers `xml:"Containers,omitempty"`
	Blobs      *Blobs      `xml:"Blobs,omitempty"`
}

// Containers wraps a list of Container elements.
type Containers struct {
	Container []Container `xml:"Container"`
}

// Container represents a single container in a list response.
type Container struct {
	Name       string              `xml:"Name"`
	Properties ContainerProperties `xml:"Properties"`
}

// ContainerProperties holds container metadata.
type ContainerProperties struct {
	LastModified string `xml:"Last-Modified"`
	Etag         string `xml:"Etag"`
}

// Blobs wraps a list of Blob elements.
type Blobs struct {
	Blob []Blob `xml:"Blob"`
}

// Blob represents a single blob in a list response.
type Blob struct {
	Name       string         `xml:"Name"`
	Properties BlobProperties `xml:"Properties"`
}

// BlobProperties holds blob metadata returned in list and HEAD responses.
type BlobProperties struct {
	ContentLength int64  `xml:"Content-Length"`
	ContentType   string `xml:"Content-Type"`
	LastModified  string `xml:"Last-Modified"`
}

// containerRecord is stored in the backing store for each container.
type containerRecord struct {
	Name         string `json:"name"`
	LastModified string `json:"lastModified"`
	Etag         string `json:"etag"`
}

// blobMeta is stored in the meta namespace for each blob.
type blobMeta struct {
	ContentType  string `json:"contentType"`
	BlobType     string `json:"blobType"`
	LastModified string `json:"lastModified"`
	Etag         string `json:"etag"`
	Size         int64  `json:"size"`
}

// xmlError is an Azure Blob Storage XML error response.
type xmlError struct {
	XMLName xml.Name `xml:"Error"`
	Code    string   `xml:"Code"`
	Message string   `xml:"Message"`
}

// ---------------------------------------------------------------------------
// RegisterRoutes wires Azure Blob Storage endpoints into the given mux.
// ---------------------------------------------------------------------------

// RegisterRoutes registers Azure Blob Storage API routes on mux.
func RegisterRoutes(mux *http.ServeMux, store storage.Store, cfg *config.Config) {
	h := &handler{store: store, cfg: cfg}

	// The Blob Storage API uses a path prefix of /{accountName}.
	// Container operations use ?restype=container query param.
	// We register a catch-all and dispatch manually because blob names
	// can contain slashes, which makes Go's mux pattern matching insufficient.
	// Register per-method to avoid a conflict with POST /{tenantId}/oauth2/v2.0/token.
	prefix := "/" + accountName + "/"
	mux.HandleFunc("GET "+prefix, h.dispatch)
	mux.HandleFunc("PUT "+prefix, h.dispatch)
	mux.HandleFunc("HEAD "+prefix, h.dispatch)
	mux.HandleFunc("DELETE "+prefix, h.dispatch)
	// Handle requests to the account root (list containers).
	mux.HandleFunc("GET /"+accountName, h.dispatch)
}

// ---------------------------------------------------------------------------
// handler
// ---------------------------------------------------------------------------

type handler struct {
	store storage.Store
	cfg   *config.Config
}

func (h *handler) dispatch(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("x-ms-request-id", server.RequestID())
	w.Header().Set("x-ms-version", "2020-10-02")

	path := r.URL.Path

	// Strip the account prefix.
	trimmed := strings.TrimPrefix(path, "/"+accountName)
	trimmed = strings.TrimPrefix(trimmed, "/")

	// If trimmed is empty, this is an account-level operation.
	if trimmed == "" {
		if r.Method == http.MethodGet && r.URL.Query().Get("comp") == "list" {
			h.listContainers(w, r)
			return
		}
		writeXMLError(w, "InvalidQueryParameterValue", "Unsupported operation", http.StatusBadRequest)
		return
	}

	// Split into container and blob parts.
	parts := strings.SplitN(trimmed, "/", 2)
	container := parts[0]
	blobName := ""
	if len(parts) > 1 {
		blobName = parts[1]
	}

	// If there is no blob name, this is a container-level operation.
	if blobName == "" {
		query := r.URL.Query()
		restype := query.Get("restype")
		comp := query.Get("comp")

		if restype == "container" && comp == "list" {
			h.listBlobs(w, r, container)
			return
		}
		if restype == "container" {
			switch r.Method {
			case http.MethodPut:
				h.createContainer(w, r, container)
				return
			case http.MethodDelete:
				h.deleteContainer(w, r, container)
				return
			}
		}

		writeXMLError(w, "InvalidQueryParameterValue", "Unsupported operation", http.StatusBadRequest)
		return
	}

	// Blob-level operations.
	switch r.Method {
	case http.MethodPut:
		h.putBlob(w, r, container, blobName)
	case http.MethodGet:
		h.getBlob(w, r, container, blobName)
	case http.MethodHead:
		h.headBlob(w, r, container, blobName)
	case http.MethodDelete:
		h.deleteBlob(w, r, container, blobName)
	default:
		writeXMLError(w, "UnsupportedHttpVerb", "The HTTP method is not supported", http.StatusMethodNotAllowed)
	}
}

// ---------------------------------------------------------------------------
// Container operations
// ---------------------------------------------------------------------------

func (h *handler) createContainer(w http.ResponseWriter, _ *http.Request, name string) {
	// Check if container already exists.
	if _, ok := h.store.Get(containerNamespace, name); ok {
		writeXMLError(w, "ContainerAlreadyExists", "The specified container already exists.", http.StatusConflict)
		return
	}

	now := time.Now().UTC().Format(http.TimeFormat)
	etag := fmt.Sprintf("0x%016X", time.Now().UnixNano())

	rec := containerRecord{
		Name:         name,
		LastModified: now,
		Etag:         etag,
	}
	data, _ := json.Marshal(rec)
	h.store.Put(containerNamespace, name, data)

	w.Header().Set("ETag", etag)
	w.Header().Set("Last-Modified", now)
	w.WriteHeader(http.StatusCreated)
}

func (h *handler) listContainers(w http.ResponseWriter, _ *http.Request) {
	keys := h.store.List(containerNamespace, "")
	sort.Strings(keys)

	var containers []Container
	for _, key := range keys {
		data, ok := h.store.Get(containerNamespace, key)
		if !ok {
			continue
		}
		var rec containerRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			continue
		}
		containers = append(containers, Container{
			Name: rec.Name,
			Properties: ContainerProperties{
				LastModified: rec.LastModified,
				Etag:         rec.Etag,
			},
		})
	}

	result := EnumerationResults{
		Containers: &Containers{Container: containers},
	}
	writeXML(w, http.StatusOK, result)
}

func (h *handler) deleteContainer(w http.ResponseWriter, _ *http.Request, name string) {
	if _, ok := h.store.Get(containerNamespace, name); !ok {
		writeXMLError(w, "ContainerNotFound", "The specified container does not exist.", http.StatusNotFound)
		return
	}

	// Check if container has blobs.
	blobs := h.store.List(contentNamespace(name), "")
	if len(blobs) > 0 {
		writeXMLError(w, "ContainerNotEmpty", "The specified container is not empty.", http.StatusConflict)
		return
	}

	h.store.Delete(containerNamespace, name)
	h.store.Clear(contentNamespace(name))
	h.store.Clear(metaNamespace(name))

	w.WriteHeader(http.StatusAccepted)
}

func (h *handler) listBlobs(w http.ResponseWriter, r *http.Request, container string) {
	if _, ok := h.store.Get(containerNamespace, container); !ok {
		writeXMLError(w, "ContainerNotFound", "The specified container does not exist.", http.StatusNotFound)
		return
	}

	prefix := r.URL.Query().Get("prefix")
	delimiter := r.URL.Query().Get("delimiter")

	keys := h.store.List(metaNamespace(container), prefix)
	sort.Strings(keys)

	// If delimiter is set, filter to only direct "children" of the prefix.
	seen := make(map[string]bool)
	var blobs []Blob
	for _, key := range keys {
		if delimiter != "" {
			suffix := strings.TrimPrefix(key, prefix)
			idx := strings.Index(suffix, delimiter)
			if idx >= 0 {
				// This is a "virtual directory"; skip it to avoid listing
				// blobs nested under sub-prefixes. We just skip these entries
				// to keep the emulator simple. Real Azure would return
				// BlobPrefix elements; we omit them for simplicity.
				dirKey := prefix + suffix[:idx+len(delimiter)]
				if seen[dirKey] {
					continue
				}
				seen[dirKey] = true
				continue
			}
		}

		data, ok := h.store.Get(metaNamespace(container), key)
		if !ok {
			continue
		}
		var meta blobMeta
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}
		blobs = append(blobs, Blob{
			Name: key,
			Properties: BlobProperties{
				ContentLength: meta.Size,
				ContentType:   meta.ContentType,
				LastModified:  meta.LastModified,
			},
		})
	}

	result := EnumerationResults{
		Blobs: &Blobs{Blob: blobs},
	}
	writeXML(w, http.StatusOK, result)
}

// ---------------------------------------------------------------------------
// Blob operations
// ---------------------------------------------------------------------------

func (h *handler) putBlob(w http.ResponseWriter, r *http.Request, container, blobName string) {
	// Check container exists.
	if _, ok := h.store.Get(containerNamespace, container); !ok {
		writeXMLError(w, "ContainerNotFound", "The specified container does not exist.", http.StatusNotFound)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeXMLError(w, "InternalError", "Failed to read request body", http.StatusInternalServerError)
		return
	}

	blobType := r.Header.Get("x-ms-blob-type")
	if blobType == "" {
		blobType = "BlockBlob"
	}

	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	now := time.Now().UTC().Format(http.TimeFormat)
	etag := fmt.Sprintf("0x%016X", time.Now().UnixNano())

	meta := blobMeta{
		ContentType:  contentType,
		BlobType:     blobType,
		LastModified: now,
		Etag:         etag,
		Size:         int64(len(body)),
	}

	metaData, _ := json.Marshal(meta)
	h.store.Put(contentNamespace(container), blobName, body)
	h.store.Put(metaNamespace(container), blobName, metaData)

	w.Header().Set("ETag", etag)
	w.Header().Set("Last-Modified", now)
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusCreated)
}

func (h *handler) getBlob(w http.ResponseWriter, _ *http.Request, container, blobName string) {
	metaData, ok := h.store.Get(metaNamespace(container), blobName)
	if !ok {
		writeXMLError(w, "BlobNotFound", "The specified blob does not exist.", http.StatusNotFound)
		return
	}

	content, ok := h.store.Get(contentNamespace(container), blobName)
	if !ok {
		writeXMLError(w, "BlobNotFound", "The specified blob does not exist.", http.StatusNotFound)
		return
	}

	var meta blobMeta
	if err := json.Unmarshal(metaData, &meta); err != nil {
		writeXMLError(w, "InternalError", "Failed to read blob metadata", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	w.Header().Set("x-ms-blob-type", meta.BlobType)
	w.Header().Set("Last-Modified", meta.LastModified)
	w.Header().Set("ETag", meta.Etag)
	w.WriteHeader(http.StatusOK)
	w.Write(content)
}

func (h *handler) headBlob(w http.ResponseWriter, _ *http.Request, container, blobName string) {
	metaData, ok := h.store.Get(metaNamespace(container), blobName)
	if !ok {
		writeXMLError(w, "BlobNotFound", "The specified blob does not exist.", http.StatusNotFound)
		return
	}

	var meta blobMeta
	if err := json.Unmarshal(metaData, &meta); err != nil {
		writeXMLError(w, "InternalError", "Failed to read blob metadata", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", meta.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	w.Header().Set("x-ms-blob-type", meta.BlobType)
	w.Header().Set("Last-Modified", meta.LastModified)
	w.Header().Set("ETag", meta.Etag)
	w.WriteHeader(http.StatusOK)
}

func (h *handler) deleteBlob(w http.ResponseWriter, _ *http.Request, container, blobName string) {
	metaExists := h.store.Delete(metaNamespace(container), blobName)
	h.store.Delete(contentNamespace(container), blobName)

	if !metaExists {
		writeXMLError(w, "BlobNotFound", "The specified blob does not exist.", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// ---------------------------------------------------------------------------
// XML helpers
// ---------------------------------------------------------------------------

func writeXML(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprint(w, xml.Header)
	enc := xml.NewEncoder(w)
	enc.Encode(v)
}

func writeXMLError(w http.ResponseWriter, code, message string, status int) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprint(w, xml.Header)
	enc := xml.NewEncoder(w)
	enc.Encode(xmlError{Code: code, Message: message})
}
