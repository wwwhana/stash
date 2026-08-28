# 여러 에이전트가 같은 작업을 이어서 실행하는 방법

Stash의 작업 계획은 소유자가 보는 구성 요소와 작업 지도를 보관한다. 이 문서의 실행 흐름은 그 안의 작업 하나를 여러 에이전트가 중복 없이 이어서 처리하는 방법을 설명한다. 작업 계획의 구성 요소, 담당 경로, 선행 관계, 결정 기록은 기존 [AGENT.md](AGENT.md) 규약을 그대로 따른다.

## 핵심 원칙

1. 정확한 프로젝트 네임스페이스에서 기존 작업을 먼저 찾는다.
2. 같은 작업이 있으면 새 카드를 만들지 않고 `resume_work`로 불러온다.
3. 완료 조건이 없을 때만 `prepare_work`로 조건을 준비한다. 소유자가 완료 기준을 바꾸지 않은 한 기존 조건을 다시 만들지 않는다.
4. `start_work`가 돌려준 작업 소유권 토큰으로 한 에이전트만 작업한다.
5. 의미 있는 행동 뒤에는 관찰한 결과와 다음 행동 하나를 진행 기록으로 남긴다.
6. 완료 조건마다 실제 근거를 연결해 확인한 뒤에만 `finish_work`를 호출한다.
7. 끝내지 못한 채 세션을 마칠 때는 `handoff_work`로 다음 행동을 남기고 소유권을 놓는다.

작업 항목은 여러 세션에 걸쳐 유지되는 식별자다. 실행 시도(`attempt`)는 그 작업을 한 에이전트가 맡은 한 번의 실행이다. 진행 기록, 완료 조건, 근거, 워크트리 연결, 기억 연결은 작업 항목에 남으므로 다음 에이전트가 이전 대화 없이도 같은 작업을 이어갈 수 있다.

## 완료 조건과 용도가 정해진 기억 연결

`prepare_work.conditions[].kind`에는 다음 값만 사용한다.

| 값 | 확인 대상 |
| --- | --- |
| `command` | 명령이 기대한 종료 상태와 출력을 냈는지 |
| `test` | 지정한 테스트가 통과했는지 |
| `http` | HTTP 요청의 상태와 응답이 기대와 같은지 |
| `file` | 파일이나 생성물이 기대한 경로와 내용을 갖는지 |
| `build` | 빌드가 성공하고 필요한 산출물이 생겼는지 |
| `ui` | 화면에서 필요한 동작과 결과를 직접 확인했는지 |
| `user` | 소유자의 확인이나 승인이 필요한지 |
| `custom` | 위 분류에 들어가지 않는 구체적인 확인인지 |

조건의 `description`에는 끝났다고 판단할 상태를 쓰고, `verification`에는 실행할 명령이나 관찰 방법을 쓴다. `required: true`인 조건이 하나 이상 있어야 한다.

`remember_work`는 작업과 오래 보존할 기억을 연결한다. `relation`에는 다음 값만 사용한다.

| 관계 | 언제 쓰는가 |
| --- | --- |
| `context` | 작업을 이해하는 데 필요한 배경 |
| `constraint` | 이후 에이전트도 반드시 지켜야 하는 제한 |
| `decision` | 선택한 방향과 그 이유 |
| `evidence` | 나중에도 참고할 수 있는 확인 결과 |
| `failure` | 실패한 접근, 원인, 다시 피할 방법 |
| `result` | 작업으로 확정된 결과 |
| `supersedes` | 이전 기억이 낡았고 이 내용으로 바뀌었음을 알릴 때 |

기억의 `evidence` 관계와 완료 조건의 근거는 역할이 다르다. `remember_work`만 호출해서는 완료 조건이 통과되지 않는다. 완료 판단에는 `submit_work_evidence`로 등록하고 `verify_work_condition`으로 연결한 근거가 필요하다.

## 작업 소유권과 만료

