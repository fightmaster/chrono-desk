package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// PhotoCache is a write-once local store of finish-photo JPEGs. Each unique phone
// URL is fetched from the smartphone exactly once and then served from disk, so
// the desk doesn't re-pull full images on every view (mirrors RaceTorch's
// fetch-once model — easier on the phone and the LAN). Lazy: an image is fetched
// the first time it is actually displayed, not eagerly for every burst frame.
type PhotoCache struct {
	dir    string
	client *http.Client
	locks  sync.Map // path -> *sync.Mutex, to coalesce concurrent first-fetches
}

// NewPhotoCache creates the cache directory.
func NewPhotoCache(dir string) (*PhotoCache, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("photo cache dir: %w", err)
	}
	return &PhotoCache{dir: dir, client: &http.Client{Timeout: 15 * time.Second}}, nil
}

func (c *PhotoCache) pathFor(rawURL string) string {
	sum := sha256.Sum256([]byte(rawURL))
	return filepath.Join(c.dir, hex.EncodeToString(sum[:])+".jpg")
}

// Get returns the image bytes for rawURL, downloading once on a cache miss.
func (c *PhotoCache) Get(ctx context.Context, rawURL string) ([]byte, error) {
	p := c.pathFor(rawURL)
	if b, err := os.ReadFile(p); err == nil {
		return b, nil
	}
	// Serialize concurrent first-fetches of the same image (the wall + the feed
	// thumbnail + the panel can all ask at once).
	mu := c.lockFor(p)
	mu.Lock()
	defer mu.Unlock()
	if b, err := os.ReadFile(p); err == nil { // recheck under lock
		return b, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("источник вернул %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 25<<20)) // 25 MiB safety cap
	if err != nil {
		return nil, err
	}
	// Publish atomically so a partial write is never served.
	tmp := p + ".tmp"
	if werr := os.WriteFile(tmp, data, 0o644); werr == nil {
		_ = os.Rename(tmp, p)
	}
	return data, nil
}

func (c *PhotoCache) lockFor(key string) *sync.Mutex {
	m, _ := c.locks.LoadOrStore(key, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// HostAllowed reports whether rawURL targets one of the allowed base URLs (same
// scheme + host + port). Guards the image proxy against SSRF — the desk only
// proxies the phones the operator registered.
func HostAllowed(rawURL string, allowed []string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	for _, base := range allowed {
		b, err := url.Parse(base)
		if err != nil {
			continue
		}
		if strings.EqualFold(u.Scheme, b.Scheme) && strings.EqualFold(u.Host, b.Host) {
			return true
		}
	}
	return false
}
