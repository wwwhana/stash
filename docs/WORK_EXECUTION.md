# 여러 에이전트가 하나의 목표를 나누어 실행하는 방법

Stash는 소유자가 보는 공통 목표 지도와 에이전트가 이어받는 짧은 작업 기록을 함께 보관한다. 이 흐름은 코드, 조사, 문서, 브라우저, API, 데이터, 장치, 사람 확인 작업에 똑같이 적용된다. 로컬 경로와 Git 저장소는 필요하지 않다.

## 기본 흐름

1. 에이전트는 `resume_project`로 공통 목표, 자신이 진행 중인 작업, 지금 맡을 수 있는 후보를 확인한다.
2. 하나를 고른 뒤 `resume_work`로 그 작업의 목표 경로와 다음 행동만 읽는다.
3. 완료 조건이 없을 때만 `prepare_work`로 조건을 정한다.
4. 실제 행동 직전에 `claim_work`로 작업권을 받는다.
5. 의미 있는 행동 뒤에는 `checkpoint_work`로 관찰한 결과와 다음 행동 하나를 남긴다.
6. 다른 결과가 필요해지면 `spawn_work`로 자식 작업이나 선행 작업을 만든다.
7. 완료 조건마다 실제 근거를 등록하고 확인한 뒤 `finish_work`를 호출한다.
8. 끝내지 못한 채 멈출 때는 `handoff_work`로 다음 행동을 남기고 작업권을 놓는다.

작업 항목은 여러 세션에 걸쳐 유지된다. 실행 시도는 한 에이전트가 그 작업을 맡은 한 번의 실행이다. 진행 기록, 완료 조건, 근거, 기억, 연결 자료, 선행 결과가 작업 항목에 남으므로 다음 에이전트는 이전 대화 없이 이어갈 수 있다.

## A가 A-1과 A-2로 완성되는 구조

프로젝트 A의 최상위 목표를 공통 목표로 정하고 A-1·A-2와 더 작은 결과를 자식 목표로 만든다. 구성 요소와 실제 작업에는 자신이 직접 채우는 가장 작은 목표의 `goal_id`를 넣는다.

- `resume_project`는 모든 에이전트에게 같은 공통 목표를 보여준다.
- `resume_work`와 `claim_work`는 현재 작업이 A까지 이어지는 짧은 목표 경로를 돌려준다.
- 공통 목표 밖의 목표를 가리키는 작업은 시작할 수 없다.
- `spawn_work`의 `child`와 `prerequisite`는 새 작업이 끝날 때까지 현재 작업을 막는다.
- `finish_work`는 끝난 실제 작업을 하위 목표와 부모 목표에 차례로 반영한다. 끝나지 않은 자식 목표나 작업이 있으면 부모 목표는 완료되지 않는다.
- 목표 전체에 필요한 배경·제약·결정·실패·결과는 `link_goal_memory`로 연결한다. 한 작업에만 필요한 내용은 `remember_work`로 연결한다.

## 에이전트 입력을 작게 유지하는 방법

`resume_project`는 자신이 진행 중인 작업과 실행 가능한 후보를 각각 최대 3개만 보낸다. `capabilities`를 보내면 그 능력으로 처리할 수 있는 후보만 받는다. 능력 이름은 작업 배정에만 쓰이며 권한을 주지 않는다.

`resume_work`의 기본 응답에는 다음 내용만 들어간다.

- 현재 작업과 다음 행동
- 공통 목표에서 현재 목표까지의 경로
- 아직 끝나지 않은 완료 조건
- 관련 기억 최대 6개
- 연결 자료 요약 최대 6개
- 끝난 선행 작업의 최종 결과 최대 6개
- 현재 작업을 막는 항목 최대 8개

응답의 `context_digest`를 다음 호출의 `known_context_digest`에 넣는다. 내용이 그대로면 Stash는 같은 자료 대신 `unchanged: true`가 든 작은 응답을 보낸다. `detail: "full"`은 현재 행동에 꼭 필요한 특정 기록이 짧은 응답에 없을 때만 쓴다.

`get_goal_map`은 소유자가 전체 진행을 살펴보는 화면용이다. 실작업 에이전트는 평소에 전체 지도를 읽지 않는다. 원문 대화를 통째로 기억에 넣지 않고, 오래 남길 제약·결정·실패·결과만 요약한다.

## 완료 조건과 근거

`prepare_work.conditions[].kind`에는 다음 값을 쓴다.

| 값 | 확인 대상 |
| --- | --- |
| `command` | 명령이 기대한 종료 상태와 출력을 냈는지 |
| `test` | 지정한 확인 절차가 통과했는지 |
| `http` | HTTP 요청의 상태와 응답이 기대와 같은지 |
| `file` | 파일이나 결과물이 기대한 경로와 내용을 갖는지 |
| `build` | 빌드가 성공하고 필요한 결과물이 생겼는지 |
| `ui` | 화면에서 필요한 동작과 결과를 직접 확인했는지 |
| `user` | 소유자의 확인이나 승인이 필요한지 |
| `custom` | 위 분류에 들어가지 않는 구체적인 확인인지 |

