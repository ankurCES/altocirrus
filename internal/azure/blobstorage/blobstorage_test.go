package blobstorage_test

import (
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/altocirrus/altocirrus/internal/azure/blobstorage"
	"github.com/altocirrus/altocirrus/internal/config"
	"github.com/altocirrus/altocirrus/internal/storage"
)

// newTestServer creates an httptest.Server with all Blob Storage routes registered.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	cfg := &config.Config{
		Port: 4567,
		Azure: config.AzureConfig{
			SubscriptionID: "test-sub-id",
			TenantID:       "test-tenant-id",
			Region:         "eastus",
		},
	}
	store := storage.NewMemoryStore()
	mux := http.NewServeMux()
	blobstorage.RegisterRoutes(mux, store, cfg)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// createContainer is a helper that PUTs a container and returns the response.
func createContainer(t *testing.T, ts *httptest.Server, name string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/devstoreaccount1/"+name+"?restype=container", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT container %s: %v", name, err)
	}
	return resp
}

// uploadBlob is a helper that PUTs a blob and returns the response.
func uploadBlob(t *testing.T, ts *httptest.Server, container, blobName, content, contentType string) *http.Response {
	t.Helper()
	url := ts.URL + "/devstoreaccount1/" + container + "/" + blobName
	req, _ := http.NewRequest(http.MethodPut, url, strings.NewReader(content))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT blob %s/%s: %v", container, blobName, err)
	}
	return resp
}

// XML response types for test assertions.

type enumerationResults struct {
	XMLName    xml.Name    `xml:"EnumerationResults"`
	Containers *containers `xml:"Containers"`
	Blobs      *blobs      `xml:"Blobs"`
}

type containers struct {
	Container []container `xml:"Container"`
}

type container struct {
	Name       string              `xml:"Name"`
	Properties containerProperties `xml:"Properties"`
}

type containerProperties struct {
	LastModified string `xml:"Last-Modified"`
	Etag         string `xml:"Etag"`
}

type blobs struct {
	Blob []blob `xml:"Blob"`
}

type blob struct {
	Name       string         `xml:"Name"`
	Properties blobProperties `xml:"Properties"`
}

type blobProperties struct {
	ContentLength int64  `xml:"Content-Length"`
	ContentType   string `xml:"Content-Type"`
	LastModified  string `xml:"Last-Modified"`
}

type xmlErrorResp struct {
	XMLName xml.Name `xml:"Error"`
	Code    string   `xml:"Code"`
	Message string   `xml:"Message"`
}

func TestContainerCRUD(t *testing.T) {
	ts := newTestServer(t)

	// 1. Create a container.
	resp := createContainer(t, ts, "mycontainer")
	resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create container: expected 201, got %d", resp.StatusCode)
	}
	if resp.Header.Get("x-ms-request-id") == "" {
		t.Error("missing x-ms-request-id header on create")
	}

	// 2. Create duplicate container should fail with 409.
	resp2 := createContainer(t, ts, "mycontainer")
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate container: expected 409, got %d", resp2.StatusCode)
	}

	// 3. List containers — should contain our container.
	listResp, err := http.Get(ts.URL + "/devstoreaccount1?comp=list")
	if err != nil {
		t.Fatalf("GET list containers: %v", err)
	}
	defer listResp.Body.Close()

	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list containers: expected 200, got %d", listResp.StatusCode)
	}

	body, _ := io.ReadAll(listResp.Body)
	var result enumerationResults
	if err := xml.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode list containers XML: %v", err)
	}
	if result.Containers == nil || len(result.Containers.Container) != 1 {
		t.Fatalf("expected 1 container, got %v", result.Containers)
	}
	if result.Containers.Container[0].Name != "mycontainer" {
		t.Errorf("container name = %q, want mycontainer", result.Containers.Container[0].Name)
	}
	if result.Containers.Container[0].Properties.LastModified == "" {
		t.Error("container Last-Modified is empty")
	}
	if result.Containers.Container[0].Properties.Etag == "" {
		t.Error("container Etag is empty")
	}

	// 4. Delete the empty container.
	delReq, _ := http.NewRequest(http.MethodDelete, ts.URL+"/devstoreaccount1/mycontainer?restype=container", nil)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("DELETE container: %v", err)
	}
	delResp.Body.Close()

	if delResp.StatusCode != http.StatusAccepted {
		t.Fatalf("delete container: expected 202, got %d", delResp.StatusCode)
	}

	// 5. Verify container is gone.
	listResp2, err := http.Get(ts.URL + "/devstoreaccount1?comp=list")
	if err != nil {
		t.Fatalf("GET list containers after delete: %v", err)
	}
	defer listResp2.Body.Close()

	body2, _ := io.ReadAll(listResp2.Body)
	var result2 enumerationResults
	if err := xml.Unmarshal(body2, &result2); err != nil {
		t.Fatalf("decode list containers XML: %v", err)
	}
	if result2.Containers != nil && len(result2.Containers.Container) != 0 {
		t.Errorf("expected 0 containers after delete, got %d", len(result2.Containers.Container))
	}
}

