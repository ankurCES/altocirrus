package pubsub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/altocirrus/altocirrus/internal/config"
	"github.com/altocirrus/altocirrus/internal/server"
	"github.com/altocirrus/altocirrus/internal/storage"
)

const (
	topicNamespace = "gcp:pubsub:topics"
	subNamespace   = "gcp:pubsub:subscriptions"
)

// ---------------------------------------------------------------------------
// Models
// ---------------------------------------------------------------------------

// Topic represents a GCP Pub/Sub topic resource.
type Topic struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
}

// Subscription represents a GCP Pub/Sub subscription resource.
type Subscription struct {
	Name               string      `json:"name"`
	Topic              string      `json:"topic"`
	AckDeadlineSeconds int         `json:"ackDeadlineSeconds"`
	PushConfig         *PushConfig `json:"pushConfig,omitempty"`
}

// PushConfig holds push delivery configuration.
type PushConfig struct {
	PushEndpoint string            `json:"pushEndpoint,omitempty"`
	Attributes   map[string]string `json:"attributes,omitempty"`
}

// PubsubMessage represents a message published to a topic.
type PubsubMessage struct {
	Data        string            `json:"data"`
	MessageID   string            `json:"messageId"`
	PublishTime string            `json:"publishTime"`
	Attributes  map[string]string `json:"attributes,omitempty"`
}

// ReceivedMessage wraps a PubsubMessage with an acknowledgment ID.
type ReceivedMessage struct {
	AckID   string        `json:"ackId"`
	Message PubsubMessage `json:"message"`
}

// ---------------------------------------------------------------------------
// Internal stored representation
// ---------------------------------------------------------------------------

type storedTopic struct {
	Project string            `json:"project"`
	TopicID string            `json:"topicId"`
	Labels  map[string]string `json:"labels,omitempty"`
}

type storedSubscription struct {
	Project            string      `json:"project"`
	SubID              string      `json:"subId"`
	Topic              string      `json:"topic"`
	AckDeadlineSeconds int         `json:"ackDeadlineSeconds"`
	PushConfig         *PushConfig `json:"pushConfig,omitempty"`
}

// queueEntry is a message sitting in a per-subscription queue.
type queueEntry struct {
	ackID       string
	message     PubsubMessage
	delivered   bool
	deliveredAt time.Time
	ackDeadline time.Duration
}

// ---------------------------------------------------------------------------
// Route registration
// ---------------------------------------------------------------------------

// RegisterRoutes registers all GCP Pub/Sub emulation routes on mux.
// It registers under /v1/projects/{project}/topics/ and /v1/projects/{project}/subscriptions/
// to avoid conflicting with Secret Manager's /v1/projects/ catch-all.
func RegisterRoutes(mux *http.ServeMux, store storage.Store, cfg *config.Config) {
	h := &handler{
		store:  store,
		cfg:    cfg,
		queues: make(map[string][]*queueEntry),
	}
	mux.HandleFunc("/v1/projects/{project}/topics", h.route)
	mux.HandleFunc("/v1/projects/{project}/topics/", h.route)
	mux.HandleFunc("/v1/projects/{project}/subscriptions", h.route)
	mux.HandleFunc("/v1/projects/{project}/subscriptions/", h.route)
}

type handler struct {
	store storage.Store
	cfg   *config.Config

	mu        sync.Mutex
	queues    map[string][]*queueEntry // key = "project/subID"
	messageID int64                    // incrementing counter
}

// ---------------------------------------------------------------------------
// Router
// ---------------------------------------------------------------------------

func (h *handler) route(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/projects/")

	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 2 {
		server.GCPError(w, http.StatusNotFound, "not found", "NOT_FOUND")
		return
	}
	project := parts[0]
	rest := parts[1]

	switch {
	case strings.HasPrefix(rest, "topics"):
		h.routeTopics(w, r, project, strings.TrimPrefix(rest, "topics"))
	case strings.HasPrefix(rest, "subscriptions"):
		h.routeSubscriptions(w, r, project, strings.TrimPrefix(rest, "subscriptions"))
	default:
		server.GCPError(w, http.StatusNotFound, "not found", "NOT_FOUND")
	}
}

