package remoteapi

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// registry holds all known protocol factories keyed by Factory.Name().
var (
	regMu    sync.RWMutex
	registry = map[string]Factory{}
)

// Register makes a protocol available to the bridge core. Call from an init()
// in each protocol package (e.g. internal/wechat). Panics on duplicate names.
func Register(f Factory) {
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := registry[f.Name()]; dup {
		panic("remoteapi: duplicate factory " + f.Name())
	}
	registry[f.Name()] = f
}

// Get returns the factory registered under name.
func Get(name string) (Factory, error) {
	regMu.RLock()
	defer regMu.RUnlock()
	f, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("remoteapi: no protocol registered as %q", name)
	}
	return f, nil
}

// Names lists registered protocols (sorted).
func Names() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Optional capabilities (type-asserted on a PuppetAPI)
// ---------------------------------------------------------------------------

// MediaUploader is an OPTIONAL capability. Some backends (like the WeChat CDN)
// require uploading media first and then sending a reference. If a PuppetAPI
// implements this, the core uploads via UploadMedia and passes the returned
// handle in Media.URL. Backends that accept inline bytes need not implement it.
type MediaUploader interface {
	UploadMedia(ctx context.Context, chat ChatID, m Media) (handle string, err error)
}

// Pinger is an OPTIONAL capability exposing a health check (e.g. WeChat
// HeartBeat). The core may call it periodically.
type Pinger interface {
	Ping(ctx context.Context) error
}

// LoginInfoProvider is an OPTIONAL capability for backends that can query
// server-side login/session/account information by account id (e.g. WeChat
// /Login/GetCacheInfo?wxid=...). Fields are intentionally dynamic because
// such APIs often return backend-specific data.
type LoginInfoProvider interface {
	GetLoginInfo(ctx context.Context, id UserID) (LoginInfo, error)
}

// LoginInfo is dynamic account/session info returned by LoginInfoProvider.
type LoginInfo struct {
	ID     UserID         `json:"id"`
	Fields map[string]any `json:"fields"`
}

// CachedLoginActivator is an OPTIONAL capability for backends that can restore
// the current client session from server-side login cache by account id.
// For WeChat this means /Login/GetCacheInfo?wxid=... contains enough state
// (wxid + sync key) to make the bridge treat the account as logged in.
type CachedLoginActivator interface {
	ActivateCachedLogin(ctx context.Context, id UserID) (LoginInfo, error)
}

// LoginRefresher is an OPTIONAL capability for backends that can refresh an
// existing cached login without a new interactive QR scan.
type LoginRefresher interface {
	RefreshLogin(ctx context.Context, id UserID) error
}
