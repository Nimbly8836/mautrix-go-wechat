# WeChat Text Emoji Mapping and Matrix Pack Import

## 中文审查摘要

**目标：** 将微信文本中的中英文内置表情标记统一映射为 Matrix 自定义 emoji shortcode，并支持从 `airinghost/wechat-emoji` 直接导入对应的 109 张图片到现有 WeChat account image pack。Matrix 发回微信时执行反向映射。

**方案：** 使用版本化 CSV 保存 109 项权威定义，包含序号、中文名称、英文别名、`wechat_*` canonical shortcode 和源文件名。新增显式确认命令 `stickers import wechat --ack-no-license`，从固定 Git commit 获取 WebP 图片，上传到 Matrix，并批量更新全局 `wechat.builtin` stable pack 及 Element 兼容 pack。入站文本优先生成 Matrix inline custom emoji；图片尚未导入时安全回退为纯文本 shortcode。

**影响范围：** 微信文本转换、Matrix 出站文本、全局 `wechat.builtin` image pack 管理、全局 MXC 清单的原子持久化、stickers 命令、Space rebuild/account store 持久化协调、相关测试和帮助文本。不改变微信 MsgType 47 动态表情处理、现有媒体 sticker 语义、通用 Matrix transport 或微信 API。

**主要风险：** 上游仓库没有 `LICENSE` 文件。本项目不会 vendor 图片，但运行时下载并上传到 Matrix 仍然会产生副本，也可能向 Space/portal 成员分发；显式确认和来源标注不授予任何权利。只有具备管理权限的用户提供 `--ack-no-license` 后才执行。GitHub 不可用或部分图片失败时允许安全重试。

**待确认事项：** 用户已确认采用固定版本运行时导入、双向映射、`wechat_` shortcode 前缀和 bridge account pack。图片源固定到 commit `2896eb55348041f5c5a66e4eccfb4882ab9cda0c`。

## Problem Statement

WeChat built-in text emojis appear as bracketed localized names. The same semantic emoji can arrive in Chinese or English form:

```text
[微笑]
[Smile]
[smile]
```

The bridge currently forwards these tokens as ordinary text. It separately supports media emoji messages (`MsgType 47`), Matrix `m.sticker`, stable `m.room.image_pack`, Element-compatible `im.ponies.room_emotes`, and image-pack account data, but it does not connect textual WeChat aliases to those packs.

The desired behavior is:

```text
WeChat -> Matrix:
[微笑], [Smile], [smile] -> :wechat_smile:

Matrix -> WeChat:
:wechat_smile: -> [微笑]
```

When the global bridge-managed `wechat.builtin` pack contains `wechat_smile`, Matrix messages should additionally include a spec-compatible formatted custom emoji image. Without an imported image, the plain shortcode remains readable and reversible.

## Source Repository Assessment

The selected source is:

```text
https://github.com/airinghost/wechat-emoji
```

The importer will pin:

```text
commit: 2896eb55348041f5c5a66e4eccfb4882ab9cda0c
path:   web.wechat.com/compressed-cwebp/lossless-z9
```

That directory contains exactly 109 WebP files, 539,338 bytes (approximately 527 KiB) total, named from:

```text
001_微笑.webp
...
077_爱心.webp
...
109_转圈.webp
```

The filenames match the 109 Chinese names supplied by the user. The repository README attributes its data to Emojipedia, WeChat Official Accounts Platform, and Web WeChat, but the repository contains no `LICENSE` or `COPYING` file.

The project will not vendor the images. Runtime import still creates copies on the Matrix homeserver and may make them available to Space or portal members; neither operator acknowledgment nor attribution grants rights. Import therefore requires an explicit no-license acknowledgment from an authorized operator, and pack metadata preserves the source repository and pinned commit.

## Considered Approaches

### 1. Pinned Runtime Import with Pack-Aware Rendering — Selected

Keep only metadata and aliases in this repository. On explicit command, fetch pinned raw GitHub assets, upload them to Matrix, and write the resulting MXC URLs to the bridge-managed account pack.

This produces actual inline custom emoji, remains reproducible, avoids silently tracking upstream changes, and avoids bundling third-party image files.

### 2. Vendor All Images

Copy the 109 WebP files into this repository or its container image.

This removes the runtime GitHub dependency, but increases distribution size and creates a stronger redistribution/licensing concern. It is rejected while the source has no explicit license.

