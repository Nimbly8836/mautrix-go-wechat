# Native replies and durable message references

## WeChat → Matrix

For an incoming WeChat app message of type 57, a known `refermsg/svrid` is resolved to the original Matrix event. The emitted content contains a real relation:

```json
{
  "msgtype": "m.text",
  "body": "引用测试",
  "m.relates_to": {
    "m.in_reply_to": { "event_id": "$original-event" }
  }
}
```

The lookup is scoped to the active WeChat account, conversation and Matrix room. A short `MsgID` cannot be mistaken for a server `NewMsgID`. Forwarded-history inner message IDs are not live message identities.

When the original mapping is unavailable, the bridge retains a readable quotation, but does **not** claim it is a native reply. It never guesses an event by matching message text. Messages bridged before this feature whose mappings have already been lost cannot automatically be recovered just because they remain visible in a Matrix client. Existing posted fallback events are not rewritten by this change.

## Matrix → WeChat

A normal **text reply** to a mapped Matrix event is sent through `/Msg/SendApp` with **outer request `Type: 49` and inner XML `<type>57</type>`**. The payload is a bare `<appmsg appid="" sdkver="0">`, not an extra `<msg>` wrapper. The API passes the outer type directly to the WeChat protocol; sending outer type 57 caused real `ret=-2` rejections. It uses the verified original server ID, sender, message kind and a bounded text snapshot. In groups, the group and original member are represented separately. XML text is escaped, including literal `&`, `<` and `]]>` sequences.

Matrix's plain/HTML reply fallback is removed before local command handling and XML detection, so the original quoted text is not duplicated. A thread fallback is not treated as an explicit quote; edits are not resent as new messages. Explicit `wx_xml` retains its existing semantics. This does not add an image/video-with-quote sending format: the native quote payload is a text response.

Successful regular outgoing text/media messages and native replies also retain their returned qualified IDs, allowing subsequent WeChat replies to target the actual Matrix-authored original. If only a short send-result ID is available, it is not upgraded to a server ID. For an unmapped target, the bridge reports the failure in Matrix and sends **nothing to WeChat**. It does not silently replace a native reply with an ordinary text imitation or send Matrix user IDs as quoted names.

The actual text-send acknowledgement is `Data.BaseResponse` plus `Data.Count=1` and `Data.List[0]`, including an item-level `Ret` and numeric `ClientMsgid`; app-send acknowledgements are flat. Both shapes are decoded explicitly, return codes are checked, and unsigned 64-bit IDs retain exact precision. Missing or ambiguous IDs are an uncertain result, never successful `msg_id=0`, and do not trigger automatic retries. These contract tests follow the deployed protocol service rather than a simplified flat-response stub.

Send confirmations are authoritative for self-message echoes: a response-first echo is not bridged again; if an echo was already delivered before the response, the scoped server ID is reassociated with the human-authored original. That early echo can remain visible, but it no longer steals the original's native-reply mapping. Other senders and unqualified IDs cannot perform this reassociation.

App-message quotes use a separate, bounded XML preview with the actual app subtype and at most 512 title characters. This covers quoting an existing quote or explicit `wx_xml` without saving Markdown fences, nested reference chains or attachment credentials as quote content. Readable fallback text remains separate from this protocol preview.

Native quote sends are attempted once. Ordinary sends retain the existing retry policy for explicit upstream logged-out rejections; an uncertain HTTP failure is not retried by this feature. Reference persistence errors after a successful send must not trigger resending.

## Persistent storage

References are stored in an append-only journal beside the configured portal file:

```text
<bridge.portal_file>.messages.jsonl
```

No new config setting or dependency is required. Keep this file on the same persistent volume as the other bridge state, and include it in backups. It contains account/chat/event identities, original senders, and text snapshots capped at 4096 UTF-8 bytes; treat it as private chat data. Files are created with mode `0600`.

The journal is loaded lazily, survives process restarts, and no longer clears all references after 5000 aliases. A complete malformed record causes an explicit error; only an incomplete final line from an interrupted append is removed before writing resumes. Append failures are latched until reload to avoid writing after a possibly incomplete record. Path collision checks prevent the derived journal path from overwriting known configuration/state files.

One bridge process must own a journal. History is intentionally retained without automatic compaction or retention deletion; journal size and in-memory indices grow with retained messages. There is no automatic import of old, lost mappings.

For a merged record expanded into a summary and native attachments, the reference keeps the summary as the reply target and confirmed attachment events as recall targets. These attachments do not receive invented WeChat IDs. A recall attempts all mapped events using the original bridge sender; an unconfirmed deletion is visibly reported. Matrix-authored human messages are not recalled by impersonating their authors.

## Verification boundary

Regression tests use local mock HTTP servers for the WeChat API and Matrix homeserver. They exercise XML generation/parsing, actual Matrix relations, restart recovery, scope isolation, strong/short IDs, and failure handling. They do not establish live WeChat protocol acceptance, actual CDN availability, or production deployment status.
