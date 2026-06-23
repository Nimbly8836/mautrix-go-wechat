# Bridgev2 Phase 1 Handoff

## Current Status

Date: 2026-06-23

The design and implementation plan for Bridgev2 Phase 1 are complete and committed locally. No feature implementation code has started yet.

## Branches And Worktrees

- Main workspace: `/Users/leat/git/mautrix-go-wechat`
- Implementation worktree: `/Users/leat/git/mautrix-go-wechat/.worktrees/bridgev2-phase1`
- Implementation branch: `bridgev2-phase1`

## Committed Work

- `.gitignore` now ignores `.worktrees/`.
- Design spec: `docs/superpowers/specs/2026-06-23-bridgev2-phase1-design.md`
- Implementation plan: `docs/superpowers/plans/2026-06-23-bridgev2-phase1.md`
- This handoff: `docs/superpowers/HANDOFF.md`

## Requested Phase 1 Scope

- Move toward a mautrix-telegram-like bridgev2 architecture.
- Keep `remoteapi.PuppetAPI` as the backend abstraction.
- Add `!wx` command prefix.
- Support `!wx help`.
- Support `!wx login`.
- Support multiple WeChat accounts logged in concurrently by one Matrix user.
- Do not use account switching for sending.
- Create one Matrix Space per WeChat account.
- Automatically create/reuse portal rooms for inbound WeChat chats.
- Route Matrix replies in a portal room through the WeChat account that owns that portal.
- Do not custom-implement Matrix E2EE in Phase 1; rely only on bridgev2/default behavior and do not crash on encrypted events.

## Important Design Correction

The multi-account model is not an active-account switch model.

Correct model:

```text
Matrix user
  -> multiple concurrent WeChat account runtimes
      -> one Matrix Space per WeChat account
          -> portal rooms mapped by owner + account + chat
```

Outbound routing must use portal room mapping:

```text
room_id -> owner_mxid + wechat_account_id + chat_id
```

So a reply in a portal room automatically sends through that portal's WeChat account.

## Current Blocker

Running Go commands currently fails in this shell:

```bash
go version
# /bin/bash: go: command not found
```

Before implementation, fix the Go toolchain/PATH in both the main workspace and the worktree, then start with Task 0 in the plan.

## Next Step

Continue in the implementation worktree:

```bash
cd /Users/leat/git/mautrix-go-wechat/.worktrees/bridgev2-phase1
git status --short --branch
go version
```

If `go version` works, execute Task 0 from:

```text
docs/superpowers/plans/2026-06-23-bridgev2-phase1.md
```

Task 0 discovers the available `mautrix` / `bridgev2` API and records exact package/type names before code implementation starts.

## Execution Mode

The chosen execution style is Subagent-Driven Development. This harness did not expose a real subagent dispatch tool during this session, so if continuing in the same harness, execute task-by-task with these checkpoints after every task:

1. Spec compliance review.
2. Code quality review.
3. Commit the completed task before moving to the next task.

## Push Status

At handoff creation time, local commits were ready to push to `origin`. Confirm with:

```bash
git status --short --branch
git log --oneline -5
```
