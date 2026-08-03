# WeChat Text Emoji Mapping and Matrix Pack Import Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Execute tasks sequentially; every task must leave the repository compiling and green. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Map all 109 Chinese/English WeChat text emoji aliases bidirectionally, securely import the pinned image set into a global Matrix `wechat.builtin` pack, and render imported emoji inline without weakening account, storage, or pack consistency.

**Architecture:** A pure `internal/wechat/textemoji` package owns the generated registry and token mapping. Matrix-specific code owns atomic stores, raw compatibility-pack merges, global pack reconciliation, inline formatting, secure fetching, import orchestration, and authorization. A local atomic shortcode→MXC store is canonical; Matrix room state is always a rebuildable replica.

**Tech Stack:** Go 1.25+, `net/http`, `net/url`, `encoding/json`, `html`, `go:generate`, mautrix `m.room.image_pack`/`im.ponies.room_emotes`, existing appservice and store patterns.

**Design:** `docs/superpowers/specs/2026-08-03-wechat-text-emoji-import-design.md`

**Registry:** `docs/superpowers/specs/2026-08-03-wechat-text-emoji-registry.csv`

## 中文审查摘要

**目标：** 完成 109 项微信中英文文本表情映射、双向桥接、固定版本图片导入、全局 Matrix image pack 和 inline custom emoji 渲染。

**实施方式：** 先实现纯 registry/mapping，再修复原子持久化和 pack 并发基础，随后加入 canonical MXC store、全局 reconciliation、inline formatting、安全 fetcher、四 worker importer 和授权命令。每项均按 RED→GREEN→review 单独提交。

**影响范围：** 新增 `internal/wechat/textemoji`，修改 Matrix 文本转换、账号持久化、Space 生命周期、sticker pack 管理、配置、命令和测试。不会修改微信 API、MsgType 47 的语义、语音链路或通用 transport。

**主要风险：** 任务 3–6 会触及账号存储和 pack 状态一致性；必须保持固定锁顺序 `spaceMu → packMu → store mutex`。上游无明确 LICENSE，导入必须由受信任用户显式传入 `--ack-no-license`。

**关键验收：** canonical 数据只来自本地原子 store；Matrix state 不能反向恢复；所有账号/portal 使用全局 `wechat.builtin`；导入失败可重试且不丢现有图片；全量、race、vet 和 reviewer 均通过。

---

### Task 1: Generate and Validate the 109-Entry Registry

**Files:**
- Create: `internal/wechat/textemoji/registry.go`
- Create: `internal/wechat/textemoji/registry_generated.go`
- Create: `internal/wechat/textemoji/registry_test.go`
- Create: `internal/wechat/textemoji/cmd/generate/main.go`

- [ ] **Step 1: Write RED registry tests**

Tests must load the approved CSV and require exact equality with compiled entries:

```go
func TestEntriesMatchApprovedManifest(t *testing.T) {
	want := readManifest(t, "../../../docs/superpowers/specs/2026-08-03-wechat-text-emoji-registry.csv")
	got := Entries()
	if !slices.Equal(want, got) {
		t.Fatalf("registry mismatch: got %#v want %#v", got, want)
	}
}
```

Also test 109 entries, indexes 1–109, unique Chinese/English-folded/shortcode/filename fields, cross-language collision rejection, defensive-copy behavior, and these exact normalizations:

```go
{"Bah！R", "wechat_bah_r"}
{"Let Down", "wechat_let_down"}
{"Yeah!", "wechat_yeah"}
{"GoForIt", "wechat_goforit"}
```

Run:

```bash
go test ./internal/wechat/textemoji -run '^TestEntries' -count=1 -v
```

Expected: RED because `Entries`, generated data, and registry validation functions are undefined.

- [ ] **Step 2: Add types, validation, and generator**

`registry.go` defines:

```go
//go:generate go run ./cmd/generate -manifest ../../../docs/superpowers/specs/2026-08-03-wechat-text-emoji-registry.csv -output registry_generated.go

type Entry struct {
	Index     int
	Chinese   string
	English   string
	Shortcode string
	Filename  string
}

func Entries() []Entry {
	return append([]Entry(nil), generatedEntries...)
}

func CanonicalShortcode(alias string) string {
	alias = strings.ToLower(strings.TrimSpace(alias))
	var out strings.Builder
	separator := false
	for _, r := range alias {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if separator && out.Len() > 0 { out.WriteByte('_') }
			separator = false
			out.WriteRune(r)
		} else {
			separator = true
		}
	}
	normalized := strings.Trim(out.String(), "_")
	if normalized == "" { return "" }
	return "wechat_" + normalized
}
```

