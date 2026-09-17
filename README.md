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

Sending Matrix voice messages to WeChat supports common client formats such as
OGG/Opus, MP3, and M4A through FFmpeg. Outbound audio is converted to 16 kHz
mono PCM and encoded as SILK at a 12 kbps target bitrate. Native SILK and exact
16 kHz mono 16-bit PCM WAV use a direct path. Install `ffmpeg` when running the bridge
outside Docker; the provided Docker image includes it.

## Keeping other bots' commands in Matrix

Configure command prefixes that should **not** be forwarded from Matrix portal
rooms to WeChat (or another configured backend):

```yaml
bridge:
    command_prefix: "!wx"
    ignored_command_prefixes:
        - "!da_bot"
        - "/tool"
```

With this example, `!da_bot action send-jielong-test` stays in Matrix for the
other bot to handle. The bridge silently skips forwarding it; it does not delete
the Matrix message or post a notice.

- The default is `[]`, preserving existing forwarding. Update the bridge binary
  or image and restart it with the changed config; there is no hot reload.
- Matching uses literal, **case-sensitive prefixes**, not regular expressions
  or whole command names: `!da_bot_extra` also matches `!da_bot`. Surrounding
  whitespace in configured prefixes and leading message whitespace are ignored;
  empty/whitespace-only entries are ignored, not treated as a match-all rule.
- For replies, only the new text is checked, excluding the Matrix reply fallback
  quote. Non-matching messages are forwarded unchanged by this filter.
- Use `["!"]` to skip all other `!` commands. The bridge's own commands, including
  `!wx info` and reply-based `!wx jl`, still run normally, as does a custom
  `bridge.command_prefix`. Existing permissions still apply.
- This only filters portal `m.text`/`m.notice` messages **from Matrix to the
  backend**. It does not filter WeChat-to-Matrix messages, including your own
  phone-sent messages, or media/file captions.
- This is not a bot-sender blocklist. Ordinary bot replies such as
  `收到消息：!da_bot ...` or `Action send-jielong-test executed.` still forward
  unless their own leading text matches a configured prefix.

## Participating in a WeChat solitaire by reply

Requires a protocol-service version that provides `POST /api/Msg/SendSolitaire`
and has local message storage (`savelocalmessage`) enabled. The bridge uses
`/Msg/SendSolitaire` relative to `network.base_url`, which normally ends in
`/api`. Updating only the bridge does not install the new upstream endpoint.

In the target portal room, **reply to the specific solitaire message** and send:

```text
!wx jl 张三，1份
```

`jl` is case-insensitive. `!wx 接龙 ...` and `!wx solitaire ...` remain compatible
aliases; a custom `bridge.command_prefix` applies too.
The entry may span multiple lines and is not passed through emoji substitution.
An ordinary reply without the command remains ordinary text. This command only
**participates in an existing version**; it does not initiate a new chain or
select the latest message automatically.

Newly bridged appmsg type 53 messages keep their original title/entries and add
just one hint: `Reply: !wx jl <text>` (using the configured prefix). They carry
an opaque source reference in the Matrix event, preserving both WeChat IDs
without float/int64 precision loss. Reply resolution works after
a bridge restart, fetches the original event in the current room, and verifies
its bridge sender, account and chat before calling the protocol API. Old Matrix
messages without this metadata, plain copied chain text, recalled messages,
edits and implicit thread fallback replies cannot be used as targets.

After a confirmed send, the bridge displays the **complete updated chain from
the upstream response's `Data.Xml`**, followed by the same short reply hint,
instead of a separate success/message-ID notice. It uses the newly returned
message IDs, so you can reply to this new event for the next entry. The bridge
does not reconstruct the chain or numbering from the old message. If the send
is confirmed but the response lacks valid chain XML, it shows a short warning
to check WeChat instead; missing content is not a reason to send again.

