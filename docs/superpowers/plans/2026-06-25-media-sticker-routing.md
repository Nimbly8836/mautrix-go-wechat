# Media and Sticker Routing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Send normal Matrix images to WeChat as images, keep stickers routed as GIF emoji, and add inbound WeChat stickers to the account Space sticker pack.

**Architecture:** Add small pure helpers in `internal/matrixas/outbound.go` and `internal/matrixas/convert.go` so routing rules are testable without a live Matrix homeserver. Keep existing media download/upload side effects in `Server` methods.

**Tech Stack:** Go, mautrix event types, existing `remoteapi` message model, existing `m.room.image_pack` helper.

---

### Task 1: Split outbound image vs sticker routing

**Files:**
- Modify: `internal/matrixas/outbound.go`
- Test: `internal/matrixas/outbound_test.go`

- [ ] Write tests for `portalMediaToRemoteMessage` covering Matrix image -> `remoteapi.MsgImage`, non-GIF sticker rejection, and GIF sticker -> `remoteapi.MsgEmoji`.
- [ ] Run: `go test ./internal/matrixas -run 'TestPortalMediaToRemoteMessage'`
- [ ] Implement `portalMediaToRemoteMessage(data, mimeType, fileName, body, msgType, eventType)`.
- [ ] Update `handlePortalMediaMessage` to call the helper and send its returned `remoteapi.OutgoingMessage`.
- [ ] Run: `go test ./internal/matrixas -run 'TestPortalMediaToRemoteMessage'`.

### Task 2: Add inbound WeChat sticker to Space pack

**Files:**
- Modify: `internal/matrixas/convert.go`
- Modify: `internal/matrixas/server.go`
- Test: `internal/matrixas/convert_test.go`

- [ ] Write a pure helper test for converting an inbound WeChat emoji plus uploaded MXC URL into `wechatStickerImage`.
- [ ] Run: `go test ./internal/matrixas -run 'TestWechatStickerImageFromRemoteEmoji'`.
- [ ] Implement `wechatStickerImageFromRemoteEmoji(m, mxcURL)`.
- [ ] In `bridgeRemoteToPortal`, after successful media upload for `remoteapi.MsgEmoji`, call `ensureAccountSpace` and `addStickerToAccountSpace` with the helper result.
- [ ] Log failures without blocking message delivery.
- [ ] Run: `go test ./internal/matrixas -run 'TestWechatStickerImageFromRemoteEmoji|TestBuildWechatStickerPackContent'`.

### Task 3: Download inbound WeChat image media

**Files:**
- Modify: `internal/wechat/convert.go`
- Modify: `internal/wechat/models.go`
- Modify: `internal/wechat/transport.go`
- Test: `internal/wechat/webhook_payload_test.go`

- [ ] Write a failing webhook test where image XML with `aeskey` and `cdnmidimgurl` calls `/Tools/CdnDownloadImage` and emits `InboundMessage.Media.Data`.
- [ ] Run: `go test ./internal/wechat -run 'TestHandleWebhookImageXMLDownloadsCDNImageMedia'`.
- [ ] Add `cdnDownloadImageReq`, `transport.postEnvelope`, image download helpers, and call `attachImageMedia` for `wxImage`.
- [ ] Run: `go test ./internal/wechat -run 'TestHandleWebhookImageXMLDownloadsCDNImageMedia|TestHandleWebhookImageXMLDoesNotLeakAsBody'`.

### Task 4: Full verification

**Files:**
- No new files.

- [ ] Run `go test ./...`.
- [ ] Confirm normal image tests, inbound image download tests, and sticker-pack tests pass.