The generator is self-contained: it defines its own manifest row and normalization logic and must not import the parent `textemoji` package before `registry_generated.go` exists. It parses exactly five CSV columns, validates every invariant, requires `shortcode == CanonicalShortcode(english)`, emits deterministic Go source, and refuses malformed rows. It must not fetch the network.

- [ ] **Step 3: Generate, verify, and commit**

```bash
go generate ./internal/wechat/textemoji
gofmt -w internal/wechat/textemoji/*.go internal/wechat/textemoji/cmd/generate/*.go
go test ./internal/wechat/textemoji -count=1
go test ./... -count=1
git add internal/wechat/textemoji
git commit -m "feat: add canonical WeChat text emoji registry"
```

---

### Task 2: Pure Bidirectional Token Mapping

**Files:**
- Create: `internal/wechat/textemoji/mapping.go`
- Create: `internal/wechat/textemoji/mapping_test.go`

- [ ] **Step 1: Write RED mapping tests**

Require:

```text
[微笑], [Smile], [smile], [SMILE] -> :wechat_smile:
:wechat_smile: -> [微笑]
```

Exhaustively test all 109 aliases, adjacent/mixed tokens, malformed/nested/unclosed/overlong brackets, unknown tokens, URL/code-like contexts, and reverse coverage.

Run:

```bash
go test ./internal/wechat/textemoji -run 'Mapping|Shortcode' -count=1 -v
```

Expected: RED with undefined mapping functions.

- [ ] **Step 2: Implement bounded scanners**

Expose:

```go
func ToShortcodes(text string) string
func ToChineseTokens(text string) string
func ScanShortcodes(text string, yield func(start, end int, entry Entry))
```

Use byte offsets, a maximum token payload of 64 UTF-8 bytes, exact Chinese lookup, `strings.ToLower` English lookup, and complete `:wechat_*:` matching. Unknown input is copied unchanged. Do not use a regex that can span brackets or newlines.

- [ ] **Step 3: Verify and commit**

```bash
gofmt -w internal/wechat/textemoji/*.go internal/wechat/textemoji/cmd/generate/*.go
go test ./internal/wechat/textemoji -count=1
go test ./... -count=1
git add internal/wechat/textemoji
git commit -m "feat: map WeChat text emoji tokens"
```

---

### Task 3: Integrate Plain Inbound and Outbound Mapping

**Files:**
- Modify: `internal/matrixas/convert.go`
- Modify: `internal/matrixas/convert_test.go`
- Modify: `internal/matrixas/outbound.go`
- Modify: `internal/matrixas/outbound_test.go`
- Modify: `internal/matrixas/server.go`

- [ ] **Step 1: Add RED integration tests**

Tests must prove only `remoteapi.MsgText` is mapped inbound, portal text is mapped outbound after command detection, leading/trailing whitespace survives, commands do not leak, and MsgType 47/media captions/app/XML/system notices remain unchanged.

Run `go test ./internal/matrixas -run 'Emoji|PortalMessage|MatrixContent' -count=1 -v`; expect RED because current conversion forwards aliases unchanged and trims portal bodies.

- [ ] **Step 2: Integrate the pure mapper**

Inbound:

```go
case remoteapi.MsgText:
	return &event.MessageEventContent{MsgType: event.MsgText, Body: textemoji.ToShortcodes(body)}
```

Outbound portal flow must retain the original body:

```go
if strings.TrimSpace(body) == "" { return }
cmd, _, isCommand := parsePortalCommand(body, s.commandPrefix())
if !isCommand {
	wechatText := textemoji.ToChineseTokens(body)
	_, err := s.api.SendMessage(ctx, rec.ChatID, remoteapi.OutgoingMessage{Type: remoteapi.MsgText, Text: wechatText})
	// existing error/reporting path
}
```

Remove the pre-dispatch `strings.TrimSpace(msg.Body)` call from `handleMatrixEvent`; parsing functions may trim their own view.

