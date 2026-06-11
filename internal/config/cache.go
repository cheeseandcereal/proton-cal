package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// cacheVersion invalidates the whole cache file on schema changes.
const cacheVersion = 1

const (
	cacheFile     = "cache.json"
	cacheLockFile = "cache.lock"
)

// cacheDoc is the on-disk shape of cache.json.
type cacheDoc struct {
	Version int                   `json:"version"`
	Scope   string                `json:"scope"` // session UID + base URL
	Entries map[string]cacheEntry `json:"entries"`
}

type cacheEntry struct {
	FetchedAt time.Time       `json:"fetched_at"`
	Data      json.RawMessage `json:"data"`
}

// Cache is an on-disk, flock-guarded store of raw API response bodies
// (bootstrap data: key material and the calendar list - never event
// content). Entries are scoped to one session+endpoint: a cache written by
// a different login or base URL is discarded wholesale. Expiry policy is
// the caller's job; the cache only records fetch times.
//
// All failures are deliberately quiet (a broken cache must never break the
// app): reads miss and writes are best-effort.
type Cache struct {
	dir   string
	scope string
	doc   cacheDoc // in-memory snapshot from open time (reads are run-local)
}

// OpenCache loads the cache for the given scope, treating a missing,
// corrupt, version-mismatched or foreign-scoped file as empty.
func OpenCache(scope string) (*Cache, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	c := &Cache{dir: dir, scope: scope}
	c.doc = c.loadDoc()
	return c, nil
}

func (c *Cache) path() string     { return filepath.Join(c.dir, cacheFile) }
func (c *Cache) lockPath() string { return filepath.Join(c.dir, cacheLockFile) }

// loadDoc reads and validates the on-disk doc, returning an empty doc on
// any mismatch or error.
func (c *Cache) loadDoc() cacheDoc {
	empty := cacheDoc{Version: cacheVersion, Scope: c.scope, Entries: map[string]cacheEntry{}}
	data, err := os.ReadFile(c.path())
	if err != nil {
		return empty
	}
	var doc cacheDoc
	if json.Unmarshal(data, &doc) != nil || doc.Version != cacheVersion || doc.Scope != c.scope || doc.Entries == nil {
		return empty
	}
	return doc
}

// Get returns the cached body and its fetch time for key. ok is false on a
// miss.
func (c *Cache) Get(key string) (data json.RawMessage, fetchedAt time.Time, ok bool) {
	e, ok := c.doc.Entries[key]
	return e.Data, e.FetchedAt, ok
}

// Put stores a response body under key (fetched now), persisting
// best-effort: the write merges with the current on-disk doc under the
// lock so concurrent processes caching different keys do not clobber each
// other.
func (c *Cache) Put(key string, data json.RawMessage) {
	c.update(func(doc *cacheDoc) {
		doc.Entries[key] = cacheEntry{FetchedAt: time.Now().UTC(), Data: data}
	})
}

// Delete drops keys (used for self-healing invalidation when cached key
// material turns out stale).
func (c *Cache) Delete(keys ...string) {
	c.update(func(doc *cacheDoc) {
		for _, k := range keys {
			delete(doc.Entries, k)
		}
	})
}

// update applies fn to the on-disk doc under the lock and refreshes the
// in-memory snapshot. Best-effort: errors leave the snapshot updated so
// the running process stays coherent.
func (c *Cache) update(fn func(doc *cacheDoc)) {
	unlock, err := lock(c.lockPath())
	if err != nil {
		fn(&c.doc)
		return
	}
	defer unlock()
	doc := c.loadDoc()
	fn(&doc)
	c.doc = doc
	if data, err := json.Marshal(doc); err == nil {
		_ = writeFileAtomic(c.path(), data)
	}
}

// ClearCache removes the cache file entirely (logout must not leave key
// material behind, encrypted or not).
func ClearCache() error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, cacheFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
