# Stash (스태시)

[🇺🇸 English](README.md) | [🇰🇷 한국어](README.ko.md)

> **안내:** 이 프로젝트는 원본 [alash3al/stash](https://github.com/alash3al/stash)의 기능 개선 포크(Fork) 버전입니다. 
> 현재 버전(`wwwhana/stash`)은 하이브리드 검색(벡터 + Trigram RRF), 결정론적 에피소드 클러스터링, 안전한 Soft-delete 수명 주기, 그리고 고급 MCP 자동화 훅(Auto-hydration) 등 중요한 개선 사항들을 포함하고 있습니다.

**당신의 AI가 겪는 건망증, 우리가 해결했습니다.**

모든 LLM은 매 대화마다 처음부터 다시 시작합니다. Stash는 에이전트에게 영구적인 기억(Memory)을 부여하여, 여러 세션에 걸쳐 기억하고, 떠올리고, 통합하고, 학습하게 합니다. 더 이상 처음부터 배경을 설명할 필요가 없습니다.

오픈 소스. 자체 호스팅(Self-hosted). MCP를 지원하는 모든 에이전트와 호환됩니다.

---

> **직접 호스팅하기 번거로우신가요?**
> **[usestash.io](https://usestash.io)** 클라우드 버전을 이용해 보세요. 구글로 로그인하고 MCP URL 하나만 복사하면 끝입니다. 무료로 시작할 수 있습니다.

---

## 빠른 시작 (Quick Start)

```bash
git clone https://github.com/wwwhana/stash.git
cd stash
cp .env.example .env   # API 키와 모델을 수정하세요
docker compose up
```

이것이 전부입니다. Postgres + pgvector, 마이그레이션, 그리고 백그라운드 기억 통합(Consolidation)을 수행하는 MCP 서버가 단일 명령어로 모두 실행됩니다.

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

원격 MCP 서버를 OIDC로 보호할 때는 Streamable HTTP와 OAuth를 사용합니다:
```bash
codex mcp add stash --url https://stash.example.com/mcp --oauth-client-id stash-codex
codex mcp login stash
```

`STASH_AUTH_MODE=oidc`와 발급자·클라이언트 설정을 지정하고,
Codex에서 사용할 OAuth 클라이언트를 `STASH_AUTH_MCP_CLIENT_ID`에 넣습니다.

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

## Stash Cloud (베타 — 무료)

Stash의 호스팅된 멀티테넌트 버전이 **[usestash.io](https://usestash.io/)**에 제공되며, 베타 기간 동안 무료로 사용할 수 있습니다.

이 클라우드 버전은 처음부터 새로 작성되었으며 현재 레포지토리와 코드를 공유하지 않습니다. 확장성, 멀티테넌시, 그리고 제품으로서의 장기적인 지속 가능성을 고려하여 설계되었습니다. 제공되는 기능 세트도 양방향으로 다릅니다 (오픈 소스에만 있는 기능이 클라우드에 없기도 하고 그 반대이기도 합니다).

## 더 알아보기

**[alash3al.github.io/stash →](https://alash3al.github.io/stash/)**

## 라이선스

Apache 2.0