- 한 작업에는 살아 있는 실행 시도가 하나만 존재한다.
- `start_work`는 평문 `lease_token`을 응답으로만 돌려주고 서버에는 해시만 저장한다.
- 토큰은 해당 실행 시도의 변경 호출에만 사용한다. 기억, 진행 기록 내용, 로그, 문서, 인계 내용에 토큰을 넣지 않는다.
- 기본 소유 시간은 15분이며 최대 24시간이다. `checkpoint_work` 또는 `renew_work_lease`만 만료 시각을 연장한다.
- 새 결과가 생겼다면 `checkpoint_work`를 사용한다. 긴 명령을 기다리는 중이라 새로 기록할 결과가 없다면 `renew_work_lease`로 소유 시간만 연장한다.
- 살아 있는 소유권이 있으면 다른 에이전트는 조건을 바꾸거나 새 실행을 시작할 수 없다. 작업을 복제해서 우회하지도 않는다.
- 호출이 끊겼다면 같은 논리 행동에는 같은 `action_key`를 사용해 다시 보낸다. 다른 행동에는 새 키를 사용한다.
- 같은 `start_work` 키와 내용을 다시 보내면 같은 실행 시도에 새 토큰을 발급한다. 각 응답의 토큰은 인계·완료·만료 전까지 모두 유효하므로 받은 토큰마다 외부에 노출하지 않는다.
- 원격 인증에서는 서버가 확인한 사용자 식별자를 단방향 키로 바꿔 실행 시도와 근거에 묶는다. 요청의 `agent_id`는 표시용이며 인증 수단이 아니다.

`resume_work`는 만료된 실행 시도를 정리하고 현재 상태를 읽는다. 최신 실행이 아직 `active`라면 그 만료 시각까지 기다리거나 현재 소유자가 `handoff_work`를 호출해야 한다. 다른 에이전트의 토큰을 복사하거나 추측해서는 안 된다.

## 진행 기록과 근거

다음과 같은 변화가 생긴 직후 `checkpoint_work`를 호출한다.

- 파일 변경을 저장했다.
- 빌드나 테스트 결과를 확인했다.
- HTTP, 명령줄, 화면에서 실제 결과를 관찰했다.
- 구현 방향을 정했거나 막힌 원인을 확인했다.
- 대화 입력 한도가 가까워져 새 세션에서 이어야 한다.

`summary`는 방금 한 일을, `result`는 관찰한 결과를 쓴다. `next_action`에는 바로 이어서 할 구체적인 행동 하나만 넣는다. 상태 설명만 반복하거나 여러 선택지를 나열하지 않는다.

근거는 변경할 수 없는 기록으로 저장된다. 서버가 내용의 SHA-256 해시, 제출 시각, 인증 주체 키와 등록된 워크트리의 현재 HEAD를 함께 기록한다. `submit_work_evidence.condition_ids`로 관련 완료 조건을 지정하고, 반환된 근거 ID를 `verify_work_condition.evidence_ids`에 넣는다. `passed`와 `waived` 모두 연결된 근거가 필요하며, `waived`에는 비워 두지 않은 `waiver_reason`도 필요하다.

`finish_work`는 다음 조건을 한 번에 확인한다.

- 필수 완료 조건이 모두 `passed` 또는 근거가 있는 `waived` 상태다.
- 각 필수 조건에 근거가 하나 이상 연결되어 있다.
- 이 작업을 막는 작업이 모두 `done` 또는 `canceled` 상태다.
- 완료 처리와 함께 서버가 `next_action`을 비운다.

하나라도 맞지 않으면 작업과 실행 시도는 완료 상태로 바뀌지 않는다. 계획에 속한 작업은 `start_work`와 `finish_work`가 `started_by`와 `completed_by`도 함께 기록한다. 완료 조건이 있는 작업은 `finish_work`를 건너뛰고 계획 상태만 `done`으로 바꾸지 않는다.

## MCP 호출 순서

아래 JSON은 MCP `tools/call` 요청 본문 예시다. 먼저 `list_work_items`와 `get_work_plan`으로 같은 작업이 있는지 찾고, 여기서는 기존 작업 ID가 `101`이라고 가정한다. 응답에서 받은 조건 ID, 실행 ID, 토큰, 근거 ID로 뒤 호출의 예시 값 `201`, `501`, `LEASE_TOKEN_FROM_START`, `301`을 바꿔야 한다.

