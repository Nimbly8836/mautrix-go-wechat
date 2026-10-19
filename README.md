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

## Official-account names and invitations

Official-account rooms use `公众号 · <remark or nickname>` and an official-account
topic. Creation resolves the current account's contact cache before choosing the
room name and applying `official_account_mode`; a temporary directory failure
must not discard already-known names or bypass the `mute`/`reject` policy.

Incoming messages also repair old official-account rooms that still have a raw
ID or stale bridge-managed name once verified contact metadata is available.
Repair updates the **existing room**, not its membership: it does not create a
replacement room, resend an invitation, upload an avatar, or replay messages.
Human-renamed Matrix rooms are left unchanged. Name/topic writes are recorded
locally only after Matrix acknowledgements, so failures can be retried by a
later message rather than permanently pinning the raw ID.

Already-issued invitations may retain an old name snapshot in the client until
accepted; this repair does not withdraw or resend them to force a client refresh.
An apparent repeated invitation should be checked against its room ID and
membership-event history, separately from room metadata updates.

## Receiving WeChat locations

WeChat location messages (type 48), from callbacks or local-message polling, are
displayed as ordinary Matrix `m.text` messages, so clients do not need location-card
support. The bridge reads the original location XML rather than a rendered `[位置]`
summary:

- `body` contains the place name (`poiname`) and complete address (`label`), on
  separate lines when both are present and different. XML entities are decoded
  once; the XML itself is not used as the location description.
- When WeChat's `x` (latitude) and `y` (longitude) are present, finite, and in range,
  the text also includes a `坐标：<latitude>,<longitude>` line and an HTTPS
  OpenStreetMap link. Explicit zero values are valid; missing values are not
  treated as `(0, 0)`. Creating the link makes no map-service request.
- Missing/invalid coordinates retain the readable name/address without inventing
  coordinates or a map link. If XML supplies no readable description, a readable
  `ContentText` is retained before a generic push summary. Invalid or ambiguous XML
  is not exposed as the description.
- A matching group sender prefix is removed before XML parsing, even when sender
  metadata is already present. This does not replace the supplied sender identity.

The bridge does not shorten the address or convert coordinate reference systems.
The event does not use `m.location` or require a `geo_uri` field. The complete
location is readable as text even if the client cannot display maps; the map link
uses the source coordinates without assuming or converting their reference system.
This applies to newly bridged messages; existing malformed events are not edited
or resent. Matrix-to-WeChat location sending is unchanged.

## Receiving WeChat recalls and quoted replies

Group recalls and quoted replies are handled for both callbacks and local-message
polling. The original XML is used instead of a rendered `[引用消息]`/system summary.

- Quoted replies (`appmsg` type 57) use a native Matrix reply when the original
  message has a known Matrix event. Otherwise, ordinary text shows the quoted
  sender, quoted text and new reply. Quoted media uses readable labels such as
  `[图片]` or `[语音]`, not attachment XML, CDN credentials or download URLs.
- WeChat recall notifications (`sysmsg` type `revokemsg`) redact the known Matrix
  original and send a readable notice. The redaction uses the original bridge
  sender, including a bot fallback, rather than a user named inside the XML.
  Unknown targets only produce a notice. A forbidden, timed-out or unacknowledged
  Matrix redaction is explicitly reported as unconfirmed.
- References are scoped to the backend account, WeChat chat and Matrix room.
  Short `MsgID` values and long `NewMsgID`/`svrid` values have separate namespaces;
  IDs are not passed through floating point. Ambiguous/malformed recall XML or a
  conflicting session cannot select a deletion target.
- Related events wait for earlier deliveries in the same chat to complete, so an
  in-flight original can be acknowledged before a reply/recall looks it up.
  Ordinary messages remain concurrent; other chats and cancellation are not
  blocked by this wait.

References are now persisted in `<bridge.portal_file>.messages.jsonl`, including
successful Matrix-to-WeChat sends. They survive restarts and are not wiped at
5,000 entries. Keep the private journal on persistent storage and in backups.
Already-lost mappings from older versions cannot be reconstructed merely because
the original is still visible in a client; the bridge never guesses by matching
message bodies or bulk-rewrites existing events.

Matrix **text replies** to mapped messages are sent as native WeChat type-57
quotes, using the verified original server ID and sender. Reply fallback markup
is removed before sending. A missing mapping is reported in Matrix without
sending a plain-text imitation of the quote to WeChat. See [native replies and storage](docs/NATIVE_REPLIES.md) for
scope checks, failure behavior and limitations.

## Receiving merged chat records

WeChat type-19 merged forwards are expanded from their inner `recorditem`, not
just the outer title/summary or WeChat's unsupported-browser URL. A readable
transcript retains the item order, original display names and times. Downloaded
images, videos and files follow as native Matrix media linked to the summary,
not raw CDN or MXC links. Historical authors remain quoted labels, not newly
impersonated senders. Recalls also cover confirmed attachment events.

Oversized, expired or unavailable attachments have visible fallback labels;
long transcripts use a bounded preview plus a native text-file attachment.
See [merged-record behavior and limits](docs/MERGED_RECORDS.md).

## Receiving files

Ordinary WeChat files acknowledge callbacks before downloading. A single filename
placeholder is edited in place into a native Matrix file; failure also updates
that same message. A preceding, unambiguous same-sender filename caption can be
reused rather than producing a second visible entry. See
[file delivery, matching rules and limits](docs/FILE_DELIVERY.md).

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
