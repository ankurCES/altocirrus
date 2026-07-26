package gcpstorage_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/altocirrus/altocirrus/internal/config"
	gcpstorage "github.com/altocirrus/altocirrus/internal/gcp/storage"
	"github.com/altocirrus/altocirrus/internal/storage"
)

// gcpErrorEnvelope mirrors the error shape returned by server.GCPError.
type gcpErrorEnvelope struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	cfg := &config.Config{
		GCP: config.GCPConfig{
			ProjectID:     "test-project",
			ProjectNumber: "123456789",
			Region:        "us-central1",
		},
	}
	store := storage.NewMemoryStore()
	mux := http.NewServeMux()
	gcpstorage.RegisterRoutes(mux, store, cfg)
	return httptest.NewServer(mux)
}

// helper: create a bucket.
func createBucket(t *testing.T, ts *httptest.Server, name string) gcpstorage.Bucket {
	t.Helper()
	body := `{"name":"` + name + `"}`
	resp, err := http.Post(ts.URL+"/storage/v1/b", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("create bucket %s: status %d, body: %s", name, resp.StatusCode, b)
	}
	var bucket gcpstorage.Bucket
	json.NewDecoder(resp.Body).Decode(&bucket)
	return bucket
}

// helper: simple upload of content.
func simpleUpload(t *testing.T, ts *httptest.Server, bucket, objectName, contentType, content string) gcpstorage.Object {
	t.Helper()
	url := ts.URL + "/upload/storage/v1/b/" + bucket + "/o?uploadType=media&name=" + objectName
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(content))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("simple upload: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("simple upload %s: status %d, body: %s", objectName, resp.StatusCode, b)
	}
	var obj gcpstorage.Object
	json.NewDecoder(resp.Body).Decode(&obj)
	return obj
}

// helper: download object content.
func downloadObject(t *testing.T, ts *httptest.Server, bucket, objectName string) (string, http.Header) {
	t.Helper()
	url := ts.URL + "/storage/v1/b/" + bucket + "/o/" + objectName + "?alt=media"
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("download %s: status %d, body: %s", objectName, resp.StatusCode, b)
	}
	body, _ := io.ReadAll(resp.Body)
	return string(body), resp.Header
}

// ---------- Tests -----------------------------------------------------------

func TestBucketCRUD(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// 1. Create bucket.
	bucket := createBucket(t, ts, "test-bucket")
	if bucket.Name != "test-bucket" {
		t.Errorf("expected name test-bucket, got %q", bucket.Name)
	}
	if bucket.Kind != "storage#bucket" {
		t.Errorf("expected kind storage#bucket, got %q", bucket.Kind)
	}
	if bucket.StorageClass != "STANDARD" {
		t.Errorf("expected storageClass STANDARD, got %q", bucket.StorageClass)
	}
	if bucket.Location != "US" {
		t.Errorf("expected default location US, got %q", bucket.Location)
	}
	if bucket.ProjectNumber != "123456789" {
		t.Errorf("expected projectNumber 123456789, got %q", bucket.ProjectNumber)
	}

	// 2. List buckets.
	resp, err := http.Get(ts.URL + "/storage/v1/b")
	if err != nil {
		t.Fatalf("list buckets: %v", err)
	}
	defer resp.Body.Close()
	var listResp gcpstorage.BucketList
	json.NewDecoder(resp.Body).Decode(&listResp)
	if listResp.Kind != "storage#buckets" {
		t.Errorf("expected kind storage#buckets, got %q", listResp.Kind)
	}
	if len(listResp.Items) != 1 {
		t.Fatalf("expected 1 bucket, got %d", len(listResp.Items))
	}
	if listResp.Items[0].Name != "test-bucket" {
		t.Errorf("expected bucket name test-bucket, got %q", listResp.Items[0].Name)
	}

	// 3. Get bucket.
	resp2, err := http.Get(ts.URL + "/storage/v1/b/test-bucket")
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("get bucket: status %d", resp2.StatusCode)
	}
	var gotBucket gcpstorage.Bucket
	json.NewDecoder(resp2.Body).Decode(&gotBucket)
	if gotBucket.Name != "test-bucket" {
		t.Errorf("expected name test-bucket, got %q", gotBucket.Name)
	}

	// 4. Delete bucket.
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/storage/v1/b/test-bucket", nil)
	delResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete bucket: %v", err)
	}
	defer delResp.Body.Close()
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", delResp.StatusCode)
	}

	// Verify gone.
	getResp, err := http.Get(ts.URL + "/storage/v1/b/test-bucket")
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 after delete, got %d", getResp.StatusCode)
	}
}

