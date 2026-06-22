package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCacheRoundTrip(t *testing.T) {
	t.Setenv(EnvConfigDir, t.TempDir())

	c, err := OpenCache("uid1|https://api")
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	if _, _, ok := c.Get("k"); ok {
		t.Fatal("empty cache must miss")
	}

	c.Put("k", json.RawMessage(`{"a":1}`))
	data, at, ok := c.Get("k")
	if !ok || string(data) != `{"a":1}` {
		t.Fatalf("Get = %q, %v", data, ok)
	}
	if time.Since(at) > time.Minute {
		t.Errorf("fetchedAt looks wrong: %v", at)
	}

	// A fresh handle on the same scope sees the persisted entry.
	c2, err := OpenCache("uid1|https://api")
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	if _, _, ok := c2.Get("k"); !ok {
		t.Fatal("persisted entry must survive reopen")
	}

	c2.Delete("k")
	if _, _, ok := c2.Get("k"); ok {
		t.Fatal("deleted entry must miss")
	}
	c3, _ := OpenCache("uid1|https://api")
	if _, _, ok := c3.Get("k"); ok {
		t.Fatal("deletion must persist")
	}
}

func TestCacheScopeMismatchDiscards(t *testing.T) {
	t.Setenv(EnvConfigDir, t.TempDir())

	c, _ := OpenCache("uid1|https://api")
	c.Put("k", json.RawMessage(`1`))

	other, _ := OpenCache("uid2|https://api")
	if _, _, ok := other.Get("k"); ok {
		t.Fatal("a different session scope must not see cached entries")
	}

	// Writing under the new scope replaces the foreign doc entirely.
	other.Put("k2", json.RawMessage(`2`))
	again, _ := OpenCache("uid1|https://api")
	if _, _, ok := again.Get("k"); ok {
		t.Fatal("old-scope entries must be gone after a new-scope write")
	}
}

func TestCacheCorruptAndVersionMismatch(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvConfigDir, dir)

	path := filepath.Join(dir, cacheFile)
	if err := os.WriteFile(path, []byte("{garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := OpenCache("s")
	if err != nil {
		t.Fatalf("corrupt cache must not error: %v", err)
	}
	if _, _, ok := c.Get("k"); ok {
		t.Fatal("corrupt cache must read as empty")
	}

	doc := cacheDoc{Version: cacheVersion + 1, Scope: "s", Entries: map[string]cacheEntry{
		"k": {FetchedAt: time.Now(), Data: json.RawMessage(`1`)},
	}}
	data, _ := json.Marshal(doc)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	c2, _ := OpenCache("s")
	if _, _, ok := c2.Get("k"); ok {
		t.Fatal("version-mismatched cache must read as empty")
	}
}

func TestClearCache(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvConfigDir, dir)

	c, _ := OpenCache("s")
	c.Put("k", json.RawMessage(`1`))
	if _, err := os.Stat(filepath.Join(dir, cacheFile)); err != nil {
		t.Fatalf("cache file should exist: %v", err)
	}
	if err := ClearCache(); err != nil {
		t.Fatalf("ClearCache: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, cacheFile)); !os.IsNotExist(err) {
		t.Fatal("cache file should be gone")
	}
	if err := ClearCache(); err != nil {
		t.Fatalf("ClearCache on missing file must be a no-op: %v", err)
	}
}
