package storage

import (
	"strings"
	"sync"
)

// Store defines the interface for the emulator's key-value storage backend.
type Store interface {
	// Get retrieves a value by namespace and key. Returns the value and
	// true if found, or nil and false if not.
	Get(namespace, key string) ([]byte, bool)

	// Put stores a value under the given namespace and key.
	Put(namespace, key string, value []byte)

	// Delete removes a key from the namespace. Returns true if the key
	// existed and was removed, false otherwise.
	Delete(namespace, key string) bool

	// List returns all keys in the namespace that match the given prefix.
	List(namespace, prefix string) []string

	// Clear removes all keys in the given namespace.
	Clear(namespace string)

	// Namespaces returns all namespace names that contain at least one key.
	Namespaces() []string
}

// MemoryStore is a thread-safe, in-memory implementation of Store.
type MemoryStore struct {
	mu   sync.RWMutex
	data map[string]map[string][]byte
}

// NewMemoryStore creates and returns a new MemoryStore.
func NewMemoryStore() Store {
	return &MemoryStore{
		data: make(map[string]map[string][]byte),
	}
}

func (m *MemoryStore) Get(namespace, key string) ([]byte, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ns, ok := m.data[namespace]
	if !ok {
		return nil, false
	}
	v, ok := ns[key]
	if !ok {
		return nil, false
	}
	// Return a copy to prevent callers from mutating internal state.
	out := make([]byte, len(v))
	copy(out, v)
	return out, true
}

func (m *MemoryStore) Put(namespace, key string, value []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()

	ns, ok := m.data[namespace]
	if !ok {
		ns = make(map[string][]byte)
		m.data[namespace] = ns
	}
	// Store a copy to prevent external mutation.
	stored := make([]byte, len(value))
	copy(stored, value)
	ns[key] = stored
}

func (m *MemoryStore) Delete(namespace, key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	ns, ok := m.data[namespace]
	if !ok {
		return false
	}
	if _, exists := ns[key]; !exists {
		return false
	}
	delete(ns, key)
	return true
}

func (m *MemoryStore) List(namespace, prefix string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ns, ok := m.data[namespace]
	if !ok {
		return nil
	}

	var keys []string
	for k := range ns {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys
}

func (m *MemoryStore) Clear(namespace string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.data, namespace)
}

func (m *MemoryStore) Namespaces() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ns := make([]string, 0, len(m.data))
	for k, v := range m.data {
		if len(v) > 0 {
			ns = append(ns, k)
		}
	}
	return ns
}