func TestBlobUploadDownloadDelete(t *testing.T) {
	ts := newTestServer(t)

	// Create container first.
	resp := createContainer(t, ts, "data")
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create container: expected 201, got %d", resp.StatusCode)
	}

	// 1. Upload a blob.
	putResp := uploadBlob(t, ts, "data", "hello.txt", "Hello, World!", "text/plain")
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusCreated {
		t.Fatalf("upload blob: expected 201, got %d", putResp.StatusCode)
	}
	if putResp.Header.Get("ETag") == "" {
		t.Error("missing ETag header on upload")
	}

	// 2. Download the blob.
	getResp, err := http.Get(ts.URL + "/devstoreaccount1/data/hello.txt")
	if err != nil {
		t.Fatalf("GET blob: %v", err)
	}
	defer getResp.Body.Close()

	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("download blob: expected 200, got %d", getResp.StatusCode)
	}
	if getResp.Header.Get("Content-Type") != "text/plain" {
		t.Errorf("Content-Type = %q, want text/plain", getResp.Header.Get("Content-Type"))
	}
	if getResp.Header.Get("x-ms-blob-type") != "BlockBlob" {
		t.Errorf("x-ms-blob-type = %q, want BlockBlob", getResp.Header.Get("x-ms-blob-type"))
	}

	content, _ := io.ReadAll(getResp.Body)
	if string(content) != "Hello, World!" {
		t.Errorf("blob content = %q, want %q", string(content), "Hello, World!")
	}

	// 3. Delete the blob.
	delReq, _ := http.NewRequest(http.MethodDelete, ts.URL+"/devstoreaccount1/data/hello.txt", nil)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("DELETE blob: %v", err)
	}
	delResp.Body.Close()

	if delResp.StatusCode != http.StatusAccepted {
		t.Fatalf("delete blob: expected 202, got %d", delResp.StatusCode)
	}

	// 4. Verify blob is gone.
	getResp2, err := http.Get(ts.URL + "/devstoreaccount1/data/hello.txt")
	if err != nil {
		t.Fatalf("GET deleted blob: %v", err)
	}
	defer getResp2.Body.Close()

	if getResp2.StatusCode != http.StatusNotFound {
		t.Fatalf("get deleted blob: expected 404, got %d", getResp2.StatusCode)
	}
}

func TestBlobWithSlashesInName(t *testing.T) {
	ts := newTestServer(t)

	resp := createContainer(t, ts, "files")
	resp.Body.Close()

	// Upload blob with path-like name.
	putResp := uploadBlob(t, ts, "files", "path/to/deep/file.json", `{"key":"value"}`, "application/json")
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusCreated {
		t.Fatalf("upload nested blob: expected 201, got %d", putResp.StatusCode)
	}

	// Download it back.
	getResp, err := http.Get(ts.URL + "/devstoreaccount1/files/path/to/deep/file.json")
	if err != nil {
		t.Fatalf("GET nested blob: %v", err)
	}
	defer getResp.Body.Close()

	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("download nested blob: expected 200, got %d", getResp.StatusCode)
	}

	content, _ := io.ReadAll(getResp.Body)
	if string(content) != `{"key":"value"}` {
		t.Errorf("blob content = %q, want %q", string(content), `{"key":"value"}`)
	}
}

