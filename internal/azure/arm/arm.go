package arm

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/altocirrus/altocirrus/internal/config"
	"github.com/altocirrus/altocirrus/internal/server"
	"github.com/altocirrus/altocirrus/internal/storage"
)

const namespaceResourceGroups = "azure:arm:resourcegroups"

// subscription represents an Azure subscription in API responses.
type subscription struct {
	ID                   string               `json:"id"`
	SubscriptionID       string               `json:"subscriptionId"`
	DisplayName          string               `json:"displayName"`
	State                string               `json:"state"`
	TenantID             string               `json:"tenantId"`
	SubscriptionPolicies subscriptionPolicies `json:"subscriptionPolicies"`
}

type subscriptionPolicies struct {
	LocationPlacementID string `json:"locationPlacementId"`
	QuotaID             string `json:"quotaId"`
}

// resourceGroup represents an Azure resource group in API responses.
type resourceGroup struct {
	ID         string              `json:"id"`
	Name       string              `json:"name"`
	Type       string              `json:"type"`
	Location   string              `json:"location"`
	Tags       map[string]string   `json:"tags"`
	Properties rgProperties        `json:"properties"`
}

type rgProperties struct {
	ProvisioningState string `json:"provisioningState"`
}

// rgCreateRequest is the body for PUT resource group.
type rgCreateRequest struct {
	Location string            `json:"location"`
	Tags     map[string]string `json:"tags"`
}

// RegisterRoutes registers Azure ARM Resource Manager stub endpoints on the given mux.
func RegisterRoutes(mux *http.ServeMux, store storage.Store, cfg *config.Config) {
	h := &handler{store: store, cfg: cfg}

	mux.HandleFunc("/subscriptions", h.handleSubscriptions)
	// Register per-method to avoid a conflict with POST /{tenantId}/oauth2/v2.0/token.
	mux.HandleFunc("GET /subscriptions/", h.handleSubscriptionsSubpath)
	mux.HandleFunc("PUT /subscriptions/", h.handleSubscriptionsSubpath)
	mux.HandleFunc("DELETE /subscriptions/", h.handleSubscriptionsSubpath)
}

type handler struct {
	store storage.Store
	cfg   *config.Config
}

// setAzureHeaders adds the standard Azure response headers.
func setAzureHeaders(w http.ResponseWriter) {
	reqID := server.RequestID()
	w.Header().Set("x-ms-request-id", reqID)
	w.Header().Set("x-ms-correlation-request-id", reqID)
}

// buildSubscription creates a subscription object from config.
func (h *handler) buildSubscription() subscription {
	return subscription{
		ID:             "/subscriptions/" + h.cfg.Azure.SubscriptionID,
		SubscriptionID: h.cfg.Azure.SubscriptionID,
		DisplayName:    "AltoCirrus Local",
		State:          "Enabled",
		TenantID:       h.cfg.Azure.TenantID,
		SubscriptionPolicies: subscriptionPolicies{
			LocationPlacementID: "Internal_2014-09-01",
			QuotaID:             "Internal_2014-09-01",
		},
	}
}

// handleSubscriptions handles GET /subscriptions (exact path, no trailing slash).
func (h *handler) handleSubscriptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		setAzureHeaders(w)
		server.AzureError(w, "MethodNotAllowed", "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	setAzureHeaders(w)
	server.WriteJSON(w, http.StatusOK, map[string]any{
		"value": []subscription{h.buildSubscription()},
	})
}