기존 작업은 항상 먼저 불러온다.

```json
{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"resume_work","arguments":{"work_item_id":101,"recent_event_limit":20}}}
```

완료 조건이 없는 새 작업일 때만 조건을 준비한다. 기존 조건을 바꾸면 이전 조건은 삭제하지 않고 이전 기준으로 표시되므로, 소유자가 기준을 바꾼 경우에만 다시 호출한다.

```json
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"prepare_work","arguments":{"work_item_id":101,"next_action":"Run the focused tests and record the observed result.","conditions":[{"kind":"test","description":"Focused tests pass.","required":true,"verification":{"command":"go test ./internal/brain -run WorkExecution","expected":"exit code 0"}},{"kind":"file","description":"The execution documentation contains the complete MCP sequence.","required":true,"verification":{"path":"docs/WORK_EXECUTION.md","check":"Compare every example with the registered MCP schemas."}}],"action_key":"work-101-prepare-docs-v1"}}}
```

작업을 맡는다. 평문 `lease_token`은 응답에서만 받을 수 있고 서버에는 해시만 남는다. 응답을 받지 못해 같은 키와 내용으로 다시 보내면 같은 실행 ID와 새 유효 토큰을 받는다.

```json
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"start_work","arguments":{"work_item_id":101,"agent_id":"codex-docs","lease_seconds":900,"action_key":"73996bc5-5458-4ab2-b3e8-bf1d4f0ca537"}}}
```

의미 있는 행동을 마친 뒤 진행 기록을 남긴다.

```json
{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"checkpoint_work","arguments":{"attempt_id":501,"lease_token":"LEASE_TOKEN_FROM_START","summary":"Added the work execution guide.","result":"The guide now covers ownership, recovery, and exact MCP tool names.","next_action":"Run the Markdown structure check.","lease_seconds":900,"action_key":"work-101-checkpoint-doc-added-v1"}}}
```

관찰한 결과를 해당 완료 조건에 등록한다.

```json
{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"submit_work_evidence","arguments":{"attempt_id":501,"lease_token":"LEASE_TOKEN_FROM_START","condition_ids":[201],"evidence_type":"test","summary":"Focused work execution tests passed.","reference":"go test ./internal/brain -run WorkExecution","payload":{"exit_code":0,"result":"pass"},"action_key":"work-101-evidence-focused-tests-v1"}}}
```

반환된 근거 ID로 조건을 확인한다. 다른 필수 조건도 같은 방식으로 각각 확인한다.

```json
{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"verify_work_condition","arguments":{"attempt_id":501,"lease_token":"LEASE_TOKEN_FROM_START","condition_id":201,"status":"passed","evidence_ids":[301],"waiver_reason":"","action_key":"work-101-verify-focused-tests-v1"}}}
```

모든 필수 조건과 선행 작업을 확인한 뒤 끝낸다.

```json
{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"finish_work","arguments":{"attempt_id":501,"lease_token":"LEASE_TOKEN_FROM_START","summary":"Completed the documented execution flow.","result":"All required conditions have linked evidence and passed.","action_key":"work-101-finish-v1"}}}
```

끝내지 못했다면 `finish_work` 대신 인계한다. `next_action`은 반드시 비어 있지 않아야 한다.

```json
{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"handoff_work","arguments":{"attempt_id":501,"lease_token":"LEASE_TOKEN_FROM_START","summary":"Documented the execution loop and validated the headings.","result":"The live MCP example still needs comparison with the final server schema.","next_action":"Compare every example argument with the registered MCP tools, then run the Markdown checks.","action_key":"work-101-handoff-v1"}}}
```

## 인계와 비정상 종료 복구

정상 인계에서는 `handoff_work`가 마지막 진행 기록을 추가하고 실행 시도를 `handed_off`로 끝낸다. 작업은 `ready`로 돌아가며 새 에이전트가 곧바로 `resume_work`, `start_work` 순서로 이어받을 수 있다.

세션이나 프로세스가 갑자기 끝나면 소유권은 정해진 시각까지 남는다. 새 에이전트는 다음 순서로 복구한다.

