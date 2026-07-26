package queuestorage

import (
	"crypto/rand"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/altocirrus/altocirrus/internal/config"
	"github.com/altocirrus/altocirrus/internal/server"
	"github.com/altocirrus/altocirrus/internal/storage"
)

const (
	// ponytail: separate account name avoids ServeMux panic when blob also uses devstoreaccount1
	accountName    = "devstoreaccount1queue"
	queueNamespace = "azure:queuestorage:queues"
)

func messagesNamespace(queue string) string {
	return fmt.Sprintf("azure:queuestorage:messages:%s", queue)
}

// ---------------------------------------------------------------------------
// XML response models — queue enumeration
// ---------------------------------------------------------------------------

type enumerationResults struct {
	XMLName xml.Name `xml:"EnumerationResults"`
	Queues  *queues  `xml:"Queues,omitempty"`
}

type queues struct {
	Queue []queue `xml:"Queue"`
}

type queue struct {
	Name string `xml:"Name"`
}

type xmlError struct {
	XMLName xml.Name `xml:"Error"`
	Code    string   `xml:"Code"`
	Message string   `xml:"Message"`
}

// queueRecord is stored in the backing store for each queue.
type queueRecord struct {
	Name         string `json:"name"`
	LastModified string `json:"lastModified"`
}

// ---------------------------------------------------------------------------
// XML response models — messages
// ---------------------------------------------------------------------------

type queueMsgInput struct {
	XMLName     xml.Name `xml:"QueueMessage"`
	MessageText string   `xml:"MessageText"`
}

type queueMsgXML struct {
	MessageId       string `xml:"MessageId"`
	InsertionTime   string `xml:"InsertionTime"`
	ExpirationTime  string `xml:"ExpirationTime"`
	DequeueCount    int    `xml:"DequeueCount,omitempty"`
	PopReceipt      string `xml:"PopReceipt,omitempty"`
	TimeNextVisible string `xml:"TimeNextVisible,omitempty"`
	MessageText     string `xml:"MessageText,omitempty"`
}

type queueMessagesList struct {
	XMLName  xml.Name      `xml:"QueueMessagesList"`
	Messages []queueMsgXML `xml:"QueueMessage"`
}

// messageRecord is the JSON-encoded internal storage format per message.
type messageRecord struct {
	MessageID       string    `json:"messageId"`
	MessageText     string    `json:"messageText"`
	InsertionTime   time.Time `json:"insertionTime"`
	ExpirationTime  time.Time `json:"expirationTime"`
	DequeueCount    int       `json:"dequeueCount"`
	PopReceipt      string    `json:"popReceipt"`
	TimeNextVisible time.Time `json:"timeNextVisible"`
}

// ---------------------------------------------------------------------------
// RegisterRoutes wires Azure Queue Storage endpoints into the given mux.
//
// ponytail: these single-segment patterns ({queue}) overlap with blobstorage's
// catch-all (/devstoreaccount1/). Go 1.22 ServeMux picks the more specific
// pattern — but that means blob container operations (single-segment + restype=container)
// will land here instead of the blob handler when both are on the same mux.
// Integration into main.go requires a unified account handler or separate muxes.
// ---------------------------------------------------------------------------

func RegisterRoutes(mux *http.ServeMux, store storage.Store, cfg *config.Config) {
	h := &handler{store: store, cfg: cfg}

	base := "/" + accountName

	// Queue CRUD — single-segment paths.
	mux.HandleFunc("PUT "+base+"/{queue}", h.createOrUpdateQueue)
	mux.HandleFunc("GET "+base+"/{queue}", h.getQueueProps)
	mux.HandleFunc("DELETE "+base+"/{queue}", h.deleteQueue)

	// List queues — account root. Note: conflicts with blobstorage's
	// "GET /devstoreaccount1" if both are registered on the same mux.
	mux.HandleFunc("GET "+base, h.listQueues)

	// Message operations — more specific paths take priority over queue CRUD.
	msgs := base + "/{queue}/messages"
	mux.HandleFunc("POST "+msgs, h.sendMessage)
	mux.HandleFunc("GET "+msgs, h.getMessages) // receive or peek via peekonly=true
	mux.HandleFunc("DELETE "+msgs, h.clearMessages)
	mux.HandleFunc("PUT "+msgs+"/{messageId}", h.updateMessage)
	mux.HandleFunc("DELETE "+msgs+"/{messageId}", h.deleteMessage)
}

// ---------------------------------------------------------------------------
// handler
// ---------------------------------------------------------------------------

type handler struct {
	store storage.Store
	cfg   *config.Config
}

func msHeaders(w http.ResponseWriter) {
	w.Header().Set("x-ms-request-id", server.RequestID())
	w.Header().Set("x-ms-version", "2020-10-02")
}

// ---------------------------------------------------------------------------
// Queue operations
// ---------------------------------------------------------------------------