조건의 `description`에는 끝났다고 판단할 상태를 쓰고, `verification`에는 실행할 명령이나 관찰 방법을 쓴다. 필수 조건이 하나 이상 있어야 한다.

근거는 변경할 수 없는 기록으로 저장된다. `submit_work_evidence.condition_ids`로 관련 조건을 지정하고, 반환된 근거 ID를 `verify_work_condition.evidence_ids`에 넣는다. 통과와 예외 처리 모두 연결된 근거가 필요하며, 예외 처리에는 이유도 필요하다.

`finish_work`는 다음을 한 번에 확인한다.

- 필수 조건이 모두 근거와 연결되어 통과했거나 이유와 근거를 갖춘 예외 상태다.
- 이 작업을 막는 작업이 모두 `done` 또는 `canceled`다.
- 완료 처리와 함께 다음 행동을 비운다.

하나라도 맞지 않으면 작업은 완료 상태로 바뀌지 않는다.

## 작업권과 중복 실행 방지

- 한 작업에는 살아 있는 실행 시도가 하나만 존재한다.
- `claim_work`는 평문 `lease_token`을 응답으로만 돌려주고 서버에는 해시만 저장한다.
- 토큰은 해당 실행 시도의 변경 호출에만 사용한다. 기억, 진행 기록, 로그, 문서, 근거, 인계 내용에 넣지 않는다.
- 기본 작업권 시간은 15분이며 최대 24시간이다. 새 결과가 있으면 `checkpoint_work`, 기다리는 중이면 `renew_work_lease`로 연장한다.
- 살아 있는 작업권이 있으면 다른 에이전트는 작업을 복제해 우회하지 않는다.
- 호출 결과를 받지 못했다면 같은 논리 행동에 같은 `action_key`를 사용해 다시 보낸다. 다른 행동에는 새 키를 쓴다.
- 원격 인증에서는 서버가 확인한 사용자만 권한을 결정한다. `agent_id`와 `capabilities`는 표시와 배정에만 쓰인다.

## 작업 중 새 작업 만들기

`spawn_work`는 현재 작업권을 가진 에이전트만 호출할 수 있다.

| `relationship` | 동작 |
| --- | --- |
| `child` | 현재 작업 아래에 작은 작업을 만들고, 끝날 때까지 부모를 막는다. |
| `prerequisite` | 구조상 자식으로 두지 않지만, 끝날 때까지 현재 작업을 막는다. |
| `related` | 관련 작업을 만들되 현재 작업을 막지 않는다. |

새 작업의 제목, 첫 행동, 완료 조건, 필요한 능력을 한 호출에서 함께 저장한다. 호출이 끊겨 같은 `action_key`로 다시 보내면 같은 작업을 돌려주며 중복 카드를 만들지 않는다.

## Jira·Confluence와 다른 자료 연결

`attach_work_resource`는 작업에 필요한 자료의 짧은 참조만 저장한다.

- Jira 이슈: `kind: "ticket"`, `source: "jira"`, `authority: "external"`
- Confluence 문서: `kind: "document"`, `source: "confluence"`, `authority: "external"`
- Git 체크아웃: `kind: "git"`, `source: "git"`
- 그 밖의 브라우저 주소, API, 데이터, 장치, 파일, 결과물도 같은 방식으로 연결한다.

외부 문서 본문과 인증 정보는 원래 시스템에 둔다. Stash에는 안정적인 `resource_key`, 짧은 제목과 요약, 주소, 외부 번호, 변경 번호만 넣는다. 주소에 사용자 정보나 비밀 쿼리가 있거나 메타데이터에 토큰·암호가 있으면 서버가 거부한다.

`authority: "external"`이면 사람의 작업 상태와 원문은 Jira·Confluence가 기준이다. Stash는 AI가 맡은 작업, 중간 결과, 근거, 인계를 기준으로 보관한다. 외부 변경 감지와 다시 쓰기는 선택형 연결 기능이며 기본 작업 흐름에는 필요하지 않다.

## Web MCP 호출 예시

아래 예시는 `/mcp`에 보내는 `tools/call`의 핵심 인자다. 실제 호출에서는 각 응답의 작업 번호, 실행 번호, 작업권, 조건 번호, 근거 번호를 사용한다.

프로젝트에 들어간다.

```json
{"name":"resume_project","arguments":{"namespace":"/projects/a","agent_id":"agent-code","capabilities":["code","browser"]}}
```

후보 하나의 짧은 내용을 읽는다.

```json
{"name":"resume_work","arguments":{"work_item_id":101}}
```