1. 같은 `work_item_id`로 `resume_work`를 호출한다.
2. 최신 진행 기록의 `result`와 `next_action`, 완료 조건, 기존 근거, 막는 작업, 워크트리와 연결된 기억 내용을 읽는다.
3. 최신 실행이 아직 `active`면 만료될 때까지 기다린다. 새 작업을 만들거나 강제로 소유권을 빼앗지 않는다.
4. 만료 뒤 `start_work`를 호출해 다음 실행 번호와 새 토큰을 받는다.
5. 남은 `next_action`부터 이어서 처리한다. 이전 실행의 근거는 그대로 사용할 수 있다.
6. 새 결과를 진행 기록과 근거로 남기고, 완료하거나 다시 인계한다.

## 대화 입력 크기가 작을 때

`resume_work`는 작업 항목, 최신 실행, 전체 실행 중 가장 최근의 진행 기록, 현재 완료 조건과 근거, 워크트리, 연결된 기억의 짧은 내용과 상태, 끝나지 않은 선행 작업, 최근 작업 이벤트를 한 묶음으로 돌려준다. MCP의 `recent_event_limit` 기본값은 20이며 서버의 목록 상한보다 커질 수 없다. 더 작은 대화 입력을 쓰는 에이전트는 이 값을 낮춰 시작한다.

MCP 결과에는 기본 32KiB 전송 상한이 있다. `resume_work`는 항목별 개수와 내용 길이를 먼저 제한하고, `totals`에 전체 개수, `truncated`에 줄어든 항목을 표시한다. 상한에 가까우면 최근 이벤트와 연결 자료부터 줄이되 작업 ID, 현재 상태, 최신 실행과 다음 행동은 남긴다. 필요한 목록은 같은 작업 ID로 별도 도구에서 작은 페이지로 이어 읽는다. 모델의 전체 입력 한도와 MCP 전송 상한은 서로 다른 설정이다.

대화가 길어질 때는 원문 대화를 통째로 기억에 넣지 않는다. 의미 있는 결과를 `checkpoint_work`에 요약하고, 오래 유지할 제약·결정·실패·결과만 `remember_work`로 연결한다. 새 세션은 `resume_work` 묶음만으로 다음 행동을 찾을 수 있어야 한다.

## Dory 중단 시험

Dory 시험은 이전 대화를 전혀 받지 않은 새 에이전트가 같은 작업을 복구하는지 확인한다. 토큰을 로그에 남기지 않는 별도 시험 네임스페이스에서 실행한다.

1. Dory A가 기존 작업을 검색하고 하나의 작업 ID를 선택한다. 완료 조건을 준비하고 `start_work`로 첫 실행을 시작한다.
2. Dory A가 의미 있는 행동 하나를 수행하고, 관찰 결과와 다음 행동 하나를 `checkpoint_work`로 남긴다. 가능하면 첫 완료 조건의 근거도 등록하고 확인한다.
3. 정상 인계 경로에서는 `handoff_work`를 호출한다. 강제 중단 경로에서는 인계 없이 Dory A 세션을 종료한다.
4. 이전 대화가 없는 Dory B에는 같은 작업 ID만 준다. Dory B의 첫 호출은 `resume_work(work_item_id, recent_event_limit=20)`이다.
5. Dory B가 같은 작업 ID, 최신 진행 기록, 완료 조건, 기존 근거를 읽었는지 확인한다. 새 작업 항목이 생기면 실패다.
6. 정상 인계 뒤에는 곧바로 새 실행을 시작한다. 강제 중단 뒤에는 만료 전 `start_work`가 살아 있는 소유권 오류를 내야 하며, 만료 뒤에는 실행 번호가 증가한 새 실행이 시작되어야 한다.
7. Dory B가 진행 기록의 `next_action`부터 처리하고, 남은 근거를 등록해 모든 필수 조건을 확인한 뒤 `finish_work`를 호출한다.

시험은 작업 항목이 하나뿐이고, 동시에 살아 있는 실행도 하나뿐이며, 이전 근거가 유지되고, 필수 조건이 모두 근거와 연결된 상태로 작업이 `done`이 되었을 때 통과한다.
