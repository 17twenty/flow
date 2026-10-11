package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CacheDirEnv names the environment variable that moves flow's cache, so that
// a test can point it at a temporary directory (spec 14).
const CacheDirEnv = "FLOW_CACHE_DIR"

// cacheDirPerm is the mode of the cache directory, and cacheFilePerm the mode
// of the files in it (spec 15 §4).
const (
	cacheDirPerm  os.FileMode = 0o700
	cacheFilePerm os.FileMode = 0o600
)

// TokenFingerprint returns a short, stable, non-reversible identifier for a
// credential. Cached responses are keyed by it because an ETag is only valid
// for the credential that obtained it: a token that may see less of a resource
// must not be handed another token's body (spec 10 §2).
//
// The empty token fingerprints as "anonymous", which is itself a key, so
// unauthenticated responses never collide with authenticated ones.
func TokenFingerprint(token string) string {
	if token == "" {
		return "anonymous"
	}
	sum := sha256.Sum256([]byte("flow-token-v1\x00" + token))
	return hex.EncodeToString(sum[:8])
}

// CacheKey is the key under which a response to one request is cached: the
// credential's fingerprint, the method and the full URL. Callers do not build
// it themselves; [Client] does.
func CacheKey(fingerprint, method, url string) string {
	return fingerprint + " " + method + " " + url
}

// CachedResponse is a validated response kept so that the next request can ask
// the provider whether it changed. A 304 costs nothing against GitHub's
// primary rate limit, so a cache hit is both faster and cheaper.
type CachedResponse struct {
	// ETag is the validator to send back as If-None-Match.
	ETag string `json:"etag"`
	// Status is the status of the cached response, always a 2xx.
	Status int `json:"status"`
	// Header is the response header the body arrived with.
	Header http.Header `json:"header,omitempty"`
	// Body is the response body, verbatim.
	Body []byte `json:"body,omitempty"`
	// Stored is when the entry was written.
	Stored time.Time `json:"stored"`
}

// ETagCache stores validated responses between runs. Implementations must be
// safe for concurrent use and must never return an entry for a key they were
// not given, because the key carries the credential's fingerprint.
type ETagCache interface {
	// Load returns the entry stored under key, if there is one.
	Load(key string) (CachedResponse, bool)
	// Store writes an entry. A cache that cannot store is not an error the
	// caller has to handle: [Client] logs it and carries on.
	Store(key string, entry CachedResponse) error
}

// MemoryCache is an [ETagCache] that lives only as long as the process. It is
// what a single command run needs, and what tests use.
type MemoryCache struct {
	mu      sync.Mutex
	entries map[string]CachedResponse
}

// NewMemoryCache returns an empty in-memory cache.
func NewMemoryCache() *MemoryCache {
	return &MemoryCache{entries: map[string]CachedResponse{}}
}

// Load returns the entry stored under key, if there is one.
func (c *MemoryCache) Load(key string) (CachedResponse, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	return entry, ok
}

// Store writes an entry.
func (c *MemoryCache) Store(key string, entry CachedResponse) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]CachedResponse{}
	}
	c.entries[key] = entry
	return nil
}

// Len reports how many entries the cache holds.
func (c *MemoryCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// FileCache is an [ETagCache] on disk, so that a daily doctor run can validate
// yesterday's answers instead of fetching them again. The directory is 0700
// and its files 0600 (spec 15 §4).
type FileCache struct {
	dir string
	mu  sync.Mutex
}

// NewFileCache returns a cache writing into dir, creating it if needed.
func NewFileCache(dir string) (*FileCache, error) {
	if dir == "" {
		return nil, Errorf(ErrValidation, "open the response cache").
			WithWhy("no directory was given")
	}
	if err := os.MkdirAll(dir, cacheDirPerm); err != nil {
		return nil, Errorf(ErrUnavailable, "create the response cache at %s", dir).
			WithWhy("%s", err).
			WithFix("set %s to a directory flow may write", CacheDirEnv)
	}
	// MkdirAll leaves an existing directory's mode alone, and umask can
	// loosen a new one, so say it again.
	if err := os.Chmod(dir, cacheDirPerm); err != nil {
		return nil, Errorf(ErrUnavailable, "secure the response cache at %s", dir).
			WithWhy("%s", err)
	}
	return &FileCache{dir: dir}, nil
}

// CacheDir returns the directory flow caches responses in: $FLOW_CACHE_DIR
// when set, otherwise "flow" under the user's cache directory.
func CacheDir() (string, error) {
	if dir := os.Getenv(CacheDirEnv); dir != "" {
		return dir, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", Errorf(ErrUnavailable, "find the user cache directory").
			WithWhy("%s", err).
			WithFix("set %s to a directory flow may write", CacheDirEnv)
	}
	return filepath.Join(base, "flow"), nil
}

// path is the file one key is stored in. The key holds a credential
// fingerprint, so it is hashed rather than spelled out on the file system.
func (c *FileCache) path(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(c.dir, hex.EncodeToString(sum[:])+".json")
}

// Load returns the entry stored under key, if there is one. A corrupt or
// unreadable file is a miss, never an error: the request simply happens.
func (c *FileCache) Load(key string) (CachedResponse, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	raw, err := os.ReadFile(c.path(key))
	if err != nil {
		return CachedResponse{}, false
	}
	var entry CachedResponse
	if err := json.Unmarshal(raw, &entry); err != nil || entry.ETag == "" {
		return CachedResponse{}, false
	}
	return entry, true
}

// Store writes an entry, replacing it atomically so that a concurrent reader
// sees either the old entry or the new one.
func (c *FileCache) Store(key string, entry CachedResponse) error {
	raw, err := json.Marshal(entry)
	if err != nil {
		return Errorf(ErrValidation, "cache a response").WithWhy("%s", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return writeAtomic(c.dir, c.path(key), "a cached response", raw)
}

// writeAtomic replaces the file at final with raw, through a temporary file in
// the same directory, so that a concurrent reader sees either the old content
// or the new one and never half of either. thing names what is being written,
// so that a failure says what was lost.
func writeAtomic(dir, final, thing string, raw []byte) error {
	tmp, err := os.CreateTemp(dir, ".flow-cache-*")
	if err != nil {
		return Errorf(ErrUnavailable, "write %s in %s", thing, dir).WithWhy("%s", err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(cacheFilePerm); err != nil {
		_ = tmp.Close()
		return Errorf(ErrUnavailable, "secure %s", thing).WithWhy("%s", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return Errorf(ErrUnavailable, "write %s", thing).WithWhy("%s", err)
	}
	if err := tmp.Close(); err != nil {
		return Errorf(ErrUnavailable, "write %s", thing).WithWhy("%s", err)
	}
	if err := os.Rename(name, final); err != nil {
		return Errorf(ErrUnavailable, "replace %s", thing).WithWhy("%s", err)
	}
	return nil
}
