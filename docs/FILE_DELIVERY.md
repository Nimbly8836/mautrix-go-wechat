# File delivery: one visible Matrix message

Incoming WeChat app-type-6 files are parsed without downloading bytes during the
webhook request. The callback can acknowledge promptly, while the existing
connector delivery goroutine resolves the file with its own lifecycle context.
A callback sender disconnecting after its five-second timeout no longer cancels
the file download.

## Placeholder and edits

The bridge creates a filename placeholder, edits it to show progress, then edits
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

There are at most four active fetch/upload operations per server. Work is bounded
by 120 seconds and the connector context. A terminal failure edit has a separate
short budget only while that connector remains live. Account/portal changes and
shutdown prevent publication into a replacement account or room.

Files are limited to 64 MiB. The existing 64-KiB backend download sections are
retained, with bounded HTTP response reads and exact chunk/aggregate length
validation. Self-sent files select the conversation peer before the download.
Private attachment identifiers remain in the backend fetch closure, not display
text or public message metadata.

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
unpaired filename and its file may prevent coalescing.

This does not automatically edit/redact pre-existing duplicate messages. Such a
repair requires explicitly identified events and separate approval. Deployment
must also preserve any independently installed production media hotfixes.
