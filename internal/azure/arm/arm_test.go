package arm_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/altocirrus/altocirrus/internal/azure/arm"
	"github.com/altocirrus/altocirrus/internal/config"
	"github.com/altocirrus/altocirrus/internal/storage"
)

// azureErrorResponse represents the Azure error envelope.
type azureErrorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// subscription mirrors the API response for subscription objects.
type subscription struct {
	ID             string `json:"id"`
	SubscriptionID string `json:"subscriptionId"`
	DisplayName    string `json:"displayName"`
	State          string `json:"state"`
	TenantID       string `json:"tenantId"`
}

// resourceGroup mirrors the API response for resource group objects.
type resourceGroup struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Type     string            `json:"type"`
	Location string            `json:"location"`
	Tags     map[string]string `json:"tags"`
	Properties struct {
		ProvisioningState string `json:"provisioningState"`
	} `json:"properties"`
}

const (
	testSubID    = "test-sub-00000"
	testTenantID = "test-tenant-00000"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	cfg := &config.Config{
		Port: 4567,
		Azure: config.AzureConfig{
			SubscriptionID: testSubID,
			TenantID:       testTenantID,
			Region:         "eastus",
		},
	}
	store := storage.NewMemoryStore()
	mux := http.NewServeMux()
	arm.RegisterRoutes(mux, store, cfg)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func TestListSubscriptions(t *testing.T) {
	ts := newTestServer(t)

	resp, err := http.Get(ts.URL + "/subscriptions")
	if err != nil {
		t.Fatalf("GET /subscriptions: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body struct {
		Value []subscription `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(body.Value) == 0 {
		t.Fatal("subscriptions list is empty")
	}

	sub := body.Value[0]
	if sub.SubscriptionID != testSubID {
		t.Errorf("subscriptionId = %q, want %q", sub.SubscriptionID, testSubID)
	}
	if sub.TenantID != testTenantID {
		t.Errorf("tenantId = %q, want %q", sub.TenantID, testTenantID)
	}
	if sub.State != "Enabled" {
		t.Errorf("state = %q, want Enabled", sub.State)
	}
	if sub.ID != "/subscriptions/"+testSubID {
		t.Errorf("id = %q, want /subscriptions/%s", sub.ID, testSubID)
	}
	if sub.DisplayName == "" {
		t.Error("displayName is empty")
	}
}

func TestGetSubscription(t *testing.T) {
	ts := newTestServer(t)

	resp, err := http.Get(ts.URL + "/subscriptions/" + testSubID)
	if err != nil {
		t.Fatalf("GET /subscriptions/{id}: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var sub subscription
	if err := json.NewDecoder(resp.Body).Decode(&sub); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if sub.SubscriptionID != testSubID {
		t.Errorf("subscriptionId = %q, want %q", sub.SubscriptionID, testSubID)
	}
}

func TestResourceGroupCRUD(t *testing.T) {
	ts := newTestServer(t)

	rgName := "my-resource-group"
	basePath := "/subscriptions/" + testSubID + "/resourceGroups/" + rgName

	// 1. Create resource group.
	createBody, _ := json.Marshal(map[string]any{
		"location": "westus2",
		"tags":     map[string]string{"env": "test"},
	})
	createReq, _ := http.NewRequest(http.MethodPut, ts.URL+basePath, bytes.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createResp, err := http.DefaultClient.Do(createReq)
	if err != nil {
		t.Fatalf("PUT create RG: %v", err)
	}
	defer createResp.Body.Close()

	if createResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT create: expected 200, got %d", createResp.StatusCode)
	}

	var created resourceGroup
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		t.Fatalf("decode created RG: %v", err)
	}

	if created.Name != rgName {
		t.Errorf("name = %q, want %q", created.Name, rgName)
	}
	if created.Location != "westus2" {
		t.Errorf("location = %q, want westus2", created.Location)
	}
	if created.Type != "Microsoft.Resources/resourceGroups" {
		t.Errorf("type = %q, want Microsoft.Resources/resourceGroups", created.Type)
	}
	if created.Properties.ProvisioningState != "Succeeded" {
		t.Errorf("provisioningState = %q, want Succeeded", created.Properties.ProvisioningState)
	}
	if created.Tags["env"] != "test" {
		t.Errorf("tag env = %q, want test", created.Tags["env"])
	}
	expectedID := "/subscriptions/" + testSubID + "/resourceGroups/" + rgName
	if created.ID != expectedID {
		t.Errorf("id = %q, want %q", created.ID, expectedID)
	}

	// 2. Read resource group.
	getResp, err := http.Get(ts.URL + basePath)
	if err != nil {
		t.Fatalf("GET RG: %v", err)
	}
	defer getResp.Body.Close()

	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET read: expected 200, got %d", getResp.StatusCode)
	}

	var fetched resourceGroup
	if err := json.NewDecoder(getResp.Body).Decode(&fetched); err != nil {
		t.Fatalf("decode fetched RG: %v", err)
	}
	if fetched.Name != rgName {
		t.Errorf("fetched name = %q, want %q", fetched.Name, rgName)
	}
	if fetched.Location != "westus2" {
		t.Errorf("fetched location = %q, want westus2", fetched.Location)
	}

	// 3. List resource groups -- should contain our RG.
	listResp, err := http.Get(ts.URL + "/subscriptions/" + testSubID + "/resourceGroups")
	if err != nil {
		t.Fatalf("GET list RGs: %v", err)
	}
	defer listResp.Body.Close()

	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("GET list: expected 200, got %d", listResp.StatusCode)
	}

	var listBody struct {
		Value []resourceGroup `json:"value"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&listBody); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listBody.Value) != 1 {
		t.Fatalf("list count = %d, want 1", len(listBody.Value))
	}
	if listBody.Value[0].Name != rgName {
		t.Errorf("listed RG name = %q, want %q", listBody.Value[0].Name, rgName)
	}

	// 4. Delete resource group.
	delReq, _ := http.NewRequest(http.MethodDelete, ts.URL+basePath, nil)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("DELETE RG: %v", err)
	}
	defer delResp.Body.Close()

	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE: expected 200, got %d", delResp.StatusCode)
	}

	// 5. GET after delete should return 404.
	getResp2, err := http.Get(ts.URL + basePath)
	if err != nil {
		t.Fatalf("GET after delete: %v", err)
	}
	defer getResp2.Body.Close()

	if getResp2.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after delete: expected 404, got %d", getResp2.StatusCode)
	}

	var errResp azureErrorResponse
	if err := json.NewDecoder(getResp2.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if errResp.Error.Code != "ResourceGroupNotFound" {
		t.Errorf("error code = %q, want ResourceGroupNotFound", errResp.Error.Code)
	}
}

