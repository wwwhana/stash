# Stash 에이전트 작업 규칙

이 파일을 작업 저장소의 `AGENTS.md` 또는 에이전트 규칙 파일로 복사해 사용합니다. Stash는 코드 파일을 대신 보관하지 않습니다. 코드는 Git에 두고, Stash에는 이슈·작업 상태·의존성·워크트리·작업 기록과 그 근거를 저장합니다.

## 세션 시작

1. `init`을 호출합니다.
2. `list_namespaces`로 사용할 네임스페이스가 있는지 확인합니다. 없으면 먼저 만듭니다.
3. 프로젝트에 해당하는 정확한 네임스페이스로 `recall`을 호출합니다.
4. `list_work_items`로 진행 중인 이슈와 막힌 이슈를 확인합니다.
5. `list_worktrees`로 현재 연결된 워크트리와 마지막 상태를 확인합니다.

## 작업 중

- 새 버그·기능·작업은 `create_work_item`으로 만듭니다. `issue_type`, `labels`, `owner`, `reporter`를 함께 기록합니다.
- 선행 작업이 있으면 `add_work_item_dependency`를 사용합니다. `from_item_id`가 `to_item_id`를 막는 방향입니다.
- 작업을 시작·검토·완료할 때 `update_work_item`으로 상태를 바꿉니다. 보드의 상태는 `backlog`, `ready`, `doing`, `blocked`, `review`, `done`, `canceled` 중 하나를 사용합니다.
- 판단 근거나 실패 원인은 `link_work_item_memory`으로 해당 이슈에 연결하고, `list_work_item_memory_links`로 연결된 근거를 확인합니다.
- 결정·진행 상황·재현 조건은 `add_work_item_comment`와 `record_work_event`로 남깁니다.

## Git 워크트리

작업을 시작하기 전에 로컬 브리지가 워크트리를 동기화하게 합니다.

```bash
stash worktree sync --repo . --namespace /projects/myapp --agent-id my-agent
```

반환된 워크트리 ID를 이슈에 `attach_worktree_to_item`으로 연결합니다. 코드와 diff는 Git에서 확인하고, Stash에서는 경로·브랜치·커밋·상태·작업 기록을 확인합니다. 작업이 끝나거나 워크트리가 사라지면 다시 동기화합니다.

## 세션 종료

1. 이슈 상태와 다음 작업을 `update_work_item`으로 저장합니다.
2. 인수인계에 필요한 내용을 `add_work_item_comment`로 남깁니다.
3. 오래 보관해야 할 사실은 `remember`로 저장하고, 작업 중인 짧은 초점은 `set_context`로 저장합니다.
4. 다음 에이전트가 바로 이어서 작업할 수 있도록 연결된 이슈 키와 워크트리 경로를 함께 남깁니다.

작업 그래프는 연결 리스트가 아닙니다. `blocks` 의존성은 방향 있는 그래프이며, 한 작업이 여러 작업을 막거나 여러 작업이 하나의 작업을 막을 수 있습니다. `relates_to`는 작업 사이의 일반적인 관련성을 표시합니다.

---

# Stash Agent Rules

Copy this file into the target repository as `AGENTS.md` or include it in the
agent's project rules. Stash does not replace Git as the source of code. Git
keeps the code and diffs; Stash keeps issues, work state, dependencies,
worktrees, activity, and the evidence attached to them.

## Session start

1. Call `init`.
2. Call `list_namespaces` and verify the namespace exists. Create it first if it does not.
3. Call `recall` in the exact project namespace.
4. Call `list_work_items` to see active and blocked issues.
5. Call `list_worktrees` to see connected worktrees and their last status.

## While working

- Create bugs, features, and tasks with `create_work_item`; record `issue_type`, `labels`, `owner`, and `reporter`.
- Connect prerequisites with `add_work_item_dependency`. `from_item_id` blocks `to_item_id`.
- Use `update_work_item` when work starts, moves to review, or finishes. Use one of `backlog`, `ready`, `doing`, `blocked`, `review`, `done`, or `canceled`.
- Attach evidence and lessons with `link_work_item_memory`; inspect linked evidence with `list_work_item_memory_links`.
- Record decisions and progress with `add_work_item_comment` and `record_work_event`.

## Git worktrees

Sync local worktrees before starting work:

```bash
stash worktree sync --repo . --namespace /projects/myapp --agent-id my-agent
```

Attach the returned worktree ID with `attach_worktree_to_item`. Use Git for
code and diffs; use Stash for the path, branch, commit, status, and activity.
Sync again when work ends or a worktree disappears.

## Session end

1. Save the issue status and next step with `update_work_item`.
2. Leave handoff details with `add_work_item_comment`.
3. Save durable facts with `remember` and short-lived focus with `set_context`.
4. Include the issue key and worktree path so the next agent can continue immediately.

The work graph is not a linked list. A `blocks` dependency is directed: one
work item may block several items, and several items may block one item.
`relates_to` records a general relationship between work items.
