# Sessions — outstanding work

Status of the durable-conversation work (PRs #1–#3) and what is left before it
can merge. Written 2026-08-14, picking the branch back up after the April
binaries/deploy stream.

---

## Where it stands

**All merged to `main` on 2026-08-15**, in order #4 (CI repair) → #1 → #2 → #3,
with merge commits rather than squashes so the stack's shared SHAs held.

| PR | Merge commit | Work |
|---|---|---|
| #4 | `ee2f949` | CI repair — fabricated action pins, build.yml concurrency |
| #1 | `ae95ba3` | raft-backed transcripts, context budget, WSL prompt fix, ULID race, leadership race, + REST owner-scoping and rune-safe elision |
| #2 | `a7756f1` | incremental compaction into a running summary, + rune-safe summary truncation |
| #3 | `a3ba645` | `session_search` / `session_list` / `session_read`, titles, + rune-safe snippets and a gofmt fix |

Merged `main` builds clean and passes `go test -race -count=1 ./...` locally.

The three feature PRs came from the `Sheardo` fork, opened 2026-08-12. They were
reviewed here, fixed (see below), force-pushed back to the fork under
`maintainerCanModify`, and merged.

**Reviewed locally, verified independently:** `go build`, `go vet` and
`go test -race -count=1 ./...` all clean on every branch of the stack (36
packages). The code is good — narrow interfaces to avoid the compute↔memory
import cycle, trimming computed on the leader so the FSM stays deterministic
across replay, and `dropOrphanedToolResults` handles the tool_call_id orphan
trap that turns later turns into provider 400s.

### Review fixes — pushed 2026-08-15

`pr1-fixed`, `pr2-fixed`, `pr3-fixed` rebuild the stack with review fixes
distributed into the PR each one belongs to, and are **pushed to the fork**
(`maintainerCanModify` was true; #2 and #3 needed a force-push because they were
rebased onto the fixed #1). All three PRs now report `MERGEABLE`, with a comment
on each explaining the rewrite.

- `pr1-fixed`: REST session ids scoped to their owner; rune-safe tool-result
  elision; docs.
- `pr2-fixed`: rune-safe summary truncation.
- `pr3-fixed`: gofmt `titler.go`; rune-safe + range-clamped search snippets.

Every fix has a test that fails without it (each was checked by reverting the
fix and re-running).

---

## 1. Session tools were not user-scoped — FIXED (PRs #6, #7)

**The gap.** `session_search` built its query without setting `UserID`;
`session_list` and `session_read` had no owner scope at all, and `session_read`
returned any transcript given a `channel` + `channel_id` that `session_list` had
just handed out. On a shared Telegram bot (`UserIDScopes map[int64]string`),
user B asking "search our past conversations" received snippets from user A's
threads. The `UserID` filter already existed on `memory.SessionSearchQuery` and
worked — nothing populated it.

**The rule** (`compute.TurnIdentity.Visible`, documented in
[MEMORY.md](../docs/dev/MEMORY.md#transcript-visibility)):

1. the conversation the turn is *in* — same channel and channel_id — is always
   readable, whoever the record says opened it;
2. any other conversation only when its `user_id` matches the caller.

Clause 1 is load-bearing, not a loophole: a Telegram group chat is one session
shared by every member and `rec.UserId` is whoever spoke first, so plain
ownership would refuse the second member the conversation they are visibly
having.

**PR #7 generalised the mechanism.** Identity was also reaching `notify`,
`commitment_create`, `oauth_start` and `research_start` through the
tool-argument map, which is built from the model's own JSON — and the injections
were conditional, so on a channelless turn (scheduler, webhook, research worker)
the model's own value survived. `__scope` was never injected by anything, so the
scope prefix on the OAuth audit field could only ever have come from the model.
`TurnIdentity` on the request context is now the single source of caller
identity for every builtin, and
`TestBuiltinsDoNotReadIdentityFromArgs` parses the package and fails on any
handler that reads a retired key out of a map, so the invariant is enforced
rather than remembered.

### Still open: cross-channel identity

Scope identity is `Claims.UserID`, which is per-channel — `tg-@alice` from
Telegram, a JWT subject over REST — with no aliasing between them. A solo
operator using both will not find one channel's threads from the other. That was
the deliberate choice (a false negative costs recall; a false positive is the
bug being fixed), but it makes a single human two users as far as transcript
search is concerned.

A `user_aliases` map is the obvious follow-up, and it is the first concrete
requirement falling out of the [`user-scoping-ownership-model`](#3-decision-user-scoping-and-ownership)
decision: principal identity has to be resolvable across channels before "owner"
means anything stable. It is also `R7 — Principal identity` in
[ROADMAP.md](../ROADMAP.md), which should reference that decision rather than
drifting from it.

## 1b. Ownership — what is left

Stages 1–3 of `user-scoping-ownership-model` shipped (PRs #11, #12): records own,
reads are scoped to an audience whose zero value matches nothing, Dream will not
consolidate across owners, `Forget` respects ownership, and commitments and
scheduled tasks are scoped on list/get/cancel/delete.

Two decisions now govern the rest:
`user-scoping-ownership-model` and `operator-role-and-cluster-authorization`.

Next, in order:

1. **Cluster gRPC principal.** `MemoryService.Search` and an empty-requester
   `Forget` are unrestricted. Derive the audience from the mTLS peer
   certificate: a peer node may assert a principal (it authenticated the user
   at its own edge), an operator gets `Everyone()` and is audited, anything
   else is denied. A `requester` field the client fills is delegation, not
   authorization — the transport has to say who is calling.
2. **The operator role itself**, which (1) and (3) both depend on. Authority
   over the deployment, not universal read; reading an individual's memories is
   `memory:read:any` granted through the policy engine, not ambient.
3. **Write `SHARED` somewhere.** The enum is honoured on read and never set, so
   operator-seeded knowledge — facts about the deployment rather than about a
   person — is invisible to a second user. Only an operator may set it: if any
   user could, a prompt injection saying "remember this for everyone" becomes a
   broadcast primitive.
4. **Claim legacy records.** Everything written before 2026-08-15 is unowned and
   readable by everyone. Deliberate (an upgrade must not hide a single-user
   node's memory) but shrinking-not-closed; wants a one-shot
   `lobslaw memory claim --owner=…`.
5. **Fold the alias map into R7's identity store**, and move resolution to the
   gateway edge. See the gap list on `ROADMAP.md` R7 — the config-only map that
   shipped is a stand-in, and two identity models must not coexist quietly.

## 2. CI has never run in this repo

Approving the queued runs on #1 produced three failures, none about the code.

**Fabricated action pins.** Three of the pinned SHAs do not exist, so every
workflow died at `Set up job`. This is the `DEFERRED.md` "Verify SHA pins" item
— *"pinned but recalled from training data, not verified against published
tags"* — whose stated trigger was the first CI run reporting a mismatch. Fixed
in **PR #4**:

| Action | Was | Now |
|---|---|---|
| `actions/setup-go` | `f111f330…a9a` "v5.2.0" — nonexistent, and two characters off the real **v5.3.0** commit | `40f1582b…` v5.6.0 |
| `golangci/golangci-lint-action` | `a4f60bb2…` "v7.0.0" | `14814048…` v7.0.0 (annotated tag — pin the *dereferenced commit*, not the tag object) |
| `bufbuild/buf-action` | `3f69d0a5…` "v1.4.0" | `fd21066d…` v1.4.0 |

`actions/checkout` and all four `docker/*` pins in `release.yml` were verified
correct. Stay on golangci-lint-action **v7** — `.golangci.yml` is `version: "2"`,
which is what v7 expects.

**build.yml died before even reaching the pins.** Its *workflow-level*
`concurrency` group referenced `matrix` context, which only exists once a job is
expanded, so the expression was invalid and every run failed at 0s with no jobs
— on `main` too, since April. Moved onto the job, where `matrix` is in scope.

**Still red after #4: the gofmt gate.** `lint.yml` runs `gofmt -l .` over the
**whole tree**, and `main` has 45 files the Go 1.26 formatter wants restyled
(mostly struct-tag alignment around comments). One mechanical
`gofmt -w ./cmd ./internal ./pkg` commit fixes it. Sequenced *after* the session
stack merges so it covers the newly merged code too — doing it first would force
a three-branch rebase with conflicts in `rest.go`, `config.go` and `session.go`.

The PR author saw the gofmt drift and deliberately left it alone rather than
bury a review diff in unrelated reformatting. That was the right call.

### What ran once CI could run (PR #4, 2026-08-15)

`build` went **green for the first time in the repo's history**. Post-merge,
`main` at `a3ba645` is **build ✅ test ✅ lint ❌**. The other two surfaced a
backlog that had been invisible:

- **A real data race, since fixed.** `RaftNode.SetLeadershipCallback`
  (raft.go:442) wrote `n.onLeadership` unguarded while `publishLeadership`
  (raft.go:338) read it from the state-watch goroutine. `test` failed on PR #4,
  which was based on pre-merge `main`, and passes on merged `main` — so CI's
  race detector independently confirmed that PR #1's
  `02de91f fix(memory): guard the raft leadership callback` was fixing a live
  bug, not a theoretical one. It never reproduced across several full local
  `-race` runs; the slower CI runner hit it every time.
- **985 golangci-lint findings**, the first time that linter has ever run:
  goconst 658, revive 130, gocyclo 59, errcheck 44, staticcheck 23, unused 10,
  gocritic 8, unconvert 2, forbidigo 2, misspell 1. goconst is two thirds of it
  and is usually noise on test files — tune `.golangci.yml` (exclude tests,
  raise `min-occurrences`) before touching 658 call sites. errcheck, staticcheck
  and unused are the ones worth reading.
- **`buf` job fails twice over:** a proto format diff (`-76/+124`), and
  `Resource not accessible by integration` when buf-action tries to post its
  findings as a PR comment — the job grants only `contents: read`, so it needs
  `pull-requests: write` or the comment disabled.
- The `gofmt -l .` gate has still never been reached: it is a later step in the
  same job, and golangci-lint fails first.

---

## 3. Episodic records vs session messages — DECIDED

Raised by the PR #1 author on 2026-08-13; settled 2026-08-15.

Every turn's text is written twice: an `EpisodicRecord` (lossy, scored,
consolidated by Dream, pruned at 24h under `RETENTION_SESSION`) and a
`SessionMessage` per message (verbatim, ordered, capped). The author offered to
collapse that into a pointer model — episodic keeping only the embedding plus a
`session_id:seq` reference.

**Decision: keep both, and add the pointer as metadata.** Episodic records now
carry `session_ref`, so a recall hit can offer to read the surrounding thread.

Why not the pointer model: it inverts the dependency. Memory is meant to outlive
the conversation — that is the whole point of the split — and under a pointer a
durable consolidated memory would depend on a transcript that is capped,
evicted oldest-first, and hard-deleted by `Forget`. "Forget this thread" would
blow holes in memories that were never about that thread. The expensive artifact
is the embedding, which is not duplicated either way, and the duplicated text is
`RETENTION_SESSION` so the pruner clears it within a day.

`session_ref` addresses the **thread, not the message**: ingest runs before the
transcript append, so the sequence number does not exist yet. Advisory either
way — a dead pointer means the link is stale, never that the memory is wrong.

## Accepted limitations — documented, not bugs

Worth knowing when reading this code later; none of these need action now.

- **Session append is not one bbolt transaction.** `FSM.applySessionAppend`
  issues N separate `Put`/`Delete` calls. Ordering (evictions, bodies, index
  record last) means a partial apply leaves unreferenced messages — harmless and
  reclaimed by the next trim — rather than an index promising messages that are
  not there. Consistent with the existing `Store` API, which exposes no batch.
- **Follower turns do not persist.** Session writes are leader-only; there is no
  forwarding layer. The gateway's in-memory cache keeps a follower-hosted
  conversation coherent for the life of the process, but it does not survive a
  restart there. Leader forwarding is the real fix.
- **`SearchTranscripts` is a full scan** of every session's every message. Fine
  at one-session-per-live-chat scale, and search is substring by design —
  semantic recall is `memory_search`'s job.
- **The 200-message cap is permanent loss.** Evicted messages are gone, so
  search only ever covers what is retained.

---

## Re-verifying

From a worktree on any branch of the stack:

```sh
go build ./... && go vet ./...
go test -race -count=1 ./...          # 36 packages, expect exit 0
gofmt -l $(git diff --name-only main..HEAD | grep '\.go$' | grep -v '\.pb\.go')
```

`TestSchedulerConcurrentClaimOnlyOneWins` is a known load-sensitive flake (it
polls on a 50ms window) — it did not reproduce across several full runs here.