- [ ] **Step 3: Verify and commit**

```bash
gofmt -w internal/matrixas/*.go
go test ./internal/matrixas -run 'Emoji|PortalMessage|MatrixContent' -count=1
go test ./... -count=1
git add internal/matrixas
git commit -m "feat: bridge WeChat text emoji aliases bidirectionally"
```

---

### Task 4: Atomic Persistence Primitive and Transactional Account Store

**Files:**
- Create: `internal/matrixas/atomic_file.go`
- Create: `internal/matrixas/atomic_file_test.go`
- Modify: `internal/matrixas/accounts.go`
- Create/Modify: `internal/matrixas/accounts_test.go`
- Modify all account-store callers in `internal/matrixas/{server,space,commands}.go`, `cmd/bridge/main.go`, and relevant tests.

- [ ] **Step 1: RED-test atomic file behavior**

Test temp-file cleanup, mode `0600`, successful reload, pre-rename failure, rename failure, and unsupported directory-sync handling.

Implementation contract:

```go
func writeFileAtomic(path string, data []byte, mode fs.FileMode) error
```

Sequence is create-in-target-dir → chmod → write → file sync → close → rename → directory sync. The atomic commit point is successful rename. Every pre-rename error is returned and leaves the destination unchanged. Directory sync is attempted for crash durability, but any post-rename sync error is logged as a durability warning and is not returned because the new destination is already committed and cannot be safely rolled back. Tests assert memory/disk consistency at this boundary.

- [ ] **Step 2: RED-test transactional account mutations**

Inject persistence failures into every mutation and assert memory and disk remain byte-for-byte unchanged:

```go
func (as *accountStore) put(savedAccount) error
func (as *accountStore) setSpace(remoteapi.UserID, string) error
func (as *accountStore) setSpaceFolder(remoteapi.UserID, string, string) error
func (as *accountStore) replaceSpace(remoteapi.UserID, string, map[string]string) error
func (as *accountStore) setCurrent(remoteapi.UserID) (bool, error)
```

Getters must return deep copies. Add startup regression coverage preventing stale `session_file` from overriding the current account store.

Run `go test ./internal/matrixas -run 'Atomic|AccountStore|StartupSession|Space' -count=1 -v`; expect RED on missing atomic primitive, log-only persistence, memory-before-disk mutation, and stale-session precedence.

- [ ] **Step 3: Implement clone→persist→publish**

Every mutation calls one shared `mutateLocked(func(*accountStoreData) error) error`: under `accountStore.mu`, deep-clone complete data, mutate the clone, marshal and atomically write it, then publish `as.data = clone`. Remove log-only `os.WriteFile` and all direct legacy `session_file` writes. Make `saveSession` error-returning and propagate through login completion, `useSavedAccount`, relogin, activation, account switching, Space/folder creation, and rebuild. `useSavedAccount` persists `Current` before replacing the live connector; on persistence failure the previous connector/account remains active. `cmd/bridge/main.go` treats the current account-store session as authoritative and reads legacy `session_file` only when no current account exists.

- [ ] **Step 4: Verify and commit**

```bash
gofmt -w internal/matrixas/*.go
go test ./internal/matrixas -run 'Atomic|AccountStore|StartupSession|Space' -count=1
go test ./... -count=1
git add internal/matrixas cmd/bridge
git commit -m "refactor: make account persistence atomic and error visible"
```

---

### Task 5: Raw Compatibility-Pack Merge and Shared Mutation Lock

**Files:**
- Create: `internal/matrixas/image_pack_merge.go`
- Create: `internal/matrixas/image_pack_merge_test.go`
- Modify: `internal/matrixas/stickers.go`
- Modify: `internal/matrixas/server.go`
- Modify: `internal/matrixas/space.go`
- Modify sticker/image-pack tests.

- [ ] **Step 1: RED-test raw JSON merge**

```go
type rawImagePackState interface {
	Load(context.Context, id.RoomID, event.Type, string) (json.RawMessage, error)
	Save(context.Context, id.RoomID, event.Type, string, json.RawMessage) error
}

func mergePackImages(raw json.RawMessage, replacements map[string]json.RawMessage) (json.RawMessage, error)
func parsePackImages(raw json.RawMessage) map[string]packImage
func validMXC(raw string) (id.ContentURIString, bool)
```

