package core

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTokenFingerprintIdentifiesWithoutRevealing(t *testing.T) {
	token := "gh" + "p_" + strings.Repeat("Z", 36)
	got := TokenFingerprint(token)
	if got == "" || strings.Contains(token, got) || strings.Contains(got, "Z") {
		t.Fatalf("fingerprint %q reveals the credential", got)
	}
	if TokenFingerprint(token) != got {
		t.Error("fingerprinting is not stable")
	}
	if TokenFingerprint(token+"x") == got {
		t.Error("two credentials fingerprinted the same")
	}
	if TokenFingerprint("") != "anonymous" {
		t.Errorf("TokenFingerprint(\"\") = %q, want anonymous", TokenFingerprint(""))
	}
}

func TestCacheKeySeparatesCredentialMethodAndURL(t *testing.T) {
	a := CacheKey("aaaa", "GET", "https://api/x")
	if a == CacheKey("bbbb", "GET", "https://api/x") {
		t.Error("two credentials share a cache key")
	}
	if a == CacheKey("aaaa", "HEAD", "https://api/x") {
		t.Error("two methods share a cache key")
	}
	if a == CacheKey("aaaa", "GET", "https://api/y") {
		t.Error("two URLs share a cache key")
	}
}

func TestMemoryCacheRoundTrips(t *testing.T) {
	cache := NewMemoryCache()
	if _, ok := cache.Load("absent"); ok {
		t.Error("an empty cache answered")
	}
	entry := CachedResponse{ETag: `"v1"`, Status: 200, Body: []byte(`{"a":1}`), Stored: time.Now()}
	if err := cache.Store("k", entry); err != nil {
		t.Fatalf("Store: %v", err)
	}
	got, ok := cache.Load("k")
	if !ok || got.ETag != entry.ETag || string(got.Body) != string(entry.Body) {
		t.Fatalf("Load = %+v, %v", got, ok)
	}
	if cache.Len() != 1 {
		t.Errorf("Len = %d, want 1", cache.Len())
	}
}

func TestFileCacheRoundTripsAndStaysPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	cache, err := NewFileCache(dir)
	if err != nil {
		t.Fatalf("NewFileCache: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("cache directory is %v, want 0700 (spec 15 §4)", perm)
	}

	entry := CachedResponse{
		ETag:   `W/"abc"`,
		Status: 200,
		Header: http.Header{"Content-Type": []string{"application/json"}},
		Body:   []byte(`{"name":"flow"}`),
		Stored: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
	}
	key := CacheKey("ffff", "GET", "https://api.github.com/repos/17twenty/flow")
	if err := cache.Store(key, entry); err != nil {
		t.Fatalf("Store: %v", err)
	}
	got, ok := cache.Load(key)
	if !ok {
		t.Fatal("stored entry not found")
	}
	if got.ETag != entry.ETag || string(got.Body) != string(entry.Body) || !got.Stored.Equal(entry.Stored) {
		t.Errorf("Load = %+v, want %+v", got, entry)
	}
	if got.Header.Get("Content-Type") != "application/json" {
		t.Errorf("header lost: %v", got.Header)
	}

	names, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(names) != 1 {
		t.Fatalf("glob = %v, %v", names, err)
	}
	if strings.Contains(filepath.Base(names[0]), "ffff") {
		t.Error("the file name spells out the cache key")
	}
	file, err := os.Stat(names[0])
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := file.Mode().Perm(); perm != 0o600 {
		t.Errorf("cache file is %v, want 0600", perm)
	}
}

func TestFileCacheTreatsRubbishAsAMiss(t *testing.T) {
	dir := t.TempDir()
	cache, err := NewFileCache(dir)
	if err != nil {
		t.Fatalf("NewFileCache: %v", err)
	}
	key := CacheKey("ffff", "GET", "https://api/x")
	if err := cache.Store(key, CachedResponse{ETag: `"v"`, Status: 200}); err != nil {
		t.Fatalf("Store: %v", err)
	}
	names, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if err := os.WriteFile(names[0], []byte("not json"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, ok := cache.Load(key); ok {
		t.Error("a corrupt entry was served; it must be a miss")
	}
}

func TestFileCacheReplacesAnEntryInPlace(t *testing.T) {
	cache, err := NewFileCache(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileCache: %v", err)
	}
	key := CacheKey("ffff", "GET", "https://api/x")
	for _, tag := range []string{`"v1"`, `"v2"`, `"v3"`} {
		if err := cache.Store(key, CachedResponse{ETag: tag, Status: 200}); err != nil {
			t.Fatalf("Store: %v", err)
		}
	}
	got, ok := cache.Load(key)
	if !ok || got.ETag != `"v3"` {
		t.Errorf("Load = %+v, want the newest entry", got)
	}
	names, _ := filepath.Glob(filepath.Join(cache.dir, "*"))
	if len(names) != 1 {
		t.Errorf("%d files left behind, want 1", len(names))
	}
}

func TestNewFileCacheRefusesNothing(t *testing.T) {
	if _, err := NewFileCache(""); !errors.Is(err, ErrValidation) {
		t.Errorf("NewFileCache(\"\") error = %v, want ErrValidation", err)
	}
}

func TestCacheDirPrefersTheEnvironment(t *testing.T) {
	t.Setenv(CacheDirEnv, "/tmp/flow-cache-test")
	got, err := CacheDir()
	if err != nil || got != "/tmp/flow-cache-test" {
		t.Fatalf("CacheDir = %q, %v", got, err)
	}
	t.Setenv(CacheDirEnv, "")
	got, err = CacheDir()
	if err != nil {
		t.Fatalf("CacheDir: %v", err)
	}
	if filepath.Base(got) != "flow" {
		t.Errorf("CacheDir = %q, want it under the user cache directory", got)
	}
}
