package queuestorage

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/altocirrus/altocirrus/internal/config"
	"github.com/altocirrus/altocirrus/internal/storage"
)

func newTestServer(t *testing.T) (*httptest.Server, storage.Store) {
	t.Helper()
	store := storage.NewMemoryStore()
	mux := http.NewServeMux()
	RegisterRoutes(mux, store, config.Load())
	return httptest.NewServer(mux), store
}

func do(t *testing.T, srv *httptest.Server, method, path string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, nil)
	req.Header.Set("x-ms-version", "2020-10-02")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	return resp
}

func doBody(t *testing.T, srv *httptest.Server, method, path, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	req.Header.Set("x-ms-version", "2020-10-02")
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	return resp
}

func createQueue(t *testing.T, srv *httptest.Server, name string) {
	t.Helper()
	resp := do(t, srv, http.MethodPut, "/devstoreaccount1queue/"+name)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		t.Fatalf("createQueue: want 201/204, got %d", resp.StatusCode)
	}
}

func sendMsg(t *testing.T, srv *httptest.Server, queueName, text string) queueMessagesList {
	t.Helper()
	body := fmt.Sprintf(`<QueueMessage><MessageText>%s</MessageText></QueueMessage>`, text)
	resp := doBody(t, srv, http.MethodPost, "/devstoreaccount1queue/"+queueName+"/messages", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("sendMsg: want 201, got %d", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	var result queueMessagesList
	if err := xml.Unmarshal(raw, &result); err != nil {
		t.Fatalf("sendMsg: invalid XML: %v\nbody: %s", err, raw)
	}
	return result
}

func receiveMsg(t *testing.T, srv *httptest.Server, queueName, query string) queueMessagesList {
	t.Helper()
	path := "/devstoreaccount1queue/" + queueName + "/messages"
	if query != "" {
		path += "?" + query
	}
	resp := do(t, srv, http.MethodGet, path)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("receiveMsg: want 200, got %d", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	var result queueMessagesList
	if err := xml.Unmarshal(raw, &result); err != nil {
		t.Fatalf("receiveMsg: invalid XML: %v\nbody: %s", err, raw)
	}
	return result
}

func TestCreateQueue(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	resp := do(t, srv, http.MethodPut, "/devstoreaccount1queue/myqueue")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("want 201, got %d", resp.StatusCode)
	}
}

func TestCreateQueueIdempotent(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	do(t, srv, http.MethodPut, "/devstoreaccount1queue/myqueue")
	resp := do(t, srv, http.MethodPut, "/devstoreaccount1queue/myqueue")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("want 204 on re-create, got %d", resp.StatusCode)
	}
}

func TestGetQueueProps(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	do(t, srv, http.MethodPut, "/devstoreaccount1queue/myqueue")
	resp := do(t, srv, http.MethodGet, "/devstoreaccount1queue/myqueue?comp=metadata")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
}

func TestGetQueuePropsNotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	resp := do(t, srv, http.MethodGet, "/devstoreaccount1queue/noqueue?comp=metadata")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var e xmlError
	if err := xml.Unmarshal(body, &e); err != nil {
		t.Fatalf("invalid XML error body: %v", err)
	}
	if e.Code != "QueueNotFound" {
		t.Fatalf("want QueueNotFound, got %q", e.Code)
	}
}

func TestGetQueuePropsMissingComp(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	do(t, srv, http.MethodPut, "/devstoreaccount1queue/myqueue")
	resp := do(t, srv, http.MethodGet, "/devstoreaccount1queue/myqueue")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 without comp=metadata, got %d", resp.StatusCode)
	}
}

func TestDeleteQueue(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	do(t, srv, http.MethodPut, "/devstoreaccount1queue/myqueue")
	resp := do(t, srv, http.MethodDelete, "/devstoreaccount1queue/myqueue")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("want 204, got %d", resp.StatusCode)
	}
}

func TestDeleteQueueNotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	resp := do(t, srv, http.MethodDelete, "/devstoreaccount1queue/noqueue")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

func TestDeleteQueueClearsMessages(t *testing.T) {
	_, store := newTestServer(t) // not used for HTTP; verify namespace side effect
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux := http.NewServeMux()
		RegisterRoutes(mux, store, config.Load())
		mux.ServeHTTP(w, r)
	}))
	defer srv.Close()

	// Pre-seed a message so we can confirm the namespace is cleared on delete.
	store.Put(messagesNamespace("myqueue"), "msg1", []byte(`{}`))

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/devstoreaccount1queue/myqueue", nil)
	req.Header.Set("x-ms-version", "2020-10-02")
	http.DefaultClient.Do(req)

	req, _ = http.NewRequest(http.MethodDelete, srv.URL+"/devstoreaccount1queue/myqueue", nil)
	req.Header.Set("x-ms-version", "2020-10-02")
	http.DefaultClient.Do(req)

	if keys := store.List(messagesNamespace("myqueue"), ""); len(keys) != 0 {
		t.Fatalf("expected messages namespace cleared after delete, got %d keys", len(keys))
	}
}

