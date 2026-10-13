# Merged chat records in Matrix

A forwarded bundle has **one summary root (`m.room.message` / `m.text`) followed by native Matrix attachments**. The escaped HTML/plain-text summary preserves the ordered transcript, nested records, historical sender/time labels, and numbered image/video/file labels. Historical names, IDs and times are untrusted quoted data, never Matrix sender identities, event timestamps, or independent live WeChat messages.

Successfully downloaded attachments are uploaded once per asset within the delivery. Their uploaded URLs are reused in standard **`m.image`, `m.video`, or `m.file`** events with `url`, `info` (MIME and byte size), and `filename`. Each attachment replies to the summary root, is sent by the **same Matrix client as the root**, and has a numbered sender/time label identifying the original item. Attachments follow original depth-first traversal order; skipped or failed items do not reorder later attachments. Raster image bytes are checked before using `m.image`; SVG/HTML is not displayed as an inline image. Other unsupported native media types remain downloadable files.

The summary does **not** depend on raw `mxc://` HTML anchors or inline HTML media to open files. Native Matrix events let clients use their normal authenticated media/download handling. Backend/CDN URLs are never exposed. Downloads that are unavailable and uploads that fail are separately labelled next to the affected item, without exposing transport errors or credentials. If a native event send cannot be confirmed, a root-reply notice identifies that item and later attachments are still attempted. If Matrix also rejects the notice, the bridge logs the failure. No attachments are sent before the root event is confirmed.

## Large bundles

Serialized root content is capped at **48 KiB**, leaving room below Matrix's 64 KiB event limit for its envelope. If the transcript is too large, the bridge uploads the full UTF-8 plain-text transcript (including any readable outer reply fallback) as `forwarded-record.txt`, then sends it as an additional **native `m.file` reply to the root**, after the ordered media attachments.

The root contains a bounded, explicitly labelled preview, the full-transcript filename, and download/upload failure counts. The initial preview is at most 4096 UTF-8 bytes and shrinks further if JSON escaping requires it. The full transcript preserves all text, nested records, and attachment labels/statuses, including items outside the preview. It does not need MXC anchors: media is supplied separately as native events.

If uploading the transcript fails, the root explicitly reports that only partial text is available. Native attachments still proceed. If the transcript event send fails after upload, a root-reply failure notice is attempted just like for other attachments.

## Parsing and download bounds

The WeChat record parser/downloader applies these per-bundle bounds:

- **1 MiB** XML input;
- **100** total items;
- **4** nesting levels;
- **8** attachment downloads;
- **16 MiB** per downloaded attachment;
- **32 MiB** aggregate downloaded bytes;
- **30 seconds** for bundle downloads.

WeChat record data types **2 (image), 4 (video), and 8 (file)** support downloads. Text and unsupported item descriptions are retained; unavailable, unsupported, or over-budget attachments remain explicitly described rather than silently disappearing. These backend limits are distinct from Matrix's serialized-event size budget.

## References, retries and recall

The outer WeChat message maps to the **summary root plus confirmed related event IDs**. The root reference is saved immediately after root confirmation; after **each** confirmed native attachment or delivery-failure notice, the accumulated related IDs are persisted again. The transcript file is related in the same way. A restart can therefore recover the confirmed prefix for reply resolution and cascade recall. Recall redacts root and related events using the original bridge sender. A child is not indexed as a new independent WeChat message or assigned an invented quote target; inner `fromnewmsgid` values never authorize live references.

Child transaction IDs deterministically include outer account, chat, Matrix room, outer message ID, root ID and item identity. Retrying a child under the same root reuses its transaction ID; different items or scopes do not collide. This is child-send idempotency, not a claim of exactly-once upload or root delivery across independent full-bundle retries.

Reference-persistence errors are currently reported as ERROR logs by the reference API (which has no error return). A confirmed Matrix send does not guarantee that disk persistence succeeded if those errors occur. The bridge cannot recall an event whose send was accepted but never returned an event ID. Deployments should monitor these errors.

Uploads and rendering use delivery-local URL/status state. They do not overwrite the inbound record, its downloaded bytes, or backend URLs. Account provenance is checked before uploads or sends.
