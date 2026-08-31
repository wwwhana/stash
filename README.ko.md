# Stash (스태시)

[🇺🇸 English](README.md) | [🇰🇷 한국어](README.ko.md)

> **안내:** 이 프로젝트는 원본 [alash3al/stash](https://github.com/alash3al/stash)의 기능 개선 포크(Fork) 버전입니다. 
> 현재 버전(`wwwhana/stash`)은 하이브리드 검색(벡터 + Trigram RRF), 결정론적 에피소드 클러스터링, 안전한 Soft-delete 수명 주기, 그리고 고급 MCP 자동화 훅(Auto-hydration) 등 중요한 개선 사항들을 포함하고 있습니다.

**당신의 AI가 겪는 건망증, 우리가 해결했습니다.**

모든 LLM은 매 대화마다 처음부터 다시 시작합니다. Stash는 에이전트에게 영구적인 기억(Memory)을 부여하여, 여러 세션에 걸쳐 기억하고, 떠올리고, 통합하고, 학습하게 합니다. 더 이상 처음부터 배경을 설명할 필요가 없습니다.

오픈 소스. 자체 호스팅(Self-hosted). MCP를 지원하는 모든 에이전트와 호환됩니다.

## 빠른 시작 (Quick Start)

```bash
git clone https://github.com/wwwhana/stash.git
cd stash
cp .env.example .env   # API 키와 모델을 수정하세요
docker compose up
```

이것이 전부입니다. Postgres + pgvector, 마이그레이션, MCP·운영 지표 서버, 백그라운드 기억 통합이 단일 명령어로 모두 실행됩니다.

**다음 단계:** [Getting Started guide](docs/GETTING_STARTED.md) — MCP 클라이언트를 연결하고, `init` / `remember` / `recall`을 실행하여 모든 것이 잘 동작하는지 확인하세요.

**완전한 로컬 환경 (클라우드 API 없음):** [Ollama setup guide](docs/LOCAL_OLLAMA.md) — Ollama와 Docker Compose를 사용하여 프라이빗 임베딩 및 추론 모델을 로컬에서 호스팅하는 방법입니다.

## LLM 제공자 설정 (OpenAI 기본 및 로컬 예제)

Stash는 벡터화(Vectorization)와 추론(Reasoning)을 위해 외부 LLM에 의존합니다. OpenAI 같은 표준 클라우드 제공자나 Ollama 같은 로컬 서버를 모두 사용할 수 있습니다.

### 기본 설정 (OpenAI)

`.env` 파일을 다음과 같이 설정하세요:

```bash
STASH_OPENAI_API_KEY=sk-your-openai-api-key
STASH_EMBEDDING_MODEL=text-embedding-3-small
STASH_REASONER_MODEL=gpt-4o-mini
STASH_VECTOR_DIM=1536
```

### 로컬/커스텀 LLM (Ollama, LM Studio)

로컬 서버나 커스텀 OpenAI 호환 서버를 사용하려면 Base URL을 변경하세요.
해당 서버가 인증을 요구하지 않으면 API 키를 비워도 됩니다.
**튜닝 팁:** `multilingual-e5-small`과 같은 비대칭 모델을 사용할 경우, 모델의 출력 차원에 맞게 `STASH_VECTOR_DIM`을 반드시 일치시켜야 합니다 (예: `384`).

```bash
STASH_OPENAI_BASE_URL=http://host.docker.internal:11434/v1
STASH_OPENAI_API_KEY=
STASH_EMBEDDING_MODEL=multilingual-e5-small
STASH_REASONER_MODEL=llama3
STASH_VECTOR_DIM=384
```

전체 설정 체크리스트는 [Getting Started](docs/GETTING_STARTED.md)를 참고하세요.

## MCP 클라이언트 설정

`docker compose up` 실행 후, Stash는 HTTP/SSE 방식으로 MCP 서버를 노출합니다. Streamable HTTP(`/mcp`)와 표준 SSE(`/sse`)를 모두 지원합니다.

### 1. Claude Code
`/mcp` HTTP 엔드포인트를 기본으로 사용합니다.
```bash
claude mcp add stash http://localhost:8080/mcp
```

### 2. Codex
Docker Compose로 실행한 로컬 Web MCP 서버는 다음처럼 등록합니다.

```bash
codex mcp add stash-local --url http://127.0.0.1:8080/mcp
```

기본 로컬 설정인 `STASH_AUTH_MODE=none`에서는 MCP 로그인이 필요하지 않습니다. `stdio` 방식으로 stash CLI 바이너리를 직접 실행할 수도 있습니다.
```json
"stash": {
  "command": "stash",
  "args": ["mcp", "execute", "--with-consolidation"]
}
```

원격 MCP 서버를 OAuth로 보호할 때는 Streamable HTTP를 사용합니다:
```bash
codex mcp add stash --url https://stash.example.com/mcp --oauth-client-id stash-codex
codex mcp login stash
```

`STASH_AUTH_MODE=oauth`, OIDC 발급자, 브라우저 클라이언트 설정,
공개 `/mcp` 주소인 `STASH_AUTH_MCP_RESOURCE_URL`을 지정합니다. Stash가
MCP 보호 리소스 정보와 OAuth 인가 코드·PKCE 흐름을 제공하고, 실제 사용자
로그인은 설정한 OIDC 제공자(예: Authentik)가 처리합니다. 동적 공개 클라이언트
등록은 `/oauth/register`에서 지원합니다.

인증 프로필은 세 가지입니다.

- `none`: HTTP 인증 없음. 격리된 로컬 실행에서만 사용합니다.
- `oauth` (기존 `oidc`도 호환): Streamable HTTP와 SSE에 OAuth 2.1 Bearer
  토큰을 사용합니다. 보호 리소스 정보, PKCE, 갱신 토큰 교체, 동적 클라이언트
  등록을 지원합니다.
- `stdio`: MCP OAuth 탐색을 사용하지 않습니다. 로컬 프로세스를 신뢰하거나
  `STASH_AUTH_STDIO_TOKEN`으로 사용자 범위를 확인할 수 있습니다.

HTTP MCP 요청은 `Authorization: Bearer <access-token>` 헤더를 보내야 합니다.
화면에 로그인할 때 쓰는 세션 쿠키는 표준 MCP 클라이언트 인증 수단이 아닙니다.

## 운영 지표와 상태 확인

`stash serve`는 MCP, 관리 화면, OAuth 경로, 운영 지표, 상태 확인을 하나의 HTTP 포트(기본 `:8080`)에서 제공합니다. Prometheus 지표는 `http://localhost:8080/metrics`에서 확인하고, `/healthz`는 데이터베이스 연결을, `/readyz`는 준비 상태를 확인합니다. HTTP 요청, 인증 결과, MCP 도구 호출, 외부 제공자 호출, 네임스페이스 범위 적용, 기억 통합 작업, 임베딩 재시도 대기 건수를 기록합니다. 요청·인증·도구·제공자·범위 지표의 라벨에는 사용자 ID와 실제 네임스페이스 이름을 넣지 않습니다.

임베딩 API가 짧은 요청 재시도 후에도 실패하면 원문은 인덱싱 대기 상태로 저장됩니다. PostgreSQL 연결은 정상이지만 벡터 값만 저장하지 못한 경우에도 원문을 보존합니다. 한 항목이 다섯 번 실패하면 자동 재시도를 멈추고 관리자가 다시 시작할 때까지 일시 중지해, 작은 일일 한도를 계속 소모하지 않게 합니다. 재시도 간격은 설정한 최댓값 안에서 늘어납니다. 임베딩 제공자가 잠시 응답하지 않아도 `recall`은 저장된 원문과 사실의 `entity`·`property`·`value` 필드를 PostgreSQL 트라이그램 검색으로 찾아 작업을 계속할 수 있습니다. `STASH_EMBEDDING_RETRY_INTERVAL`, `STASH_EMBEDDING_RETRY_MAX_INTERVAL`, `STASH_EMBEDDING_RETRY_BATCH_SIZE`로 주기와 한 번에 처리할 수를 설정합니다.

`STASH_ADMIN_SUBJECTS`(쉼표로 구분한 OIDC subject)나 `STASH_ADMIN_TOKEN`을 설정하면 관리 화면에 **임베딩 관리** 메뉴가 나타납니다. 대기 건수, 현재 모델과 차원, 최근 제공자 오류를 확인할 수 있습니다. **대기 항목 즉시 재시도**는 지금 처리 중인 항목을 건드리지 않고 예약된 실패 건을 즉시 깨웁니다. **전체 다시 계산**은 저장된 벡터와 임시 캐시를 비운 뒤 살아 있는 모든 에피소드와 팩트를 다시 등록하며 원문은 유지합니다.

MCP 도구 결과에는 기본 32KB 안전 상한이 있습니다. 이 값은 전송을 보호하기 위한 값이며 모델의 컨텍스트 한도가 아닙니다. 목록이 상한을 넘으면 `items`, `has_more`, `next_offset`으로 나누므로 같은 도구에 `offset=next_offset`을 보내 이어서 읽을 수 있습니다. 목록이 아닌 큰 결과는 클라이언트의 입력 한도를 넘기기 전에 생략 안내를 반환합니다. 상한은 `STASH_MCP_MAX_RESPONSE_BYTES`로 바꿀 수 있습니다.

리즌 모델과 임베딩 모델의 입력 한도는 MCP 응답 상한과 별도로 설정합니다. 리즌 모델의 전체 컨텍스트 크기를 `STASH_REASONER_CONTEXT_TOKENS`에, 지시문과 JSON 답변을 위해 남겨 둘 공간을 `STASH_REASONER_RESERVED_TOKENS`에 넣습니다. 임베딩 모델의 입력 한도는 `STASH_EMBEDDING_CONTEXT_TOKENS`에 넣습니다. Stash는 이 토큰 예산을 보수적인 UTF-8 바이트 예산으로 바꾸고, 문단을 먼저 나눈 뒤 마침표 같은 문장 끝을 우선해 자료를 나눠 호출합니다. 자연스러운 경계가 없을 때만 글자 중간을 나눕니다. 긴 임베딩 입력은 여러 묶음의 벡터를 합쳐 하나로 저장합니다. MCP는 모델의 토크나이저나 현재 대화에 남은 토큰을 알려주지 않으므로 값을 `0`으로 두면 제공자가 알려 준 한도를 읽어 자동으로 맞추고, 한도를 알 수 없을 때도 컨텍스트 초과 후 더 작게 나눠 다시 시도합니다. 컨텍스트가 44,544토큰인 리즌 모델이라면 `STASH_REASONER_CONTEXT_TOKENS=44544`, `STASH_REASONER_RESERVED_TOKENS=4096` 이상으로 시작하면 됩니다.

`info`에서는 MCP/API 호출과 OpenAI 호환 제공자 호출이 콘솔에 남습니다. `STASH_LOG_LEVEL=debug`로 올리면 일반 관리 화면 요청도 볼 수 있고, 실패한 요청은 `warn`으로 표시됩니다. 로그에는 메서드, 제한된 경로, 상태, 걸린 시간, 호출 구성 요소·모델, 가능한 경우 요청 ID를 넣으며 쿼리 문자열, 인증 헤더, 쿠키, 요청 본문은 기록하지 않습니다.

제공자 요청과 MCP 도구 호출은 기본 2분 안에 끝나야 합니다. 모델 서버가 느리거나 큰 계획을 검토하는 환경에서는 `STASH_OPENAI_REQUEST_TIMEOUT`과 `STASH_MCP_TOOL_TIMEOUT`을 같은 값 또는 도구 제한을 더 크게 설정하세요. 제한 시간이 지나면 요청은 오류로 끝나고, 임베딩은 원문을 보존한 채 다음 재시도 대상으로 남습니다.

`STASH_EMBEDDING_MODEL`이나 `STASH_VECTOR_DIM`을 바꾸고 서버를 다시 시작하면 Stash가 변경을 감지합니다. 필요하면 pgvector 열을 새 차원으로 바꾸고, 이전 벡터와 임베딩 캐시를 비운 뒤 살아 있는 모든 에피소드와 팩트를 새 모델의 인덱싱 대기열에 넣습니다. 원문은 삭제하지 않으며 백그라운드 작업자가 새 벡터를 계산하고 실패한 항목을 계속 다시 시도합니다. 수동 작업량만 확인하려면 `stash reindex --dry-run`, 즉시 다시 계산하려면 `stash reindex`를 사용할 수 있습니다.

### 3. agy (Antigravity)
`~/.gemini/config/mcp_config.json`을 통해 설정합니다:
```json
{
  "mcpServers": {
    "stash": {
      "serverUrl": "http://localhost:8080/sse"
    }
  }
}
```

### 4. 범용 MCP 클라이언트 (Cursor, Windsurf, OpenCode, Pi 등)
Streamable HTTP를 지원하면 `http://localhost:8080/mcp`를 사용하세요. 지원하지 않는 클라이언트만 `http://localhost:8080/sse`를 사용합니다.

**Cursor** — `~/.cursor/mcp.json`
```json
{
  "mcpServers": {
    "stash": {
      "url": "http://localhost:8080/mcp"
    }
  }
}
```

**Windsurf** — `~/.codeium/windsurf/mcp_config.json`
```json
{
  "mcpServers": {
    "stash": {
      "url": "http://localhost:8080/mcp"
    }
  }
}
```

### 자동 저장 및 심리스 핸드오프 (Seamless Handoff)
AI 에이전트가 매번 프롬프트 없이도 기계적으로 기억을 저장하고 다음 세션으로 바통을 넘기게 하려면 **[Seamless Agent Handoff Guide](docs/AGENT_HANDOFF.md)**를 읽어보세요. Cursor, Claude Desktop, Antigravity IDE 등에서 자동으로 컨텍스트를 불러오고 저장하도록 강제할 수 있습니다.

## 동작 원리 (What It Does)

Stash는 AI 에이전트와 현실 세계 사이의 인지적 계층(Cognitive layer)입니다. 에피소드(Episodes)는 팩트(Facts)가 되고, 팩트는 관계(Relationships)가 되며, 관계는 패턴(Patterns)이 되고, 마침내 패턴은 지혜(Wisdom)가 됩니다.

9단계의 기억 통합(Consolidation) 파이프라인이 원시 관측 데이터를 팩트, 관계, 인과 고리(Causal links), 패턴, 모순(Contradictions), 목표 추적(Goal tracking), 실패 패턴(Failure patterns), 가설 검증(Hypothesis verification)과 같은 구조화된 지식으로 변환합니다. 각 단계는 마지막 실행 이후의 새로운 데이터만을 처리합니다.

## 공통 작업 지도와 선택형 연결 기능

작업 카드는 기억 데이터와 분리해 저장하고 목표·작업·의존성·연결 자료·작업 기록을 한 그래프로 묶습니다. 프로젝트마다 공통 최상위 목표 하나를 정하고 A-1·A-2와 더 작은 결과로 나눌 수 있습니다. 목표 지도는 기억과 외부 자료가 각 작업을 거쳐 공통 목표로 모이는 흐름, 진행률, 작업 중인 에이전트, 막힌 지점, 다음 행동, 최근 결과를 함께 보여줍니다. Jira, Confluence, Git, 브라우저, 문서, API, 데이터, 장치도 필요한 작업에만 연결할 수 있습니다.

웹 콘솔의 작업 그래프는 `/projects/<프로젝트명>` 네임스페이스를 프로젝트 목록으로 보여줍니다. 프로젝트를 고르면 그 프로젝트와 하위 네임스페이스만 그래프에 표시하고, `모든 프로젝트`를 고르면 `/projects` 아래 작업을 한 그래프로 표시합니다. MCP에서는 `get_work_graph`에 `project: "/projects/myapp"`를 넘겨 한 프로젝트만 조회할 수 있습니다.

소유자가 읽는 작업 계획은 바뀌는 `PLAN.md` 대신 작업 계획 API에 저장합니다. 고정된 구성 요소 아래에 실제 작업, 선행 관계, 선택형 연결 자료, 구현 전 결정 내용을 둡니다. `get_work_plan`이 모두가 보는 현재 계획이며, `create_plan_component`, `update_plan_component`, `create_plan_task`, `update_plan_task`, 작업 상태 도구, `link_plan_components`, `record_plan_decision`으로 갱신합니다. `validate_work_plan`은 설정된 Reasoner 모델로 계획의 의미를 검사하고 최신 결과를 저장합니다. 임베딩 모델은 이때 사용하지 않습니다. 계획 내용이 바뀌면 `get_work_plan.validation.stale`이 이전 검사 결과임을 표시합니다. 일반 이슈 보드는 별도의 로컬 이슈용이며 계획에 속한 카드를 섞어 보여주지 않습니다.

모든 Web MCP 에이전트는 `resume_project(namespace, agent_id, capabilities)`로 시작합니다. 로컬 경로, Git 저장소, MCP Roots 없이도 자신이 진행 중인 작업과 지금 맡을 수 있는 후보를 최대 3개씩 받습니다. 항목을 고른 뒤 `resume_work`를 호출하면 공통 목표 경로, 다음 행동, 남은 완료 조건, 관련 기억, 짧은 자료 요약, 끝난 선행 작업의 결과, 막는 작업만 받습니다. 같은 `context_digest`를 다시 보내면 바뀌지 않은 내용을 반복하지 않습니다. 실제 행동 직전에는 `claim_work`로 작업권 하나를 받습니다.

작업 중 새 결과가 필요해지면 `spawn_work`로 자식 작업, 선행 작업, 관련 작업을 만듭니다. 자식 작업과 선행 작업은 끝날 때까지 부모를 막으므로 여러 에이전트가 A-1·A-2를 나누어도 공통 목표를 놓치지 않습니다. `finish_work`는 필수 조건에 근거가 연결되고 막는 작업이 모두 끝난 뒤에만 성공하며, 충족된 자식 목표를 부모 목표에 반영합니다.

`attach_work_resource`는 외부 원문 전체를 복사하지 않고 짧은 요약과 주소만 연결합니다. 사람의 Jira 작업과 Confluence 문서는 외부 시스템을 기준으로 유지하고, AI 작업은 Stash에 별도로 기록한 뒤 목표 지도에서 함께 볼 수 있습니다. 현재 버전은 어느 서비스에도 묶이지 않는 자료 연결 구조를 제공합니다. 외부 변경 감지와 다시 쓰기는 필요할 때 붙이는 부가 기능입니다.

Git 워크트리 명령은 코드 프로젝트용 선택 기능으로 남아 있습니다. Web MCP 작업을 시작하기 위한 조건은 아닙니다.

```bash
stash workspace facts --cwd . --agent-id codex --project-namespace /projects/myapp
stash worktree sync --repo . --namespace /projects/myapp
stash worktree list --namespaces /projects/myapp
stash issue create --namespace / "로그인 오류" --type bug --labels auth,login
stash issue list --namespaces / --status doing --label auth
stash issue comment add W-000001 --body "재현 조건을 확인했습니다"
```

영문 에이전트 규칙 예시는 [docs/AGENT.md](docs/AGENT.md)에서 복사할 수 있습니다. 기본 순서는 `resume_project` → `resume_work` → `claim_work`이며, 이후 중간 기록·근거·조건 확인·인계·완료 도구를 사용합니다. 작업 중 다른 결과가 필요해지면 `spawn_work`로 나눕니다. 먼저 프로젝트 네임스페이스와 공통 목표를 만들고, Git이나 외부 서비스는 프로젝트에 필요할 때만 연결하세요.

### 작업 계획 스킬

같은 MCP 작업 절차를 Codex와 Claude 플러그인으로 제공합니다. 먼저 `stash`라는 이름으로 Stash MCP 서버를 등록한 뒤 이 저장소에서 플러그인을 설치합니다.

```bash
claude plugin marketplace add wwwhana/stash
claude plugin install stash-work-plan@stash-tools

codex plugin marketplace add wwwhana/stash
codex plugin add stash-work-plan@stash-tools
```

Claude에서는 `/stash-work-plan:stash-work-plan`, Codex에서는 `$stash-work-plan`으로 실행합니다.

Streamable HTTP `/mcp`에서는 SEP-2640 초안의 `io.modelcontextprotocol/skills` 확장으로 내장 `stash-work` 규칙도 제공합니다. 초안을 지원하는 클라이언트는 `skills/list`로 찾을 수 있고, 아직 지원하지 않는 클라이언트는 위 Codex·Claude 플러그인을 사용하면 됩니다. 실제 사용자 정의 메서드를 받을 수 있는 `/mcp`에서만 이 확장을 알립니다.

## 라이선스

Apache 2.0
