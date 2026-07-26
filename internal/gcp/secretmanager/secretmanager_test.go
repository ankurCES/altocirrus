package secretmanager_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/altocirrus/altocirrus/internal/config"
	"github.com/altocirrus/altocirrus/internal/gcp/secretmanager"
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
	secretmanager.RegisterRoutes(mux, store, cfg)
	return httptest.NewServer(mux)
}

// helper: create a secret and return the parsed response.
func createSecret(t *testing.T, ts *httptest.Server, project, secretID string) secretmanager.Secret {
	t.Helper()
	body := `{"replication":{"automatic":{}}}`
	url := ts.URL + "/v1/projects/" + project + "/secrets?secretId=" + secretID
	resp, err := http.Post(url, "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("create secret: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("create secret %s: status %d, body: %s", secretID, resp.StatusCode, b)
	}
	var s secretmanager.Secret
	json.NewDecoder(resp.Body).Decode(&s)
	return s
}

// helper: add a version and return the parsed response.
func addVersion(t *testing.T, ts *httptest.Server, project, secretID, plaintext string) secretmanager.SecretVersion {
	t.Helper()
	encoded := base64.StdEncoding.EncodeToString([]byte(plaintext))
	body := `{"payload":{"data":"` + encoded + `"}}`
	url := ts.URL + "/v1/projects/" + project + "/secrets/" + secretID + ":addVersion"
	resp, err := http.Post(url, "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("add version: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("add version: status %d, body: %s", resp.StatusCode, b)
	}
	var sv secretmanager.SecretVersion
	json.NewDecoder(resp.Body).Decode(&sv)
	return sv
}

// helper: access a version and return the parsed response.
func accessVersion(t *testing.T, ts *httptest.Server, project, secretID, version string) secretmanager.AccessResponse {
	t.Helper()
	url := ts.URL + "/v1/projects/" + project + "/secrets/" + secretID + "/versions/" + version + ":access"
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("access version: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("access version %s: status %d, body: %s", version, resp.StatusCode, b)
	}
	var ar secretmanager.AccessResponse
	json.NewDecoder(resp.Body).Decode(&ar)
	return ar
}

func TestSecretLifecycle(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	project := "test-project"

	// 1. Create secret.
	created := createSecret(t, ts, project, "my-secret")
	if created.Name != "projects/test-project/secrets/my-secret" {
		t.Errorf("unexpected name: %q", created.Name)
	}
	if created.Replication == nil || created.Replication.Automatic == nil {
		t.Error("expected automatic replication to be set")
	}
	if created.CreateTime == "" {
		t.Error("createTime should not be empty")
	}

	// 2. Add a version.
	ver := addVersion(t, ts, project, "my-secret", "super-secret-value")
	if ver.State != "ENABLED" {
		t.Errorf("expected state ENABLED, got %q", ver.State)
	}
	if ver.Name != "projects/test-project/secrets/my-secret/versions/1" {
		t.Errorf("unexpected version name: %q", ver.Name)
	}

	// 3. Access latest version.
	ar := accessVersion(t, ts, project, "my-secret", "latest")
	decoded, err := base64.StdEncoding.DecodeString(ar.Payload.Data)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if string(decoded) != "super-secret-value" {
		t.Errorf("expected payload %q, got %q", "super-secret-value", string(decoded))
	}
	if ar.Payload.DataCrc32c == "" {
		t.Error("dataCrc32c should not be empty")
	}

	// 4. List secrets.
	resp, err := http.Get(ts.URL + "/v1/projects/" + project + "/secrets")
	if err != nil {
		t.Fatalf("list secrets: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list secrets: status %d", resp.StatusCode)
	}
	var listResp struct {
		Secrets   []secretmanager.Secret `json:"secrets"`
		TotalSize int                    `json:"totalSize"`
	}
	json.NewDecoder(resp.Body).Decode(&listResp)
	if len(listResp.Secrets) != 1 {
		t.Fatalf("expected 1 secret, got %d", len(listResp.Secrets))
	}
	if listResp.TotalSize != 1 {
		t.Errorf("expected totalSize=1, got %d", listResp.TotalSize)
	}

	// 5. Delete secret.
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/v1/projects/"+project+"/secrets/my-secret", nil)
	delResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete secret: %v", err)
	}
	defer delResp.Body.Close()
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("delete secret: status %d", delResp.StatusCode)
	}

	// Verify gone.
	getResp, err := http.Get(ts.URL + "/v1/projects/" + project + "/secrets/my-secret")
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 after delete, got %d", getResp.StatusCode)
	}
}