func (h *handler) createOrUpdateQueue(w http.ResponseWriter, r *http.Request) {
	msHeaders(w)
	name := r.PathValue("queue")

	if _, ok := h.store.Get(queueNamespace, name); ok {
		// Azure returns 204 on re-create of an existing queue (idempotent).
		w.WriteHeader(http.StatusNoContent)
		return
	}

	rec := queueRecord{Name: name, LastModified: time.Now().UTC().Format(http.TimeFormat)}
	data, _ := json.Marshal(rec)
	h.store.Put(queueNamespace, name, data)
	w.WriteHeader(http.StatusCreated)
}

func (h *handler) getQueueProps(w http.ResponseWriter, r *http.Request) {
	msHeaders(w)
	name := r.PathValue("queue")

	if r.URL.Query().Get("comp") != "metadata" {
		writeXMLError(w, "InvalidQueryParameterValue", "comp=metadata required", http.StatusBadRequest)
		return
	}

	data, ok := h.store.Get(queueNamespace, name)
	if !ok {
		writeXMLError(w, "QueueNotFound", "The specified queue does not exist.", http.StatusNotFound)
		return
	}

	var rec queueRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		writeXMLError(w, "InternalError", "failed to read queue record", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Last-Modified", rec.LastModified)
	w.Header().Set("x-ms-approximate-messages-count", "0")
	w.WriteHeader(http.StatusOK)
}

func (h *handler) deleteQueue(w http.ResponseWriter, r *http.Request) {
	msHeaders(w)
	name := r.PathValue("queue")

	if _, ok := h.store.Get(queueNamespace, name); !ok {
		writeXMLError(w, "QueueNotFound", "The specified queue does not exist.", http.StatusNotFound)
		return
	}

	h.store.Delete(queueNamespace, name)
	h.store.Clear(messagesNamespace(name))
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) listQueues(w http.ResponseWriter, r *http.Request) {
	msHeaders(w)

	if r.URL.Query().Get("comp") != "list" {
		writeXMLError(w, "InvalidQueryParameterValue", "comp=list required", http.StatusBadRequest)
		return
	}

	keys := h.store.List(queueNamespace, "")
	sort.Strings(keys)

	var qs []queue
	for _, k := range keys {
		qs = append(qs, queue{Name: k})
	}

	result := enumerationResults{Queues: &queues{Queue: qs}}
	writeXML(w, http.StatusOK, result)
}

// ---------------------------------------------------------------------------
// Message operations
// ---------------------------------------------------------------------------

func (h *handler) sendMessage(w http.ResponseWriter, r *http.Request) {
	msHeaders(w)
	queueName := r.PathValue("queue")

	if _, ok := h.store.Get(queueNamespace, queueName); !ok {
		writeXMLError(w, "QueueNotFound", "The specified queue does not exist.", http.StatusNotFound)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeXMLError(w, "InvalidXMLDocument", "Could not read request body.", http.StatusBadRequest)
		return
	}
	var input queueMsgInput
	if xml.Unmarshal(body, &input) != nil {
		writeXMLError(w, "InvalidXMLDocument", "The XML document is invalid.", http.StatusBadRequest)
		return
	}

	now := time.Now().UTC()
	// ponytail: 7-day default TTL per Azure Queue Storage spec
	msg := messageRecord{
		MessageID:       newID(),
		MessageText:     input.MessageText,
		InsertionTime:   now,
		ExpirationTime:  now.Add(7 * 24 * time.Hour),
		PopReceipt:      newID(),
		TimeNextVisible: now,
	}
	data, _ := json.Marshal(msg)
	h.store.Put(messagesNamespace(queueName), msg.MessageID, data)

	writeXML(w, http.StatusCreated, queueMessagesList{Messages: []queueMsgXML{toXML(msg, false)}})
}

func (h *handler) getMessages(w http.ResponseWriter, r *http.Request) {
	msHeaders(w)
	q := r.URL.Query()
	peekOnly := q.Get("peekonly") == "true"
	n := parseIntDefault(q.Get("numofmessages"), 1)
	if n > 32 {
		n = 32
	}
	vt := parseIntDefault(q.Get("visibilitytimeout"), 30)
	queueName := r.PathValue("queue")

	if _, ok := h.store.Get(queueNamespace, queueName); !ok {
		writeXMLError(w, "QueueNotFound", "The specified queue does not exist.", http.StatusNotFound)
		return
	}

	msgs := h.loadMessages(queueName)
	now := time.Now().UTC()

	var out []queueMsgXML
	for i := range msgs {
		if len(out) >= n {
			break
		}
		m := &msgs[i]
		if !peekOnly && m.TimeNextVisible.After(now) {
			continue // invisible to receivers
		}
		if peekOnly {
			out = append(out, toXML(*m, true))
		} else {
			m.DequeueCount++
			m.PopReceipt = newID()
			m.TimeNextVisible = now.Add(time.Duration(vt) * time.Second)
			data, _ := json.Marshal(m)
			h.store.Put(messagesNamespace(queueName), m.MessageID, data)
			out = append(out, toXML(*m, false))
		}
	}

	writeXML(w, http.StatusOK, queueMessagesList{Messages: out})
}

func (h *handler) deleteMessage(w http.ResponseWriter, r *http.Request) {
	msHeaders(w)
	queueName := r.PathValue("queue")
	msgID := r.PathValue("messageId")
	popReceipt := r.URL.Query().Get("popreceipt")

	data, ok := h.store.Get(messagesNamespace(queueName), msgID)
	if !ok {
		writeXMLError(w, "MessageNotFound", "The specified message does not exist.", http.StatusNotFound)
		return
	}
	var msg messageRecord
	if json.Unmarshal(data, &msg) != nil || msg.PopReceipt != popReceipt {
		writeXMLError(w, "PopReceiptMismatch",
			"The specified pop receipt did not match the pop receipt for a dequeued message.",
			http.StatusBadRequest)
		return
	}
	h.store.Delete(messagesNamespace(queueName), msgID)
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) updateMessage(w http.ResponseWriter, r *http.Request) {
	msHeaders(w)
	queueName := r.PathValue("queue")
	msgID := r.PathValue("messageId")
	q := r.URL.Query()
	popReceipt := q.Get("popreceipt")
	vt := parseIntDefault(q.Get("visibilitytimeout"), 0)

	data, ok := h.store.Get(messagesNamespace(queueName), msgID)
	if !ok {
		writeXMLError(w, "MessageNotFound", "The specified message does not exist.", http.StatusNotFound)
		return
	}
	var msg messageRecord
	if json.Unmarshal(data, &msg) != nil || msg.PopReceipt != popReceipt {
		writeXMLError(w, "PopReceiptMismatch", "The specified pop receipt did not match.", http.StatusBadRequest)
		return
	}

	body, _ := io.ReadAll(r.Body)
	if len(body) > 0 {
		var input queueMsgInput
		if xml.Unmarshal(body, &input) == nil && input.MessageText != "" {
			msg.MessageText = input.MessageText
		}
	}

	now := time.Now().UTC()
	msg.PopReceipt = newID()
	msg.TimeNextVisible = now.Add(time.Duration(vt) * time.Second)
	updated, _ := json.Marshal(msg)
	h.store.Put(messagesNamespace(queueName), msgID, updated)

	w.Header().Set("x-ms-popreceipt", msg.PopReceipt)
	w.Header().Set("x-ms-time-next-visible", msg.TimeNextVisible.Format(http.TimeFormat))
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) clearMessages(w http.ResponseWriter, r *http.Request) {
	msHeaders(w)
	queueName := r.PathValue("queue")

	if _, ok := h.store.Get(queueNamespace, queueName); !ok {
		writeXMLError(w, "QueueNotFound", "The specified queue does not exist.", http.StatusNotFound)
		return
	}

	h.store.Clear(messagesNamespace(queueName))
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Message helpers
// ---------------------------------------------------------------------------

// loadMessages fetches all messages for a queue, sorted by InsertionTime (FIFO).
func (h *handler) loadMessages(queueName string) []messageRecord {
	keys := h.store.List(messagesNamespace(queueName), "")
	msgs := make([]messageRecord, 0, len(keys))
	for _, k := range keys {
		data, ok := h.store.Get(messagesNamespace(queueName), k)
		if !ok {
			continue
		}
		var m messageRecord
		if json.Unmarshal(data, &m) == nil {
			msgs = append(msgs, m)
		}
	}
	sort.Slice(msgs, func(i, j int) bool {
		return msgs[i].InsertionTime.Before(msgs[j].InsertionTime)
	})
	return msgs
}

func toXML(m messageRecord, peekOnly bool) queueMsgXML {
	x := queueMsgXML{
		MessageId:      m.MessageID,
		InsertionTime:  m.InsertionTime.Format(http.TimeFormat),
		ExpirationTime: m.ExpirationTime.Format(http.TimeFormat),
		DequeueCount:   m.DequeueCount,
		MessageText:    m.MessageText,
	}
	if !peekOnly {
		x.PopReceipt = m.PopReceipt
		x.TimeNextVisible = m.TimeNextVisible.Format(http.TimeFormat)
	}
	return x
}

// newID returns a random UUID v4.
func newID() string {
	var b [16]byte
	rand.Read(b[:]) //nolint:errcheck
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return def
	}
	return n
}

// ---------------------------------------------------------------------------
// XML helpers
// ---------------------------------------------------------------------------

func writeXML(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprint(w, xml.Header)
	xml.NewEncoder(w).Encode(v)
}

func writeXMLError(w http.ResponseWriter, code, message string, status int) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprint(w, xml.Header)
	xml.NewEncoder(w).Encode(xmlError{Code: code, Message: message})
}
