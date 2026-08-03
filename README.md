# mautrix-go-wechat

A Matrix appservice bridge built on the idea that **the bridge core never talks
to your chat API directly**. It talks to one small Go interface,
`remoteapi.PuppetAPI`. Your WeChat HTTP API (see `swagger.json`) is just *one*
implementation. To switch to QQ — or any other backend — you implement the same
interface and change one line of config.

The current Matrix layer is a mautrix appservice bridge MVP:
invite/chat with `@wechatbot:<domain>`, send `!wx login`, scan the QR, then inbound
WeChat chats automatically create Matrix portal rooms. In those rooms, virtual
users like `@wechat_<encoded-id>:<domain>` represent WeChat senders, and Matrix
text messages are sent back to WeChat. The login model follows
[mautrix-telegram](https://github.com/mautrix/telegram) / mautrix bridgev2's
state machine (`LoginFlow` → `LoginProcess` → `LoginStep`).

## Layering

```
mautrix-go bridgev2 (future)         generic Matrix framework
        │
        ▼
internal/connector                   glue: neutral events <-> Matrix sink
        │  depends ONLY on ▼
internal/remoteapi                   THE CONTRACT (interfaces + neutral types)
        ▲  implemented by
internal/wechat                      your swagger.json HTTP API
internal/qq  (future)                swap target — same interface
```

Rule: `internal/connector` imports `internal/remoteapi` only. It must **never**
import `internal/wechat`. Concrete backends register themselves via
`remoteapi.Register` from an `init()` and are selected at runtime by name.

## The contract: `remoteapi.PuppetAPI`

Everything the bridge needs from a backend, in protocol-neutral terms:

| Area      | Methods |
|-----------|---------|
| lifecycle | `Connect`, `Disconnect`, `Events` |
| auth      | `LoginFlows`, `CreateLogin`, `Logout`, `IsLoggedIn`, `Self`, `DumpSession` |
| outbound  | `SendMessage`, `RedactMessage` |
| directory | `GetContact`, `ListContacts`, `GetChat` |

Optional capabilities are type-asserted, so backends only implement what they
support:
- `remoteapi.MediaUploader` — for CDN-upload-then-send backends (WeChat).
- `remoteapi.Pinger` — health check (WeChat `HeartBeat`).

Inbound events (messages, recalls, receipts, connect/disconnect) all arrive on
the single `Events()` channel, hiding whether the backend *polls* (WeChat
`/Msg/QueryLocal`, default) or *pushes* to `network.receive.webhook_path`.

## Login (bridgev2 / mautrix-telegram style)

```go
flows := api.LoginFlows()                  // [{ID:"qr", Name:"Scan QR code"}]
proc, _ := api.CreateLogin(ctx, "qr")
step, _ := proc.Start(ctx)                 // -> display_and_wait {QR}
// render QR to the user...
step, _ = proc.Wait(ctx)                   // blocks until scanned+confirmed
// step.Type == complete, step.Self == wxid
```

Step types mirror bridgev2: `user_input`, `display_and_wait`, `complete`,
`error`. A code/SMS flow simply returns `user_input` steps and reads
`SubmitUserInput`.

## How to add a new backend (e.g. QQ)

1. `mkdir internal/qq`, create a `Client` implementing `remoteapi.PuppetAPI`.
2. Add a `Factory` whose `Name()` is `"qq"` and call `remoteapi.Register` in
   `init()`.
3. Side-effect import it in `cmd/bridge/main.go`:
   `_ "github.com/leat/mautrix-go-wechat/internal/qq"`.
4. Run with `-protocol qq`. Nothing in `internal/connector` changes.

If your WeChat *API itself* changes (new endpoints, renamed fields), you only
touch `internal/wechat/*` — the contract and the connector stay put.

## Run locally

Generate registration first, add it to Synapse, then run:

```bash
go run ./cmd/bridge -c config.yaml -r registration.yaml
```

In Matrix, invite `@wechatbot:<your-domain>` to an unencrypted room or start a
DM with encryption disabled, then send `!wx help`. Use `!wx login` to list WeChat login
protocols (iPad/Windows/Mac/etc), then e.g. `!wx login ipad` or `!wx login win`. Saved
accounts can be listed with `!wx accounts`. `!wx account <wxid>` calls the WeChat
`/Login/GetCacheInfo?wxid=...` API and, if cached login data exists, activates
that wxid as the current logged-in account. Locally saved accounts can also be
switched with `!wx use <wxid>`. `bridge.account_file` (`accounts.json`) is the
authoritative session store. `bridge.session_file` is a deprecated, read-only
startup fallback and is not updated after login; rolling back to an older bridge
version may therefore expose stale login state from that legacy file. After
login/activation, portal rooms are created for WeChat chats. Creation can be
automatic or manually confirmed via bot commands (`!wx portal-mode manual`,
`!wx pending`, `!wx create <chat>`), similar to mautrix-telegram.

## Importing WeChat built-in emoji

An authorized operator can import the fixed set of 109 WebP built-in emoji with
this exact command (replace `!wx` only if `bridge.command_prefix` differs):

```text
!wx stickers import wechat --ack-no-license
```

The sender must first pass the general `bridge.permissions` gate and must also
be either the exact configured `bridge.personal_sticker_user_id` or an exact
user/domain permission match whose level is `admin`. An explicit general denial
such as `relay` still denies import. Portal ownership is not authorization.

The source is fixed in code to
[`airinghost/wechat-emoji`](https://github.com/airinghost/wechat-emoji), commit
`2896eb55348041f5c5a66e4eccfb4882ab9cda0c`, path
`web.wechat.com/compressed-cwebp/lossless-z9`. The importer uses 4 workers,
commits batches of 16, allows 10 minutes total including login and account-Space
preflight, allows 30 seconds for each fetch/upload, and validates the pinned
HTTPS origin, MIME type, WebP signature,
and 256 KiB size limit.

The source repository has no explicit `LICENSE` or `COPYING` grant. Runtime
download, Matrix upload, and distribution through rooms/Spaces involve copying
and may involve redistribution. Attribution and `--ack-no-license`
acknowledgment do **not** grant rights or imply permission; the operator accepts
that risk.

Imported shortcode-to-MXC mappings are global. The bridge atomically stores the
canonical manifest at `bridge.wechat_builtin_emoji_file`, then reconciles stable
`m.room.image_pack[wechat.builtin]` and Element-compatible pack state into every
known account Space and portal. New Spaces and portals bootstrap from that local
canonical store; editable Matrix room state never becomes canonical. Existing
valid entries are skipped, failed/missing entries are retried, and every rerun
reconciles all targets without deleting existing entries. Uploaded media from a
failed persistence batch may be unreferenced and is reported only as a count.

Personal sticker-picker account data is best-effort and uses only the exact
configured `personal_sticker_user_id` plus `personal_sticker_access_token`. The
user must already be joined to a room containing the stable pack; invite-only or
inaccessible rooms are not selected. A joined account Space is preferred, then
a joined portal by lexicographic room ID. Missing credentials, membership, or an
account-data write failure does not undo the global pack import and is reported
for retry/reconciliation. Never post the personal token in commands or logs.

## Docker (mautrix-telegram-style deployment)

```bash
docker compose build
docker compose run --rm mautrix-wechat   # 1. writes ./data/config.yaml, exits
# edit ./data/config.yaml (homeserver, network.base_url, permissions)
docker compose run --rm mautrix-wechat   # 2. generates ./data/registration.yaml, exits
# add registration.yaml to your homeserver, then:
docker compose up -d mautrix-wechat
```

Full instructions: [`docs/DEPLOY.md`](docs/DEPLOY.md). Deployment files:
`Dockerfile`, `docker-compose.yml`, `docker-run.sh`, `config.example.yaml`.

## Layout

```
cmd/bridge/main.go            entrypoint, config load, -g registration, appservice run
internal/config/config.go     config.yaml + registration.yaml handling
internal/matrixas/            minimal Matrix appservice bot + login commands
internal/remoteapi/api.go     the contract (interfaces + neutral types)
internal/remoteapi/registry.go protocol registry + optional capabilities
internal/connector/           framework-agnostic event pump
internal/wechat/transport.go  HTTP envelope handling
internal/wechat/models.go     swagger.json request/response structs
internal/wechat/client.go     PuppetAPI implementation
internal/wechat/convert.go    raw WeChat msg -> neutral event
internal/wechat/login.go      QR login state machine
```