### 3. Alias Mapping Without Images

Translate both languages to plain `:wechat_*:` text and rely on clients to interpret it.

This is simple, but Matrix clients generally require a resolved MXC image in `formatted_body` for reliable inline rendering. It remains the fallback behavior, not the primary design.

## Canonical Emoji Registry

A dedicated package-level registry will contain one entry per source file:

```go
type wechatTextEmoji struct {
    Index     int
    Chinese   string
    English   string
    Shortcode string
}
```

Examples:

```go
{Index: 1, Chinese: "微笑", English: "Smile", Shortcode: "wechat_smile"}
{Index: 52, Chinese: "失望", English: "Let Down", Shortcode: "wechat_let_down"}
{Index: 71, Chinese: "666", English: "Awesome", Shortcode: "wechat_awesome"}
{Index: 109, Chinese: "转圈", English: "Twirl", Shortcode: "wechat_twirl"}
```

The complete authoritative registry is versioned at `docs/superpowers/specs/2026-08-03-wechat-text-emoji-registry.csv`. It contains all 109 rows with `index,chinese,english,shortcode,filename`; implementation and exhaustive tests are derived from that manifest, not from prose examples.

Canonical shortcodes use a **new dedicated canonical normalizer**, not the existing general-purpose `normalizeStickerShortcode`. The new normalizer is used only to validate registry constants and is prefixed with `wechat_`:

- lowercase ASCII;
- spaces and punctuation become `_`;
- repeated or edge separators are removed;
- collisions are rejected by tests rather than silently overwritten.

Examples with punctuation:

```text
Bah！R    -> wechat_bah_r
Let Down  -> wechat_let_down
Yeah!     -> wechat_yeah
GoForIt   -> wechat_goforit
```

Registry validation tests will require:

1. exactly 109 entries;
2. unique indexes from 1 through 109;
3. unique Chinese names;
4. case-insensitively unique English aliases;
5. unique canonical shortcodes;
6. non-empty and normalized fields;
7. source filenames that match `<index>_<Chinese>.webp`;
8. no cross-language collision: a case-folded English alias may not equal any other entry's Chinese alias, and vice versa.

## Bidirectional Text Mapping

### WeChat to Matrix

The matcher scans bounded bracket tokens without interpreting arbitrary HTML or nested brackets. For each known token:

- Chinese names match exactly;
- English aliases match case-insensitively;
- surrounding brackets are required;
- unknown tokens remain byte-for-byte unchanged.

Examples:

```text
你好[微笑]             -> 你好:wechat_smile:
[Smile][smile][微笑]  -> :wechat_smile::wechat_smile::wechat_smile:
[unknown]             -> [unknown]
```

The replacement applies only to inbound `remoteapi.MsgText`. It does not rewrite XML payloads, filenames, captions, media emoji messages, or system notices.

### Matrix to WeChat

Before sending a portal text message to WeChat, known canonical shortcodes are replaced by their Chinese form:

```text
:wechat_smile: -> [微笑]
```

Every complete, known `:wechat_*:` token in `msg.Body` is replaced regardless of prose, URL, or code-like context, because the current outbound bridge retains only the plain body and cannot distinguish those contexts. Unknown shortcodes, ordinary colons, and non-text messages remain unchanged. This deterministic limitation is covered explicitly by tests.

The reverse mapping always emits Chinese because the selected behavior is deterministic and compatible with the user's actual WeChat input format.

## Matrix Inline Emoji Rendering

The plain Matrix `body` always contains canonical shortcodes. This guarantees readable fallback and enables deterministic reverse bridging.

WeChat built-in text emojis are identical across accounts, while the current portal store has no account ownership field. The imported pack is therefore explicitly **global**, not per-account. Its dedicated stable state key is `wechat.builtin`; mirroring the same registry to every portal is intentional and cannot leak account-specific media.

The canonical global source is a local atomic store, not any single account Space. A new bridge setting `bridge.wechat_builtin_emoji_file` defaults to `/data/wechat-builtin-emoji-pack.json`. It persists schema version, upstream repository/commit/path, and the 109 shortcode-to-MXC records. Matrix Space and portal state events are replicas that can be rebuilt from this store. On account activation, account Space creation, portal creation, repair, or account switch, the bridge bootstraps `wechat.builtin` from the same global store.

For inbound WeChat text, the bridge resolves imported entries from:

1. the portal's stable `m.room.image_pack[wechat.builtin]` state, which is authoritative for that room;
2. the portal's Element-compatible `im.ponies.room_emotes['']` state as compatibility fallback;
3. a selected account Space's `m.room.image_pack[wechat.builtin]` only when populating or repairing a portal.

Pack state is read once per message, not once per token. Resolution falls back **per shortcode**, not merely per event: a valid MXC in stable `wechat.builtin` wins; if that individual entry is missing, nil, empty, or has an invalid MXC, the compatibility event may supply that shortcode. A malformed entire event behaves as an empty source. If a shortcode resolves to a strictly parsed `mxc://` URL, the bridge sets:

```text
format = org.matrix.custom.html
formatted_body = escaped text plus
<img data-mx-emoticon src="mxc://..." alt=":wechat_smile:" title=":wechat_smile:" height="32">
```

The content retains `msgtype: m.text`. `format` is `org.matrix.custom.html`, and `formatted_body` is set only when at least one known token resolves. Ordinary text and attributes are HTML-escaped; newlines become `<br>`, tabs become four non-breaking spaces, and runs of ordinary spaces are encoded with non-breaking spaces so visible spacing is preserved. Only known registry shortcodes and URLs accepted by the Matrix MXC parser become image elements. If no image resolves, the shortcode stays plain text and no formatted body is emitted.

The implementation does not change existing media conversion behavior.

## Runtime Import Command

The existing stickers command gains:

```text
stickers import wechat --ack-no-license
stickers 导入 微信 --ack-no-license
```

The command accepts no arguments other than the exact acknowledgment flag. Without `--ack-no-license`, it prints the source and copying risk and refuses to download. Authorization never trusts the mutable persisted portal owner, because ordinary account/login/space flows can change it. The sender must first pass the existing general `allowed()` gate and must additionally be either (a) the exact immutable `bridge.personal_sticker_user_id` from configuration, or (b) matched by an exact user/domain permission level of `admin`. Import-specific authorization never bypasses an explicit general denial such as `relay`. If neither trusted principal is configured, import is disabled even when the general permissions map is empty. Denial explains which configuration is required. Authorization and argument parsing are tested, including attempts to change portal ownership before import.

The command calls `IsLoggedIn` and requires a currently valid login, a captured current account, and an available account Space. The account and initiating room are captured before background work starts. The assets are global; an account switch or logout after start does not retarget the operation, but server shutdown cancels it.

The command immediately reports:

- source repository and pinned commit;
- selected 109-file directory;
- that no explicit upstream license was found;
- import start status.

### Asset URLs

Each source URL is derived only from static registry data:

```text
https://raw.githubusercontent.com/airinghost/wechat-emoji/
2896eb55348041f5c5a66e4eccfb4882ab9cda0c/
web.wechat.com/compressed-cwebp/lossless-z9/
001_微笑.webp
```

The URL host, commit, path, index, and filename are not user-controlled. URL path segments are encoded through `net/url`; string concatenation must not create ambiguous or unescaped paths.

### Download and Upload Safety

The importer will:

- process exactly the 109 registry entries;
- use exactly four workers;
- use a 10-minute total timeout and a 30-second child timeout per fetch/upload;
- accept only HTTPS `raw.githubusercontent.com` URLs and at most three redirects;
- revalidate scheme and host on every redirect and on the final response URL;
- require HTTP 200;
- accept `image/webp` or `application/octet-stream` only when the RIFF/WEBP signature is valid;
- read at most 256 KiB + 1 and reject files larger than 256 KiB, empty files, or invalid signatures;
- construct filenames as `%03d_<Chinese>.webp` and escape path segments exactly once through `net/url`;
- upload through an injected Matrix media adapter;
- never log image bytes or access tokens.

The pinned Git commit, HTTPS origin validation, and WebP validation are the selected integrity controls. Per-file SHA-256 digests are not required in this version because the source is addressed by an immutable Git object; changing the commit requires code and registry review.

Existing `wechat.builtin` entries with the same canonical shortcode and a valid MXC URL are skipped for download, but every run still reconciles all state targets. This makes retries repair a missing compatibility event or portal mirror instead of incorrectly treating one successful state write as complete.

### Pack Update

All image-pack read/merge/write operations—manual additions, MsgType 47 auto-adds, repairs, Space rebuild, and built-in imports—share a dedicated pack mutation lock. Internal locked helpers avoid recursive locking. This prevents concurrent read-modify-write operations from losing entries.

