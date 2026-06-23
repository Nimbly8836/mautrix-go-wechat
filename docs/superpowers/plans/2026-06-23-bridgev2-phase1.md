# Bridgev2 Phase 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build Phase 1 of a mautrix-telegram-like bridge: `!wx` management commands, concurrent multi-WeChat-account login, one Matrix Space per WeChat account, automatic portal room creation, inbound WeChat delivery, and portal-room outbound routing.

**Architecture:** Keep `internal/remoteapi` as the backend contract and add a new bridge-oriented runtime layer with testable account, portal, command, and message-routing units. First build the bridge-independent core with fakes, then attach it to the Matrix appservice/bridgev2 API after confirming the available `mautrix` package version and Go toolchain.

**Tech Stack:** Go, `maunium.net/go/mautrix`, eventual `mautrix/bridgev2` package if available, existing `remoteapi.PuppetAPI`, existing appservice registration/config, SQLite/Postgres-compatible persistence through the existing `database.uri` configuration.

---

## Preconditions

The current shell reported `go: command not found`. Before executing code tasks, make Go available on `PATH` or run commands through the project's expected Go environment. Do not start implementation until `go version` succeeds.

Recommended verification command:

```bash
go version
```

Expected: prints a Go version compatible with `go 1.25.0` in `go.mod`.

---

## File Structure

Create focused packages so the core behavior is testable without a live Matrix server.

- Create `internal/bridgecore/account.go` — account identity, account runtime interfaces, account registry behavior.
- Create `internal/bridgecore/account_test.go` — multi-account registry tests.
- Create `internal/bridgecore/portal.go` — portal keys, forward/reverse mapping, portal manager interfaces.
- Create `internal/bridgecore/portal_test.go` — portal key and reverse lookup tests.
- Create `internal/bridgecore/commands.go` — `!wx` command parser and command model.
- Create `internal/bridgecore/commands_test.go` — command parser tests.
- Create `internal/bridgecore/router.go` — inbound/outbound routing orchestration independent of Matrix SDK details.
- Create `internal/bridgecore/router_test.go` — fake Matrix sink and fake remote API routing tests.
- Create `internal/bridgecore/store.go` — persistence interfaces and in-memory test store.
- Create `internal/bridgecore/store_test.go` — store behavior tests.
- Create `internal/matrixbridge/server.go` — Matrix-facing bridge/appservice startup and event handler after dependency/API discovery.
- Create `internal/matrixbridge/commands.go` — bridge command handlers that call `bridgecore`.
- Create `internal/matrixbridge/portal_rooms.go` — Matrix room/Space creation adapter.
- Modify `cmd/bridge/main.go` — switch startup from `internal/matrixas` to the new bridge layer once it compiles.
- Modify `docs/BRIDGEV2.md` — update status and Phase 1 behavior after implementation.
- Keep `internal/matrixas` intact until `internal/matrixbridge` is wired and tested, then remove or leave unused only if removal risks delaying Phase 1.

---

### Task 0: Toolchain And Bridgev2 API Discovery

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Create: `docs/superpowers/plans/2026-06-23-bridgev2-api-notes.md`

- [ ] **Step 1: Verify Go is available**

Run:

```bash
go version
```

Expected: command succeeds and prints a Go version. If it fails with `go: command not found`, fix the development environment before continuing.

- [ ] **Step 2: Check whether current dependency has bridgev2**

Run:

```bash
MAUTRIX_DIR="$(go env GOMODCACHE)/maunium.net/go/mautrix@v0.28.1"
find "$MAUTRIX_DIR" -maxdepth 3 -type d -name bridgev2 -print
rg -n "package bridgev2|type NetworkConnector|type NetworkAPI|QueueRemoteEvent|LoginProcessDisplayAndWait" "$MAUTRIX_DIR" || true
```

Expected before upgrade: either no `bridgev2` package is found, or the command prints the exact API names to use.

- [ ] **Step 3: Upgrade or add the correct mautrix dependency**

Run:

```bash
go list -m -versions maunium.net/go/mautrix
go get maunium.net/go/mautrix@latest
go mod tidy
```

Expected: `go.mod` and `go.sum` update if a newer version is available. If `bridgev2` is a separate module, use the module path shown by `go list`/package docs and add that module instead.

- [ ] **Step 4: Record discovered bridgev2 APIs**

Create `docs/superpowers/plans/2026-06-23-bridgev2-api-notes.md` with this structure, replacing the bullets with exact discovered package paths and type names:

Generate `docs/superpowers/plans/2026-06-23-bridgev2-api-notes.md` from the discovery output. The file must contain these headings and concrete values copied from the local module source:

```markdown
# Bridgev2 API Notes

## Package Paths

- Bridge package: `maunium.net/go/mautrix/bridgev2`
- Event package: `COPY_FROM_DISCOVERY_OUTPUT`

## Key Types

- Bridge constructor: `COPY_FROM_DISCOVERY_OUTPUT`
- Network connector interface: `COPY_FROM_DISCOVERY_OUTPUT`
- Network API interface: `COPY_FROM_DISCOVERY_OUTPUT`
- Login process types: `COPY_FROM_DISCOVERY_OUTPUT`
- Remote event queue API: `COPY_FROM_DISCOVERY_OUTPUT`
- Matrix message handler API: `COPY_FROM_DISCOVERY_OUTPUT`

## Decisions

- Use the discovered API names in `internal/matrixbridge`.
- Keep bridge-independent logic in `internal/bridgecore` so API changes are isolated.
```

Expected: the file contains no `COPY_FROM_DISCOVERY_OUTPUT` markers after Step 4 is complete.

- [ ] **Step 5: Verify dependency state**

Run:

```bash
go test ./internal/connector ./internal/remoteapi
```

Expected: existing tests pass before new implementation starts.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum docs/superpowers/plans/2026-06-23-bridgev2-api-notes.md
git commit -m "chore: discover bridgev2 api"
```

---

### Task 1: Command Parser Core

**Files:**
- Create: `internal/bridgecore/commands.go`
- Create: `internal/bridgecore/commands_test.go`

- [ ] **Step 1: Write failing command parser tests**

Create `internal/bridgecore/commands_test.go`:

```go
package bridgecore

import "testing"