조건이 없는 작업만 준비한다.

```json
{"name":"prepare_work","arguments":{"work_item_id":101,"next_action":"작은 확인 절차를 실행한다.","conditions":[{"kind":"test","description":"지정한 확인 절차가 통과한다.","verification":{"command":"go test ./internal/brain"},"required":true}],"action_key":"work-101-prepare-v1"}}
```

행동 직전에 작업권을 받는다. `action_key`에는 새 UUIDv4를 쓴다.

```json
{"name":"claim_work","arguments":{"work_item_id":101,"agent_id":"agent-code","lease_seconds":900,"action_key":"73996bc5-5458-4ab2-b3e8-bf1d4f0ca537"}}
```

조사 선행 작업이 필요해졌다면 현재 작업 아래에서 만든다.

```json
{"name":"spawn_work","arguments":{"attempt_id":501,"lease_token":"LEASE_TOKEN","action_key":"work-101-spawn-research-v1","relationship":"prerequisite","title":"공식 사양 확인","next_action":"공식 문서에서 필요한 제한을 확인한다.","capabilities":["research"],"conditions":[{"kind":"custom","description":"제한과 출처를 확인한다.","verification":{"check":"출처 주소와 확인 결과가 있음"},"required":true}]}}
```

사람이 관리하는 Jira 이슈를 새 작업에 연결한다.

```json
{"name":"attach_work_resource","arguments":{"work_item_id":102,"resource_key":"jira:APP-12","kind":"ticket","source":"jira","authority":"external","title":"APP-12 검토","uri":"https://jira.example.com/browse/APP-12","summary":"사람 검토 상태는 Jira에서 관리","external_id":"APP-12","revision":"7","role":"input"}}
```

의미 있는 행동 뒤에는 관찰 결과를 남긴다.

```json
{"name":"checkpoint_work","arguments":{"attempt_id":501,"lease_token":"LEASE_TOKEN","summary":"작은 확인 절차를 실행했다.","result":"지정한 패키지 확인이 종료 코드 0으로 끝났다.","next_action":"확인 결과를 완료 조건에 연결한다.","lease_seconds":900,"action_key":"work-101-checkpoint-test-v1"}}
```

근거를 등록하고 조건에 연결한다.

```json
{"name":"submit_work_evidence","arguments":{"attempt_id":501,"lease_token":"LEASE_TOKEN","condition_ids":[201],"evidence_type":"test","summary":"지정한 확인 절차가 통과했다.","reference":"go test ./internal/brain","payload":{"exit_code":0},"action_key":"work-101-evidence-test-v1"}}
```

```json
{"name":"verify_work_condition","arguments":{"attempt_id":501,"lease_token":"LEASE_TOKEN","condition_id":201,"status":"passed","evidence_ids":[301],"action_key":"work-101-verify-test-v1"}}
```

필수 조건과 선행 작업이 모두 끝나면 완료한다.

```json
{"name":"finish_work","arguments":{"attempt_id":501,"lease_token":"LEASE_TOKEN","summary":"요청한 결과를 완성했다.","result":"모든 필수 조건과 선행 작업 결과를 확인했다.","action_key":"work-101-finish-v1"}}
```

끝내지 못했다면 완료 대신 인계한다.

```json
{"name":"handoff_work","arguments":{"attempt_id":501,"lease_token":"LEASE_TOKEN","summary":"구현과 작은 확인을 마쳤다.","result":"화면 확인이 남아 있다.","next_action":"390px 화면에서 버튼과 목표 지도를 확인한다.","action_key":"work-101-handoff-v1"}}
```

## 갑작스러운 중단 뒤 복구

1. 새 에이전트가 같은 네임스페이스로 `resume_project`를 호출한다.
2. 진행 중인 같은 작업을 찾고 `resume_work`로 마지막 결과와 다음 행동을 읽는다.
3. 이전 작업권이 살아 있으면 만료를 기다리거나 현재 소유자의 인계를 기다린다.
4. 인계되었거나 만료된 뒤 같은 작업 ID로 `claim_work`를 호출한다.
5. 기록된 다음 행동부터 이어가고, 이전 근거와 끝난 선행 작업 결과를 다시 사용한다.

이전 대화가 없거나 예전 토큰을 잃었다는 이유로 새 작업을 만들지 않는다.

## 선택형 Git 연결

코드 프로젝트는 필요할 때만 다음 명령으로 체크아웃 정보를 모을 수 있다.

```bash
stash workspace facts --cwd . --agent-id codex --project-namespace /projects/a
```

`resolve_workspace`, `resume_workspace`, `claim_workspace`는 같은 작업 기록에 Git 저장소와 워크트리 정보를 덧붙이는 편의 도구다. Web MCP 프로젝트를 시작하거나 다른 종류의 작업을 맡기 위한 조건은 아니다.