Lock order is fixed and tested: `spaceMu` → `packMu` → individual persistent-store mutex. Code that needs only pack mutation never acquires `spaceMu` afterward. Space rebuild holds `spaceMu`, then `packMu`, until global-source snapshot, new-Space state write, account-data attempt, and persisted Space-ID switch finish. Import resolves/creates its target Space before acquiring `packMu`.

Successful uploads are committed in batches of 16. A batch entry counts as `uploaded` after Matrix media upload, as `persisted` only after the global local store is atomically replaced, and as `reconciled` only after the required stable Space/portal replicas are written. Local persistence uses a same-directory temporary file, file sync, atomic rename, and directory sync where supported; in-memory state changes only after persistence succeeds. A persistence failure leaves the previous in-memory/on-disk store unchanged and reports newly uploaded MXCs as unreferenced.

If Matrix state writing fails after local persistence, the run reports incomplete reconciliation and rerunning repairs all targets without redownloading. A timeout keeps previously persisted batches. Uploaded MXCs from an unpersisted batch are reported as unreferenced and may be orphaned.

Imported entries replace only matching `wechat_*` keys and preserve unrelated source-pack images. The dedicated stable portal event `m.room.image_pack[wechat.builtin]` is bridge-owned. The shared legacy `im.ponies.room_emotes['']` event is always read and merged under the same lock before writing, so portal-local images are preserved.

The final content keeps both usages:

```text
sticker
emoticon
```

Stable `m.room.image_pack[wechat.builtin]` metadata persists provenance rather than relying only on transient notices: the display name identifies the WeChat built-in set, `attribution` records the upstream repository, pinned commit, source path, and that no explicit license was identified, and `fi.mau.bridged_pack` records the fixed repository/commit URL.

The shared compatibility event `im.ponies.room_emotes['']` has one metadata object for all merged images. All compatibility writers (manual add, MsgType 47 auto-add, repair, portal mirror, and built-in reconciliation) therefore use a raw-JSON merge helper that changes only the `images` object and preserves every other top-level field and every unknown metadata/extension field semantically. JSON byte ordering is not promised. If the event is newly created, metadata remains empty. Import provenance is authoritative only in the dedicated stable event, preventing writers from oscillating shared metadata.

It is written as both stable `m.room.image_pack[wechat.builtin]` and raw-merged compatibility `im.ponies.room_emotes['']` state in every known account Space and portal. This ensures either stable or Element-compatible account-data can reference the same selected joined source room. New account Spaces and portals bootstrap from the global local store, so importing under account A then switching to B produces the same pack for B and all subsequently created portals. The built-in source is global by design, while account-specific MsgType 47 packs continue using their existing `wechat.<account>` keys.

Image-pack account-data registration targets only the configured `bridge.personal_sticker_user_id`, never whichever admin invoked import. It is attempted only through that user's configured personal access token and only after selecting a source room that both contains stable `wechat.builtin` and has verified `m.room.member` membership `join` for that user. The bridge prefers a joined account Space, then a joined portal in lexicographic room-ID order. Invite-only or inaccessible rooms do not qualify. If no joined source room, personal user/token, or account-data write is available, pack state import remains successful but the final result reports `account-data visibility: skipped/failed`; inline formatted messages and room packs still work. Registration is retried during import, account activation, Space creation, and repair.

Account Space rebuild reads the canonical local store while holding the prescribed locks, writes the stable pack to the new Space, attempts account-data registration, and atomically persists the new Space ID only after the stable state write succeeds. Account-data registration is best-effort and does not block the ID switch. Matrix room state is never promoted back into the canonical local store because room administrators can edit it. If the local store is absent, corrupt, unsupported, or fails provenance validation, rebuild proceeds without a built-in pack, reports that the operator must rerun the acknowledged import, and does not copy any stable or compatibility room state globally. After a valid-store rebuild succeeds it reconciles every portal.

The global store loader fails closed: it rejects unknown schema versions, wrong repository/commit/path provenance, unknown or duplicate shortcodes, invalid MXCs, and entries not present in the 109-row registry. It preserves the invalid file for diagnosis and never treats Matrix state as recovery authority.

The account store gains one error-returning atomic persistence path used by **every** mutation, including current-account changes, session updates, Space IDs, folder IDs, and future fields. The old direct `os.WriteFile` path is removed. Write/sync/rename completes before any in-memory mutation becomes visible; persistence failure keeps the complete old in-memory and on-disk store and returns an actionable error to every caller.