func TestAccessSpecificVersion(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	project := "test-project"
	createSecret(t, ts, project, "versioned-secret")

	// Add two versions with different data.
	addVersion(t, ts, project, "versioned-secret", "version-one-data")
	addVersion(t, ts, project, "versioned-secret", "version-two-data")

	// Access version 1 specifically.
	ar := accessVersion(t, ts, project, "versioned-secret", "1")
	decoded, _ := base64.StdEncoding.DecodeString(ar.Payload.Data)
	if string(decoded) != "version-one-data" {
		t.Errorf("expected version 1 data %q, got %q", "version-one-data", string(decoded))
	}
	if ar.Name != "projects/test-project/secrets/versioned-secret/versions/1" {
		t.Errorf("unexpected version name: %q", ar.Name)
	}

	// Access version 2 specifically.
	ar2 := accessVersion(t, ts, project, "versioned-secret", "2")
	decoded2, _ := base64.StdEncoding.DecodeString(ar2.Payload.Data)
	if string(decoded2) != "version-two-data" {
		t.Errorf("expected version 2 data %q, got %q", "version-two-data", string(decoded2))
	}

	// Access latest should be version 2.
	arLatest := accessVersion(t, ts, project, "versioned-secret", "latest")
	decodedLatest, _ := base64.StdEncoding.DecodeString(arLatest.Payload.Data)
	if string(decodedLatest) != "version-two-data" {
		t.Errorf("latest should be version-two-data, got %q", string(decodedLatest))
	}
}

func TestCreateDuplicate(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	project := "test-project"

	// First creation succeeds.
	createSecret(t, ts, project, "dup-secret")

	// Second creation should return 409.
	body := `{"replication":{"automatic":{}}}`
	url := ts.URL + "/v1/projects/" + project + "/secrets?secretId=dup-secret"
	resp, err := http.Post(url, "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("create duplicate: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 Conflict, got %d", resp.StatusCode)
	}

	var errResp gcpErrorEnvelope
	json.NewDecoder(resp.Body).Decode(&errResp)
	if errResp.Error.Code != http.StatusConflict {
		t.Errorf("expected error code 409, got %d", errResp.Error.Code)
	}
	if errResp.Error.Status != "ALREADY_EXISTS" {
		t.Errorf("expected status ALREADY_EXISTS, got %q", errResp.Error.Status)
	}
}

func TestDeleteNotFound(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	project := "test-project"

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/v1/projects/"+project+"/secrets/nonexistent-secret", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}

	var errResp gcpErrorEnvelope
	json.NewDecoder(resp.Body).Decode(&errResp)
	if errResp.Error.Code != http.StatusNotFound {
		t.Errorf("expected error code 404, got %d", errResp.Error.Code)
	}
	if errResp.Error.Status != "NOT_FOUND" {
		t.Errorf("expected status NOT_FOUND, got %q", errResp.Error.Status)
	}
	if errResp.Error.Message == "" {
		t.Error("expected non-empty error message")
	}
}

func TestAddVersionToNonexistent(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	project := "test-project"
	encoded := base64.StdEncoding.EncodeToString([]byte("data"))
	body := `{"payload":{"data":"` + encoded + `"}}`
	url := ts.URL + "/v1/projects/" + project + "/secrets/ghost:addVersion"
	resp, err := http.Post(url, "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("add version: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestAccessVersionNotFound(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	project := "test-project"
	createSecret(t, ts, project, "empty-secret")

	// No versions added -- accessing latest should be 404.
	url := ts.URL + "/v1/projects/" + project + "/secrets/empty-secret/versions/latest:access"
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("access: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for empty versions, got %d", resp.StatusCode)
	}
}