func TestListQueues(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	for _, name := range []string{"alpha", "beta", "gamma"} {
		do(t, srv, http.MethodPut, "/devstoreaccount1queue/"+name)
	}

	resp := do(t, srv, http.MethodGet, "/devstoreaccount1queue?comp=list")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var result enumerationResults
	if err := xml.Unmarshal(body, &result); err != nil {
		t.Fatalf("invalid XML: %v", err)
	}
	if result.Queues == nil || len(result.Queues.Queue) != 3 {
		t.Fatalf("want 3 queues, got %v", result.Queues)
	}
	if result.Queues.Queue[0].Name != "alpha" {
		t.Fatalf("want sorted, first = alpha, got %q", result.Queues.Queue[0].Name)
	}
}

func TestListQueuesEmpty(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	resp := do(t, srv, http.MethodGet, "/devstoreaccount1queue?comp=list")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "EnumerationResults") {
		t.Fatalf("expected EnumerationResults in body, got: %s", body)
	}
}

func TestListQueuesMissingComp(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	resp := do(t, srv, http.MethodGet, "/devstoreaccount1queue")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 without comp=list, got %d", resp.StatusCode)
	}
}

func TestXMLErrorFormat(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	resp := do(t, srv, http.MethodGet, "/devstoreaccount1queue/missing?comp=metadata")
	body, _ := io.ReadAll(resp.Body)

	if ct := resp.Header.Get("Content-Type"); ct != "application/xml" {
		t.Fatalf("want application/xml, got %q", ct)
	}
	var e xmlError
	if err := xml.Unmarshal(body, &e); err != nil {
		t.Fatalf("body is not valid XML: %v\nbody: %s", err, body)
	}
	if e.Code == "" || e.Message == "" {
		t.Fatalf("missing Code or Message in error: %+v", e)
	}
}

// ---------------------------------------------------------------------------
// Message operation tests
// ---------------------------------------------------------------------------

func TestSendReceive(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()
	createQueue(t, srv, "q")

	sent := sendMsg(t, srv, "q", "aGVsbG8=") // base64 "hello"
	if len(sent.Messages) != 1 {
		t.Fatalf("send: want 1 message in response, got %d", len(sent.Messages))
	}
	if sent.Messages[0].MessageId == "" {
		t.Fatal("send: empty MessageId")
	}
	if sent.Messages[0].PopReceipt == "" {
		t.Fatal("send: empty PopReceipt in send response")
	}

	got := receiveMsg(t, srv, "q", "")
	if len(got.Messages) != 1 {
		t.Fatalf("receive: want 1 message, got %d", len(got.Messages))
	}
	m := got.Messages[0]
	if m.MessageText != "aGVsbG8=" {
		t.Fatalf("receive: want text aGVsbG8=, got %q", m.MessageText)
	}
	if m.DequeueCount != 1 {
		t.Fatalf("receive: want DequeueCount=1, got %d", m.DequeueCount)
	}
	if m.PopReceipt == "" {
		t.Fatal("receive: empty PopReceipt")
	}
}

func TestPeekDoesNotHide(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()
	createQueue(t, srv, "q")
	sendMsg(t, srv, "q", "dGVzdA==")

	peek1 := receiveMsg(t, srv, "q", "peekonly=true")
	if len(peek1.Messages) != 1 {
		t.Fatalf("peek1: want 1 message, got %d", len(peek1.Messages))
	}
	if peek1.Messages[0].PopReceipt != "" {
		t.Fatal("peek: PopReceipt must be empty for peek")
	}

	// Second peek should still see the message.
	peek2 := receiveMsg(t, srv, "q", "peekonly=true")
	if len(peek2.Messages) != 1 {
		t.Fatalf("peek2: want 1 message still visible, got %d", len(peek2.Messages))
	}

	// Receive (non-peek) should also see it and increment DequeueCount.
	got := receiveMsg(t, srv, "q", "")
	if len(got.Messages) != 1 || got.Messages[0].DequeueCount != 1 {
		t.Fatalf("receive after peek: want DequeueCount=1, got %+v", got.Messages)
	}
}