func TestParseCommandRequiresWXPrefix(t *testing.T) {
	cases := []struct {
		body string
		ok   bool
		name string
		args []string
	}{
		{body: "!wx help", ok: true, name: "help"},
		{body: "!wx login", ok: true, name: "login"},
		{body: "!wx accounts", ok: true, name: "accounts"},
		{body: "!wx logout wxid_123", ok: true, name: "logout", args: []string{"wxid_123"}},
		{body: "help", ok: false},
		{body: "!help", ok: false},
		{body: "!wx", ok: true, name: "help"},
		{body: "  !wx   help  ", ok: true, name: "help"},
	}

	for _, tc := range cases {
		cmd, ok := ParseCommand(tc.body)
		if ok != tc.ok {
			t.Fatalf("ParseCommand(%q) ok=%v, want %v", tc.body, ok, tc.ok)
		}
		if !tc.ok {
			continue
		}
		if cmd.Name != tc.name {
			t.Fatalf("ParseCommand(%q) name=%q, want %q", tc.body, cmd.Name, tc.name)
		}
		if len(cmd.Args) != len(tc.args) {
			t.Fatalf("ParseCommand(%q) args=%v, want %v", tc.body, cmd.Args, tc.args)
		}
		for i := range tc.args {
			if cmd.Args[i] != tc.args[i] {
				t.Fatalf("ParseCommand(%q) args=%v, want %v", tc.body, cmd.Args, tc.args)
			}
		}
	}
}

