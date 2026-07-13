# Push Event Queue Startup Deadlock Fix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

## 中文审查摘要

**目标：** 通过一个回归测试和一个最小生产代码改动，解除 push/webhook 模式下满事件队列造成的 connector 启动死锁。

**实施方式：** 先在 `internal/wechat/webhook_test.go` 写入稳定复现测试并确认 RED；再把 `Client.Connect(push)` 的 `EventConnected` 投递改为原子非阻塞 `select`；最后验证 focused test 与全套 Go 测试。

**影响范围：** 仅修改 `internal/wechat/client.go` 和 `internal/wechat/webhook_test.go`。真实消息的 `emit` 行为保持不变。

**主要风险：** 极端启动积压时可能跳过一次管理状态通知；不会主动丢弃队列中的真实消息。

**待确认事项：** 无。设计规格已批准。

**Goal:** Ensure a full push-mode event queue cannot deadlock `Client.Connect`, allowing the connector to begin draining queued WeChat messages into Matrix.

**Architecture:** Keep the existing bounded message queue and blocking message backpressure. Change only the push-mode lifecycle notification to an atomic best-effort channel send, so a redundant `EventConnected` event yields when queued user messages already occupy all capacity.

**Tech Stack:** Go, channels and `select`, standard `testing` package, existing `remoteapi.Event` types.

---

## File Structure

- Modify `internal/wechat/webhook_test.go`: add one deterministic regression test beside the existing push-mode `Connect` test.
- Modify `internal/wechat/client.go`: make only the push-mode `EventConnected` enqueue non-blocking.
- No new production files, abstractions, dependencies, or configuration are required.

### Task 1: Reproduce and Fix the Full-Queue Push Connect Deadlock

**Files:**
- Modify: `internal/wechat/webhook_test.go:3-72`
- Modify: `internal/wechat/client.go:95-101`
- Test: `internal/wechat/webhook_test.go`

- [ ] **Step 1: Write the failing regression test**

Add `time` to the imports in `internal/wechat/webhook_test.go`:

```go
import (
	"context"
	"testing"
	"time"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)
```

Append this test after `TestConnectInPushModeSkipsLoginProbe`:

```go
func TestConnectInPushModeDoesNotBlockWhenEventQueueIsFull(t *testing.T) {
	queued := remoteapi.Event{
		Kind: remoteapi.EventMessage,
		Message: &remoteapi.InboundMessage{
			ID:   "queued-message",
			Chat: "wxid_friend",
			Text: "queued",
		},
	}
	c := &Client{
		events:      make(chan remoteapi.Event, 1),
		done:        make(chan struct{}),
		receiveMode: "push",
	}
	c.events <- queued

	connectReturned := make(chan struct{})
	releasedQueue := make(chan remoteapi.Event, 1)
	helperDone := make(chan struct{})
	go func() {
		defer close(helperDone)
		timer := time.NewTimer(250 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-connectReturned:
		case <-timer.C:
			// Release the old implementation without leaving Connect blocked.
			releasedQueue <- <-c.events
		}
	}()

	err := c.Connect(context.Background())
	close(connectReturned)
	<-helperDone
	if err != nil {
		t.Fatalf("Connect returned error: %v", err)
	}

	select {
	case released := <-releasedQueue:
		t.Fatalf("push Connect blocked while the event queue was full; released queued event: %#v", released)
	default:
	}

	select {
	case got := <-c.events:
		if got.Kind != queued.Kind || got.Message == nil || got.Message.ID != queued.Message.ID || got.Message.Chat != queued.Message.Chat || got.Message.Text != queued.Message.Text {
			t.Fatalf("queued message was replaced or discarded: %#v", got)
		}
	default:
		t.Fatal("queued message was discarded")
	}
}
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
go test ./internal/wechat -run TestConnectInPushModeDoesNotBlockWhenEventQueueIsFull -count=1 -v
```

Expected result: `FAIL` after approximately 250 ms with:

```text
push Connect blocked while the event queue was full
```

This failure proves the existing synchronous `EventConnected` emission deadlocks when the queue is full.

- [ ] **Step 3: Implement the minimal atomic non-blocking status emission**

In `internal/wechat/client.go`, replace the push branch in `Client.Connect`:

```go
if c.receiveMode == "push" {
	c.emit(remoteapi.Event{Kind: remoteapi.EventConnected})
	return nil
}
```

with:

```go
if c.receiveMode == "push" {
	select {
	case c.events <- remoteapi.Event{Kind: remoteapi.EventConnected}:
	default:
		log.Printf("wechat connect: event queue full; skipping connected status to prioritize queued messages")
	}
	return nil
}
```

Do not use `len(c.events)` or `cap(c.events)`: a separate capacity check would race with concurrent webhook producers. Do not change `Client.emit`; real message events must retain the existing blocking backpressure behavior.

- [ ] **Step 4: Run both push connection tests and verify GREEN**

Run:

```bash
go test ./internal/wechat -run 'TestConnectInPushMode(SkipsLoginProbe|DoesNotBlockWhenEventQueueIsFull)' -count=1 -v
```

Expected result:

```text
--- PASS: TestConnectInPushModeSkipsLoginProbe
--- PASS: TestConnectInPushModeDoesNotBlockWhenEventQueueIsFull
PASS
```

The existing test proves `EventConnected` is still emitted when capacity is available. The regression test proves a full queue no longer blocks and its queued message remains intact.

- [ ] **Step 5: Run formatting and full verification**

Run:

```bash
gofmt -w internal/wechat/client.go internal/wechat/webhook_test.go
git diff --check
go test ./...
```

Expected result: `git diff --check` prints nothing, all packages compile, and every test passes.

- [ ] **Step 6: Review the final diff for scope**

Run:

```bash
git diff -- internal/wechat/client.go internal/wechat/webhook_test.go
```

Confirm the diff contains only:

1. The new `time` import.
2. The full-queue regression test.
3. The atomic non-blocking push-mode `EventConnected` send and diagnostic log.

- [ ] **Step 7: Commit the fix**

```bash
git add internal/wechat/client.go internal/wechat/webhook_test.go
git commit -m "fix: prevent push event queue startup deadlock"
```

## Acceptance Criteria

- A full event queue cannot block `Client.Connect` in push mode.
- Existing queued `EventMessage` data remains in the queue unchanged.
- An available queue still receives `EventConnected`.
- Real message emissions retain the existing blocking backpressure behavior.
- `go test ./...` passes.
- Production logs will progress from `wechat webhook: emit` to `webhook request handled` once the connector drains backlog; downstream logs should then include `remote message` and Matrix delivery activity.

## Technical Vocabulary / 技术词汇

- **atomic non-blocking send** — 原子非阻塞发送
- **bounded event queue** — 有界事件队列
- **startup deadlock** — 启动死锁
- **backpressure** — 背压
- **regression test** — 回归测试
- **focused test** — 聚焦测试
- **lifecycle notification** — 生命周期通知
- **queued message integrity** — 队列消息完整性
- **goroutine leak** — goroutine 泄漏
- **acceptance criteria** — 验收标准

## 中文审查清单

- [ ] RED 测试是否精确复现“队列满时 `Connect(push)` 阻塞”？
- [ ] 失败测试是否会主动释放阻塞 goroutine，避免污染测试进程？
- [ ] 实现是否使用单个原子 `select`，而不是 `len/cap` 检查？
- [ ] 是否只有 `EventConnected` 可以降级，真实消息仍不被主动丢弃？
- [ ] 现有空队列连接测试和新增满队列测试是否都执行？
- [ ] 是否运行 `gofmt`、`git diff --check` 和 `go test ./...`？