func (h *handler) routeTopics(w http.ResponseWriter, r *http.Request, project, sub string) {
	// /v1/projects/{project}/topics  (list)
	if sub == "" || sub == "/" {
		if r.Method == http.MethodGet {
			h.listTopics(w, r, project)
			return
		}
		server.GCPError(w, http.StatusMethodNotAllowed, "method not allowed", "INVALID_ARGUMENT")
		return
	}

	sub = strings.TrimPrefix(sub, "/")

	// Check for colon actions: {topic}:publish
	if colonIdx := strings.Index(sub, ":"); colonIdx >= 0 && !strings.Contains(sub[:colonIdx], "/") {
		topicID := sub[:colonIdx]
		action := sub[colonIdx+1:]
		if action == "publish" && r.Method == http.MethodPost {
			h.publish(w, r, project, topicID)
			return
		}
		server.GCPError(w, http.StatusNotFound, "not found", "NOT_FOUND")
		return
	}

	// /{topic} — create (PUT), get (GET), delete (DELETE)
	topicID := sub
	switch r.Method {
	case http.MethodPut:
		h.createTopic(w, r, project, topicID)
	case http.MethodGet:
		h.getTopic(w, r, project, topicID)
	case http.MethodDelete:
		h.deleteTopic(w, r, project, topicID)
	default:
		server.GCPError(w, http.StatusMethodNotAllowed, "method not allowed", "INVALID_ARGUMENT")
	}
}

func (h *handler) routeSubscriptions(w http.ResponseWriter, r *http.Request, project, sub string) {
	// /v1/projects/{project}/subscriptions  (list)
	if sub == "" || sub == "/" {
		if r.Method == http.MethodGet {
			h.listSubscriptions(w, r, project)
			return
		}
		server.GCPError(w, http.StatusMethodNotAllowed, "method not allowed", "INVALID_ARGUMENT")
		return
	}

	sub = strings.TrimPrefix(sub, "/")

	// Check for colon actions: {sub}:pull, {sub}:acknowledge
	if colonIdx := strings.Index(sub, ":"); colonIdx >= 0 && !strings.Contains(sub[:colonIdx], "/") {
		subID := sub[:colonIdx]
		action := sub[colonIdx+1:]
		if r.Method == http.MethodPost {
			switch action {
			case "pull":
				h.pull(w, r, project, subID)
				return
			case "acknowledge":
				h.acknowledge(w, r, project, subID)
				return
			}
		}
		server.GCPError(w, http.StatusNotFound, "not found", "NOT_FOUND")
		return
	}

	// /{sub} — create (PUT), get (GET), delete (DELETE)
	subID := sub
	switch r.Method {
	case http.MethodPut:
		h.createSubscription(w, r, project, subID)
	case http.MethodGet:
		h.getSubscription(w, r, project, subID)
	case http.MethodDelete:
		h.deleteSubscription(w, r, project, subID)
	default:
		server.GCPError(w, http.StatusMethodNotAllowed, "method not allowed", "INVALID_ARGUMENT")
	}
}

// ---------------------------------------------------------------------------
// Topic handlers
// ---------------------------------------------------------------------------

func (h *handler) createTopic(w http.ResponseWriter, r *http.Request, project, topicID string) {
	key := project + "/" + topicID

	if _, exists := h.store.Get(topicNamespace, key); exists {
		server.GCPError(w, http.StatusConflict,
			fmt.Sprintf("Topic already exists: projects/%s/topics/%s", project, topicID),
			"ALREADY_EXISTS")
		return
	}

	var body struct {
		Labels map[string]string `json:"labels"`
	}
	// Body may be empty, that is fine.
	_ = json.NewDecoder(r.Body).Decode(&body)

	stored := storedTopic{
		Project: project,
		TopicID: topicID,
		Labels:  body.Labels,
	}
	data, _ := json.Marshal(stored)
	h.store.Put(topicNamespace, key, data)

	resp := Topic{
		Name:   fmt.Sprintf("projects/%s/topics/%s", project, topicID),
		Labels: stored.Labels,
	}
	server.WriteJSON(w, http.StatusOK, resp)
}

