# Bridgev2 Phase 1 Design

## Goal

Migrate the Matrix side toward a mautrix-telegram-like bridgev2 model while keeping the existing `remoteapi.PuppetAPI` backend contract. Phase 1 focuses on management commands, multi-account login, automatic per-account Spaces, and automatic portal room creation for inbound WeChat messages.

Matrix E2EE is not implemented in Phase 1. Encrypted Matrix events are accepted only to the extent provided by bridgev2/default Matrix event handling; this phase does not add crypto store, decryption, key sharing, or encrypted sending.

## User-facing behavior

### Commands

Management commands use the `!wx` prefix and are primarily intended for bot DM / management rooms.

Required Phase 1 commands:

- `!wx help` — show available commands and phase limitations.
- `!wx login` — start a new WeChat QR login flow for the Matrix user.
- `!wx accounts` — list all WeChat accounts logged in by this Matrix user, including connection status and associated Space.
- `!wx logout <account>` — disconnect and remove one selected WeChat account.

There is no active-account switching. Multiple WeChat accounts logged in by the same Matrix user run at the same time.

### Multi-account model

A single Matrix user may log in multiple WeChat accounts concurrently. Each account owns its own backend client/session and event pump.

Messages are routed by portal room mapping, not by a selected current account:

- Inbound WeChat message from account A and chat X goes to account A's portal room for chat X.
- A Matrix message sent in that portal room is sent back through account A to chat X.
- If two WeChat accounts both have a chat with the same chat ID/name, they still get separate Matrix portal rooms because the mapping includes the account ID.

### Spaces and portals

After a WeChat account login completes, the bridge creates or reuses one Matrix Space for that account.

Example naming:

- Space: `WeChat - <display name or wxid>`
- Portal room: `<chat display name>` with account disambiguation in topic or canonical metadata when useful.

All portal rooms belonging to that WeChat account are attached to the account's Space.

When a new inbound WeChat message arrives:

1. Resolve the owning WeChat account.
2. Resolve or create the portal room for `(owner Matrix user, WeChat account ID, WeChat chat ID)`.
3. Attach the room to the account Space if not already attached.
4. Send the Matrix message event into that portal room.

## Architecture

### Keep `remoteapi`

`internal/remoteapi` remains the backend boundary. The WeChat implementation stays behind `remoteapi.PuppetAPI`.

Phase 1 should avoid moving WeChat-specific logic into the Matrix/bridgev2 layer. The bridgev2 adapter should depend on neutral concepts: account ID, chat ID, sender ID, message type, text/media.

### Add bridgev2-facing layer

Add a new Matrix bridge layer that owns:

- bridge startup and Matrix appservice integration through bridgev2 where practical;
- management command parsing for `!wx`;
- per-Matrix-user account registry;
- per-account connector lifecycle;
- portal room lookup/creation;
- Matrix-to-WeChat message routing by portal mapping.

The existing `internal/matrixas` management bot can remain temporarily during migration, but Phase 1's target behavior should live in the new bridgev2-oriented layer rather than extending management-room forwarding further.

### Account runtime

Introduce an account runtime concept:

```text
Matrix user
  └── WeChat account runtime(s)
        ├── remoteapi.PuppetAPI client
        ├── connector/event pump
        ├── session persistence
        ├── account Space room ID
        └── portal mappings for this account
```

Each runtime connects independently and consumes its own `Events()` channel.

### Portal mapping

Portal identity must include the Matrix owner and WeChat account:

```text
(owner_mxid, wechat_account_id, chat_id) -> room_id
room_id -> (owner_mxid, wechat_account_id, chat_id)
```

The reverse mapping is required for Matrix messages sent inside portal rooms.

## Data persistence

Phase 1 needs persistent state so bridge restarts preserve accounts, sessions, Spaces, and portal rooms.

Minimum logical tables/records:

### Accounts

- `owner_mxid`
- `account_id` / wxid
- display name
- serialized backend session blob from `DumpSession()`
- connection status / last error metadata
- `space_room_id`
- created/updated timestamps

### Portals