func TestDeleteWithValidPopReceipt(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()
	createQueue(t, srv, "q")
	sendMsg(t, srv, "q", "dA==")

	got := receiveMsg(t, srv, "q", "visibilitytimeout=1")
	m := got.Messages[0]

	resp := do(t, srv, http.MethodDelete,
		"/devstoreaccount1queue/q/messages/"+m.MessageId+"?popreceipt="+m.PopReceipt)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: want 204, got %d", resp.StatusCode)
	}

	// Queue should now be empty — even after visibility expires.
	empty := receiveMsg(t, srv, "q", "visibilitytimeout=0")
	if len(empty.Messages) != 0 {
		t.Fatalf("after delete: want 0 messages, got %d", len(empty.Messages))
	}
}

func TestDeleteWithInvalidPopReceipt(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()
	createQueue(t, srv, "q")
	sendMsg(t, srv, "q", "dA==")

	got := receiveMsg(t, srv, "q", "")
	m := got.Messages[0]

	resp := do(t, srv, http.MethodDelete,
		"/devstoreaccount1queue/q/messages/"+m.MessageId+"?popreceipt=wrong-receipt")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("delete with bad receipt: want 400, got %d", resp.StatusCode)
	}
}

func TestUpdateChangesContent(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()
	createQueue(t, srv, "q")
	sendMsg(t, srv, "q", "b2xk") // "old"

	got := receiveMsg(t, srv, "q", "visibilitytimeout=30")
	m := got.Messages[0]

	// Update: new text, vt=0 makes it immediately visible.
	updateBody := `<QueueMessage><MessageText>bmV3</MessageText></QueueMessage>` // "new"
	resp := doBody(t, srv, http.MethodPut,
		"/devstoreaccount1queue/q/messages/"+m.MessageId+"?popreceipt="+m.PopReceipt+"&visibilitytimeout=0",
		updateBody)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("update: want 204, got %d", resp.StatusCode)
	}
	newReceipt := resp.Header.Get("x-ms-popreceipt")
	if newReceipt == "" {
		t.Fatal("update: missing x-ms-popreceipt header")
	}

	// Receive again to confirm new text.
	after := receiveMsg(t, srv, "q", "visibilitytimeout=30")
	if len(after.Messages) == 0 {
		t.Fatal("after update: expected message to be visible")
	}
	if after.Messages[0].MessageText != "bmV3" {
		t.Fatalf("after update: want text bmV3, got %q", after.Messages[0].MessageText)
	}
}

func TestClearEmpiesQueue(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()
	createQueue(t, srv, "q")

	for i := range 3 {
		sendMsg(t, srv, "q", fmt.Sprintf("bXNn%d", i))
	}

	resp := do(t, srv, http.MethodDelete, "/devstoreaccount1queue/q/messages")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("clear: want 204, got %d", resp.StatusCode)
	}

	empty := receiveMsg(t, srv, "q", "numofmessages=10")
	if len(empty.Messages) != 0 {
		t.Fatalf("after clear: want 0 messages, got %d", len(empty.Messages))
	}
}

func TestVisibilityTimeout(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()
	createQueue(t, srv, "q")
	sendMsg(t, srv, "q", "dA==")

	// Receive hides the message for 30s.
	got := receiveMsg(t, srv, "q", "visibilitytimeout=30")
	if len(got.Messages) != 1 {
		t.Fatalf("first receive: want 1 message, got %d", len(got.Messages))
	}

	// Second receive should get nothing — message is invisible.
	hidden := receiveMsg(t, srv, "q", "")
	if len(hidden.Messages) != 0 {
		t.Fatalf("while hidden: want 0 messages, got %d", len(hidden.Messages))
	}

	// Peek should still see it regardless of visibility.
	peeked := receiveMsg(t, srv, "q", "peekonly=true")
	if len(peeked.Messages) != 1 {
		t.Fatalf("peek while hidden: want 1 message, got %d", len(peeked.Messages))
	}
}

func TestNumOfMessagesCapping(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()
	createQueue(t, srv, "q")

	for i := range 5 {
		sendMsg(t, srv, "q", fmt.Sprintf("bXNn%d", i))
	}

	got := receiveMsg(t, srv, "q", "numofmessages=2&visibilitytimeout=30")
	if len(got.Messages) != 2 {
		t.Fatalf("numofmessages=2: want 2 messages, got %d", len(got.Messages))
	}

	// Remaining 3 still visible.
	rest := receiveMsg(t, srv, "q", "numofmessages=10&visibilitytimeout=30")
	if len(rest.Messages) != 3 {
		t.Fatalf("remaining: want 3 messages, got %d", len(rest.Messages))
	}
}