func (h *handler) getTopic(w http.ResponseWriter, _ *http.Request, project, topicID string) {
	key := project + "/" + topicID

	raw, exists := h.store.Get(topicNamespace, key)
	if !exists {
		server.GCPError(w, http.StatusNotFound,
			fmt.Sprintf("Topic not found: projects/%s/topics/%s", project, topicID),
			"NOT_FOUND")
		return
	}

	var stored storedTopic
	if err := json.Unmarshal(raw, &stored); err != nil {
		server.GCPError(w, http.StatusInternalServerError, "internal error", "INTERNAL")
		return
	}

	resp := Topic{
		Name:   fmt.Sprintf("projects/%s/topics/%s", project, topicID),
		Labels: stored.Labels,
	}
	server.WriteJSON(w, http.StatusOK, resp)
}

func (h *handler) listTopics(w http.ResponseWriter, _ *http.Request, project string) {
	prefix := project + "/"
	keys := h.store.List(topicNamespace, prefix)
	sort.Strings(keys)

	topics := make([]Topic, 0, len(keys))
	for _, k := range keys {
		raw, ok := h.store.Get(topicNamespace, k)
		if !ok {
			continue
		}
		var stored storedTopic
		if err := json.Unmarshal(raw, &stored); err != nil {
			continue
		}
		topics = append(topics, Topic{
			Name:   fmt.Sprintf("projects/%s/topics/%s", stored.Project, stored.TopicID),
			Labels: stored.Labels,
		})
	}

	type listResponse struct {
		Topics        []Topic `json:"topics"`
		NextPageToken string  `json:"nextPageToken"`
	}
	server.WriteJSON(w, http.StatusOK, listResponse{Topics: topics})
}

func (h *handler) deleteTopic(w http.ResponseWriter, _ *http.Request, project, topicID string) {
	key := project + "/" + topicID

	if !h.store.Delete(topicNamespace, key) {
		server.GCPError(w, http.StatusNotFound,
			fmt.Sprintf("Topic not found: projects/%s/topics/%s", project, topicID),
			"NOT_FOUND")
		return
	}

	server.WriteJSON(w, http.StatusOK, map[string]any{})
}

// ---------------------------------------------------------------------------
// Subscription handlers
// ---------------------------------------------------------------------------