Tests require only `images` to change while preserving unknown top-level fields, metadata extensions, and unrelated image objects. Malformed state becomes an empty object and never panics.

- [ ] **Step 2: RED-test concurrent mutations**

Inject `rawImagePackState` through a production adapter over `BotIntent().StateEvent/SendStateEvent`. Use blocked fakes to interleave manual add, MsgType 47 auto-add, status/inline reads, repair, removal, reconciliation, and mirror. Assert no lost images, no deadlock, and unknown compatibility metadata survives every writer.

Run `go test ./internal/matrixas -run 'ImagePack|Sticker' -count=1 -v`; expect RED because current typed writes discard unknown fields and concurrent read-modify-write loses updates.

- [ ] **Step 3: Add `packMu` and locked helpers**

Add `packMu sync.Mutex` to `Server`. Route every image-pack read/modify/write—including status reads and the later inline resolver—through public wrappers that acquire `packMu` and non-recursive `...Locked` helpers used only under the lock. Required lock order is documented in code:

```text
spaceMu -> packMu -> accountStore.mu / builtin store mutex
```

Never hold `packMu` during external HTTP download or Matrix media upload.

- [ ] **Step 4: Verify race behavior and commit**

```bash
gofmt -w internal/matrixas/*.go
go test ./internal/matrixas -run 'ImagePack|Sticker' -count=1
go test -race ./internal/matrixas -run 'ImagePack|Sticker' -count=5
go test ./... -count=1
git add internal/matrixas
git commit -m "fix: serialize and preserve image pack mutations"
```

---

### Task 6: Canonical Global Built-in Emoji Store

**Files:**
- Create: `internal/matrixas/builtin_emoji_store.go`
- Create: `internal/matrixas/builtin_emoji_store_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `config.example.yaml`
- Modify: `internal/matrixas/server.go`

- [ ] **Step 1: RED-test schema and fail-closed loading**

Schema:

```go
type builtinEmojiStoreData struct {
	SchemaVersion int                  `json:"schema_version"`
	Source        builtinEmojiSource   `json:"source"`
	Images        []builtinEmojiRecord `json:"images"`
}

type builtinEmojiRecord struct {
	Shortcode string `json:"shortcode"`
	MXC       string `json:"mxc"`
}
```

Tests reject corrupt JSON, unsupported schema, wrong repository/commit/path, duplicate/unknown shortcode, invalid MXC, and records outside the registry. Matrix state must never be consulted for recovery.

- [ ] **Step 2: Implement atomic merged persistence**

Use the Task 4 primitive. Arrays are sorted by registry index. `PersistMerged` validates and persists before replacing memory. Partial manifests are valid. Corrupt files remain untouched and exposed through `LoadError()`. Add `ResetForAcknowledgedImport(now time.Time) error`: only the authorized acknowledged import path may recover by atomically renaming the invalid file to `<path>.corrupt-<UTC timestamp>`, then creating an empty valid store. If quarantine fails, import aborts. Matrix state is never consulted.

Run `go test ./internal/matrixas -run 'BuiltinEmojiStore' -count=1 -v`; expect RED because no canonical store, provenance validator, quarantine recovery, or config field exists.

Add:

```yaml
bridge:
  wechat_builtin_emoji_file: /data/wechat-builtin-emoji-pack.json