func TestHelpTextMentionsPhaseOneCommands(t *testing.T) {
	text := HelpText()
	for _, want := range []string{"!wx help", "!wx login", "!wx accounts", "!wx logout <account>"} {
		if !contains(text, want) {
			t.Fatalf("HelpText() missing %q in %q", want, text)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return sub == ""
}
```

- [ ] **Step 2: Run tests to verify failure**

Run:

```bash
go test ./internal/bridgecore -run 'TestParseCommand|TestHelpText' -count=1
```

Expected: fails because `internal/bridgecore` and parser symbols do not exist.

- [ ] **Step 3: Implement command parser**

Create `internal/bridgecore/commands.go`:

```go
package bridgecore

import "strings"

type Command struct {
	Name string
	Args []string
}

func ParseCommand(body string) (Command, bool) {
	fields := strings.Fields(strings.TrimSpace(body))
	if len(fields) == 0 || strings.ToLower(fields[0]) != "!wx" {
		return Command{}, false
	}
	if len(fields) == 1 {
		return Command{Name: "help"}, true
	}
	return Command{Name: strings.ToLower(fields[1]), Args: fields[2:]}, true
}

func HelpText() string {
	return strings.TrimSpace(`可用命令:
!wx help                 显示帮助
!wx login                登录一个新的微信账号
!wx accounts             列出当前 Matrix 用户已登录的微信账号
!wx logout <account>     退出指定微信账号

第一阶段说明:
- 多个微信账号会同时在线运行，不需要切换账号。
- 每个微信账号会自动创建独立 Matrix Space。
- 微信会话会自动创建 portal 房间，房间内回复会发回对应微信账号。
- Matrix E2EE 不在第一阶段自研支持。`)
}
```

- [ ] **Step 4: Run tests to verify pass**

Run:

```bash
go test ./internal/bridgecore -run 'TestParseCommand|TestHelpText' -count=1
```

Expected: tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/bridgecore/commands.go internal/bridgecore/commands_test.go
git commit -m "feat: add wx command parser"
```

---

### Task 2: Account Registry Core

**Files:**
- Create: `internal/bridgecore/account.go`
- Create: `internal/bridgecore/account_test.go`

- [ ] **Step 1: Write failing account registry tests**

Create `internal/bridgecore/account_test.go`:

```go
package bridgecore

import (
	"testing"

	"maunium.net/go/mautrix/id"
)

func TestAccountRegistryStoresMultipleAccountsPerOwner(t *testing.T) {
	registry := NewAccountRegistry()
	owner := id.UserID("@alice:example.com")

	registry.Upsert(Account{Owner: owner, AccountID: "wxid_a", DisplayName: "A", SpaceRoomID: "!spaceA:example.com"})
	registry.Upsert(Account{Owner: owner, AccountID: "wxid_b", DisplayName: "B", SpaceRoomID: "!spaceB:example.com"})

	accounts := registry.List(owner)
	if len(accounts) != 2 {
		t.Fatalf("got %d accounts, want 2", len(accounts))
	}
	if accounts[0].AccountID != "wxid_a" || accounts[1].AccountID != "wxid_b" {
		t.Fatalf("unexpected accounts: %#v", accounts)
	}
}

func TestAccountRegistryUpsertUpdatesExistingAccount(t *testing.T) {
	registry := NewAccountRegistry()
	owner := id.UserID("@alice:example.com")

	registry.Upsert(Account{Owner: owner, AccountID: "wxid_a", DisplayName: "Old", Session: []byte("old")})
	registry.Upsert(Account{Owner: owner, AccountID: "wxid_a", DisplayName: "New", Session: []byte("new")})

	accounts := registry.List(owner)
	if len(accounts) != 1 {
		t.Fatalf("got %d accounts, want 1", len(accounts))
	}
	if accounts[0].DisplayName != "New" || string(accounts[0].Session) != "new" {
		t.Fatalf("account not updated: %#v", accounts[0])
	}
}

func TestAccountRegistryRemoveIsOwnerScoped(t *testing.T) {
	registry := NewAccountRegistry()
	alice := id.UserID("@alice:example.com")
	bob := id.UserID("@bob:example.com")

	registry.Upsert(Account{Owner: alice, AccountID: "wxid_same", DisplayName: "Alice"})
	registry.Upsert(Account{Owner: bob, AccountID: "wxid_same", DisplayName: "Bob"})

	if !registry.Remove(alice, "wxid_same") {
		t.Fatal("expected remove to return true")
	}
	if len(registry.List(alice)) != 0 {
		t.Fatal("expected alice account removed")
	}
	if len(registry.List(bob)) != 1 {
		t.Fatal("expected bob account to remain")
	}
}
```

- [ ] **Step 2: Run tests to verify failure**

Run:

```bash
go test ./internal/bridgecore -run 'TestAccountRegistry' -count=1
```

Expected: fails because account registry symbols do not exist.

- [ ] **Step 3: Implement account registry**

Create `internal/bridgecore/account.go`:

```go
package bridgecore

import (
	"sort"
	"sync"
	"time"

	"maunium.net/go/mautrix/id"
)

type AccountID string

type Account struct {
	Owner       id.UserID
	AccountID   AccountID
	DisplayName string
	Session     []byte
	SpaceRoomID id.RoomID
	Connected   bool
	LastError   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type accountKey struct {
	Owner     id.UserID
	AccountID AccountID
}

type AccountRegistry struct {
	mu       sync.RWMutex
	accounts map[accountKey]Account
}

func NewAccountRegistry() *AccountRegistry {
	return &AccountRegistry{accounts: make(map[accountKey]Account)}
}

func (r *AccountRegistry) Upsert(account Account) Account {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := accountKey{Owner: account.Owner, AccountID: account.AccountID}
	now := time.Now().UTC()
	if existing, ok := r.accounts[key]; ok {
		account.CreatedAt = existing.CreatedAt
	} else if account.CreatedAt.IsZero() {
		account.CreatedAt = now
	}
	account.UpdatedAt = now
	r.accounts[key] = account
	return account
}

func (r *AccountRegistry) Get(owner id.UserID, accountID AccountID) (Account, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	account, ok := r.accounts[accountKey{Owner: owner, AccountID: accountID}]
	return account, ok
}

func (r *AccountRegistry) List(owner id.UserID) []Account {
	r.mu.RLock()
	defer r.mu.RUnlock()
	accounts := make([]Account, 0)
	for key, account := range r.accounts {
		if key.Owner == owner {
			accounts = append(accounts, account)
		}
	}
	sort.Slice(accounts, func(i, j int) bool {
		return accounts[i].AccountID < accounts[j].AccountID
	})
	return accounts
}

func (r *AccountRegistry) Remove(owner id.UserID, accountID AccountID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := accountKey{Owner: owner, AccountID: accountID}
	if _, ok := r.accounts[key]; !ok {
		return false
	}
	delete(r.accounts, key)
	return true
}
```

- [ ] **Step 4: Run tests to verify pass**

Run:

```bash
go test ./internal/bridgecore -run 'TestAccountRegistry' -count=1
```

Expected: tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/bridgecore/account.go internal/bridgecore/account_test.go
git commit -m "feat: add multi-account registry"
```

---

### Task 3: Portal Mapping Core

**Files:**
- Create: `internal/bridgecore/portal.go`
- Create: `internal/bridgecore/portal_test.go`

- [ ] **Step 1: Write failing portal mapping tests**

Create `internal/bridgecore/portal_test.go`:

```go
package bridgecore

import (
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"maunium.net/go/mautrix/id"
)

func TestPortalKeyIncludesOwnerAccountAndChat(t *testing.T) {
	manager := NewPortalRegistry()
	owner := id.UserID("@alice:example.com")

	roomA := id.RoomID("!roomA:example.com")
	roomB := id.RoomID("!roomB:example.com")
	manager.Upsert(Portal{Owner: owner, AccountID: "wxid_a", ChatID: "same_chat", RoomID: roomA})
	manager.Upsert(Portal{Owner: owner, AccountID: "wxid_b", ChatID: "same_chat", RoomID: roomB})

	portalA, ok := manager.GetByChat(owner, "wxid_a", remoteapi.ChatID("same_chat"))
	if !ok || portalA.RoomID != roomA {
		t.Fatalf("account A portal mismatch: %#v ok=%v", portalA, ok)
	}
	portalB, ok := manager.GetByChat(owner, "wxid_b", remoteapi.ChatID("same_chat"))
	if !ok || portalB.RoomID != roomB {
		t.Fatalf("account B portal mismatch: %#v ok=%v", portalB, ok)
	}
}

func TestPortalReverseLookupRoutesByRoom(t *testing.T) {
	manager := NewPortalRegistry()
	portal := Portal{
		Owner:     "@alice:example.com",
		AccountID: "wxid_a",
		ChatID:    "chat_a",
		RoomID:    "!portal:example.com",
	}
	manager.Upsert(portal)

	got, ok := manager.GetByRoom("!portal:example.com")
	if !ok {
		t.Fatal("expected reverse lookup")
	}
	if got.Owner != portal.Owner || got.AccountID != portal.AccountID || got.ChatID != portal.ChatID {
		t.Fatalf("got %#v, want %#v", got, portal)
	}
}
```

- [ ] **Step 2: Run tests to verify failure**

Run:

```bash
go test ./internal/bridgecore -run 'TestPortal' -count=1
```

Expected: fails because portal registry symbols do not exist.

- [ ] **Step 3: Implement portal registry**

Create `internal/bridgecore/portal.go`:

```go
package bridgecore

import (
	"sync"
	"time"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"maunium.net/go/mautrix/id"
)

type Portal struct {
	Owner       id.UserID
	AccountID   AccountID
	ChatID      remoteapi.ChatID
	RoomID      id.RoomID
	DisplayName string
	SpaceRoomID id.RoomID
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type portalKey struct {
	Owner     id.UserID
	AccountID AccountID
	ChatID    remoteapi.ChatID
}

type PortalRegistry struct {
	mu       sync.RWMutex
	byChat   map[portalKey]Portal
	byRoomID map[id.RoomID]Portal
}

func NewPortalRegistry() *PortalRegistry {
	return &PortalRegistry{
		byChat:   make(map[portalKey]Portal),
		byRoomID: make(map[id.RoomID]Portal),
	}
}

func (r *PortalRegistry) Upsert(portal Portal) Portal {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := portalKey{Owner: portal.Owner, AccountID: portal.AccountID, ChatID: portal.ChatID}
	now := time.Now().UTC()
	if existing, ok := r.byChat[key]; ok {
		portal.CreatedAt = existing.CreatedAt
	} else if portal.CreatedAt.IsZero() {
		portal.CreatedAt = now
	}
	portal.UpdatedAt = now
	r.byChat[key] = portal
	r.byRoomID[portal.RoomID] = portal
	return portal
}

func (r *PortalRegistry) GetByChat(owner id.UserID, accountID AccountID, chatID remoteapi.ChatID) (Portal, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	portal, ok := r.byChat[portalKey{Owner: owner, AccountID: accountID, ChatID: chatID}]
	return portal, ok
}

func (r *PortalRegistry) GetByRoom(roomID id.RoomID) (Portal, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	portal, ok := r.byRoomID[roomID]
	return portal, ok
}
```

- [ ] **Step 4: Run tests to verify pass**

Run:

```bash
go test ./internal/bridgecore -run 'TestPortal' -count=1
```

Expected: tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/bridgecore/portal.go internal/bridgecore/portal_test.go
git commit -m "feat: add portal mapping registry"
```

---

### Task 4: Persistence Interface And In-Memory Store

**Files:**
- Create: `internal/bridgecore/store.go`
- Create: `internal/bridgecore/store_test.go`

- [ ] **Step 1: Write failing store tests**

Create `internal/bridgecore/store_test.go`:

```go
package bridgecore

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/id"
)

func TestMemoryStorePersistsAccountsAndPortals(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	owner := id.UserID("@alice:example.com")

	account := Account{Owner: owner, AccountID: "wxid_a", DisplayName: "Alice WX", SpaceRoomID: "!space:example.com", Session: []byte("session")}
	if err := store.SaveAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	accounts, err := store.ListAccounts(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].AccountID != "wxid_a" || string(accounts[0].Session) != "session" {
		t.Fatalf("unexpected accounts: %#v", accounts)
	}

	portal := Portal{Owner: owner, AccountID: "wxid_a", ChatID: "chat_a", RoomID: "!portal:example.com", SpaceRoomID: "!space:example.com"}
	if err := store.SavePortal(ctx, portal); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetPortalByRoom(ctx, "!portal:example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got.AccountID != "wxid_a" || got.ChatID != "chat_a" {
		t.Fatalf("unexpected portal: %#v", got)
	}
}
```

- [ ] **Step 2: Run tests to verify failure**

Run:

```bash
go test ./internal/bridgecore -run 'TestMemoryStore' -count=1
```

Expected: fails because store symbols do not exist.

- [ ] **Step 3: Implement store interface and memory store**

Create `internal/bridgecore/store.go`:

```go
package bridgecore

import (
	"context"
	"fmt"

	"maunium.net/go/mautrix/id"
)

type Store interface {
	SaveAccount(ctx context.Context, account Account) error
	ListAccounts(ctx context.Context, owner id.UserID) ([]Account, error)
	DeleteAccount(ctx context.Context, owner id.UserID, accountID AccountID) error
	SavePortal(ctx context.Context, portal Portal) error
	GetPortalByRoom(ctx context.Context, roomID id.RoomID) (Portal, error)
	GetPortalByChat(ctx context.Context, owner id.UserID, accountID AccountID, chatID string) (Portal, error)
}

type MemoryStore struct {
	accounts *AccountRegistry
	portals  *PortalRegistry
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{accounts: NewAccountRegistry(), portals: NewPortalRegistry()}
}

func (s *MemoryStore) SaveAccount(_ context.Context, account Account) error {
	s.accounts.Upsert(account)
	return nil
}

func (s *MemoryStore) ListAccounts(_ context.Context, owner id.UserID) ([]Account, error) {
	return s.accounts.List(owner), nil
}

func (s *MemoryStore) DeleteAccount(_ context.Context, owner id.UserID, accountID AccountID) error {
	if !s.accounts.Remove(owner, accountID) {
		return fmt.Errorf("account %s for %s not found", accountID, owner)
	}
	return nil
}

func (s *MemoryStore) SavePortal(_ context.Context, portal Portal) error {
	s.portals.Upsert(portal)
	return nil
}

func (s *MemoryStore) GetPortalByRoom(_ context.Context, roomID id.RoomID) (Portal, error) {
	portal, ok := s.portals.GetByRoom(roomID)
	if !ok {
		return Portal{}, fmt.Errorf("portal room %s not found", roomID)
	}
	return portal, nil
}

func (s *MemoryStore) GetPortalByChat(_ context.Context, owner id.UserID, accountID AccountID, chatID string) (Portal, error) {
	portal, ok := s.portals.GetByChat(owner, accountID, remoteChatID(chatID))
	if !ok {
		return Portal{}, fmt.Errorf("portal chat %s/%s/%s not found", owner, accountID, chatID)
	}
	return portal, nil
}
```

Also add helper in `internal/bridgecore/portal.go`:

```go
func remoteChatID(chatID string) remoteapi.ChatID {
	return remoteapi.ChatID(chatID)
}
```

- [ ] **Step 4: Run tests to verify pass**

Run:

```bash
go test ./internal/bridgecore -run 'TestMemoryStore' -count=1
```

Expected: tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/bridgecore/store.go internal/bridgecore/store_test.go internal/bridgecore/portal.go
git commit -m "feat: add bridgecore store interface"
```

---

### Task 5: Router Core With Fake Matrix Sink

**Files:**
- Create: `internal/bridgecore/router.go`
- Create: `internal/bridgecore/router_test.go`

- [ ] **Step 1: Write failing inbound/outbound routing tests**

Create `internal/bridgecore/router_test.go`:

```go
package bridgecore

import (
	"context"
	"testing"
	"time"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"maunium.net/go/mautrix/id"
)

type fakeMatrixPortalSink struct {
	createdSpaces  []Account
	createdPortals []Portal
	sentMessages   []sentMatrixMessage
}

type sentMatrixMessage struct {
	RoomID id.RoomID
	Text   string
}

func (f *fakeMatrixPortalSink) EnsureAccountSpace(_ context.Context, account Account) (id.RoomID, error) {
	if account.SpaceRoomID == "" {
		account.SpaceRoomID = id.RoomID("!space_" + string(account.AccountID) + ":example.com")
	}
	f.createdSpaces = append(f.createdSpaces, account)
	return account.SpaceRoomID, nil
}

func (f *fakeMatrixPortalSink) EnsurePortalRoom(_ context.Context, portal Portal) (id.RoomID, error) {
	if portal.RoomID == "" {
		portal.RoomID = id.RoomID("!portal_" + string(portal.AccountID) + "_" + string(portal.ChatID) + ":example.com")
	}
	f.createdPortals = append(f.createdPortals, portal)
	return portal.RoomID, nil
}

func (f *fakeMatrixPortalSink) SendText(_ context.Context, roomID id.RoomID, text string) error {
	f.sentMessages = append(f.sentMessages, sentMatrixMessage{RoomID: roomID, Text: text})
	return nil
}

type fakeRemoteSender struct {
	sent []remoteapi.OutgoingMessage
}

func (f *fakeRemoteSender) SendMessage(_ context.Context, _ remoteapi.ChatID, msg remoteapi.OutgoingMessage) (remoteapi.MessageID, error) {
	f.sent = append(f.sent, msg)
	return "sent", nil
}

func TestRouterInboundCreatesAccountSpaceAndPortal(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	sink := &fakeMatrixPortalSink{}
	router := NewRouter(store, sink)
	account := Account{Owner: "@alice:example.com", AccountID: "wxid_a", DisplayName: "Alice WX"}

	err := router.HandleInboundMessage(ctx, account, &remoteapi.InboundMessage{
		ID: "m1", Chat: "chat_a", Sender: "friend", Type: remoteapi.MsgText, Text: "hello", Timestamp: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sink.createdSpaces) != 1 {
		t.Fatalf("createdSpaces=%d, want 1", len(sink.createdSpaces))
	}
	if len(sink.createdPortals) != 1 {
		t.Fatalf("createdPortals=%d, want 1", len(sink.createdPortals))
	}
	if len(sink.sentMessages) != 1 || sink.sentMessages[0].Text != "hello" {
		t.Fatalf("sentMessages=%#v", sink.sentMessages)
	}
}

func TestRouterOutboundUsesPortalAccountMapping(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	sink := &fakeMatrixPortalSink{}
	router := NewRouter(store, sink)
	portal := Portal{Owner: "@alice:example.com", AccountID: "wxid_a", ChatID: "chat_a", RoomID: "!portal:example.com"}
	if err := store.SavePortal(ctx, portal); err != nil {
		t.Fatal(err)
	}
	sender := &fakeRemoteSender{}

	err := router.HandlePortalText(ctx, "!portal:example.com", "reply", sender)
	if err != nil {
		t.Fatal(err)
	}
	if len(sender.sent) != 1 || sender.sent[0].Text != "reply" || sender.sent[0].Type != remoteapi.MsgText {
		t.Fatalf("sent=%#v", sender.sent)
	}
}
```

- [ ] **Step 2: Run tests to verify failure**

Run:

```bash
go test ./internal/bridgecore -run 'TestRouter' -count=1
```

Expected: fails because router symbols do not exist.

- [ ] **Step 3: Implement router core**

Create `internal/bridgecore/router.go`:

```go
package bridgecore

import (
	"context"
	"fmt"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"maunium.net/go/mautrix/id"
)

type MatrixPortalSink interface {
	EnsureAccountSpace(ctx context.Context, account Account) (id.RoomID, error)
	EnsurePortalRoom(ctx context.Context, portal Portal) (id.RoomID, error)
	SendText(ctx context.Context, roomID id.RoomID, text string) error
}

type RemoteSender interface {
	SendMessage(ctx context.Context, chat remoteapi.ChatID, msg remoteapi.OutgoingMessage) (remoteapi.MessageID, error)
}

type Router struct {
	store Store
	sink  MatrixPortalSink
}

func NewRouter(store Store, sink MatrixPortalSink) *Router {
	return &Router{store: store, sink: sink}
}

func (r *Router) HandleInboundMessage(ctx context.Context, account Account, message *remoteapi.InboundMessage) error {
	if message == nil {
		return fmt.Errorf("inbound message is nil")
	}
	spaceID, err := r.sink.EnsureAccountSpace(ctx, account)
	if err != nil {
		return err
	}
	account.SpaceRoomID = spaceID
	if err := r.store.SaveAccount(ctx, account); err != nil {
		return err
	}

	portal, err := r.store.GetPortalByChat(ctx, account.Owner, account.AccountID, string(message.Chat))
	if err != nil {
		portal = Portal{Owner: account.Owner, AccountID: account.AccountID, ChatID: message.Chat, SpaceRoomID: spaceID}
	}
	roomID, err := r.sink.EnsurePortalRoom(ctx, portal)
	if err != nil {
		return err
	}
	portal.RoomID = roomID
	portal.SpaceRoomID = spaceID
	if err := r.store.SavePortal(ctx, portal); err != nil {
		return err
	}
	return r.sink.SendText(ctx, roomID, message.Text)
}

func (r *Router) HandlePortalText(ctx context.Context, roomID id.RoomID, text string, sender RemoteSender) error {
	portal, err := r.store.GetPortalByRoom(ctx, roomID)
	if err != nil {
		return err
	}
	_, err = sender.SendMessage(ctx, portal.ChatID, remoteapi.OutgoingMessage{Type: remoteapi.MsgText, Text: text})
	return err
}
```

- [ ] **Step 4: Run tests to verify pass**

Run:

```bash
go test ./internal/bridgecore -run 'TestRouter' -count=1
```

Expected: tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/bridgecore/router.go internal/bridgecore/router_test.go
git commit -m "feat: add portal message router"
```

---

### Task 6: Runtime Manager For Concurrent Accounts

**Files:**
- Create: `internal/bridgecore/runtime.go`
- Create: `internal/bridgecore/runtime_test.go`

- [ ] **Step 1: Write failing runtime manager tests**

Create `internal/bridgecore/runtime_test.go`:

```go
package bridgecore

import (
	"context"
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

type fakeRuntimeAPI struct {
	events chan remoteapi.Event
}

func (f *fakeRuntimeAPI) Connect(context.Context) error { return nil }
func (f *fakeRuntimeAPI) Disconnect()                  {}
func (f *fakeRuntimeAPI) Events() <-chan remoteapi.Event { return f.events }
func (f *fakeRuntimeAPI) LoginFlows() []remoteapi.LoginFlow { return nil }
func (f *fakeRuntimeAPI) CreateLogin(context.Context, string) (remoteapi.LoginProcess, error) { return nil, nil }
func (f *fakeRuntimeAPI) Logout(context.Context) error { return nil }
func (f *fakeRuntimeAPI) IsLoggedIn(context.Context) (bool, error) { return true, nil }
func (f *fakeRuntimeAPI) Self(context.Context) (remoteapi.UserID, error) { return "wxid", nil }
func (f *fakeRuntimeAPI) DumpSession() ([]byte, error) { return []byte("session"), nil }
func (f *fakeRuntimeAPI) SendMessage(context.Context, remoteapi.ChatID, remoteapi.OutgoingMessage) (remoteapi.MessageID, error) { return "sent", nil }
func (f *fakeRuntimeAPI) RedactMessage(context.Context, remoteapi.ChatID, remoteapi.MessageID) error { return nil }
func (f *fakeRuntimeAPI) GetContact(context.Context, remoteapi.UserID) (remoteapi.Contact, error) { return remoteapi.Contact{}, nil }
func (f *fakeRuntimeAPI) ListContacts(context.Context) ([]remoteapi.Contact, error) { return nil, nil }
func (f *fakeRuntimeAPI) GetChat(context.Context, remoteapi.ChatID) (remoteapi.Chat, error) { return remoteapi.Chat{}, nil }

func TestRuntimeManagerStoresMultipleRunningAccounts(t *testing.T) {
	manager := NewRuntimeManager()
	owner := "@alice:example.com"
	apiA := &fakeRuntimeAPI{events: make(chan remoteapi.Event)}
	apiB := &fakeRuntimeAPI{events: make(chan remoteapi.Event)}

	manager.Upsert(Account{Owner: owner, AccountID: "wxid_a"}, apiA)
	manager.Upsert(Account{Owner: owner, AccountID: "wxid_b"}, apiB)

	if _, ok := manager.Get(owner, "wxid_a"); !ok {
		t.Fatal("missing runtime A")
	}
	if _, ok := manager.Get(owner, "wxid_b"); !ok {
		t.Fatal("missing runtime B")
	}
}
```

- [ ] **Step 2: Run tests to verify failure**

Run:

```bash
go test ./internal/bridgecore -run 'TestRuntimeManager' -count=1
```

Expected: fails because runtime manager symbols do not exist.

- [ ] **Step 3: Implement runtime manager**

Create `internal/bridgecore/runtime.go`:

```go
package bridgecore

import (
	"sync"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"maunium.net/go/mautrix/id"
)

type Runtime struct {
	Account Account
	API     remoteapi.PuppetAPI
}

type RuntimeManager struct {
	mu       sync.RWMutex
	runtimes map[accountKey]Runtime
}

func NewRuntimeManager() *RuntimeManager {
	return &RuntimeManager{runtimes: make(map[accountKey]Runtime)}
}

func (m *RuntimeManager) Upsert(account Account, api remoteapi.PuppetAPI) Runtime {
	m.mu.Lock()
	defer m.mu.Unlock()
	runtime := Runtime{Account: account, API: api}
	m.runtimes[accountKey{Owner: account.Owner, AccountID: account.AccountID}] = runtime
	return runtime
}

func (m *RuntimeManager) Get(owner id.UserID, accountID AccountID) (Runtime, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	runtime, ok := m.runtimes[accountKey{Owner: owner, AccountID: accountID}]
	return runtime, ok
}

func (m *RuntimeManager) Remove(owner id.UserID, accountID AccountID) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := accountKey{Owner: owner, AccountID: accountID}
	runtime, ok := m.runtimes[key]
	if !ok {
		return false
	}
	if runtime.API != nil {
		runtime.API.Disconnect()
	}
	delete(m.runtimes, key)
	return true
}
```

- [ ] **Step 4: Run tests to verify pass**

Run:

```bash
go test ./internal/bridgecore -run 'TestRuntimeManager' -count=1
```

Expected: tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/bridgecore/runtime.go internal/bridgecore/runtime_test.go
git commit -m "feat: add concurrent account runtimes"
```

---

### Task 7: Matrix Adapter Skeleton

**Files:**
- Create: `internal/matrixbridge/server.go`
- Create: `internal/matrixbridge/commands.go`
- Create: `internal/matrixbridge/portal_rooms.go`
- Create: `internal/matrixbridge/server_test.go`

- [ ] **Step 1: Write adapter tests against bridgecore seams**

Create `internal/matrixbridge/server_test.go`:

```go
package matrixbridge

import (
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/bridgecore"
)

func TestServerUsesWXCommandPrefix(t *testing.T) {
	cmd, ok := bridgecore.ParseCommand("!wx help")
	if !ok {
		t.Fatal("expected !wx command")
	}
	if cmd.Name != "help" {
		t.Fatalf("got command %q", cmd.Name)
	}
}
```

- [ ] **Step 2: Run tests to verify failure**

Run:

```bash
go test ./internal/matrixbridge -count=1
```

Expected: fails because package does not exist.

- [ ] **Step 3: Add Matrix bridge skeleton**

Create `internal/matrixbridge/server.go`:

```go
package matrixbridge

import (
	"github.com/leat/mautrix-go-wechat/internal/bridgecore"
	"github.com/leat/mautrix-go-wechat/internal/config"
	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

type Server struct {
	cfg      *config.Config
	apiCfg   remoteapi.Config
	store    bridgecore.Store
	runtimes *bridgecore.RuntimeManager
}

func New(cfg *config.Config, apiCfg remoteapi.Config, store bridgecore.Store) *Server {
	if store == nil {
		store = bridgecore.NewMemoryStore()
	}
	return &Server{cfg: cfg, apiCfg: apiCfg, store: store, runtimes: bridgecore.NewRuntimeManager()}
}
```

Create `internal/matrixbridge/commands.go`:

```go
package matrixbridge

import "github.com/leat/mautrix-go-wechat/internal/bridgecore"

func ParseCommand(body string) (bridgecore.Command, bool) {
	return bridgecore.ParseCommand(body)
}
```

Create `internal/matrixbridge/portal_rooms.go`:

```go
package matrixbridge

// Matrix room and Space creation is attached in the bridgev2 integration task.
// This file owns the Matrix-facing adapter boundary so bridgecore stays SDK-independent.
```

- [ ] **Step 4: Run tests to verify pass**

Run:

```bash
go test ./internal/matrixbridge -count=1
```

Expected: tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/matrixbridge/server.go internal/matrixbridge/commands.go internal/matrixbridge/portal_rooms.go internal/matrixbridge/server_test.go
git commit -m "feat: add matrix bridge adapter skeleton"
```

---

### Task 8: Login Command Flow

**Files:**
- Modify: `internal/matrixbridge/commands.go`
- Create: `internal/matrixbridge/commands_test.go`

- [ ] **Step 1: Write failing login/account command handler tests**

Create `internal/matrixbridge/commands_test.go`:

```go
package matrixbridge

import (
	"context"
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/bridgecore"
	"maunium.net/go/mautrix/id"
)

func TestAccountsResponseListsMultipleAccounts(t *testing.T) {
	ctx := context.Background()
	store := bridgecore.NewMemoryStore()
	server := &Server{store: store}
	owner := id.UserID("@alice:example.com")
	_ = store.SaveAccount(ctx, bridgecore.Account{Owner: owner, AccountID: "wxid_a", DisplayName: "A"})
	_ = store.SaveAccount(ctx, bridgecore.Account{Owner: owner, AccountID: "wxid_b", DisplayName: "B"})

	text, err := server.accountsText(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"wxid_a", "wxid_b", "A", "B"} {
		if !contains(text, want) {
			t.Fatalf("accountsText missing %q in %q", want, text)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return sub == ""
}
```

- [ ] **Step 2: Run tests to verify failure**

Run:

```bash
go test ./internal/matrixbridge -run 'TestAccountsResponse' -count=1
```

Expected: fails because `accountsText` does not exist.

- [ ] **Step 3: Implement accounts text helper**

Append to `internal/matrixbridge/commands.go`:

```go
import (
	"context"
	"fmt"
	"strings"

	"maunium.net/go/mautrix/id"
)

func (s *Server) accountsText(ctx context.Context, owner id.UserID) (string, error) {
	accounts, err := s.store.ListAccounts(ctx, owner)
	if err != nil {
		return "", err
	}
	if len(accounts) == 0 {
		return "当前没有已登录的微信账号。发送 `!wx login` 添加账号。", nil
	}
	var b strings.Builder
	b.WriteString("已登录微信账号:\n")
	for _, account := range accounts {
		name := account.DisplayName
		if name == "" {
			name = string(account.AccountID)
		}
		status := "disconnected"
		if account.Connected {
			status = "connected"
		}
		fmt.Fprintf(&b, "- %s (%s) space=%s status=%s\n", name, account.AccountID, account.SpaceRoomID, status)
	}
	return strings.TrimSpace(b.String()), nil
}
```

When applying this step, merge imports with the existing `bridgecore` import so `commands.go` has one import block.

- [ ] **Step 4: Run tests to verify pass**

Run:

```bash
go test ./internal/matrixbridge -run 'TestAccountsResponse' -count=1
```

Expected: tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/matrixbridge/commands.go internal/matrixbridge/commands_test.go
git commit -m "feat: add wx accounts response"
```

---

### Task 9: Matrix Room And Space Adapter

**Files:**
- Modify: `internal/matrixbridge/portal_rooms.go`
- Create: `internal/matrixbridge/portal_rooms_test.go`

- [ ] **Step 1: Write fake adapter tests**

Create `internal/matrixbridge/portal_rooms_test.go`:

```go
package matrixbridge

import (
	"context"
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/bridgecore"
)

func TestRoomNamerBuildsStableSpaceAndPortalNames(t *testing.T) {
	namer := RoomNamer{}
	space := namer.SpaceName(bridgecore.Account{AccountID: "wxid_a", DisplayName: "Alice WX"})
	if space != "WeChat - Alice WX" {
		t.Fatalf("space=%q", space)
	}
	portal := namer.PortalName(bridgecore.Portal{DisplayName: "Family"})
	if portal != "Family" {
		t.Fatalf("portal=%q", portal)
	}
}

func TestMemoryRoomCreatorCreatesIDs(t *testing.T) {
	ctx := context.Background()
	creator := NewMemoryRoomCreator("example.com")
	space, err := creator.EnsureAccountSpace(ctx, bridgecore.Account{AccountID: "wxid_a", DisplayName: "Alice WX"})
	if err != nil {
		t.Fatal(err)
	}
	portal, err := creator.EnsurePortalRoom(ctx, bridgecore.Portal{AccountID: "wxid_a", ChatID: "chat_a", SpaceRoomID: space})
	if err != nil {
		t.Fatal(err)
	}
	if space == "" || portal == "" || space == portal {
		t.Fatalf("space=%q portal=%q", space, portal)
	}
}
```

- [ ] **Step 2: Run tests to verify failure**

Run:

```bash
go test ./internal/matrixbridge -run 'TestRoom' -count=1
```

Expected: fails because room adapter symbols do not exist.

- [ ] **Step 3: Implement room naming and memory creator**

Replace `internal/matrixbridge/portal_rooms.go` with:

```go
package matrixbridge

import (
	"context"
	"strings"

	"github.com/leat/mautrix-go-wechat/internal/bridgecore"
	"maunium.net/go/mautrix/id"
)

type RoomNamer struct{}

func (RoomNamer) SpaceName(account bridgecore.Account) string {
	name := account.DisplayName
	if name == "" {
		name = string(account.AccountID)
	}
	return "WeChat - " + name
}

func (RoomNamer) PortalName(portal bridgecore.Portal) string {
	if portal.DisplayName != "" {
		return portal.DisplayName
	}
	return string(portal.ChatID)
}

type MemoryRoomCreator struct {
	domain string
}

func NewMemoryRoomCreator(domain string) *MemoryRoomCreator {
	return &MemoryRoomCreator{domain: domain}
}

func (m *MemoryRoomCreator) EnsureAccountSpace(_ context.Context, account bridgecore.Account) (id.RoomID, error) {
	return id.RoomID("!space_" + sanitize(string(account.AccountID)) + ":" + m.domain), nil
}

func (m *MemoryRoomCreator) EnsurePortalRoom(_ context.Context, portal bridgecore.Portal) (id.RoomID, error) {
	return id.RoomID("!portal_" + sanitize(string(portal.AccountID)) + "_" + sanitize(string(portal.ChatID)) + ":" + m.domain), nil
}

func (m *MemoryRoomCreator) SendText(_ context.Context, _ id.RoomID, _ string) error {
	return nil
}

func sanitize(value string) string {
	value = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, value)
	return strings.Trim(value, "_")
}
```

- [ ] **Step 4: Run tests to verify pass**

Run:

```bash
go test ./internal/matrixbridge -run 'TestRoom' -count=1
```

Expected: tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/matrixbridge/portal_rooms.go internal/matrixbridge/portal_rooms_test.go
git commit -m "feat: add matrix room naming adapter"
```

---

### Task 10: Attach Real Matrix/Bridgev2 API

**Files:**
- Modify: `internal/matrixbridge/server.go`
- Modify: `internal/matrixbridge/portal_rooms.go`
- Modify: `cmd/bridge/main.go`
- Modify: `docs/BRIDGEV2.md`

- [ ] **Step 1: Use discovered bridgev2 API notes**

Open `docs/superpowers/plans/2026-06-23-bridgev2-api-notes.md` and identify exact package paths and methods recorded in Task 0.

Expected: the notes contain concrete package paths and type names. If the notes still contain angle brackets, return to Task 0 Step 4 and complete discovery.

- [ ] **Step 2: Wire Matrix appservice event ingestion**

Modify `internal/matrixbridge/server.go` so it can start the Matrix bridge/appservice and feed incoming Matrix events to:

```go
cmd, ok := bridgecore.ParseCommand(messageBody)
if ok {
    // management command path
}
portal, err := s.store.GetPortalByRoom(ctx, roomID)
if err == nil {
    runtime, ok := s.runtimes.Get(portal.Owner, portal.AccountID)
    if ok {
        router := bridgecore.NewRouter(s.store, s.matrixSink)
        _ = router.HandlePortalText(ctx, roomID, messageBody, runtime.API)
    }
}
```

Use the exact event/body extraction APIs from the discovered Matrix package. Keep encrypted events non-fatal: ignore them or let bridgev2 default handling deliver decrypted events if available.

- [ ] **Step 3: Wire real room and Space creation**

Modify `internal/matrixbridge/portal_rooms.go` so the production adapter creates Matrix Spaces and portal rooms using the discovered Matrix client/bridge APIs.

Required behavior:

```text
EnsureAccountSpace(account) -> create or reuse one Space for account.AccountID
EnsurePortalRoom(portal) -> create or reuse one room for owner/account/chat and attach it to portal.SpaceRoomID
SendText(roomID, text) -> send m.room.message text event
```

Use `RoomNamer` for stable names.

- [ ] **Step 4: Wire login completion to account runtime**

In the management command handler for `!wx login`, drive the existing `remoteapi.LoginProcess`:

```text
CreateLogin(ctx, "qr")
Start(ctx)
render LoginDisplayAndWait QR/display data
Wait(ctx)
on LoginComplete:
  Self(ctx) or step.Self gives account ID
  DumpSession()
  SaveAccount(owner, account ID, session)
  create remoteapi client for that session
  Connect account runtime
  create account Space
```

If the account already exists for that Matrix owner, update the stored session and replace/restart the runtime.

- [ ] **Step 5: Switch main startup**

Modify `cmd/bridge/main.go` to construct and run `internal/matrixbridge.Server` instead of `internal/matrixas.Server` once the new server exposes the equivalent run method.

Keep registration generation unchanged.

- [ ] **Step 6: Run package tests**

Run:

```bash
go test ./internal/bridgecore ./internal/matrixbridge ./internal/connector
```

Expected: tests pass.

- [ ] **Step 7: Run full tests**

Run:

```bash
go test ./...
```

Expected: all tests pass.

- [ ] **Step 8: Commit**

```bash
git add internal/matrixbridge cmd/bridge/main.go docs/BRIDGEV2.md go.mod go.sum
git commit -m "feat: wire bridgev2 phase one matrix layer"
```

---

### Task 11: Documentation And Manual Validation

**Files:**
- Modify: `README.md`
- Modify: `docs/BRIDGEV2.md`
- Modify: `config.example.yaml` if new config keys were added

- [ ] **Step 1: Update README command docs**

Update the Matrix usage section to say:

```markdown
In Matrix, invite `@wechatbot:<your-domain>` to an unencrypted management room or DM, then use:

- `!wx help`
- `!wx login`
- `!wx accounts`
- `!wx logout <account>`

Each logged-in WeChat account runs concurrently. The bridge creates one Matrix Space per WeChat account and automatically creates portal rooms for inbound WeChat conversations.
```

- [ ] **Step 2: Update bridgev2 status docs**

Update `docs/BRIDGEV2.md` status to reflect Phase 1 completion:

```markdown
> Status: Phase 1 provides `!wx` management commands, concurrent multi-account runtimes, per-account Matrix Spaces, and automatic portal rooms for inbound WeChat messages. Matrix E2EE is not custom-implemented in this phase.
```

- [ ] **Step 3: Run docs-adjacent tests**

Run:

```bash
go test ./...
```

Expected: all tests pass after documentation/config changes.

- [ ] **Step 4: Manual validation checklist**

Run the bridge against a test homeserver and validate:

```text
1. Invite bot to an unencrypted management room.
2. Send !wx help and confirm help text appears.
3. Send !wx login and complete QR login for WeChat account A.
4. Send !wx login and complete QR login for WeChat account B.
5. Send !wx accounts and confirm both accounts are listed.
6. Confirm Space A and Space B exist.
7. Receive a WeChat message on account A and confirm a portal room appears under Space A.
8. Receive a WeChat message on account B and confirm a portal room appears under Space B.
9. Reply in account A portal and confirm it sends through account A.
10. Reply in account B portal and confirm it sends through account B.
11. Send an encrypted Matrix event to the bridge and confirm it does not crash.
```

- [ ] **Step 5: Commit**

```bash
git add README.md docs/BRIDGEV2.md config.example.yaml
git commit -m "docs: document bridgev2 phase one usage"
```

---

## Plan Self-Review

- Spec coverage: covered `!wx` commands, login, multi-account concurrent runtime, per-account Spaces, automatic portal mapping, inbound delivery, portal outbound routing, non-custom E2EE handling, testing, and docs.
- Dependency risk: Task 0 explicitly handles the current missing `go` command and unknown bridgev2 package availability.
- Type consistency: `AccountID`, `Account`, `Portal`, `Store`, `Router`, `RuntimeManager`, and `MatrixPortalSink` are introduced before later tasks use them.
- Scope control: rich media, custom E2EE, double puppeting, history backfill, and complete contact sync remain outside Phase 1.
