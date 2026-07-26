package pubsub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/altocirrus/altocirrus/internal/config"
	"github.com/altocirrus/altocirrus/internal/storage"
)

func setup(t *testing.T) *httptest.Server {
	t.Helper()
	store := storage.NewMemoryStore()
	cfg := config.Load()
	mux := http.NewServeMux()
	RegisterRoutes(mux, store, cfg)
	return httptest.NewServer(mux)
}

func jsonBody(t *testing.T, v any) *bytes.Buffer {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return bytes.NewBuffer(b)
}

func doReq(t *testing.T, method, url string, body any) *http.Response {
	t.Helper()
	var buf *bytes.Buffer
	if body != nil {
		buf = jsonBody(t, body)
	} else {
		buf = bytes.NewBuffer(nil)
	}
	req, err := http.NewRequest(method, url, buf)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

func decode(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Topic CRUD
// ---------------------------------------------------------------------------

func TestCreateTopic(t *testing.T) {
	srv := setup(t)
	defer srv.Close()

	resp := doReq(t, http.MethodPut, srv.URL+"/v1/projects/my-proj/topics/my-topic", map[string]any{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var topic Topic
	decode(t, resp, &topic)
	if topic.Name != "projects/my-proj/topics/my-topic" {
		t.Fatalf("unexpected name: %s", topic.Name)
	}
}

func TestCreateTopicConflict(t *testing.T) {
	srv := setup(t)
	defer srv.Close()

	doReq(t, http.MethodPut, srv.URL+"/v1/projects/my-proj/topics/my-topic", map[string]any{}).Body.Close()

	resp := doReq(t, http.MethodPut, srv.URL+"/v1/projects/my-proj/topics/my-topic", map[string]any{})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestGetTopic(t *testing.T) {
	srv := setup(t)
	defer srv.Close()

	doReq(t, http.MethodPut, srv.URL+"/v1/projects/my-proj/topics/my-topic", map[string]any{}).Body.Close()

	resp := doReq(t, http.MethodGet, srv.URL+"/v1/projects/my-proj/topics/my-topic", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var topic Topic
	decode(t, resp, &topic)
	if topic.Name != "projects/my-proj/topics/my-topic" {
		t.Fatalf("unexpected name: %s", topic.Name)
	}
}

func TestGetTopicNotFound(t *testing.T) {
	srv := setup(t)
	defer srv.Close()

	resp := doReq(t, http.MethodGet, srv.URL+"/v1/projects/my-proj/topics/nope", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestListTopics(t *testing.T) {
	srv := setup(t)
	defer srv.Close()

	doReq(t, http.MethodPut, srv.URL+"/v1/projects/my-proj/topics/t1", map[string]any{}).Body.Close()
	doReq(t, http.MethodPut, srv.URL+"/v1/projects/my-proj/topics/t2", map[string]any{}).Body.Close()

	resp := doReq(t, http.MethodGet, srv.URL+"/v1/projects/my-proj/topics", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result struct {
		Topics []Topic `json:"topics"`
	}
	decode(t, resp, &result)
	if len(result.Topics) != 2 {
		t.Fatalf("expected 2 topics, got %d", len(result.Topics))
	}
}

func TestDeleteTopic(t *testing.T) {
	srv := setup(t)
	defer srv.Close()

	doReq(t, http.MethodPut, srv.URL+"/v1/projects/my-proj/topics/my-topic", map[string]any{}).Body.Close()

	resp := doReq(t, http.MethodDelete, srv.URL+"/v1/projects/my-proj/topics/my-topic", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Verify gone.
	resp = doReq(t, http.MethodGet, srv.URL+"/v1/projects/my-proj/topics/my-topic", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// ---------------------------------------------------------------------------
// Subscription CRUD
// ---------------------------------------------------------------------------

func TestCreateSubscription(t *testing.T) {
	srv := setup(t)
	defer srv.Close()

	doReq(t, http.MethodPut, srv.URL+"/v1/projects/my-proj/topics/my-topic", map[string]any{}).Body.Close()

	body := map[string]any{
		"topic":              "projects/my-proj/topics/my-topic",
		"ackDeadlineSeconds": 10,
	}
	resp := doReq(t, http.MethodPut, srv.URL+"/v1/projects/my-proj/subscriptions/my-sub", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var sub Subscription
	decode(t, resp, &sub)
	if sub.Name != "projects/my-proj/subscriptions/my-sub" {
		t.Fatalf("unexpected name: %s", sub.Name)
	}
	if sub.Topic != "projects/my-proj/topics/my-topic" {
		t.Fatalf("unexpected topic: %s", sub.Topic)
	}
}

func TestCreateSubscriptionConflict(t *testing.T) {
	srv := setup(t)
	defer srv.Close()

	doReq(t, http.MethodPut, srv.URL+"/v1/projects/my-proj/topics/my-topic", map[string]any{}).Body.Close()

	body := map[string]any{
		"topic":              "projects/my-proj/topics/my-topic",
		"ackDeadlineSeconds": 10,
	}
	doReq(t, http.MethodPut, srv.URL+"/v1/projects/my-proj/subscriptions/my-sub", body).Body.Close()

	resp := doReq(t, http.MethodPut, srv.URL+"/v1/projects/my-proj/subscriptions/my-sub", body)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestCreateSubscriptionTopicNotFound(t *testing.T) {
	srv := setup(t)
	defer srv.Close()

	body := map[string]any{
		"topic":              "projects/my-proj/topics/nonexistent",
		"ackDeadlineSeconds": 10,
	}
	resp := doReq(t, http.MethodPut, srv.URL+"/v1/projects/my-proj/subscriptions/my-sub", body)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestGetSubscription(t *testing.T) {
	srv := setup(t)
	defer srv.Close()

	doReq(t, http.MethodPut, srv.URL+"/v1/projects/my-proj/topics/my-topic", map[string]any{}).Body.Close()
	body := map[string]any{
		"topic":              "projects/my-proj/topics/my-topic",
		"ackDeadlineSeconds": 10,
	}
	doReq(t, http.MethodPut, srv.URL+"/v1/projects/my-proj/subscriptions/my-sub", body).Body.Close()

	resp := doReq(t, http.MethodGet, srv.URL+"/v1/projects/my-proj/subscriptions/my-sub", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var sub Subscription
	decode(t, resp, &sub)
	if sub.Name != "projects/my-proj/subscriptions/my-sub" {
		t.Fatalf("unexpected name: %s", sub.Name)
	}
}

func TestListSubscriptions(t *testing.T) {
	srv := setup(t)
	defer srv.Close()

	doReq(t, http.MethodPut, srv.URL+"/v1/projects/my-proj/topics/my-topic", map[string]any{}).Body.Close()

	for _, name := range []string{"sub1", "sub2"} {
		body := map[string]any{
			"topic":              "projects/my-proj/topics/my-topic",
			"ackDeadlineSeconds": 10,
		}
		doReq(t, http.MethodPut, srv.URL+"/v1/projects/my-proj/subscriptions/"+name, body).Body.Close()
	}

	resp := doReq(t, http.MethodGet, srv.URL+"/v1/projects/my-proj/subscriptions", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result struct {
		Subscriptions []Subscription `json:"subscriptions"`
	}
	decode(t, resp, &result)
	if len(result.Subscriptions) != 2 {
		t.Fatalf("expected 2 subscriptions, got %d", len(result.Subscriptions))
	}
}

func TestDeleteSubscription(t *testing.T) {
	srv := setup(t)
	defer srv.Close()

	doReq(t, http.MethodPut, srv.URL+"/v1/projects/my-proj/topics/my-topic", map[string]any{}).Body.Close()
	body := map[string]any{
		"topic":              "projects/my-proj/topics/my-topic",
		"ackDeadlineSeconds": 10,
	}
	doReq(t, http.MethodPut, srv.URL+"/v1/projects/my-proj/subscriptions/my-sub", body).Body.Close()

	resp := doReq(t, http.MethodDelete, srv.URL+"/v1/projects/my-proj/subscriptions/my-sub", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = doReq(t, http.MethodGet, srv.URL+"/v1/projects/my-proj/subscriptions/my-sub", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// ---------------------------------------------------------------------------
// Publish / Pull / Ack
// ---------------------------------------------------------------------------

func TestPublishAndPull(t *testing.T) {
	srv := setup(t)
	defer srv.Close()

	// Create topic + subscription.
	doReq(t, http.MethodPut, srv.URL+"/v1/projects/my-proj/topics/my-topic", map[string]any{}).Body.Close()
	subBody := map[string]any{
		"topic":              "projects/my-proj/topics/my-topic",
		"ackDeadlineSeconds": 10,
	}
	doReq(t, http.MethodPut, srv.URL+"/v1/projects/my-proj/subscriptions/my-sub", subBody).Body.Close()

	// Publish.
	pubBody := map[string]any{
		"messages": []map[string]any{
			{"data": "aGVsbG8=", "attributes": map[string]string{"key": "val"}},
			{"data": "d29ybGQ="},
		},
	}
	resp := doReq(t, http.MethodPost, srv.URL+"/v1/projects/my-proj/topics/my-topic:publish", pubBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("publish: expected 200, got %d", resp.StatusCode)
	}

	var pubResult struct {
		MessageIDs []string `json:"messageIds"`
	}
	decode(t, resp, &pubResult)
	if len(pubResult.MessageIDs) != 2 {
		t.Fatalf("expected 2 messageIds, got %d", len(pubResult.MessageIDs))
	}

	// Pull.
	pullBody := map[string]any{"maxMessages": 10}
	resp = doReq(t, http.MethodPost, srv.URL+"/v1/projects/my-proj/subscriptions/my-sub:pull", pullBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pull: expected 200, got %d", resp.StatusCode)
	}

	var pullResult struct {
		ReceivedMessages []ReceivedMessage `json:"receivedMessages"`
	}
	decode(t, resp, &pullResult)
	if len(pullResult.ReceivedMessages) != 2 {
		t.Fatalf("expected 2 received messages, got %d", len(pullResult.ReceivedMessages))
	}

	// Check first message.
	msg := pullResult.ReceivedMessages[0]
	if msg.Message.Data != "aGVsbG8=" {
		t.Fatalf("unexpected data: %s", msg.Message.Data)
	}
	if msg.Message.Attributes["key"] != "val" {
		t.Fatalf("unexpected attributes: %v", msg.Message.Attributes)
	}
	if msg.AckID == "" {
		t.Fatal("ackId should not be empty")
	}
}

func TestAckRemovesMessages(t *testing.T) {
	srv := setup(t)
	defer srv.Close()

	doReq(t, http.MethodPut, srv.URL+"/v1/projects/my-proj/topics/my-topic", map[string]any{}).Body.Close()
	subBody := map[string]any{
		"topic":              "projects/my-proj/topics/my-topic",
		"ackDeadlineSeconds": 600, // long deadline so messages stay "in flight"
	}
	doReq(t, http.MethodPut, srv.URL+"/v1/projects/my-proj/subscriptions/my-sub", subBody).Body.Close()

	// Publish one message.
	pubBody := map[string]any{
		"messages": []map[string]any{
			{"data": "dGVzdA=="},
		},
	}
	doReq(t, http.MethodPost, srv.URL+"/v1/projects/my-proj/topics/my-topic:publish", pubBody).Body.Close()

	// Pull to get the ackId.
	pullBody := map[string]any{"maxMessages": 10}
	resp := doReq(t, http.MethodPost, srv.URL+"/v1/projects/my-proj/subscriptions/my-sub:pull", pullBody)
	var pullResult struct {
		ReceivedMessages []ReceivedMessage `json:"receivedMessages"`
	}
	decode(t, resp, &pullResult)
	if len(pullResult.ReceivedMessages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(pullResult.ReceivedMessages))
	}

	ackID := pullResult.ReceivedMessages[0].AckID

	// Ack the message.
	ackBody := map[string]any{"ackIds": []string{ackID}}
	resp = doReq(t, http.MethodPost, srv.URL+"/v1/projects/my-proj/subscriptions/my-sub:acknowledge", ackBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ack: expected 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Pull again — should be empty.
	resp = doReq(t, http.MethodPost, srv.URL+"/v1/projects/my-proj/subscriptions/my-sub:pull", pullBody)
	var pullResult2 struct {
		ReceivedMessages []ReceivedMessage `json:"receivedMessages"`
	}
	decode(t, resp, &pullResult2)
	if len(pullResult2.ReceivedMessages) != 0 {
		t.Fatalf("expected 0 messages after ack, got %d", len(pullResult2.ReceivedMessages))
	}
}

func TestPullEmptyReturnsEmpty(t *testing.T) {
	srv := setup(t)
	defer srv.Close()

	doReq(t, http.MethodPut, srv.URL+"/v1/projects/my-proj/topics/my-topic", map[string]any{}).Body.Close()
	subBody := map[string]any{
		"topic":              "projects/my-proj/topics/my-topic",
		"ackDeadlineSeconds": 10,
	}
	doReq(t, http.MethodPut, srv.URL+"/v1/projects/my-proj/subscriptions/my-sub", subBody).Body.Close()

	pullBody := map[string]any{"maxMessages": 10}
	resp := doReq(t, http.MethodPost, srv.URL+"/v1/projects/my-proj/subscriptions/my-sub:pull", pullBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result struct {
		ReceivedMessages []ReceivedMessage `json:"receivedMessages"`
	}
	decode(t, resp, &result)
	if len(result.ReceivedMessages) != 0 {
		t.Fatalf("expected 0 messages, got %d", len(result.ReceivedMessages))
	}
}

func TestPublishToNonExistentTopic(t *testing.T) {
	srv := setup(t)
	defer srv.Close()

	pubBody := map[string]any{
		"messages": []map[string]any{
			{"data": "dGVzdA=="},
		},
	}
	resp := doReq(t, http.MethodPost, srv.URL+"/v1/projects/my-proj/topics/nonexistent:publish", pubBody)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}