If some assets fail:

- successful entries are retained;
- failures are listed by index/name with bounded error text;
- the final notice reports imported, skipped, and failed counts;
- rerunning the command retries only missing entries.

No import run deletes existing entries.

## Concurrency and Command Lifecycle

Only one global WeChat built-in emoji import may run per Server. A second command receives an “already running” notice, even after an account switch.

The import executes outside the immediate command-dispatch critical path. Its 10-minute timeout derives from the long-lived `Run` context passed through `matrixLoop`, so server shutdown cancels outstanding work. It captures sender, initiating room, account, and Space before launch. Completion notices always return to the initiating room. Logout or account switch does not retarget an already-running global import.

The running flag is protected by a dedicated import mutex and cleared with `defer` on success, failure, timeout, panic-safe worker completion, and cancellation. Tests exercise duplicate-start rejection and cleanup after every exit class.

## Error Handling and Observability

Operator-facing notices include:

- source commit;
- total/imported/skipped/failed counts;
- at most the first 20 failed names;
- each displayed error truncated to 256 UTF-8 bytes without splitting a rune;
- uploaded-but-unreferenced counts;
- local-store persisted and stable/compatibility/portal reconciliation status;
- personal-user account-data visibility status;
- rerun guidance;
- upstream attribution and license notice.

Logs include account, source commit, index/name, operation stage, and error. Logs exclude access tokens, full response bodies, and image data.

Mapping itself never fails a message. An unavailable pack or importer error only degrades inbound display to plain shortcode text.

## Testing Strategy

### Registry and Mapping

- registry contains exactly 109 valid unique entries;
- all supplied Chinese/English pairs resolve to the expected canonical shortcode;
- English matching is case-insensitive;
- punctuation and spaces normalize deterministically;
- mixed text and adjacent tokens are preserved correctly;
- unknown bracket tokens and unknown Matrix shortcodes remain unchanged;
- every canonical shortcode reverses to the correct Chinese token.

### Importer

The importer is a focused service with injected interfaces for asset fetch, Matrix media upload, pack state load/save, notices, and time/contexts. Production adapters wrap the concrete appservice/mautrix clients. Mapping and HTML formatting remain pure functions. Tests use an injected HTTP server and fake adapters; they must not access GitHub or require a fake homeserver.

- URLs use the pinned commit and encoded filenames;
- all 109 entries are considered;
- valid WebP files upload and produce image-pack entries;
- existing entries are skipped;
- invalid status, content, MIME, size, and timeout failures are bounded;
- redirects to a hostile host, redirects to HTTP, more than three redirects, and a mismatched final URL are rejected by the production fetch policy;
- partial success writes only successful items and preserves unrelated entries;
- per-shortcode formatting fallback uses compatibility only for missing/invalid stable entries;
- compatibility raw-JSON image merges preserve all non-image and unknown extension fields across manual add, MsgType 47 auto-add, repair, portal mirror, and import;
- global local-store persistence is atomic and a failed write does not mutate memory;
- importing under account A, switching to B, and creating a new portal bootstraps the same global pack;
- concurrent import/manual mutation and persistence-failure tests prove the documented lock order and no lost updates;
- Space rebuild loads or deterministically recovers the global store before changing the persisted Space ID;
- rerun imports only missing entries;
- duplicate concurrent global imports are rejected;
- exact English and Chinese command forms work; missing acknowledgment, extra arguments, mutable-owner-only users, unauthorized users, and logged-out state are rejected;
- configured `personal_sticker_user_id` and exact user/domain `admin` permissions are accepted, while empty general permissions alone are not;
- stable-only partial writes are reconciled on retry without redownload;
- compatibility merge preserves unrelated portal entries;
- shutdown, timeout, account switch, and logout-after-start follow the documented captured-operation semantics;
- corrupt/unsupported/wrong-provenance global stores fail closed and Matrix state is never promoted to canonical local state;
- personal-user account data uses only the configured user/token and a source room with verified joined membership; invite-only/inaccessible rooms, missing token, and write failure are best-effort and clearly reported;
- every account-store mutation uses atomic persistence and injected failures leave all in-memory and on-disk fields unchanged;
- final counts, bounded failures, provenance, copying-risk acknowledgment, and license/source notices are correct.

