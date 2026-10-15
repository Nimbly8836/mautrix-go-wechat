# File delivery: one visible Matrix message

Incoming WeChat app-type-6 files are parsed without downloading bytes during the
webhook request. The callback can acknowledge promptly, while the existing
connector delivery goroutine resolves the file with its own lifecycle context.
A callback sender disconnecting after its five-second timeout no longer cancels
the file download.

## Placeholder and edits

WeChat app type 74 is an upload announcement, not an ordinary filename text.
It is shown immediately as `[文件] filename（等待文件就绪）` with its declared
size, without attempting a download. A later app-type-6 file can explicitly name
that announcement through `appattach/overwrite_newmsgid`. The bridge resolves
that exact source ID within the same account/chat/room/sender and edits the
original bridge-owned event, even after restart or a long upload delay. This
explicit association takes precedence over filename/time heuristics; invalid
explicit IDs are never substituted with a similarly named message.

If the original is a verified same-sender target but cannot safely be edited
(for example, it is human-authored in Matrix), a clearly marked file placeholder
is sent as a reply to it. A missing or mismatched target does not authorize an
edit or relation to an unrelated event. A replayed upload announcement does not
create another visible entry.

The bridge creates a marked `[文件]` placeholder when needed, edits it to show progress, then edits
that **same visible Matrix event** into a native `m.file` with `url`, `filename`
and MIME/size information. Failure is another edit of the same event, not a
separate notice or file card. The original Matrix sender is preserved, including
cases where a bot fallback originally sent the placeholder.

Matrix events are immutable: this uses `m.replace` and `m.new_content`; it does
not rewrite the original event JSON. The original GET-by-event-ID response may
still contain `m.text`, while clients render the latest replacement as a file.
The Matrix specification explicitly permits changing `msgtype` within
`m.room.message`: https://spec.matrix.org/latest/client-server-api/#event-replacements
Progress and completion edits use increasing timestamps so a progress edit
cannot win aggregation merely because both edits had the original timestamp.

If the backend first sends a separate plain-text filename, the bridge may reuse
that already-acknowledged event instead of creating another placeholder. Matching
is deliberately narrow:

- same account, conversation, Matrix room, remote sender and connector generation;
- only a bridge-authored, single-line filename, not a user-authored Matrix event,
  quote, forwarded bundle or solitaire message;
- the immediate preceding same-sender arrival, using connector arrival order,
  not asynchronous HTTP acknowledgement order;
- valid source timestamps at most 30 seconds apart;
- exact filename equality, or the observed parentheses/hyphen-to-underscore
  filename conversion; no substring or general fuzzy/history matching;
- one atomic claim only, with a bounded expiring candidate history.

If that evidence is unavailable or ambiguous, the file gets its own placeholder;
unrelated text is never edited or deleted to force a match.

## Budgets and cancellation

There are at most four active fetch/upload operations per server. Downloads have
up to **10 minutes**, and upload to Matrix has its own **2-minute** budget. The
whole operation, including waiting for a transfer slot, is bounded by **12
minutes** and the connector context. A terminal failure edit has a separate
short budget only while that connector remains live. Status messages distinguish
download failure/timeout from Matrix upload failure/timeout without exposing
credentials. Account/portal changes and shutdown prevent publication into a
replacement account or room.

Files are limited to **64 MiB**. When ordinary file XML supplies `cdnattachurl`
and `aeskey`, the bridge uses the passive `/Tools/CdnDownloadRecordItem` endpoint
with **DataType=8** and the real file size. In fk-zhang v6.0.9 this selects the
normal file channel followed by the large-file channel when the protocol says
it is needed. The actual response is `Data.Image` Base64, even for ZIP/PDF files.
A per-operation HTTP client uses the caller's download deadline instead of the
ordinary 70-second timeout. Response bounds include Base64 expansion; downloaded
length and any supplied original MD5 checksum must match. There is no forwarding
to filehelper or fallback to the legacy CGI after a CDN failure.

Files without CDN metadata retain 64-KiB legacy sections with exact chunk/total
validation. A nested `BaseResponse.ret != 0` is rejected even if the outer JSON
says `Success=true`; metadata is never mistaken for file bytes. Self-sent legacy
files select the conversation peer before the download. Private attachment
identifiers remain in the backend fetch closure, not display text or public
message metadata.

## References and restart

When caption and file are distinct backend messages, the caption keeps its
original reference and the first progress **edit event** anchors the file's
reference. Confirmed edits are linked through the existing `RelatedEventIDs`
journal field. A scoped, unique, same-author association resolves both source IDs
to the visible root; outgoing quotes prefer the file identity. Recalls include
all confirmed edits. Existing strong-ID collision guards and journal schema are
unchanged, so unrelated message IDs cannot be reassigned by text matching.

Confirmed terminal delivery is not republished on replay. A replay of a pending
file can reuse its persisted root. The fetch closure itself is not a durable job
queue: after a crash, work resumes only when the backend/history delivers that
file again. Candidate matching history is in memory, so a restart between an
unpaired plain-text filename and its file may prevent heuristic coalescing.
Explicit type-74 replacement identities use the durable journal instead and do
not depend on that short-lived cache.

This does not automatically edit/redact pre-existing duplicate messages. Such a
repair requires explicitly identified events and separate approval. Deployment
must also preserve any independently installed production media hotfixes.
