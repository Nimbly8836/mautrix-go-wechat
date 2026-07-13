# Push Event Queue Startup Deadlock Fix

## 中文审查摘要

**目标：** 恢复 push/webhook 模式下微信消息到 Matrix 的可靠转发。

**方案：** 当内部事件队列已满时，`Connect(push)` 不再阻塞等待写入纯状态事件 `EventConnected`，而是记录日志并立即进入 connector 消费循环。真实消息事件仍使用现有阻塞式投递，不主动丢弃。

**影响范围：** `internal/wechat/client.go` 的 push 连接行为及对应回归测试。不会改变 webhook 格式、消息转换、portal 路由或 Matrix 发送逻辑。

**主要风险：** 极端启动积压期间，Matrix 管理房间可能收不到一次“WeChat backend connected”状态通知；真实消息转发优先级高于该通知。

**待确认事项：** 无。用户已选择最小修复，并明确要求消息转发成功优先。

## Problem Statement

In push mode, the application service HTTP listener starts before the connector event pump is guaranteed to be consuming events. A burst of upstream webhook deliveries can fill the bounded `Client.events` channel before `Connector.Run` reaches its receive loop.

`Client.Connect` currently emits `EventConnected` synchronously. If the channel is already full, this status emission blocks forever. Because `Connector.Run` does not begin consuming until `Connect` returns, the system enters a startup deadlock. Subsequent webhook handlers log `wechat webhook: emit` and then block inside `Client.emit`, so they never log `webhook request handled`, and no messages reach Matrix.

The observed production logs match this failure boundary exactly.

## Goals

1. Ensure a full pre-existing event queue cannot prevent the connector consumer from starting.
2. Ensure this fix does not actively discard queued `EventMessage` values and preserves the existing backpressure model.
3. Restore WeChat-to-Matrix forwarding with the smallest possible behavioral change.
4. Add a deterministic regression test for the full-queue startup condition.

## Non-Goals

- Implementing an unbounded or disk-backed message queue.
- Reordering the entire appservice and connector startup lifecycle.
- Changing webhook payload decoding, deduplication, portal creation, or Matrix delivery.
- Guaranteeing delivery across process crashes, container restarts, Matrix send failures, or other downstream failures.
- Fixing the existing `Disconnect` versus concurrent webhook emission race around channel closure.

## Selected Design

`Client.Connect` will treat the push-mode `EventConnected` notification as best-effort:

- Use one atomic non-blocking `select` to enqueue `EventConnected`; do not use a separate `len`/`cap` check that could race with webhook producers.
- If the event channel has capacity, enqueue `EventConnected` exactly as today.
- If the event channel is full, skip only this status event and emit a diagnostic log.
- Return successfully in either case so `Connector.Run` can enter its receive loop and drain queued message events.

All message events continue to use the existing blocking `emit` method. This change does not actively drop queued messages; it does not claim end-to-end losslessness because downstream Matrix failures, process termination, and other existing conditions remain outside this fix. It only allows a redundant lifecycle notification to yield to queued user messages.

> 中文提示：被允许降级的只有“已连接”状态通知，不是微信消息。connector 一旦进入消费循环，积压消息会继续按现有逻辑转发到 Matrix。

## Data Flow After the Fix

1. The webhook listener receives upstream messages and enqueues decoded `EventMessage` values.
2. The connector starts and calls `Client.Connect`.
3. If the queue is full, `EventConnected` is skipped without blocking.
4. `Client.Connect` returns.
5. `Connector.Run` begins receiving and dispatching queued messages.
6. `Server.OnMessage` routes each message to the existing portal and sends it to Matrix.
7. Blocked webhook handlers obtain queue capacity, complete `emit`, and return HTTP 200.

## Error Handling and Observability

Skipping `EventConnected` due to a full queue will produce a clear log message that identifies the condition and states that queued messages are being prioritized. No error is returned because failing the connection would also prevent message consumption and recreate the outage.

Existing webhook, connector panic recovery, portal, and Matrix delivery logs remain unchanged.

## Testing Strategy

A regression test will:

1. Create a push-mode `Client` with a deliberately full event channel.
2. Call `Connect` in a goroutine.
3. Assert that `Connect` returns within a short deterministic timeout rather than deadlocking.
4. If the assertion times out on the unfixed implementation, drain one event and wait for the goroutine to exit so the test leaves no blocked goroutine behind.
5. Confirm the queued message event remains available and was not replaced or discarded.
6. Preserve the existing test that verifies an empty push-mode queue still receives `EventConnected`.

The test must fail against the current implementation because `Connect` blocks while trying to enqueue `EventConnected`. After the minimal production change, the focused test and `go test ./...` must pass.

## Trade-offs

This design may omit one `EventConnected` notification during an extreme startup backlog. That can suppress a management-room status notice, but it does not affect the actual backend connection or message routing. This is an intentional priority decision: forwarding user messages is more important than delivering a redundant status notification.

A startup readiness barrier could preserve the status event, but it would require broader lifecycle changes and create more regression risk during an active forwarding outage. A persistent queue would improve crash resilience but is outside the root cause and current scope.

## Technical Vocabulary / 技术词汇

- **startup deadlock** — 启动死锁
- **bounded channel** — 有界通道
- **backpressure** — 背压
- **event pump** — 事件泵 / 事件消费循环
- **best-effort notification** — 尽力而为的通知
- **lifecycle event** — 生命周期事件
- **regression test** — 回归测试
- **failure boundary** — 故障边界
- **deterministic timeout** — 确定性超时
- **persistent queue** — 持久化队列

## 中文审查清单

- [ ] 是否同意只允许 `EventConnected` 在队列满时被跳过？
- [ ] 是否确认真实 `EventMessage` 继续保持现有阻塞式投递，不主动丢弃？
- [ ] 是否接受极端启动积压时可能缺少一次管理房间“backend connected”通知？
- [ ] 是否同意本次不扩大到持久化队列或完整启动顺序重构？
- [ ] 是否确认“微信消息成功到达 Matrix”是本次验收的最高优先级？