func TestResourceGroupNotFound(t *testing.T) {
	ts := newTestServer(t)

	resp, err := http.Get(ts.URL + "/subscriptions/" + testSubID + "/resourceGroups/nonexistent-rg")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}

	var errResp azureErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if errResp.Error.Code != "ResourceGroupNotFound" {
		t.Errorf("error code = %q, want ResourceGroupNotFound", errResp.Error.Code)
	}
}

func TestListResourceGroupsEmpty(t *testing.T) {
	ts := newTestServer(t)

	resp, err := http.Get(ts.URL + "/subscriptions/" + testSubID + "/resourceGroups")
	if err != nil {
		t.Fatalf("GET list: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body struct {
		Value []resourceGroup `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Value) != 0 {
		t.Errorf("list count = %d, want 0", len(body.Value))
	}
}

func TestAzureResponseHeaders(t *testing.T) {
	ts := newTestServer(t)

	resp, err := http.Get(ts.URL + "/subscriptions")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	reqID := resp.Header.Get("x-ms-request-id")
	if reqID == "" {
		t.Error("x-ms-request-id header is missing")
	}

	corrID := resp.Header.Get("x-ms-correlation-request-id")
	if corrID == "" {
		t.Error("x-ms-correlation-request-id header is missing")
	}

	// Both should be the same UUID.
	if reqID != corrID {
		t.Errorf("x-ms-request-id (%s) != x-ms-correlation-request-id (%s)", reqID, corrID)
	}
}

func TestResourceGroupUpdateExisting(t *testing.T) {
	ts := newTestServer(t)

	rgName := "updatable-rg"
	basePath := "/subscriptions/" + testSubID + "/resourceGroups/" + rgName

	// Create.
	body1, _ := json.Marshal(map[string]any{
		"location": "eastus",
		"tags":     map[string]string{"version": "1"},
	})
	req1, _ := http.NewRequest(http.MethodPut, ts.URL+basePath, bytes.NewReader(body1))
	req1.Header.Set("Content-Type", "application/json")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatalf("PUT create: %v", err)
	}
	resp1.Body.Close()

	// Update with new tags.
	body2, _ := json.Marshal(map[string]any{
		"location": "eastus",
		"tags":     map[string]string{"version": "2", "extra": "tag"},
	})
	req2, _ := http.NewRequest(http.MethodPut, ts.URL+basePath, bytes.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("PUT update: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("PUT update: expected 200, got %d", resp2.StatusCode)
	}

	var updated resourceGroup
	if err := json.NewDecoder(resp2.Body).Decode(&updated); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if updated.Tags["version"] != "2" {
		t.Errorf("tag version = %q, want 2", updated.Tags["version"])
	}
	if updated.Tags["extra"] != "tag" {
		t.Errorf("tag extra = %q, want tag", updated.Tags["extra"])
	}
}