func (h *handler) createSubscription(w http.ResponseWriter, r *http.Request, project, subID string) {
	key := project + "/" + subID

	if _, exists := h.store.Get(subNamespace, key); exists {
		server.GCPError(w, http.StatusConflict,
			fmt.Sprintf("Subscription already exists: projects/%s/subscriptions/%s", project, subID),
			"ALREADY_EXISTS")
		return
	}

	var body struct {
		Topic              string      `json:"topic"`
		AckDeadlineSeconds int         `json:"ackDeadlineSeconds"`
		PushConfig         *PushConfig `json:"pushConfig"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.GCPError(w, http.StatusBadRequest, "invalid JSON body", "INVALID_ARGUMENT")
		return
	}

	// Validate topic exists. Topic name format: projects/{p}/topics/{t}
	topicParts := strings.Split(body.Topic, "/")
	if len(topicParts) != 4 || topicParts[0] != "projects" || topicParts[2] != "topics" {
		server.GCPError(w, http.StatusBadRequest, "invalid topic name", "INVALID_ARGUMENT")
		return
	}
	topicProject := topicParts[1]
	topicName := topicParts[3]
	topicKey := topicProject + "/" + topicName
	if _, exists := h.store.Get(topicNamespace, topicKey); !exists {
		server.GCPError(w, http.StatusNotFound,
			fmt.Sprintf("Topic not found: %s", body.Topic),
			"NOT_FOUND")
		return
	}

	if body.AckDeadlineSeconds <= 0 {
		body.AckDeadlineSeconds = 10
	}

	stored := storedSubscription{
		Project:            project,
		SubID:              subID,
		Topic:              body.Topic,
		AckDeadlineSeconds: body.AckDeadlineSeconds,
		PushConfig:         body.PushConfig,
	}
	data, _ := json.Marshal(stored)
	h.store.Put(subNamespace, key, data)

	resp := Subscription{
		Name:               fmt.Sprintf("projects/%s/subscriptions/%s", project, subID),
		Topic:              stored.Topic,
		AckDeadlineSeconds: stored.AckDeadlineSeconds,
		PushConfig:         stored.PushConfig,
	}
	server.WriteJSON(w, http.StatusOK, resp)
}

func (h *handler) getSubscription(w http.ResponseWriter, _ *http.Request, project, subID string) {
	key := project + "/" + subID

	raw, exists := h.store.Get(subNamespace, key)
	if !exists {
		server.GCPError(w, http.StatusNotFound,
			fmt.Sprintf("Subscription not found: projects/%s/subscriptions/%s", project, subID),
			"NOT_FOUND")
		return
	}

	var stored storedSubscription
	if err := json.Unmarshal(raw, &stored); err != nil {
		server.GCPError(w, http.StatusInternalServerError, "internal error", "INTERNAL")
		return
	}

	resp := Subscription{
		Name:               fmt.Sprintf("projects/%s/subscriptions/%s", project, subID),
		Topic:              stored.Topic,
		AckDeadlineSeconds: stored.AckDeadlineSeconds,
		PushConfig:         stored.PushConfig,
	}
	server.WriteJSON(w, http.StatusOK, resp)
}

func (h *handler) listSubscriptions(w http.ResponseWriter, _ *http.Request, project string) {
	prefix := project + "/"
	keys := h.store.List(subNamespace, prefix)
	sort.Strings(keys)

	subs := make([]Subscription, 0, len(keys))
	for _, k := range keys {
		raw, ok := h.store.Get(subNamespace, k)
		if !ok {
			continue
		}
		var stored storedSubscription
		if err := json.Unmarshal(raw, &stored); err != nil {
			continue
		}
		subs = append(subs, Subscription{
			Name:               fmt.Sprintf("projects/%s/subscriptions/%s", stored.Project, stored.SubID),
			Topic:              stored.Topic,
			AckDeadlineSeconds: stored.AckDeadlineSeconds,
			PushConfig:         stored.PushConfig,
		})
	}

	type listResponse struct {
		Subscriptions []Subscription `json:"subscriptions"`
		NextPageToken string         `json:"nextPageToken"`
	}
	server.WriteJSON(w, http.StatusOK, listResponse{Subscriptions: subs})
}

func (h *handler) deleteSubscription(w http.ResponseWriter, _ *http.Request, project, subID string) {
	key := project + "/" + subID

	if !h.store.Delete(subNamespace, key) {
		server.GCPError(w, http.StatusNotFound,
			fmt.Sprintf("Subscription not found: projects/%s/subscriptions/%s", project, subID),
			"NOT_FOUND")
		return
	}

	// Clean up the message queue.
	h.mu.Lock()
	delete(h.queues, key)
	h.mu.Unlock()

	server.WriteJSON(w, http.StatusOK, map[string]any{})
}

// ---------------------------------------------------------------------------
// Message handlers
// ---------------------------------------------------------------------------

func (h *handler) publish(w http.ResponseWriter, r *http.Request, project, topicID string) {
	topicKey := project + "/" + topicID

	if _, exists := h.store.Get(topicNamespace, topicKey); !exists {
		server.GCPError(w, http.StatusNotFound,
			fmt.Sprintf("Topic not found: projects/%s/topics/%s", project, topicID),
			"NOT_FOUND")
		return
	}

	var body struct {
		Messages []struct {
			Data       string            `json:"data"`
			Attributes map[string]string `json:"attributes"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.GCPError(w, http.StatusBadRequest, "invalid JSON body", "INVALID_ARGUMENT")
		return
	}

	if len(body.Messages) == 0 {
		server.GCPError(w, http.StatusBadRequest, "messages list is empty", "INVALID_ARGUMENT")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	messageIDs := make([]string, 0, len(body.Messages))
	messages := make([]PubsubMessage, 0, len(body.Messages))

	h.mu.Lock()
	for _, m := range body.Messages {
		h.messageID++
		id := strconv.FormatInt(h.messageID, 10)
		messageIDs = append(messageIDs, id)
		messages = append(messages, PubsubMessage{
			Data:        m.Data,
			MessageID:   id,
			PublishTime: now,
			Attributes:  m.Attributes,
		})
	}

	// Fan out to all subscriptions for this topic.
	topicName := fmt.Sprintf("projects/%s/topics/%s", project, topicID)
	subKeys := h.store.List(subNamespace, "")
	for _, sk := range subKeys {
		raw, ok := h.store.Get(subNamespace, sk)
		if !ok {
			continue
		}
		var stored storedSubscription
		if err := json.Unmarshal(raw, &stored); err != nil {
			continue
		}
		if stored.Topic != topicName {
			continue
		}

		deadline := time.Duration(stored.AckDeadlineSeconds) * time.Second
		for _, msg := range messages {
			entry := &queueEntry{
				ackID:       server.RequestID(),
				message:     msg,
				delivered:   false,
				ackDeadline: deadline,
			}
			h.queues[sk] = append(h.queues[sk], entry)
		}
	}
	h.mu.Unlock()

	type publishResponse struct {
		MessageIDs []string `json:"messageIds"`
	}
	server.WriteJSON(w, http.StatusOK, publishResponse{MessageIDs: messageIDs})
}

func (h *handler) pull(w http.ResponseWriter, r *http.Request, project, subID string) {
	subKey := project + "/" + subID

	if _, exists := h.store.Get(subNamespace, subKey); !exists {
		server.GCPError(w, http.StatusNotFound,
			fmt.Sprintf("Subscription not found: projects/%s/subscriptions/%s", project, subID),
			"NOT_FOUND")
		return
	}

	var body struct {
		MaxMessages int `json:"maxMessages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.GCPError(w, http.StatusBadRequest, "invalid JSON body", "INVALID_ARGUMENT")
		return
	}
	if body.MaxMessages <= 0 {
		body.MaxMessages = 1
	}

	now := time.Now()
	received := make([]ReceivedMessage, 0)

	h.mu.Lock()
	queue := h.queues[subKey]
	count := 0
	for _, entry := range queue {
		if count >= body.MaxMessages {
			break
		}
		// Skip messages that have been delivered and are still within the ack deadline.
		if entry.delivered && now.Before(entry.deliveredAt.Add(entry.ackDeadline)) {
			continue
		}
		// Mark as delivered (or re-delivered after deadline).
		entry.delivered = true
		entry.deliveredAt = now
		received = append(received, ReceivedMessage{
			AckID:   entry.ackID,
			Message: entry.message,
		})
		count++
	}
	h.mu.Unlock()

	type pullResponse struct {
		ReceivedMessages []ReceivedMessage `json:"receivedMessages"`
	}
	server.WriteJSON(w, http.StatusOK, pullResponse{ReceivedMessages: received})
}

func (h *handler) acknowledge(w http.ResponseWriter, r *http.Request, project, subID string) {
	subKey := project + "/" + subID

	if _, exists := h.store.Get(subNamespace, subKey); !exists {
		server.GCPError(w, http.StatusNotFound,
			fmt.Sprintf("Subscription not found: projects/%s/subscriptions/%s", project, subID),
			"NOT_FOUND")
		return
	}

	var body struct {
		AckIDs []string `json:"ackIds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		server.GCPError(w, http.StatusBadRequest, "invalid JSON body", "INVALID_ARGUMENT")
		return
	}

	ackSet := make(map[string]bool, len(body.AckIDs))
	for _, id := range body.AckIDs {
		ackSet[id] = true
	}

	h.mu.Lock()
	queue := h.queues[subKey]
	filtered := make([]*queueEntry, 0, len(queue))
	for _, entry := range queue {
		if !ackSet[entry.ackID] {
			filtered = append(filtered, entry)
		}
	}
	h.queues[subKey] = filtered
	h.mu.Unlock()

	server.WriteJSON(w, http.StatusOK, map[string]any{})
}
