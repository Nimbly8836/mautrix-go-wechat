# mautrix-go-wechat

A Matrix appservice bridge built on the idea that **the bridge core never talks
to your chat API directly**. It talks to one small Go interface,
`remoteapi.PuppetAPI`. Your WeChat HTTP API (see `swagger.json`) is just *one*
implementation. To switch to QQ — or any other backend — you implement the same
interface and change one line of config.

The current Matrix layer is a mautrix appservice bridge MVP:
invite/chat with `@wechatbot:<domain>`, send `login`, scan the QR, then inbound
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
`/Msg/Sync` long-poll) or *pushes* (webhook).

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
DM with encryption disabled, then send `help` / `login`. After login, portal
rooms are created automatically for WeChat chats.

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