// handleSubscriptionsSubpath handles all routes under /subscriptions/{subscriptionId}/...
func (h *handler) handleSubscriptionsSubpath(w http.ResponseWriter, r *http.Request) {
	setAzureHeaders(w)

	// Parse: /subscriptions/{subId}[/resourceGroups[/{rgName}]]
	parts := splitPath(r.URL.Path)
	// parts[0]="subscriptions", parts[1]=subId, ...

	if len(parts) < 2 {
		server.AzureError(w, "InvalidRequest", "Missing subscriptionId", http.StatusBadRequest)
		return
	}

	// GET /subscriptions/{subscriptionId}
	if len(parts) == 2 {
		if r.Method != http.MethodGet {
			server.AzureError(w, "MethodNotAllowed", "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		server.WriteJSON(w, http.StatusOK, h.buildSubscription())
		return
	}

	// Expect parts[2] == "resourceGroups" or "resourcegroups"
	if !strings.EqualFold(parts[2], "resourceGroups") {
		server.AzureError(w, "InvalidResourceType", "Unknown resource type: "+parts[2], http.StatusBadRequest)
		return
	}

	// GET /subscriptions/{subId}/resourceGroups — list all RGs
	if len(parts) == 3 {
		if r.Method != http.MethodGet {
			server.AzureError(w, "MethodNotAllowed", "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.listResourceGroups(w, r)
		return
	}

	// /subscriptions/{subId}/resourceGroups/{rgName}
	rgName := parts[3]

	switch r.Method {
	case http.MethodGet:
		h.getResourceGroup(w, r, rgName)
	case http.MethodPut:
		h.createOrUpdateResourceGroup(w, r, rgName)
	case http.MethodDelete:
		h.deleteResourceGroup(w, r, rgName)
	default:
		server.AzureError(w, "MethodNotAllowed", "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *handler) listResourceGroups(w http.ResponseWriter, _ *http.Request) {
	keys := h.store.List(namespaceResourceGroups, "")

	rgs := make([]resourceGroup, 0, len(keys))
	for _, key := range keys {
		data, ok := h.store.Get(namespaceResourceGroups, key)
		if !ok {
			continue
		}
		var rg resourceGroup
		if err := json.Unmarshal(data, &rg); err != nil {
			continue
		}
		rgs = append(rgs, rg)
	}

	server.WriteJSON(w, http.StatusOK, map[string]any{
		"value": rgs,
	})
}

func (h *handler) getResourceGroup(w http.ResponseWriter, _ *http.Request, rgName string) {
	key := strings.ToLower(rgName)
	data, ok := h.store.Get(namespaceResourceGroups, key)
	if !ok {
		server.AzureError(w, "ResourceGroupNotFound",
			"Resource group '"+rgName+"' could not be found.",
			http.StatusNotFound)
		return
	}

	var rg resourceGroup
	if err := json.Unmarshal(data, &rg); err != nil {
		server.AzureError(w, "InternalError", "Failed to read resource group", http.StatusInternalServerError)
		return
	}

	server.WriteJSON(w, http.StatusOK, rg)
}

func (h *handler) createOrUpdateResourceGroup(w http.ResponseWriter, r *http.Request, rgName string) {
	var req rgCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		server.AzureError(w, "InvalidRequestContent", "Failed to parse request body", http.StatusBadRequest)
		return
	}

	if req.Tags == nil {
		req.Tags = map[string]string{}
	}

	rg := resourceGroup{
		ID:       "/subscriptions/" + h.cfg.Azure.SubscriptionID + "/resourceGroups/" + rgName,
		Name:     rgName,
		Type:     "Microsoft.Resources/resourceGroups",
		Location: req.Location,
		Tags:     req.Tags,
		Properties: rgProperties{
			ProvisioningState: "Succeeded",
		},
	}

	data, err := json.Marshal(rg)
	if err != nil {
		server.AzureError(w, "InternalError", "Failed to marshal resource group", http.StatusInternalServerError)
		return
	}

	key := strings.ToLower(rgName)
	h.store.Put(namespaceResourceGroups, key, data)

	server.WriteJSON(w, http.StatusOK, rg)
}

func (h *handler) deleteResourceGroup(w http.ResponseWriter, _ *http.Request, rgName string) {
	key := strings.ToLower(rgName)
	h.store.Delete(namespaceResourceGroups, key)
	w.WriteHeader(http.StatusAccepted)
}

// splitPath splits a URL path into non-empty segments.
// e.g. "/subscriptions/abc/resourceGroups/myRg" -> ["subscriptions","abc","resourceGroups","myRg"]
func splitPath(path string) []string {
	var parts []string
	for _, p := range strings.Split(path, "/") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}