func TestDeleteNonEmptyBucket(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	createBucket(t, ts, "non-empty-bucket")
	simpleUpload(t, ts, "non-empty-bucket", "file.txt", "text/plain", "hello")

	// Attempt to delete -- should get 409.
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/storage/v1/b/non-empty-bucket", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete bucket: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 Conflict, got %d", resp.StatusCode)
	}

	var errResp gcpErrorEnvelope
	json.NewDecoder(resp.Body).Decode(&errResp)
	if errResp.Error.Status != "FAILED_PRECONDITION" {
		t.Errorf("expected FAILED_PRECONDITION, got %q", errResp.Error.Status)
	}
}

func TestSimpleUpload(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	createBucket(t, ts, "upload-bucket")

	content := "hello, world! this is a test file."
	obj := simpleUpload(t, ts, "upload-bucket", "greeting.txt", "text/plain", content)

	// Verify object metadata.
	if obj.Name != "greeting.txt" {
		t.Errorf("expected name greeting.txt, got %q", obj.Name)
	}
	if obj.Bucket != "upload-bucket" {
		t.Errorf("expected bucket upload-bucket, got %q", obj.Bucket)
	}
	if obj.Kind != "storage#object" {
		t.Errorf("expected kind storage#object, got %q", obj.Kind)
	}
	if obj.ContentType != "text/plain" {
		t.Errorf("expected content-type text/plain, got %q", obj.ContentType)
	}
	if obj.Size != "34" {
		t.Errorf("expected size 34, got %q", obj.Size)
	}
	if obj.Md5Hash == "" {
		t.Error("md5Hash should not be empty")
	}
	if obj.Crc32c == "" {
		t.Error("crc32c should not be empty")
	}

	// Download and verify content.
	downloaded, headers := downloadObject(t, ts, "upload-bucket", "greeting.txt")
	if downloaded != content {
		t.Errorf("expected %q, got %q", content, downloaded)
	}
	if ct := headers.Get("Content-Type"); ct != "text/plain" {
		t.Errorf("expected Content-Type text/plain, got %q", ct)
	}

	// Get metadata without alt=media.
	resp, err := http.Get(ts.URL + "/storage/v1/b/upload-bucket/o/greeting.txt")
	if err != nil {
		t.Fatalf("get metadata: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get metadata: status %d", resp.StatusCode)
	}
	var meta gcpstorage.Object
	json.NewDecoder(resp.Body).Decode(&meta)
	if meta.Name != "greeting.txt" {
		t.Errorf("metadata name: expected greeting.txt, got %q", meta.Name)
	}
}

func TestResumableUpload(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	createBucket(t, ts, "resumable-bucket")

	// 1. Initiate resumable upload.
	initBody := `{"name":"big-file.bin","contentType":"application/octet-stream"}`
	initURL := ts.URL + "/upload/storage/v1/b/resumable-bucket/o?uploadType=resumable"
	initResp, err := http.Post(initURL, "application/json", bytes.NewBufferString(initBody))
	if err != nil {
		t.Fatalf("initiate resumable: %v", err)
	}
	defer initResp.Body.Close()

	if initResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(initResp.Body)
		t.Fatalf("initiate: status %d, body: %s", initResp.StatusCode, b)
	}

	location := initResp.Header.Get("Location")
	if location == "" {
		t.Fatal("expected Location header in initiate response")
	}
	if !strings.Contains(location, "uploadType=resumable") {
		t.Errorf("Location should contain uploadType=resumable: %q", location)
	}
	if !strings.Contains(location, "upload_id=") {
		t.Errorf("Location should contain upload_id: %q", location)
	}

	// 2. Complete the upload by PUTting data to the location.
	content := "binary-content-here-0123456789"
	putURL := ts.URL + location
	putReq, _ := http.NewRequest(http.MethodPut, putURL, strings.NewReader(content))
	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		t.Fatalf("complete resumable: %v", err)
	}
	defer putResp.Body.Close()

	if putResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(putResp.Body)
		t.Fatalf("complete resumable: status %d, body: %s", putResp.StatusCode, b)
	}

	var obj gcpstorage.Object
	json.NewDecoder(putResp.Body).Decode(&obj)
	if obj.Name != "big-file.bin" {
		t.Errorf("expected name big-file.bin, got %q", obj.Name)
	}
	if obj.Bucket != "resumable-bucket" {
		t.Errorf("expected bucket resumable-bucket, got %q", obj.Bucket)
	}
	if obj.ContentType != "application/octet-stream" {
		t.Errorf("expected content-type application/octet-stream, got %q", obj.ContentType)
	}

	// 3. Download and verify.
	downloaded, _ := downloadObject(t, ts, "resumable-bucket", "big-file.bin")
	if downloaded != content {
		t.Errorf("expected %q, got %q", content, downloaded)
	}
}

