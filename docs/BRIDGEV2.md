# Wiring to mautrix-go bridgev2

This document maps our protocol-neutral `remoteapi.PuppetAPI` onto the
`maunium.net/go/mautrix/bridgev2` framework, the same framework
[mautrix-telegram](https://github.com/mautrix/telegram) uses. The point of the
`remoteapi` layer is that this mapping is written **once** and never changes
when you swap WeChat for QQ.

> Status: the repo now includes a minimal `mautrix` appservice management bot in
> `internal/matrixas` (invite `@wechatbot:<domain>`, send `login`). The notes
> below describe the next step: replacing the management-room forwarding MVP
> with full bridgev2 portal/login glue.

## Concept mapping

| bridgev2 concept            | our `remoteapi` equivalent                         |
|-----------------------------|----------------------------------------------------|
| `NetworkConnector`          | a struct in `internal/connector` holding `PuppetAPI`|
| `NetworkAPI` (per login)    | `remoteapi.PuppetAPI`                              |
| `GetLoginFlows()`           | `PuppetAPI.LoginFlows()`                          |
| `CreateLogin()`             | `PuppetAPI.CreateLogin()`                         |
| `LoginProcess` / `LoginStep`| `remoteapi.LoginProcess` / `remoteapi.LoginStep`  |
| `LoginStepTypeUserInput`    | `remoteapi.LoginInputs`                           |
| `LoginStepTypeDisplayAndWait`| `remoteapi.LoginDisplayAndWait`                  |
| `LoginStepTypeComplete`     | `remoteapi.LoginComplete`                         |
| `Connect()` / `Disconnect()`| same names on `PuppetAPI`                         |
| remote → Matrix events      | `PuppetAPI.Events()` channel                      |
| `HandleMatrixMessage`       | `PuppetAPI.SendMessage`                           |
| `HandleMatrixRedaction`     | `PuppetAPI.RedactMessage`                         |
| `GetUserInfo` / `GetChatInfo`| `GetContact` / `GetChat`                         |

## Login bridge

bridgev2 calls your `LoginProcess` with typed sub-interfaces
(`LoginProcessUserInput`, `LoginProcessDisplayAndWait`). Adapt like:

```go
func (l *loginAdapter) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
    step, err := l.inner.Start(ctx)        // remoteapi.LoginProcess
    if err != nil { return nil, err }
    return toBridgeStep(step), nil
}

func toBridgeStep(s *remoteapi.LoginStep) *bridgev2.LoginStep {
    switch s.Type {
    case remoteapi.LoginDisplayAndWait:
        return &bridgev2.LoginStep{
            Type:         bridgev2.LoginStepTypeDisplayAndWait,
            StepID:       s.StepID,
            Instructions: s.Instructions,
            DisplayAndWaitParams: &bridgev2.LoginDisplayAndWaitParams{
                Type: bridgev2.LoginDisplayType(s.Display.Type), // "qr"
                Data: s.Display.Data,
            },
        }
    case remoteapi.LoginInputs:
        // map s.Inputs -> []bridgev2.LoginInputDataField
    case remoteapi.LoginComplete:
        return &bridgev2.LoginStep{Type: bridgev2.LoginStepTypeComplete, ...}
    }
}
```

`bridgev2.LoginProcessDisplayAndWait.Wait(ctx)` maps straight to our
`LoginProcess.Wait(ctx)`.

## Event pump

The current MVP sink in `internal/matrixas` forwards remote messages to
management rooms. For full bridgev2 portals, replace that sink with calls to
`bridge.QueueRemoteEvent(...)`:

```go
func (s *bridgeSink) OnMessage(ctx context.Context, m *remoteapi.InboundMessage) {
    s.login.QueueRemoteEvent(&simplevent.Message[*remoteapi.InboundMessage]{
        EventMeta: simplevent.EventMeta{
            Type:      bridgev2.RemoteEventMessage,
            PortalKey: portalKeyFor(m.Chat),
            Sender:    senderFor(m.Sender),
            Timestamp: m.Timestamp,
        },
        Data:               m,
        ID:                 networkid.MessageID(m.ID),
        ConvertMessageFunc: convertToMatrix, // build event.MessageEventContent
    })
}
```

`convertToMatrix` reads our neutral `InboundMessage` (Type/Text/Media) and emits
a Matrix `m.room.message`. Because the input is already neutral, this converter
is shared across every backend.

## Outbound

In your `NetworkAPI.HandleMatrixMessage`, translate the Matrix event into a
`remoteapi.OutgoingMessage` and call `conn.API().SendMessage(...)`. For media,
type-assert `remoteapi.MediaUploader`:

```go
if up, ok := conn.API().(remoteapi.MediaUploader); ok {
    handle, _ := up.UploadMedia(ctx, chat, media)
    media.URL = handle
}
```

## Checklist to go live

1. Implement `bridgev2.NetworkConnector` in `internal/connector` delegating to
   `remoteapi`.
2. Replace `internal/matrixas` management-room forwarding with bridgev2 portal
   event creation.
3. Implement the login + event adapters above.
4. Wire `bridgev2.NewBridge(...)` in `cmd/bridge/main.go`.
5. Keep all of this protocol-agnostic — only `internal/wechat` knows WeChat.