### Matrix Formatting

- resolved aliases produce `msgtype: m.text`, `format: org.matrix.custom.html`, and escaped `formatted_body` image tags;
- `formatted_body` is omitted when no valid MXC resolves;
- the plain body remains canonical shortcode text;
- stable `wechat.builtin` portal state is preferred; malformed/missing state falls back to the compatibility pack;
- multiple aliases resolve with one read per pack source;
- malicious surrounding text is escaped, newlines become `<br>`, tabs become four non-breaking spaces, and repeated spaces remain visibly preserved;
- non-text messages and MsgType 47 media emoji behavior are unchanged.

### Integration

- inbound Chinese and English WeChat aliases bridge to the same Matrix shortcode;
- outbound canonical shortcode bridges to the Chinese token;
- existing `m.sticker`, image-pack compatibility, and stickers commands remain green;
- full tests, race tests for mapping/import state, vet, formatting, and diff checks pass.

## Non-Goals

- Redistributing or vendoring third-party image files.
- Automatically importing assets at startup.
- Preserving shortcode-like text in URLs or code contexts; every complete known token in a Matrix plain body is intentionally reversible.
- Translating arbitrary natural-language bracket text.
- Replacing WeChat MsgType 47 media emoji handling.
- Supporting arbitrary user-provided Git repositories in the importer.
- Sending Matrix custom emoji as WeChat media stickers; canonical text emoji are sent as `[中文名]`.
- Guaranteeing license rights that the upstream repository does not grant.

## Rollout and Recovery

The feature is inert until the operator runs `stickers import wechat`. Text alias mapping can safely ship before or after import because unresolved shortcodes remain readable.

If the source disappears, existing Matrix MXC URLs and pack state continue to work. A later source commit requires an explicit code review and constant update; the importer never follows the upstream default branch implicitly.

If imported entries must be removed, existing pack management can be extended separately. This design intentionally does not add destructive bulk removal.

## Technical Vocabulary / 技术词汇

- **canonical shortcode** — 规范短码
- **bidirectional mapping** — 双向映射
- **runtime import** — 运行时导入
- **pinned commit** — 固定提交版本
- **image pack** — 图片表情包
- **inline custom emoji** — 行内自定义表情
- **formatted body** — 格式化消息正文
- **bounded concurrency** — 有界并发
- **idempotent import** — 幂等导入
- **partial success** — 部分成功
- **source attribution** — 来源标注
- **redistribution** — 再分发
- **license notice** — 许可证提示
- **fallback rendering** — 回退渲染

## 中文审查清单

- [ ] 是否确认使用固定 commit，而不是跟随上游默认分支？
- [ ] 是否确认只在显式执行 `stickers import wechat` 时下载图片？
- [ ] 是否接受上游没有明确 LICENSE、运行时复制仍可能构成分发，并要求 `--ack-no-license`？
- [ ] 是否确认 109 项中英文别名统一为 `wechat_*` shortcode？
- [ ] 是否确认 Matrix 发回微信时统一转换为中文 `[名称]`？
- [ ] 是否确认内置表情使用全局 `wechat.builtin` pack，并有意镜像到所有账号的 portal？
- [ ] 是否确认导入权限只信任配置中的 `personal_sticker_user_id` 或显式 `admin`，不信任可变 portal owner？
- [ ] 是否确认全局 shortcode→MXC 清单原子持久化到 `wechat_builtin_emoji_file`，所有账号 Space/portal 都是可重建副本？
- [ ] 是否确认 Matrix room state 永远不能反向成为全局 canonical store，清单损坏时必须重新导入？
- [ ] 是否确认 Space rebuild 必须在固定锁顺序下先写新 pack、原子持久化，再切换内存 Space ID？
- [ ] 是否确认 account store 的所有写操作都改用同一个错误可见的原子持久化路径？
- [ ] 是否确认 image-pack account data 只写配置的 personal user，并且必须验证该用户已 join source room；失败仅影响可发现性而不回滚 room pack？
- [ ] 是否确认找不到 MXC 图片时回退为纯文本 shortcode？
- [ ] 是否确认 importer 只接受固定 GitHub 来源，不接受任意仓库 URL？
- [ ] 是否同意部分成功、可重试、保留现有非 `wechat_*` 项？
- [ ] 是否确认不改变 MsgType 47 媒体表情和现有 sticker 发送逻辑？