func TestListObjectsWithPrefix(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	createBucket(t, ts, "prefix-bucket")

	// Upload objects: a/1, a/2, b/1
	simpleUpload(t, ts, "prefix-bucket", "a/1", "text/plain", "a1")
	simpleUpload(t, ts, "prefix-bucket", "a/2", "text/plain", "a2")
	simpleUpload(t, ts, "prefix-bucket", "b/1", "text/plain", "b1")

	// List with prefix=a/
	resp, err := http.Get(ts.URL + "/storage/v1/b/prefix-bucket/o?prefix=a/")
	if err != nil {
		t.Fatalf("list objects: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list objects: status %d", resp.StatusCode)
	}

	var listResp gcpstorage.ObjectList
	json.NewDecoder(resp.Body).Decode(&listResp)

	if listResp.Kind != "storage#objects" {
		t.Errorf("expected kind storage#objects, got %q", listResp.Kind)
	}
	if len(listResp.Items) != 2 {
		t.Fatalf("expected 2 objects with prefix a/, got %d", len(listResp.Items))
	}

	// Verify all returned objects have prefix "a/".
	for _, obj := range listResp.Items {
		if !strings.HasPrefix(obj.Name, "a/") {
			t.Errorf("object %q does not have prefix a/", obj.Name)
		}
	}

	// Verify the exact names.
	names := map[string]bool{}
	for _, obj := range listResp.Items {
		names[obj.Name] = true
	}
	if !names["a/1"] || !names["a/2"] {
		t.Errorf("expected a/1 and a/2 in results, got %v", names)
	}
}

func TestListObjectsWithDelimiter(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	createBucket(t, ts, "delim-bucket")

	simpleUpload(t, ts, "delim-bucket", "photos/2024/jan.jpg", "image/jpeg", "jan")
	simpleUpload(t, ts, "delim-bucket", "photos/2024/feb.jpg", "image/jpeg", "feb")
	simpleUpload(t, ts, "delim-bucket", "photos/2025/mar.jpg", "image/jpeg", "mar")
	simpleUpload(t, ts, "delim-bucket", "docs/readme.txt", "text/plain", "readme")

	// List with prefix=photos/ and delimiter=/
	resp, err := http.Get(ts.URL + "/storage/v1/b/delim-bucket/o?prefix=photos/&delimiter=/")
	if err != nil {
		t.Fatalf("list objects: %v", err)
	}
	defer resp.Body.Close()

	var listResp gcpstorage.ObjectList
	json.NewDecoder(resp.Body).Decode(&listResp)

	// With delimiter, subdirectories collapse into prefixes.
	if len(listResp.Items) != 0 {
		t.Errorf("expected 0 direct items, got %d", len(listResp.Items))
	}
	if len(listResp.Prefixes) != 2 {
		t.Fatalf("expected 2 common prefixes, got %d: %v", len(listResp.Prefixes), listResp.Prefixes)
	}

	prefixSet := map[string]bool{}
	for _, p := range listResp.Prefixes {
		prefixSet[p] = true
	}
	if !prefixSet["photos/2024/"] || !prefixSet["photos/2025/"] {
		t.Errorf("expected prefixes photos/2024/ and photos/2025/, got %v", listResp.Prefixes)
	}
}

func TestDeleteObject(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	createBucket(t, ts, "del-obj-bucket")
	simpleUpload(t, ts, "del-obj-bucket", "file.txt", "text/plain", "data")

	// Delete the object.
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/storage/v1/b/del-obj-bucket/o/file.txt", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete object: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	// Verify gone.
	getResp, err := http.Get(ts.URL + "/storage/v1/b/del-obj-bucket/o/file.txt?alt=media")
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 after delete, got %d", getResp.StatusCode)
	}

	// Now bucket delete should succeed since it is empty.
	delBucket, err := http.NewRequest(http.MethodDelete, ts.URL+"/storage/v1/b/del-obj-bucket", nil)
	if err != nil {
		t.Fatalf("new request for bucket delete: %v", err)
	}
	delBucketResp, err := http.DefaultClient.Do(delBucket)
	if err != nil {
		t.Fatalf("bucket delete: %v", err)
	}
	defer delBucketResp.Body.Close()
	if delBucketResp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204 for empty bucket delete, got %d", delBucketResp.StatusCode)
	}
}

func TestBucketNotFound(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/storage/v1/b/ghost-bucket")
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}

	var errResp gcpErrorEnvelope
	json.NewDecoder(resp.Body).Decode(&errResp)
	if errResp.Error.Status != "NOT_FOUND" {
		t.Errorf("expected NOT_FOUND, got %q", errResp.Error.Status)
	}
}

func TestUploadDefaultContentType(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	createBucket(t, ts, "ct-bucket")

	// Upload without Content-Type header.
	url := ts.URL + "/upload/storage/v1/b/ct-bucket/o?uploadType=media&name=noct.bin"
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader("binary"))
	// Explicitly clear Content-Type.
	req.Header.Del("Content-Type")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	defer resp.Body.Close()

	var obj gcpstorage.Object
	json.NewDecoder(resp.Body).Decode(&obj)

	// The implementation defaults to application/octet-stream.
	if obj.ContentType != "application/octet-stream" {
		t.Errorf("expected default content type application/octet-stream, got %q", obj.ContentType)
	}
}