Send responses and callbacks/history with overlapping, typed message-ID aliases
share one Matrix display, scoped to account, chat and room. This is a bounded
in-memory cache of 1,024 versions, not permanent history deduplication. A failed
or unacknowledged Matrix display is not marked complete; a later delivery reuses
the same Matrix transaction ID. That does **not** resend the WeChat action.
Different versions are never merged by comparing their text.

Other Matrix bots can use the same command if their sender IDs are allowed by
`bridge.permissions`. They must send `m.text` with a real
`m.relates_to.m.in_reply_to.event_id` pointing to the bridged chain in the same
room. A quoted body alone, `m.notice`, or the other bot's own command prefix does
not invoke this action.

The protocol service resolves the exact saved message. This also applies when
replying to a newly displayed send result: its message must first be saved by
the protocol service. A response or callback may arrive before that persistence
finishes; a not-found response is an error, not a reason
to guess a different version or send client-supplied XML. Using an older source
may create a divergent version: the bridge does not merge concurrent entries.

The bridge checks both `Success` and `Code`, plus the nested send result. It does
not automatically retry this action or follow HTTP redirects. Only the protocol
service handles its documented, explicit `-13` authentication recovery. If the
result is unknown, inspect WeChat before sending a new command.

Before any send, the bridge persists a per-command claim in
`<bridge.portal_file>.solitaire-claims/` (normally next to the portal JSON file).
These small files contain the Matrix room/event IDs, not the entry text, and
prevent duplicate command delivery from sending again, including after restart.
Keep and back up this directory with bridge data; do not delete it casually.
A storage failure prevents sending. A crash after the claim but before the HTTP
call can leave an unsent command claimed: this is deliberately at-most-once,
not guaranteed delivery, and requires checking WeChat before a new command.

Tests use simulated HTTP services; real phone rendering and subsequent solitaire
interactions still require validation after the new upstream version is deployed.

## Sending custom WeChat XML

In a WeChat portal room, send a **single, standalone** fenced code block with
language `wx_xml` (not `xml`). For example, paste this entire message:

````text
```wx_xml
<msg>
  <appmsg appid="" sdkver="0">
    <title>Example link</title>
    <des>A user-constructed link card</des>
    <type>5</type>
    <url>https://example.com/</url>
  </appmsg>
</msg>
```
````

The bridge sends the XML through `/Msg/SendApp`, using the current account and
this portal's recipient. It reads the API `Type` from the direct `appmsg/type`
element; this is the app **subtype** (e.g. `5` for a link), not the outer WeChat
message type `49`. Both `<msg><appmsg>...</appmsg></msg>` and a bare
`<appmsg>...</appmsg>` are accepted. The payload is not reserialized or subjected
to emoji substitution. Matrix clients that render the fence as HTML
`<pre><code class="language-wx_xml">` are supported too.

- Existing `bridge.permissions` apply; this is not a management-room command.
- Use one code block with no surrounding prose, other blocks, or reply quotes.
  Backtick or tilde fences of at least three characters are accepted, with an
  optional space before `wx_xml`. The closing fence must use the same character
  and be at least as long as the opening fence. Markers inside another code
  block are examples and remain text.
- XML must be well-formed, at most 1 MiB, with exactly one top-level appmsg
  (optionally wrapped in msg) and one direct positive 32-bit integer type.
  DTDs/external entities are not supported.
- Raw XML without the `wx_xml` marker, and ordinary `xml` code blocks, remain
  text. Recognized but invalid `wx_xml` blocks produce an error notice instead
  of falling back to text.
- This sends **App XML**, not arbitrary image/voice/location protocol XML. File
  IDs, CDN keys, signatures, thumbnails and other subtype-specific fields must
  be supplied correctly by the user; the bridge does not upload or generate
  them. Acceptance and rendering depend on the WeChat server and clients.
- XML is submitted from your logged-in account. Only send payloads you trust;
  local validation checks syntax, not whether a card's contents are trustworthy.

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