- `owner_mxid`
- `account_id`
- `chat_id`
- `room_id`
- chat display name
- last seen timestamp

### Management rooms

Optional but useful:

- `owner_mxid`
- `room_id`
- room type: management

The existing config already has a database URI. Phase 1 should reuse that storage path rather than introducing unrelated persistence.

## Message flow

### Matrix management command flow

```text
Matrix event
  -> command parser checks !wx prefix
  -> permission check
  -> command handler
  -> account registry / login flow / status response
```

Only prefixed commands are handled. Non-prefixed messages in management rooms are ignored.

### Login flow

```text
!wx login
  -> create remoteapi login process
  -> show QR/login step in Matrix
  -> wait for completion
  -> persist account session
  -> create account runtime
  -> create/reuse account Space
  -> connect account event pump
```

Multiple `!wx login` runs by the same Matrix user create multiple account records if they complete as different WeChat accounts.

If login completes for an already-known account, update its session and reconnect that account runtime rather than duplicating it.

### WeChat inbound flow

```text
remoteapi.EventMessage
  -> account runtime receives event
  -> portal manager resolves/creates room for account+chat
  -> portal manager attaches room to account Space
  -> convert remoteapi.InboundMessage to Matrix message content
  -> send event to portal room
```

### Matrix portal outbound flow

```text
Matrix message in portal room
  -> reverse lookup room_id
  -> identify owner/account/chat
  -> verify sender is allowed for owner/account
  -> convert Matrix message to remoteapi.OutgoingMessage
  -> account runtime API.SendMessage(chat, message)
```

## Permissions

Phase 1 keeps permissions conservative:

- Only authorized Matrix users may run `!wx login`.
- A portal room belongs to one Matrix owner and one WeChat account.
- Messages from unauthorized Matrix users in a portal room should not be bridged to WeChat.

The existing `bridge.permissions` config can remain the source of authorization for initial implementation.

## E2EE scope

Phase 1 does not implement Matrix E2EE.

Expected behavior:

- Non-encrypted management rooms and portal rooms are supported.
- Encrypted Matrix events are not explicitly decrypted by custom code.
- The bridge should not crash on encrypted events.
- `!wx help` and `!wx login` are only guaranteed in unencrypted management rooms unless bridgev2/default stack already provides decrypted events.

## Error handling

- Login failure: report error in management room, do not create account runtime.
- Duplicate login for same wxid: update session and restart that account runtime.
- Account connection failure: keep account record, mark disconnected, report in management room if known.
- Portal creation failure: log error and optionally report to management room; do not drop the account runtime.
- Matrix send failure: log and keep portal mapping for retry/future messages.
- WeChat send failure from portal: reply with a Matrix notice in the portal room.

## Testing plan

Unit tests:

- command prefix parser accepts `!wx help`, `!wx login`, rejects non-prefixed text;
- account registry supports multiple accounts for one Matrix user;
- duplicate account login updates instead of duplicating;
- portal key includes owner, account, and chat;
- reverse room lookup routes outbound messages to the correct account.

Integration-style tests with fakes:

- fake `remoteapi.PuppetAPI` emits an inbound message and portal manager creates/reuses a room;
- two accounts with the same chat ID create separate portal rooms;
- Matrix message in portal room calls `SendMessage` on the correct fake account.

Manual validation:

1. Start bridge.
2. Invite bot to unencrypted management room.
3. Run `!wx help`.
4. Run `!wx login` for account A.
5. Run `!wx login` for account B.
6. Confirm two Spaces are created.
7. Receive message on account A; confirm portal under Space A.
8. Receive message on account B; confirm portal under Space B.
9. Reply in each portal; confirm each reply uses the matching WeChat account.
10. Run `!wx accounts`; confirm both accounts are listed.

## Out of scope for Phase 1

- Custom Matrix E2EE crypto implementation.
- Historical message backfill.
- Rich media parity with mautrix-telegram beyond existing text/image primitives.
- Double puppeting.
- Full contact sync UI.
- Cross-account merged rooms.
- Account switching command (`!wx use`) because routing is portal/account based.