func TestListBlobsWithPrefix(t *testing.T) {
	ts := newTestServer(t)

	resp := createContainer(t, ts, "logs")
	resp.Body.Close()

	// Upload several blobs.
	blobs := map[string]string{
		"2024/01/app.log":    "jan log",
		"2024/02/app.log":    "feb log",
		"2024/02/error.log":  "feb errors",
		"2025/01/app.log":    "next year",
	}
	for name, content := range blobs {
		r := uploadBlob(t, ts, "logs", name, content, "text/plain")
		r.Body.Close()
	}

	// List with prefix "2024/02/".
	listResp, err := http.Get(ts.URL + "/devstoreaccount1/logs?restype=container&comp=list&prefix=2024/02/")
	if err != nil {
		t.Fatalf("GET list blobs: %v", err)
	}
	defer listResp.Body.Close()

	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list blobs: expected 200, got %d", listResp.StatusCode)
	}

	body, _ := io.ReadAll(listResp.Body)
	var result enumerationResults
	if err := xml.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode list blobs XML: %v", err)
	}

	if result.Blobs == nil || len(result.Blobs.Blob) != 2 {
		count := 0
		if result.Blobs != nil {
			count = len(result.Blobs.Blob)
		}
		t.Fatalf("expected 2 blobs with prefix 2024/02/, got %d", count)
	}

	// Verify blob names are the ones with the prefix.
	names := make(map[string]bool)
	for _, b := range result.Blobs.Blob {
		names[b.Name] = true
	}
	if !names["2024/02/app.log"] {
		t.Error("expected 2024/02/app.log in results")
	}
	if !names["2024/02/error.log"] {
		t.Error("expected 2024/02/error.log in results")
	}
}

func TestListBlobsWithDelimiter(t *testing.T) {
	ts := newTestServer(t)

	resp := createContainer(t, ts, "hierarchy")
	resp.Body.Close()

	// Upload blobs at different "depths".
	uploads := []string{
		"root.txt",
		"dir1/file1.txt",
		"dir1/file2.txt",
		"dir2/file3.txt",
	}
	for _, name := range uploads {
		r := uploadBlob(t, ts, "hierarchy", name, "content", "text/plain")
		r.Body.Close()
	}

	// List with delimiter "/" — should only return "root.txt" (the direct blobs).
	listResp, err := http.Get(ts.URL + "/devstoreaccount1/hierarchy?restype=container&comp=list&delimiter=/")
	if err != nil {
		t.Fatalf("GET list blobs with delimiter: %v", err)
	}
	defer listResp.Body.Close()

	body, _ := io.ReadAll(listResp.Body)
	var result enumerationResults
	if err := xml.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode list blobs XML: %v", err)
	}

	if result.Blobs == nil || len(result.Blobs.Blob) != 1 {
		count := 0
		if result.Blobs != nil {
			count = len(result.Blobs.Blob)
		}
		t.Fatalf("expected 1 blob at root level, got %d", count)
	}
	if result.Blobs.Blob[0].Name != "root.txt" {
		t.Errorf("expected root.txt, got %s", result.Blobs.Blob[0].Name)
	}
}

func TestDeleteNonEmptyContainer(t *testing.T) {
	ts := newTestServer(t)

	resp := createContainer(t, ts, "notempty")
	resp.Body.Close()

	// Upload a blob.
	putResp := uploadBlob(t, ts, "notempty", "file.txt", "data", "text/plain")
	putResp.Body.Close()

	// Try to delete the container — should fail with 409.
	delReq, _ := http.NewRequest(http.MethodDelete, ts.URL+"/devstoreaccount1/notempty?restype=container", nil)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("DELETE non-empty container: %v", err)
	}
	defer delResp.Body.Close()

	if delResp.StatusCode != http.StatusConflict {
		t.Fatalf("delete non-empty container: expected 409, got %d", delResp.StatusCode)
	}

	body, _ := io.ReadAll(delResp.Body)
	var errResp xmlErrorResp
	if err := xml.Unmarshal(body, &errResp); err != nil {
		t.Fatalf("decode error XML: %v", err)
	}
	if errResp.Code != "ContainerNotEmpty" {
		t.Errorf("error code = %q, want ContainerNotEmpty", errResp.Code)
	}
}

