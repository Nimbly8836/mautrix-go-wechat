# Media and Sticker Routing Design

## Goal

Fix Matrix-to-WeChat media routing so normal images are sent as WeChat images, while Matrix stickers/emoji remain routed through WeChat GIF emoji sending. Also make inbound WeChat emoji/stickers available in the current WeChat account Space sticker pack.

## Current behavior

`internal/matrixas/outbound.go` handles Matrix `m.image`, `m.sticker`, and `m.sticker`-typed image messages in one path and requires all of them to be GIF. This makes normal Matrix images fail with “微信 emoji/sticker 只能发送 GIF”.

Inbound WeChat images can currently become `[image]` placeholders when no binary media is attached. Inbound WeChat emoji with media is displayed as a Matrix sticker but is not automatically added to the account Space sticker pack.

## Design

1. Matrix `m.image` is routed to `remoteapi.MsgImage` with the downloaded media bytes and detected MIME type.
2. Matrix stickers (`event.EventSticker` or `msgtype=m.sticker`) are routed to `remoteapi.MsgEmoji` and must be GIF.
3. Inbound WeChat emoji with uploaded Matrix media URL is added to the current account Space `m.room.image_pack` using the existing sticker-pack state helper.
4. Inbound WeChat images with XML CDN metadata are downloaded through `/Tools/CdnDownloadImage`; when download succeeds, `InboundMessage.Media.Data` is filled so Matrix receives an actual `m.image`. If download fails, the existing `[image]` placeholder behavior remains.

## Error handling

- Missing Matrix media URL still posts a portal notice.
- Download failures still post a portal notice.
- Non-GIF Matrix stickers post the existing GIF-only notice.
- Space sticker-pack update failures are logged and do not block message delivery.

## Tests

- Normal Matrix image converts to `remoteapi.MsgImage` and does not require GIF.
- Matrix sticker non-GIF is rejected; GIF sticker converts to `remoteapi.MsgEmoji`.
- Inbound WeChat emoji with an uploaded MXC URL can be converted into a Space sticker-pack image.
