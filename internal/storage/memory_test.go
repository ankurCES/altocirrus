package storage

import (
	"bytes"
	"sort"
	"sync"
	"testing"
)

func TestMemoryGetMissing(t *testing.T) {
	s := NewMemoryStore()
	val, ok := s.Get("ns", "missing")
	if ok || val != nil {
		t.Fatalf("expected nil/false for missing key, got %v/%v", val, ok)
	}
	// Missing namespace entirely.
	val, ok = s.Get("nope", "k")
	if ok || val != nil {
		t.Fatalf("expected nil/false for missing namespace, got %v/%v", val, ok)
	}
}

func TestMemoryPutGet(t *testing.T) {
	s := NewMemoryStore()
	s.Put("ns", "k", []byte("hello"))
	val, ok := s.Get("ns", "k")
	if !ok || string(val) != "hello" {
		t.Fatalf("expected hello/true, got %s/%v", val, ok)
	}

	// Overwrite returns updated value.
	s.Put("ns", "k", []byte("world"))
	val, ok = s.Get("ns", "k")
	if !ok || string(val) != "world" {
		t.Fatalf("expected world/true after overwrite, got %s/%v", val, ok)
	}

	// Different namespace is isolated.
	val, ok = s.Get("other", "k")
	if ok || val != nil {
		t.Fatalf("expected nil/false for different namespace, got %v/%v", val, ok)
	}
}

func TestMemoryGetReturnsCopy(t *testing.T) {
	s := NewMemoryStore()
	s.Put("ns", "k", []byte("data"))
	val, _ := s.Get("ns", "k")
	val[0] = 'X' // mutate returned slice
	val2, _ := s.Get("ns", "k")
	if val2[0] == 'X' {
		t.Fatal("Get should return a copy; internal state was mutated")
	}
}

func TestMemoryDeleteNonExistent(t *testing.T) {
	s := NewMemoryStore()
	if s.Delete("ns", "nope") {
		t.Fatal("expected false for non-existent key")
	}
	// Non-existent namespace.
	if s.Delete("nope", "k") {
		t.Fatal("expected false for non-existent namespace")
	}
}

func TestMemoryDeleteExisting(t *testing.T) {
	s := NewMemoryStore()
	s.Put("ns", "k", []byte("v"))

	if !s.Delete("ns", "k") {
		t.Fatal("expected true for existing delete")
	}
	if _, ok := s.Get("ns", "k"); ok {
		t.Fatal("key should be gone after delete")
	}
	// Second delete returns false.
	if s.Delete("ns", "k") {
		t.Fatal("expected false for already-deleted key")
	}
}

func TestMemoryList(t *testing.T) {
	s := NewMemoryStore()
	s.Put("ns", "app/one", []byte("1"))
	s.Put("ns", "app/two", []byte("2"))
	s.Put("ns", "other/thing", []byte("3"))
	s.Put("ns2", "app/four", []byte("4"))

	keys := s.List("ns", "app/")
	sort.Strings(keys)
	if len(keys) != 2 || keys[0] != "app/one" || keys[1] != "app/two" {
		t.Fatalf("unexpected prefix list: %v", keys)
	}

	// Empty prefix returns all keys in namespace.
	all := s.List("ns", "")
	sort.Strings(all)
	if len(all) != 3 {
		t.Fatalf("expected 3 keys, got %v", all)
	}
}

func TestMemoryListEmptyNamespace(t *testing.T) {
	s := NewMemoryStore()
	if keys := s.List("empty", ""); keys != nil {
		t.Fatalf("expected nil for missing namespace, got %v", keys)
	}
}

func TestMemoryClear(t *testing.T) {
	s := NewMemoryStore()
	s.Put("ns", "a", []byte("1"))
	s.Put("ns", "b", []byte("2"))
	s.Put("other", "c", []byte("3"))

	s.Clear("ns")

	if keys := s.List("ns", ""); keys != nil {
		t.Fatalf("expected nil after clear, got %v", keys)
	}

	// Other namespace is untouched.
	val, ok := s.Get("other", "c")
	if !ok || string(val) != "3" {
		t.Fatal("other namespace should be untouched after clear")
	}
}

func TestMemoryClearNonExistent(t *testing.T) {
	s := NewMemoryStore()
	s.Clear("nope") // should not panic
}

func TestMemoryNamespaces(t *testing.T) {
	s := NewMemoryStore()
	if ns := s.Namespaces(); len(ns) != 0 {
		t.Fatalf("expected empty namespaces, got %v", ns)
	}

	s.Put("alpha", "k", []byte("1"))
	s.Put("beta", "k", []byte("2"))

	ns := s.Namespaces()
	sort.Strings(ns)
	if len(ns) != 2 || ns[0] != "alpha" || ns[1] != "beta" {
		t.Fatalf("unexpected namespaces: %v", ns)
	}

	// After clearing a namespace, it no longer appears.
	s.Clear("alpha")
	ns2 := s.Namespaces()
	if len(ns2) != 1 || ns2[0] != "beta" {
		t.Fatalf("expected only beta after clear, got %v", ns2)
	}
}

func TestMemoryConcurrent(t *testing.T) {
	s := NewMemoryStore()
	const goroutines = 20
	const ops = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range goroutines {
		go func(id int) {
			defer wg.Done()
			key := "key"
			val := bytes.Repeat([]byte{byte(id)}, 8)
			for range ops {
				s.Put("ns", key, val)
				s.Get("ns", key)
				s.List("ns", "")
			}
		}(i)
	}
	wg.Wait()
}