func TestHeadBlobProperties(t *testing.T) {
	ts := newTestServer(t)

	resp := createContainer(t, ts, "props")
	resp.Body.Close()

	putResp := uploadBlob(t, ts, "props", "data.bin", "binary-content", "application/octet-stream")
	putResp.Body.Close()

	// HEAD request for blob properties.
	headReq, _ := http.NewRequest(http.MethodHead, ts.URL+"/devstoreaccount1/props/data.bin", nil)
	headResp, err := http.DefaultClient.Do(headReq)
	if err != nil {
		t.Fatalf("HEAD blob: %v", err)
	}
	defer headResp.Body.Close()

	if headResp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD blob: expected 200, got %d", headResp.StatusCode)
	}

	if headResp.Header.Get("Content-Type") != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", headResp.Header.Get("Content-Type"))
	}
	if headResp.Header.Get("Content-Length") != "14" {
		t.Errorf("Content-Length = %q, want 14", headResp.Header.Get("Content-Length"))
	}
	if headResp.Header.Get("x-ms-blob-type") != "BlockBlob" {
		t.Errorf("x-ms-blob-type = %q, want BlockBlob", headResp.Header.Get("x-ms-blob-type"))
	}
	if headResp.Header.Get("Last-Modified") == "" {
		t.Error("Last-Modified header is missing")
	}
	if headResp.Header.Get("ETag") == "" {
		t.Error("ETag header is missing")
	}
}

func TestHeadBlobNotFound(t *testing.T) {
	ts := newTestServer(t)

	resp := createContainer(t, ts, "empty")
	resp.Body.Close()

	headReq, _ := http.NewRequest(http.MethodHead, ts.URL+"/devstoreaccount1/empty/nonexistent.txt", nil)
	headResp, err := http.DefaultClient.Do(headReq)
	if err != nil {
		t.Fatalf("HEAD blob: %v", err)
	}
	defer headResp.Body.Close()

	if headResp.StatusCode != http.StatusNotFound {
		t.Fatalf("HEAD nonexistent blob: expected 404, got %d", headResp.StatusCode)
	}
}

func TestGetBlobNotFound(t *testing.T) {
	ts := newTestServer(t)

	resp := createContainer(t, ts, "bucket")
	resp.Body.Close()

	getResp, err := http.Get(ts.URL + "/devstoreaccount1/bucket/no-such-blob")
	if err != nil {
		t.Fatalf("GET blob: %v", err)
	}
	defer getResp.Body.Close()

	if getResp.StatusCode != http.StatusNotFound {
		t.Fatalf("get nonexistent blob: expected 404, got %d", getResp.StatusCode)
	}

	body, _ := io.ReadAll(getResp.Body)
	var errResp xmlErrorResp
	if err := xml.Unmarshal(body, &errResp); err != nil {
		t.Fatalf("decode error XML: %v", err)
	}
	if errResp.Code != "BlobNotFound" {
		t.Errorf("error code = %q, want BlobNotFound", errResp.Code)
	}
}

func TestUploadBlobToNonexistentContainer(t *testing.T) {
	ts := newTestServer(t)

	putResp := uploadBlob(t, ts, "nosuchcontainer", "file.txt", "data", "text/plain")
	defer putResp.Body.Close()

	if putResp.StatusCode != http.StatusNotFound {
		t.Fatalf("upload to nonexistent container: expected 404, got %d", putResp.StatusCode)
	}
}

func TestResponseHeaders(t *testing.T) {
	ts := newTestServer(t)

	resp := createContainer(t, ts, "headers-test")
	resp.Body.Close()

	if resp.Header.Get("x-ms-request-id") == "" {
		t.Error("missing x-ms-request-id header")
	}
}