```

`Config.validate` assigns `/data/wechat-builtin-emoji-pack.json` when the key is omitted, before `Server.New` constructs the store. Tests load a legacy config with no key and require the default, plus a custom-path case. `Server.New` loads fail-closed status without preventing bridge startup.

- [ ] **Step 3: Verify and commit**

```bash
gofmt -w internal/matrixas/*.go internal/config/*.go
go test ./internal/matrixas -run 'BuiltinEmojiStore' -count=1
go test ./internal/config -count=1
go test ./... -count=1
git add internal/matrixas internal/config config.example.yaml
git commit -m "feat: persist global WeChat built-in emoji manifest"
```

---

### Task 7: Global Pack Reconciliation, Visibility, and Space Lifecycle

**Files:**
- Create: `internal/matrixas/builtin_emoji_pack.go`
- Create: `internal/matrixas/builtin_emoji_pack_test.go`
- Modify: `internal/matrixas/stickers.go`
- Modify: `internal/matrixas/space.go`
- Modify: `internal/matrixas/portal.go`
- Modify: `internal/matrixas/server.go`
- Modify lifecycle/account-data tests.

- [ ] **Step 1: RED-test stable/compatibility reconciliation**

Require `m.room.image_pack[wechat.builtin]` and raw-merged `im.ponies.room_emotes['']` in every saved account Space and portal, unrelated image preservation, deterministic sorting, and no deletion when canonical store is absent/invalid. Stable metadata assertions are exact: usages contain both `sticker` and `emoticon`; attribution contains repository, commit, source path, and the no-license-found notice; `fi.mau.bridged_pack` contains the fixed network/commit URL. Account-data reconciliation updates both stable and Element-compatible account-data types while preserving unrelated room sources.

- [ ] **Step 2: RED-test personal-user visibility**

Only configured `personal_sticker_user_id` plus configured token may receive account-data writes. Select one room containing both stable `wechat.builtin` and compatibility `im.ponies.room_emotes['']`, where membership is exactly `join`: account Spaces first, then portals, both sorted. Write stable and unstable account-data references to that same verified source. Invite-only/inaccessible/no-token/write-failure are best-effort statuses.

- [ ] **Step 3: RED-test Space/account switching**

Require account A import to bootstrap account B and future portals. Failed stable write or failed account persistence preserves the old Space ID. Account-data failure does not block a stable successful switch. Invalid canonical store never copies Matrix state globally.

Run `go test ./internal/matrixas -run 'BuiltinEmojiPack|AccountData|Space|Portal' -count=1 -v`; expect RED because `wechat.builtin`, global reconciliation, membership-gated account data, and transactional Space transitions do not exist.

- [ ] **Step 4: Implement reconciliation and lock order**

Expose internal functions:

```go
const builtinEmojiStateKey = "wechat.builtin"
func buildBuiltinEmojiPack(store builtinEmojiStoreData) json.RawMessage
func (s *Server) reconcileBuiltinEmojiPacksLocked(context.Context) reconciliationResult
func (s *Server) registerBuiltinPackAccountDataLocked(context.Context) accountDataVisibilityResult
```

Introduce `spaceTransitionResult` carrying Space, folders, portal count, built-in-pack status, account-data status, and warnings. Update `ensureAccountSpaceLocked`, `rebuildAccountSpace`, startup, activation/switch, repair commands, and portal creation to propagate or display it. Space transitions hold `spaceMu`, acquire `packMu`, write the stable pack, attempt account data, atomically persist Space IDs, then publish. Stable-pack failure blocks ID persistence; account-data failure is recorded but permits it. Bootstrap runs on startup, activation, switch, creation, repair, rebuild, and new portal creation.

- [ ] **Step 5: Verify and commit**

```bash
gofmt -w internal/matrixas/*.go
go test ./internal/matrixas -run 'BuiltinEmojiPack|AccountData|Space|Portal' -count=1
go test -race ./internal/matrixas -run 'BuiltinEmojiPack|Space' -count=3
go test ./... -count=1
git add internal/matrixas
git commit -m "feat: reconcile global built-in emoji packs"
```

---

### Task 8: Pack-Aware Inline Matrix Formatting

**Files:**
- Create: `internal/matrixas/text_emoji_format.go`
- Create: `internal/matrixas/text_emoji_format_test.go`
- Modify: `internal/matrixas/server.go`

- [ ] **Step 1: RED-test formatting and resolution**

Require stable-per-shortcode preference, compatibility fallback for individual missing/invalid entries, one read per source, strict MXC parsing, mixed resolved/unresolved tokens, malicious text escaping, and no formatted body when nothing resolves. Assert exact HTML for single, leading, trailing, and repeated spaces; lone `\r`; `\n`; CRLF; tabs; and mixed CRLF/tab/space runs.

Run `go test ./internal/matrixas -run 'InlineWechatEmoji|BuiltinEmojiResolution' -count=1 -v`; expect RED because no pack-aware formatter or resolver exists.

- [ ] **Step 2: Implement pure formatter and server resolver**

```go
func formatInlineWechatEmoji(body string, images map[string]id.ContentURIString) (string, bool)
func (s *Server) resolvePortalBuiltinEmoji(context.Context, id.RoomID) map[string]id.ContentURIString // wrapper acquires packMu
```

In `bridgeRemoteToPortal`, after pure conversion and before send, apply formatting only to `MsgText`. Keep plain body canonical. If used, set `MsgType=m.text`, `Format=event.FormatHTML`, and `FormattedBody`.

- [ ] **Step 3: Verify and commit**

```bash
gofmt -w internal/matrixas/*.go
go test ./internal/matrixas -run 'InlineWechatEmoji|BuiltinEmojiResolution' -count=1
go test ./... -count=1
git add internal/matrixas
git commit -m "feat: render imported WeChat emoji inline"
```

---

### Task 9: Secure Pinned GitHub Asset Fetcher

**Files:**
- Create: `internal/matrixas/builtin_emoji_fetch.go`
- Create: `internal/matrixas/builtin_emoji_fetch_test.go`

- [ ] **Step 1: RED-test every security boundary**

Test all 109 generated URLs and exact single escaping. Use an injected `RoundTripper` while retaining production HTTPS URLs. Cover stalled-response deadline, hostile-host redirect, HTTP downgrade, fourth redirect, userinfo, unexpected port, query/fragment, wrong final path, non-200, invalid MIME, empty/256KiB+1 body, invalid RIFF/WEBP, and valid `image/webp`/`application/octet-stream`.

Run `go test ./internal/matrixas -run 'BuiltinEmojiAsset|WebP|Redirect' -count=1 -v`; expect RED because URL construction and the fixed-origin fetch policy are undefined.

- [ ] **Step 2: Implement fixed-origin fetcher**

```go
func builtinEmojiAssetURL(textemoji.Entry) (*url.URL, error)
func newBuiltinEmojiHTTPClient(http.RoundTripper) *http.Client
func validateWebP([]byte) error
func (f *githubBuiltinEmojiFetcher) Fetch(context.Context, textemoji.Entry) ([]byte, error)
```

Use fixed HTTPS host, commit, path, max three redirects, per-request 30-second context, `io.LimitReader(..., 256<<10+1)`, and RIFF bytes 0–3 plus `WEBP` bytes 8–11.

- [ ] **Step 3: Verify and commit**

```bash
gofmt -w internal/matrixas/*.go
go test ./internal/matrixas -run 'BuiltinEmojiAsset|WebP|Redirect' -count=1
go test ./... -count=1
git add internal/matrixas
git commit -m "feat: securely fetch pinned WeChat emoji assets"
```

---

### Task 10: Four-Worker Batched Import Service

**Files:**
- Create: `internal/matrixas/builtin_emoji_import.go`
- Create: `internal/matrixas/builtin_emoji_import_test.go`

- [ ] **Step 1: RED-test importer state machine**

Inject fetch, upload, store, reconciliation, notice, and clock/context interfaces. Assert exactly four workers, all 109 considered, deterministic index order, batches `16...16,remainder`, existing valid entries skipped, every run reconciles, partial success, fetch/upload timeout, persistence failure, uploaded-unreferenced reporting, retry without redownload, and worker panic cleanup. Every worker uses `defer recover` to convert panic into an indexed bounded failure instead of terminating the process.

Run `go test ./internal/matrixas -run 'BuiltinEmojiImport' -count=1 -v`; expect RED because no ordered four-worker importer, batch committer, or panic boundary exists.

- [ ] **Step 2: Implement importer**

Fetcher/uploader run without `packMu`; every fetch and every Matrix upload gets a 30-second child context. Four workers send indexed outcomes to one coordinator, which buffers by registry index and advances only when the next index completes. Successful entries fill batches of 16; failed/skipped entries do not consume capacity; a final remainder is committed. Batch commit acquires `packMu`, rechecks canonical state, atomically persists, then reconciles. On cancellation or persistence failure, stop dispatch, cancel in-flight children, drain outcomes, record uploaded/unpersisted MXCs, and start no new work. Prior persisted batches survive timeout. Sort failures by registry index.

- [ ] **Step 3: Verify and commit**

```bash
gofmt -w internal/matrixas/*.go
go test ./internal/matrixas -run 'BuiltinEmojiImport' -count=1
go test -race ./internal/matrixas -run 'BuiltinEmojiImport' -count=5
go test ./... -count=1
git add internal/matrixas
git commit -m "feat: import built-in emoji with bounded workers"
```

---

### Task 11: Authorized Import Command and Lifecycle

**Files:**
- Create: `internal/matrixas/builtin_emoji_command.go`
- Create: `internal/matrixas/builtin_emoji_command_test.go`
- Modify: `internal/matrixas/commands.go`
- Modify: `internal/matrixas/stickers.go`
- Modify: `internal/matrixas/server.go`

- [ ] **Step 1: RED-test parsing and authorization**

Only accept:

```text
import wechat --ack-no-license
导入 微信 --ack-no-license
```

Missing/changed/extra arguments fail. Authorization first requires `allowed(sender)`, then exact configured personal user or exact user/domain `admin`. Explicit `relay` denial wins. Empty permissions and mutable owner do not authorize.

- [ ] **Step 2: RED-test lifecycle**

Require `IsLoggedIn`, capture account/Space/room/sender, one global import slot, 10-minute context derived from `Run`, shutdown cancellation, no retarget after account switch/logout, and flag cleanup after success/error/timeout/cancel/panic.

Run `go test ./internal/matrixas -run 'BuiltinEmojiCommand|ImportPermission|ImportLifecycle' -count=1 -v`; expect RED because parsing, immutable authorization, start notices, and lifecycle recovery do not exist.

- [ ] **Step 3: Implement command orchestration**

Add `importMu`, running flag, and `defer` cleanup. Never use `context.WithoutCancel`. Without acknowledgment, send source/copying-risk notice and perform no fetch. After valid authorization and acknowledgment, call `ResetForAcknowledgedImport` if the canonical store has a load error; quarantine failure aborts before any fetch. Before background work, immediately send repository, pinned commit, source path, missing-license warning, total 109, and captured account/Space. The outer goroutine uses `defer recover` to convert panic to a bounded failure notice and always clear the slot. Completion always returns to the initiating room.

- [ ] **Step 4: Verify and commit**

```bash
gofmt -w internal/matrixas/*.go
go test ./internal/matrixas -run 'BuiltinEmojiCommand|ImportPermission|ImportLifecycle' -count=1
go test -race ./internal/matrixas -run 'BuiltinEmojiCommand|ImportLifecycle' -count=5
go test ./... -count=1
git add internal/matrixas
git commit -m "feat: add authorized WeChat emoji import command"
```

---

### Task 12: Result Formatting, Help, Configuration, and Full Verification

**Files:**
- Create: `internal/matrixas/builtin_emoji_result.go`
- Create: `internal/matrixas/builtin_emoji_result_test.go`
- Modify: `internal/matrixas/builtin_emoji_fetch.go`
- Modify: `internal/matrixas/builtin_emoji_import.go`
- Modify: `internal/matrixas/builtin_emoji_command.go`
- Modify: `internal/matrixas/commands.go`
- Modify: `config.example.yaml`
- Modify: `README.md`
- Modify: `docs/DEPLOY.md`

- [ ] **Step 1: RED-test bounded result formatting**

```go
func truncateUTF8Bytes(string, int) string
func formatBuiltinEmojiImportResult(importResult) string
```

Report pinned source, total/imported/skipped/failed, first 20 failures, each error ≤256 UTF-8 bytes without rune splitting, uploaded-unreferenced count, persistence/reconciliation/account-data status, rerun guidance, attribution, and no-license acknowledgment. Ensure tokens, image bytes, and response bodies never appear.

Run `go test ./internal/matrixas -run 'BuiltinEmojiImportResult|TruncateUTF8|BuiltinEmojiLogging' -count=1 -v`; expect RED because result formatting and UTF-8 byte truncation do not exist.

- [ ] **Step 2: Implement bounded result formatting and verify GREEN**

Implement both functions from Step 1. Truncation follows UTF-8 rune boundaries while counting encoded bytes. Result formatting sorts by registry index, selects 20 failures, truncates each error to 256 bytes, and emits every required status/provenance field.

Refine fetch/import/command logging through shared safe failure codes (`fetch_failed`, `upload_failed`, `persist_failed`, `reconcile_failed`) and allowlisted fields: account, commit, registry index/name, stage, HTTP status, and safe validator reason. Arbitrary upstream error strings, response bodies, URLs with query/userinfo, Matrix tokens, and image bytes are never logged or included in notices. Logger-capture tests inject secrets into wrapped external errors and require their absence.

```bash
gofmt -w internal/matrixas/*.go
go test ./internal/matrixas -run 'BuiltinEmojiImportResult|TruncateUTF8|BuiltinEmojiLogging' -count=1
```

- [ ] **Step 3: Update help and example configuration**

Document exact command, authorization, fixed commit, copying risk, global pack behavior, and `wechat_builtin_emoji_file`. Do not claim the acknowledgment grants rights.

- [ ] **Step 4: Run complete verification**

```bash
gofmt -w internal/wechat/textemoji/*.go internal/wechat/textemoji/cmd/generate/*.go internal/matrixas/*.go internal/config/*.go
test -z "$(gofmt -l internal/wechat/textemoji/*.go internal/wechat/textemoji/cmd/generate/*.go internal/matrixas/*.go internal/config/*.go)"
go generate ./internal/wechat/textemoji
git diff --exit-code -- internal/wechat/textemoji/registry_generated.go
go test ./... -count=1
go test -race ./internal/matrixas ./internal/wechat/textemoji -count=3
go vet ./...
git diff --check
```

Expected: all commands exit 0. If a pre-existing third-party checkptr failure appears outside these packages/paths, reproduce it at the plan base and document it separately; do not hide it.

- [ ] **Step 5: Final audit and review**

Audit:

```text
[ ] 109 registry rows and generated constants match
[ ] both mapping directions are exhaustive
[ ] canonical store is local/atomic and fail-closed
[ ] Matrix state never becomes canonical
[ ] all account writes are atomic/error-visible
[ ] all pack mutations share lock order and preserve raw compatibility metadata
[ ] every account Space/portal bootstraps global wechat.builtin
[ ] inline HTML is safe and plain-body fallback remains
[ ] fetch policy enforces fixed origin/redirect/MIME/WebP/size/time limits
[ ] importer uses four workers and batches of 16
[ ] command requires acknowledgment and immutable admin/personal principal
[ ] MsgType 47 and unrelated media behavior remain unchanged
```

Dispatch final spec and code-quality reviewers. Fix every Critical/Important finding with a new RED/GREEN cycle.

- [ ] **Step 6: Commit final documentation/result changes**

```bash
git add config.example.yaml README.md docs/DEPLOY.md \
  internal/matrixas/builtin_emoji_result.go \
  internal/matrixas/builtin_emoji_result_test.go \
  internal/matrixas/builtin_emoji_fetch.go \
  internal/matrixas/builtin_emoji_import.go \
  internal/matrixas/builtin_emoji_command.go \
  internal/matrixas/commands.go
git commit -m "docs: document WeChat built-in emoji import"
```

Do not create an empty commit.

---

## Technical Vocabulary / 技术词汇

- **canonical registry** — 权威注册表
- **generated source** — 生成源码
- **transactional store** — 事务式存储
- **atomic rename** — 原子重命名
- **fail-closed loading** — 失败时拒绝使用
- **raw JSON merge** — 原始 JSON 合并
- **lock ordering** — 锁顺序
- **global replica** — 全局副本
- **per-shortcode fallback** — 按短码回退
- **origin validation** — 来源校验
- **bounded worker pool** — 有界 worker 池
- **reconciliation** — 状态协调
- **operator acknowledgment** — 操作者确认
- **visibility registration** — 可见性注册

## 中文审查清单

- [ ] 是否接受 12 个顺序任务和每任务独立提交/双重 review？
- [ ] 是否确认 registry 使用 CSV→生成 Go 源码，禁止运行时读取 docs？
- [ ] 是否确认所有 account store 写操作先原子持久化再更新内存？
- [ ] 是否确认兼容 pack 必须 raw JSON 合并并共享 `packMu`？
- [ ] 是否确认全局本地 store 是唯一 canonical 数据源？
- [ ] 是否确认所有账号 Space/portal 都镜像同一 `wechat.builtin`？
- [ ] 是否确认 personal account data 仅写配置用户且要求已 join？
- [ ] 是否确认 importer 固定 GitHub commit、4 workers、16 条一批？
- [ ] 是否确认导入命令要求 general permission + personal user/admin + `--ack-no-license`？
- [ ] 是否确认不改变 MsgType 47 和现有媒体语义？
