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
로컬 프로세스는 `stdio` 전송 방식으로 stash CLI 바이너리를 직접 가리킵니다:
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

`stash serve`는 MCP, 관리 화면, OAuth 경로, 운영 지표, 상태 확인을 하나의 HTTP 포트(기본 `:8080`)에서 제공합니다. Prometheus 지표는 `http://localhost:8080/metrics`에서 확인하고, `/healthz`는 데이터베이스 연결을, `/readyz`는 준비 상태를 확인합니다. HTTP 요청, 인증 결과, MCP 도구 호출, 네임스페이스 범위 적용, 기억 통합 작업을 기록합니다. 요청·인증·도구·범위 지표의 라벨에는 사용자 ID와 원본 네임스페이스 이름을 넣지 않습니다.

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

### 4. 범용 SSE 클라이언트 (Cursor, Windsurf, OpenCode, Pi 등)
MCP를 지원하는 모든 클라이언트의 SSE URL을 `http://localhost:8080/sse`로 지정하세요.

**Cursor** — `~/.cursor/mcp.json`
```json
{
  "mcpServers": {
    "stash": {
      "url": "http://localhost:8080/sse"
    }
  }
}
```

**Windsurf** — `~/.codeium/windsurf/mcp_config.json`
```json
{
  "mcpServers": {
    "stash": {
      "url": "http://localhost:8080/sse"
    }
  }
}
```

### 자동 저장 및 심리스 핸드오프 (Seamless Handoff)
AI 에이전트가 매번 프롬프트 없이도 기계적으로 기억을 저장하고 다음 세션으로 바통을 넘기게 하려면 **[Seamless Agent Handoff Guide](docs/AGENT_HANDOFF.md)**를 읽어보세요. Cursor, Claude Desktop, Antigravity IDE 등에서 자동으로 컨텍스트를 불러오고 저장하도록 강제할 수 있습니다.

## 동작 원리 (What It Does)

Stash는 AI 에이전트와 현실 세계 사이의 인지적 계층(Cognitive layer)입니다. 에피소드(Episodes)는 팩트(Facts)가 되고, 팩트는 관계(Relationships)가 되며, 관계는 패턴(Patterns)이 되고, 마침내 패턴은 지혜(Wisdom)가 됩니다.

9단계의 기억 통합(Consolidation) 파이프라인이 원시 관측 데이터를 팩트, 관계, 인과 고리(Causal links), 패턴, 모순(Contradictions), 목표 추적(Goal tracking), 실패 패턴(Failure patterns), 가설 검증(Hypothesis verification)과 같은 구조화된 지식으로 변환합니다. 각 단계는 마지막 실행 이후의 새로운 데이터만을 처리합니다.

## 작업 그래프와 Git 워크트리

작업 카드는 기억 데이터와 분리해 저장하고, 목표·작업·의존성·워크트리·작업 이벤트를 하나의 그래프로 연결할 수 있습니다. 이슈 종류(버그·기능·작업), 라벨, 담당자, 댓글도 함께 관리하므로 별도 서비스 없이 로컬 이슈 트래커로 사용할 수 있습니다. 같은 데이터가 상태별 칸반 보드와 의존성 그래프로 표시됩니다. 작업 카드는 관련 사실·실패·가설에도 연결할 수 있어, 에이전트가 작업을 이어받을 때 필요한 근거를 함께 불러옵니다.

소유자가 읽는 작업 계획은 바뀌는 `PLAN.md` 대신 작업 계획 API에 저장합니다. 계획은 5~9개의 고정된 구성 요소로 만들고, 각 구성 요소에는 맡는 경로, 실제 작업, 선행 관계, 워크트리, 구현 전 결정 내용을 넣습니다. `get_work_plan`이 모두가 보는 현재 계획이며, `create_plan_component`, `update_plan_component`, `create_plan_task`, `update_plan_task`, 작업 상태 도구, `link_plan_components`, `record_plan_decision`으로 갱신합니다. `validate_work_plan`은 설정된 Reasoner 모델로 계획의 의미를 검사하고 최신 결과를 저장합니다. 임베딩 모델은 이때 사용하지 않습니다. 계획 내용이 바뀌면 `get_work_plan.validation.stale`이 이전 검사 결과임을 표시합니다. 일반 이슈 보드는 별도의 로컬 이슈용이며 계획에 속한 카드를 섞어 보여주지 않습니다.

로컬 에이전트는 현재 저장소의 워크트리를 Stash에 동기화할 수 있습니다. 코드와 실제 변경 내용은 Git에 남고, Stash에는 경로·브랜치·커밋·상태·작업 기록이 저장됩니다.

```bash
stash worktree sync --repo . --namespace /projects/myapp
stash worktree list --namespaces /projects/myapp
stash issue create --namespace / "로그인 오류" --type bug --labels auth,login
stash issue list --namespaces / --status doing --label auth
stash issue comment add W-000001 --body "재현 조건을 확인했습니다"
```

영문 에이전트 규칙 예시는 [docs/AGENT.md](docs/AGENT.md)에서 복사할 수 있습니다. 작업 계획에는 `get_work_plan`, `validate_work_plan`, `create_plan_component`, `update_plan_component`, `create_plan_task`, `update_plan_task`, `start_plan_task`, `complete_plan_task`, `block_plan_task`, `unblock_plan_task`, `set_plan_component_paths`, `link_plan_components`, `record_plan_decision`을 사용합니다. `create_work_item`, `list_work_items`, `add_work_item_dependency`, `get_work_graph`, `add_work_item_comment`, `list_work_item_comments`, `link_work_item_memory`, `list_work_item_memory_links`, `list_worktrees`, `record_work_event`는 로컬 이슈 관리에 계속 사용할 수 있습니다. 처음 한 번 `init`을 호출하면 기본 작업 공간이 만들어집니다.

### 작업 계획 스킬

같은 MCP 작업 절차를 Codex와 Claude 플러그인으로 제공합니다. 먼저 `stash`라는 이름으로 Stash MCP 서버를 등록한 뒤 이 저장소에서 플러그인을 설치합니다.

```bash
claude plugin marketplace add wwwhana/stash
claude plugin install stash-work-plan@stash-tools

codex plugin marketplace add wwwhana/stash
codex plugin add stash-work-plan@stash-tools
```

Claude에서는 `/stash-work-plan:stash-work-plan`, Codex에서는 `$stash-work-plan`으로 실행합니다.

## 라이선스

Apache 2.0
