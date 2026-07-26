package storage

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func tempDBPath(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp("", "altocirrus-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	f.Close()
	t.Cleanup(func() {
		os.Remove(path)
		// SQLite may create journal/wal files next to the db.
		os.Remove(path + "-journal")
		os.Remove(path + "-wal")
		os.Remove(path + "-shm")
	})
	return path
}

func newTestStore(t *testing.T, path string) *SQLiteStore {
	t.Helper()
	s, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(%q): %v", path, err)
	}
	return s
}

func TestSQLitePutGet(t *testing.T) {
	path := tempDBPath(t)
	s := newTestStore(t, path)
	defer s.Close()

	// Get on missing key returns nil, false.
	val, ok := s.Get("ns", "missing")
	if ok || val != nil {
		t.Fatalf("expected nil/false for missing key, got %v/%v", val, ok)
	}

	// Put then Get.
	s.Put("ns", "k1", []byte("hello"))
	val, ok = s.Get("ns", "k1")
	if !ok || string(val) != "hello" {
		t.Fatalf("expected hello/true, got %s/%v", val, ok)
	}

	// Overwrite.
	s.Put("ns", "k1", []byte("world"))
	val, ok = s.Get("ns", "k1")
	if !ok || string(val) != "world" {
		t.Fatalf("expected world/true after overwrite, got %s/%v", val, ok)
	}

	// Different namespace is isolated.
	val, ok = s.Get("other", "k1")
	if ok || val != nil {
		t.Fatalf("expected nil/false for different namespace, got %v/%v", val, ok)
	}
}

func TestSQLiteDelete(t *testing.T) {
	path := tempDBPath(t)
	s := newTestStore(t, path)
	defer s.Close()

	// Delete non-existent key returns false.
	if s.Delete("ns", "nope") {
		t.Fatal("expected false for non-existent delete")
	}

	s.Put("ns", "k1", []byte("data"))
	if !s.Delete("ns", "k1") {
		t.Fatal("expected true for existing delete")
	}

	// Confirm gone.
	if _, ok := s.Get("ns", "k1"); ok {
		t.Fatal("key should be gone after delete")
	}

	// Delete again returns false.
	if s.Delete("ns", "k1") {
		t.Fatal("expected false for already-deleted key")
	}
}

func TestSQLiteList(t *testing.T) {
	path := tempDBPath(t)
	s := newTestStore(t, path)
	defer s.Close()

	s.Put("ns", "app/one", []byte("1"))
	s.Put("ns", "app/two", []byte("2"))
	s.Put("ns", "other/thing", []byte("3"))
	s.Put("ns2", "app/three", []byte("4"))

	keys := s.List("ns", "app/")
	sort.Strings(keys)
	if len(keys) != 2 || keys[0] != "app/one" || keys[1] != "app/two" {
		t.Fatalf("unexpected list result: %v", keys)
	}

	// Empty prefix lists all keys in namespace.
	all := s.List("ns", "")
	sort.Strings(all)
	if len(all) != 3 {
		t.Fatalf("expected 3 keys, got %v", all)
	}

	// Non-existent namespace returns nil.
	if keys := s.List("nope", ""); keys != nil {
		t.Fatalf("expected nil for empty namespace, got %v", keys)
	}
}

func TestSQLiteListEscapesWildcards(t *testing.T) {
	path := tempDBPath(t)
	s := newTestStore(t, path)
	defer s.Close()

	s.Put("ns", "100%_done", []byte("a"))
	s.Put("ns", "100_other", []byte("b"))
	s.Put("ns", "100%match", []byte("c"))

	// Prefix "100%" should only match keys starting with literal "100%".
	keys := s.List("ns", "100%")
	sort.Strings(keys)
	if len(keys) != 2 || keys[0] != "100%_done" || keys[1] != "100%match" {
		t.Fatalf("expected [100%%_done 100%%match], got %v", keys)
	}
}

func TestSQLiteClear(t *testing.T) {
	path := tempDBPath(t)
	s := newTestStore(t, path)
	defer s.Close()

	s.Put("ns", "a", []byte("1"))
	s.Put("ns", "b", []byte("2"))
	s.Put("other", "c", []byte("3"))

	s.Clear("ns")

	if keys := s.List("ns", ""); keys != nil {
		t.Fatalf("expected nil after clear, got %v", keys)
	}

	// Other namespace untouched.
	val, ok := s.Get("other", "c")
	if !ok || string(val) != "3" {
		t.Fatal("other namespace should be untouched after clear")
	}
}

func TestSQLitePersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "persist.db")

	// Open, write, close.
	s1, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	s1.Put("ns", "key", []byte("persisted"))
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen — data should still be there.
	s2, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	val, ok := s2.Get("ns", "key")
	if !ok || string(val) != "persisted" {
		t.Fatalf("expected persisted/true after reopen, got %s/%v", val, ok)
	}
}
