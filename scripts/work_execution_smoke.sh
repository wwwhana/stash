#!/usr/bin/env bash

set -Eeuo pipefail

usage() {
    cat <<'EOF'
Usage: scripts/work_execution_smoke.sh

Builds the current checkout into a disposable local image, starts an isolated
pgvector PostgreSQL and Stash server, then exercises the MCP work-execution flow.

Environment variables:
  STASH_SMOKE_IMAGE             Existing local image to use instead of a disposable build
  STASH_SMOKE_BUILD_IMAGE       Set to 1 to rebuild STASH_SMOKE_IMAGE (default: 0)
  STASH_SMOKE_PGVECTOR_IMAGE    PostgreSQL image (default: pgvector/pgvector:pg16)
  STASH_SMOKE_STARTUP_TIMEOUT   Startup timeout in seconds (default: 120)
EOF
}

if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
    usage
    exit 0
fi
if [[ $# -ne 0 ]]; then
    usage >&2
    exit 2
fi

for dependency in docker curl python3 awk grep; do
    if ! command -v "$dependency" >/dev/null 2>&1; then
        printf 'error: required command not found: %s\n' "$dependency" >&2
        exit 1
    fi
done

if ! docker info >/dev/null 2>&1; then
    printf 'error: Docker is not available; start Docker and try again\n' >&2
    exit 1
fi

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)"
STARTUP_TIMEOUT="${STASH_SMOKE_STARTUP_TIMEOUT:-120}"
PGVECTOR_IMAGE="${STASH_SMOKE_PGVECTOR_IMAGE:-pgvector/pgvector:pg16}"

if ! [[ "$STARTUP_TIMEOUT" =~ ^[1-9][0-9]*$ ]]; then
    printf 'error: STASH_SMOKE_STARTUP_TIMEOUT must be a positive integer\n' >&2
    exit 1
fi

RUN_SUFFIX="$(date -u +%Y%m%d%H%M%S)-$$-${RANDOM}"
NETWORK_NAME="stash-work-execution-smoke-net-${RUN_SUFFIX}"
PG_CONTAINER="stash-work-execution-smoke-pg-${RUN_SUFFIX}"
STASH_CONTAINER="stash-work-execution-smoke-app-${RUN_SUFFIX}"

REMOVE_STASH_IMAGE=0
if [[ -n "${STASH_SMOKE_IMAGE:-}" ]]; then
    STASH_IMAGE="$STASH_SMOKE_IMAGE"
    BUILD_STASH_IMAGE="${STASH_SMOKE_BUILD_IMAGE:-0}"
else
    STASH_IMAGE="stash-work-execution-smoke:${RUN_SUFFIX}"
    BUILD_STASH_IMAGE="${STASH_SMOKE_BUILD_IMAGE:-1}"
    REMOVE_STASH_IMAGE=1
fi

if [[ "$BUILD_STASH_IMAGE" != "0" && "$BUILD_STASH_IMAGE" != "1" ]]; then
    printf 'error: STASH_SMOKE_BUILD_IMAGE must be 0 or 1\n' >&2
    exit 1
fi

TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/stash-work-execution-smoke.XXXXXX")"

cleanup() {
    local status=$?
    trap - EXIT INT TERM HUP

    docker rm -f "$STASH_CONTAINER" >/dev/null 2>&1 || true
    docker rm -f "$PG_CONTAINER" >/dev/null 2>&1 || true
    docker network rm "$NETWORK_NAME" >/dev/null 2>&1 || true
    if [[ "$REMOVE_STASH_IMAGE" == "1" ]]; then
        docker image rm "$STASH_IMAGE" >/dev/null 2>&1 || true
    fi
    rm -rf -- "$TMP_DIR"
    exit "$status"
}

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

say() {
    printf '[work-execution-smoke] %s\n' "$*"
}

container_logs() {
    local container=$1
    if docker inspect "$container" >/dev/null 2>&1; then
        printf '\n--- %s logs ---\n' "$container" >&2
        docker logs --tail 200 "$container" >&2 || true
    fi
}

die() {
    printf 'error: %s\n' "$*" >&2
    container_logs "$STASH_CONTAINER"
    container_logs "$PG_CONTAINER"
    exit 1
}

host_port() {
    local container=$1
    local container_port=$2
    local mapping

    mapping="$(docker port "$container" "${container_port}/tcp" | awk 'NR == 1 { print; exit }')"
    if [[ -z "$mapping" || "$mapping" == "$container_port" ]]; then
        return 1
    fi
    printf '%s\n' "${mapping##*:}"
}

wait_for_postgres() {
    local deadline=$((SECONDS + STARTUP_TIMEOUT))
    # pg_isready can report that the temporary init server accepts connections
    # before the requested database exists. Probe the exact database instead.
    until docker exec "$PG_CONTAINER" \
        psql -U stash_smoke -d stash_smoke -tAc 'SELECT 1' >/dev/null 2>&1; do
        if [[ "$(docker inspect --format '{{.State.Running}}' "$PG_CONTAINER" 2>/dev/null || true)" != "true" ]]; then
            die "PostgreSQL container stopped before becoming ready"
        fi
        if (( SECONDS >= deadline )); then
            die "PostgreSQL did not become ready within ${STARTUP_TIMEOUT}s"
        fi
        sleep 1
    done
}

wait_for_stash() {
    local deadline=$((SECONDS + STARTUP_TIMEOUT))
    until curl --silent --show-error --fail --max-time 2 "${STASH_BASE_URL}/readyz" >/dev/null 2>&1; do
        if [[ "$(docker inspect --format '{{.State.Running}}' "$STASH_CONTAINER" 2>/dev/null || true)" != "true" ]]; then
            die "Stash container stopped before becoming ready"
        fi
        if (( SECONDS >= deadline )); then
            die "Stash did not become ready within ${STARTUP_TIMEOUT}s"
        fi
        sleep 1
    done
}

json_object() {
    python3 -c '
import json
import sys

parts = sys.argv[1:]
if len(parts) % 3:
    raise SystemExit("json_object expects kind/key/value triples")

result = {}
for index in range(0, len(parts), 3):
    kind, key, value = parts[index:index + 3]
    if kind == "s":
        result[key] = value
    elif kind == "n":
        result[key] = int(value)
    elif kind == "f":
        result[key] = float(value)
    elif kind == "b":
        result[key] = value.lower() == "true"
    elif kind == "j":
        result[key] = json.loads(value)
    else:
        raise SystemExit(f"unsupported JSON value kind: {kind}")
print(json.dumps(result, separators=(",", ":")))
' "$@"
}

normalize_mcp_body() {
    python3 -c '
import json
import sys

text = sys.stdin.read().strip()
if not text:
    raise SystemExit("empty MCP response")
try:
    value = json.loads(text)
except json.JSONDecodeError:
    events = []
    current = []
    for line in text.splitlines():
        if line.startswith("data:"):
            current.append(line[5:].lstrip())
        elif not line.strip() and current:
            events.append("\n".join(current))
            current = []
    if current:
        events.append("\n".join(current))
    value = None
    for candidate in reversed(events):
        try:
            value = json.loads(candidate)
            break
        except json.JSONDecodeError:
            pass
    if value is None:
        raise SystemExit("MCP response is neither JSON nor JSON-bearing SSE")
print(json.dumps(value, separators=(",", ":")))
'
}

mcp_result_value() {
    python3 -c '
import json
import sys

response = json.load(sys.stdin)
if "error" in response:
    error = response["error"]
    raise SystemExit("JSON-RPC error {}: {}".format(error.get("code"), error.get("message", error)))
result = response.get("result", {})
if result.get("isError"):
    texts = [entry.get("text", "") for entry in result.get("content", []) if entry.get("type") == "text"]
    raise SystemExit("; ".join(filter(None, texts)) or "MCP tool returned an error")
texts = [entry.get("text", "") for entry in result.get("content", []) if entry.get("type") == "text"]
if not texts:
    print("null")
    raise SystemExit(0)
text = texts[0]
try:
    value = json.loads(text)
except json.JSONDecodeError:
    value = text
print(json.dumps(value, separators=(",", ":")))
'
}

mcp_error_text() {
    python3 -c '
import json
import sys

response = json.load(sys.stdin)
if "error" in response:
    error = response["error"]
    print("JSON-RPC error {}: {}".format(error.get("code"), error.get("message", error)))
    raise SystemExit(0)
result = response.get("result", {})
if result.get("isError"):
    texts = [entry.get("text", "") for entry in result.get("content", []) if entry.get("type") == "text"]
    print("; ".join(filter(None, texts)) or "MCP tool returned an error")
    raise SystemExit(0)
raise SystemExit(1)
'
}

json_get() {
    local payload=$1
    local path=$2
    printf '%s' "$payload" | python3 -c '
import json
import sys

value = json.load(sys.stdin)
for part in sys.argv[1].split("."):
    if not part:
        continue
    if isinstance(value, list):
        value = value[int(part)]
    else:
        value = value[part]
if value is None:
    print("")
elif isinstance(value, bool):
    print("true" if value else "false")
elif isinstance(value, (dict, list)):
    print(json.dumps(value, separators=(",", ":")))
else:
    print(value)
' "$path"
}

json_get_optional() {
    local payload=$1
    local path=$2
    printf '%s' "$payload" | python3 -c '
import json
import sys

value = json.load(sys.stdin)
for part in sys.argv[1].split("."):
    if not part:
        continue
    if isinstance(value, list):
        index = int(part)
        value = value[index] if 0 <= index < len(value) else None
    elif isinstance(value, dict):
        value = value.get(part)
    else:
        value = None
    if value is None:
        break
if value is None:
    print("")
elif isinstance(value, bool):
    print("true" if value else "false")
elif isinstance(value, (dict, list)):
    print(json.dumps(value, separators=(",", ":")))
else:
    print(value)
' "$path"
}

canonical_json() {
    printf '%s' "$1" | python3 -c '
import json
import sys
print(json.dumps(json.load(sys.stdin), sort_keys=True, separators=(",", ":")))
'
}

validate_tool_schemas() {
    python3 -c '
import json
import sys

response = json.load(sys.stdin)
if "error" in response:
    raise SystemExit("tools/list failed: {}".format(response["error"]))
tools = {tool.get("name"): tool for tool in response.get("result", {}).get("tools", [])}
expected = {
    "create_namespace": {"slug", "name"},
    "create_goal": {"content"},
    "set_project_goal": {"goal_id"},
    "create_work_item": {"namespace", "title"},
    "resume_project": {"namespace", "agent_id", "capabilities", "known_context_digest"},
    "claim_work": {"work_item_id", "agent_id", "lease_seconds", "action_key"},
    "set_work_capabilities": {"work_item_id", "capabilities"},
    "spawn_work": {"attempt_id", "lease_token", "action_key", "title", "relationship", "next_action", "conditions", "capabilities"},
    "attach_work_resource": {"work_item_id", "resource_key", "kind", "source", "authority", "title", "uri", "summary", "role", "metadata"},
    "list_work_resources": {"work_item_id"},
    "get_work_resource": {"resource_id"},
    "get_goal_map": {"namespace"},
    "resolve_workspace": {"cwd", "repository_instance_id", "git_common_dir", "git_dir", "worktree_path"},
    "resume_workspace": {"namespace"},
    "claim_workspace": {"work_item_id", "cwd", "repository_instance_id", "git_common_dir", "git_dir", "worktree_path", "agent_id", "lease_seconds", "action_key"},
    "resume_work": {"work_item_id", "known_context_digest", "expected_context_digest", "fact_offset"},
    "prepare_work": {"work_item_id", "next_action", "conditions", "action_key"},
    "start_work": {"work_item_id", "agent_id", "lease_seconds", "action_key"},
    "checkpoint_work": {"attempt_id", "lease_token", "summary", "result", "next_action", "lease_seconds", "action_key"},
    "submit_work_evidence": {"attempt_id", "lease_token", "evidence_type", "summary", "reference", "payload", "condition_ids", "action_key"},
    "verify_work_condition": {"attempt_id", "lease_token", "condition_id", "status", "evidence_ids", "action_key"},
    "renew_work_lease": {"attempt_id", "lease_token", "lease_seconds", "action_key"},
    "handoff_work": {"attempt_id", "lease_token", "summary", "result", "next_action", "action_key"},
    "finish_work": {"attempt_id", "lease_token", "summary", "result", "action_key"},
    "remember_work": {"work_item_id", "content", "relation", "action_key"},
}

missing_tools = sorted(set(expected) - set(tools))
if missing_tools:
    raise SystemExit("server is missing work-execution tools: " + ", ".join(missing_tools))

schema_errors = []
for name, properties in expected.items():
    schema = tools[name].get("inputSchema", {})
    actual = set(schema.get("properties", {}))
    missing = sorted(properties - actual)
    if missing:
        schema_errors.append("{} missing properties: {}".format(name, ", ".join(missing)))
if schema_errors:
    raise SystemExit("live MCP schemas do not match the smoke flow:\n  " + "\n  ".join(schema_errors))

for name in expected:
    schema = tools[name].get("inputSchema", {})
    print(name + " " + json.dumps(schema, sort_keys=True, separators=(",", ":")))
' >"$TMP_DIR/work-execution-tool-schemas.txt"
}

validate_resource_templates() {
    python3 -c '
import json
import sys

response = json.load(sys.stdin)
if "error" in response:
    raise SystemExit("resources/templates/list failed: {}".format(response["error"]))
templates = {
    template.get("uriTemplate")
    for template in response.get("result", {}).get("resourceTemplates", [])
}
expected = {"stash://work/{id}/brief", "stash://work-resource/{id}"}
missing = sorted(expected - templates)
if missing:
    raise SystemExit("server is missing work resource templates: " + ", ".join(missing))
print("\n".join(sorted(templates)))
' >"$TMP_DIR/work-resource-templates.txt"
}

REQUEST_ID=0
SESSION_ID=""
MCP_RESPONSE=""
MCP_VALUE=""
MCP_ERROR=""

mcp_request() {
    local method=$1
    local params=$2
    local notification=${3:-0}
    local request_number
    local payload
    local headers_file
    local body_file
    local http_code
    local response_session
    local -a curl_args

    REQUEST_ID=$((REQUEST_ID + 1))
    request_number=$REQUEST_ID
    headers_file="$TMP_DIR/headers-${request_number}.txt"
    body_file="$TMP_DIR/body-${request_number}.txt"

    if [[ "$notification" == "1" ]]; then
        payload="$(python3 -c 'import json,sys; print(json.dumps({"jsonrpc":"2.0","method":sys.argv[1],"params":json.loads(sys.argv[2])},separators=(",",":")))' "$method" "$params")"
    else
        payload="$(python3 -c 'import json,sys; print(json.dumps({"jsonrpc":"2.0","id":int(sys.argv[1]),"method":sys.argv[2],"params":json.loads(sys.argv[3])},separators=(",",":")))' "$request_number" "$method" "$params")"
    fi

    curl_args=(
        --silent --show-error
        --request POST
        --header 'Accept: application/json, text/event-stream'
        --header 'Content-Type: application/json'
        --dump-header "$headers_file"
        --output "$body_file"
        --write-out '%{http_code}'
        --data-binary "$payload"
    )
    if [[ -n "$SESSION_ID" ]]; then
        curl_args+=(--header "Mcp-Session-Id: $SESSION_ID")
    fi

    if ! http_code="$(curl "${curl_args[@]}" "$MCP_URL")"; then
        die "MCP request failed at the HTTP transport: $method"
    fi

    response_session="$(awk '
        tolower($1) == "mcp-session-id:" {
            sub(/^[^:]*:[[:space:]]*/, "")
            gsub(/\r/, "")
            value = $0
        }
        END { print value }
    ' "$headers_file")"
    if [[ -n "$response_session" ]]; then
        SESSION_ID="$response_session"
    fi

    if [[ ! "$http_code" =~ ^2[0-9][0-9]$ ]]; then
        die "MCP request $method returned HTTP $http_code: $(tr '\n' ' ' <"$body_file")"
    fi
    if [[ "$notification" == "1" ]]; then
        MCP_RESPONSE=""
        return 0
    fi
    if ! MCP_RESPONSE="$(normalize_mcp_body <"$body_file")"; then
        die "could not parse MCP response for $method"
    fi
}

mcp_call() {
    local tool=$1
    local arguments=$2
    local params

    params="$(json_object s name "$tool" j arguments "$arguments")"
    mcp_request "tools/call" "$params"
    if ! MCP_VALUE="$(printf '%s' "$MCP_RESPONSE" | mcp_result_value)"; then
        die "MCP tool failed: $tool"
    fi
}

mcp_call_expect_error() {
    local tool=$1
    local arguments=$2
    local params

    params="$(json_object s name "$tool" j arguments "$arguments")"
    mcp_request "tools/call" "$params"
    if MCP_ERROR="$(printf '%s' "$MCP_RESPONSE" | mcp_error_text)"; then
        return 0
    fi
    die "MCP tool unexpectedly succeeded: $tool"
}

if [[ "$BUILD_STASH_IMAGE" == "1" ]]; then
    say "building local Stash image $STASH_IMAGE"
    docker build --tag "$STASH_IMAGE" "$REPO_ROOT"
fi
if ! docker image inspect "$STASH_IMAGE" >/dev/null 2>&1; then
    die "local Stash image not found: $STASH_IMAGE"
fi

say "creating isolated Docker network"
docker network create --label "stash.work-execution-smoke.run=$RUN_SUFFIX" "$NETWORK_NAME" >/dev/null

say "starting isolated pgvector PostgreSQL"
docker run --detach \
    --name "$PG_CONTAINER" \
    --label "stash.work-execution-smoke.run=$RUN_SUFFIX" \
    --network "$NETWORK_NAME" \
    --publish '127.0.0.1::5432' \
    --env POSTGRES_DB=stash_smoke \
    --env POSTGRES_USER=stash_smoke \
    --env POSTGRES_PASSWORD=stash_smoke_password \
    "$PGVECTOR_IMAGE" >/dev/null
wait_for_postgres
PG_HOST_PORT="$(host_port "$PG_CONTAINER" 5432)" || die "could not resolve the dynamic PostgreSQL port"

say "starting Stash from the local image"
docker run --detach \
    --name "$STASH_CONTAINER" \
    --label "stash.work-execution-smoke.run=$RUN_SUFFIX" \
    --network "$NETWORK_NAME" \
    --publish '127.0.0.1::8080' \
    --env "STASH_POSTGRES_DSN=postgresql://stash_smoke:stash_smoke_password@${PG_CONTAINER}:5432/stash_smoke?sslmode=disable" \
    --env STASH_VECTOR_DIM=3 \
    --env STASH_MAX_RESULT_SIZE=1000 \
    --env STASH_OPENAI_BASE_URL=http://127.0.0.1:9/v1 \
    --env STASH_OPENAI_API_KEY= \
    --env STASH_EMBEDDING_MODEL=smoke-embedding \
    --env STASH_REASONER_MODEL=smoke-reasoner \
    --env STASH_CONTEXT_TTL=1h \
    --env STASH_HTTP_ADDR=:8080 \
    --env STASH_LOG_LEVEL=info \
    --env STASH_LOG_FORMAT=text \
    --env STASH_AUTH_MODE=none \
    "$STASH_IMAGE" mcp serve --host 0.0.0.0 --port 8080 >/dev/null

STASH_HOST_PORT="$(host_port "$STASH_CONTAINER" 8080)" || die "could not resolve the dynamic Stash port"
STASH_BASE_URL="http://127.0.0.1:${STASH_HOST_PORT}"
MCP_URL="${STASH_BASE_URL}/mcp"
wait_for_stash
say "PostgreSQL is on 127.0.0.1:${PG_HOST_PORT}; Stash is on ${STASH_BASE_URL}"

say "initializing MCP Streamable HTTP session"
INITIALIZE_PARAMS="$(json_object \
    s protocolVersion 2025-06-18 \
    j capabilities '{}' \
    j clientInfo '{"name":"stash-work-execution-smoke","version":"1"}')"
mcp_request initialize "$INITIALIZE_PARAMS"
if [[ -z "$SESSION_ID" ]]; then
    die "initialize succeeded without an Mcp-Session-Id response header"
fi
mcp_request notifications/initialized '{}' 1

say "reading live MCP tool schemas"
mcp_request tools/list '{}'
if ! printf '%s' "$MCP_RESPONSE" | validate_tool_schemas; then
    die "work-execution tool schemas are not available or do not match the harness"
fi

say "reading live MCP resource templates"
mcp_request resources/templates/list '{}'
if ! printf '%s' "$MCP_RESPONSE" | validate_resource_templates; then
    die "work resource templates are not available"
fi

PROJECT_NAMESPACE="/projects/work-execution-smoke-${RUN_SUFFIX}"
AGENT_A="smoke-agent-a-${RUN_SUFFIX}"
AGENT_B="smoke-agent-b-${RUN_SUFFIX}"
ACTION_PREFIX="work-execution-smoke-${RUN_SUFFIX}"
ACTION_PREFIX="${ACTION_PREFIX}-$(python3 -c 'import uuid; print(uuid.uuid4())')"

say "creating project namespace and issue"
ARGS="$(json_object \
    s slug "$PROJECT_NAMESPACE" \
    s name "Work execution smoke ${RUN_SUFFIX}" \
    s description "Isolated namespace created by scripts/work_execution_smoke.sh")"
mcp_call create_namespace "$ARGS"
PROJECT_NAMESPACE_ID="$(json_get "$MCP_VALUE" id)"

say "creating the shared project goal"
ARGS="$(json_object \
    s namespace "$PROJECT_NAMESPACE" \
    s content "Complete project A through coordinated work" \
    n priority 10)"
mcp_call create_goal "$ARGS"
ROOT_GOAL_ID="$(json_get "$MCP_VALUE" id)"
mcp_call set_project_goal "$(json_object s namespace "$PROJECT_NAMESPACE" n goal_id "$ROOT_GOAL_ID" s set_by smoke-owner)"

say "creating Git-independent prepared work"
GENERIC_CONDITIONS='[{"kind":"custom","description":"The generic Web MCP coordination path is observed","verification":{"check":"resume, claim, spawn, resource, and map agree"},"required":true}]'
ARGS="$(json_object \
    s namespace "$PROJECT_NAMESPACE" \
    n goal_id "$ROOT_GOAL_ID" \
    s title "Coordinate project A without a local workspace" \
    s description "Disposable generic work used by the isolated Web MCP smoke harness" \
    s issue_type task \
    s status ready \
    s reporter "$AGENT_A" \
    j capabilities '["code","browser"]')"
mcp_call create_work_item "$ARGS"
GENERIC_WORK_ITEM_ID="$(json_get "$MCP_VALUE" id)"
mcp_call prepare_work "$(json_object \
    n work_item_id "$GENERIC_WORK_ITEM_ID" \
    s next_action "Claim this item without Git facts" \
    j conditions "$GENERIC_CONDITIONS" \
    s action_key "${ACTION_PREFIX}:prepare-generic")"

say "resuming the project without Git, a path, or MCP Roots"
mcp_call resume_project "$(json_object \
    s namespace "$PROJECT_NAMESPACE" \
    s agent_id "$AGENT_A" \
    j capabilities '["code","browser"]')"
if [[ "$(json_get "$MCP_VALUE" shared_goal.id)" != "$ROOT_GOAL_ID" || "$(json_get "$MCP_VALUE" ready_work.0.id)" != "$GENERIC_WORK_ITEM_ID" ]]; then
    die "resume_project did not return the shared goal and generic runnable item"
fi
PROJECT_CONTEXT_DIGEST="$(json_get "$MCP_VALUE" context_digest)"
mcp_call resume_project "$(json_object \
    s namespace "$PROJECT_NAMESPACE" \
    s agent_id "$AGENT_A" \
    j capabilities '["code","browser"]' \
    s known_context_digest "$PROJECT_CONTEXT_DIGEST")"
if [[ "$(json_get "$MCP_VALUE" unchanged)" != "true" ]]; then
    die "resume_project did not return an unchanged digest receipt"
fi

say "claiming generic work without workspace metadata"
mcp_call claim_work "$(json_object \
    n work_item_id "$GENERIC_WORK_ITEM_ID" \
    s agent_id "$AGENT_A" \
    n lease_seconds 600 \
    s action_key "${ACTION_PREFIX}:generic-claim")"
GENERIC_ATTEMPT_ID="$(json_get "$MCP_VALUE" attempt.id)"
GENERIC_LEASE_TOKEN="$(json_get "$MCP_VALUE" lease_token)"
if [[ -n "$(json_get_optional "$MCP_VALUE" attempt.worktree_id)" ]]; then
    die "generic claim unexpectedly attached a worktree"
fi

say "attaching an external human-work reference"
ARGS="$(json_object \
    n work_item_id "$GENERIC_WORK_ITEM_ID" \
    s resource_key jira:APP-12 \
    s kind ticket \
    s source jira \
    s authority external \
    s title "APP-12 human review" \
    s uri https://jira.example.invalid/browse/APP-12 \
    s summary "Human review state remains authoritative in Jira" \
    s external_id APP-12 \
    s revision 7 \
    s role input \
    j metadata '{"status":"In Progress"}')"
mcp_call attach_work_resource "$ARGS"
GENERIC_RESOURCE_ID="$(json_get "$MCP_VALUE" resource.id)"
mcp_call list_work_resources "$(json_object n work_item_id "$GENERIC_WORK_ITEM_ID")"
if [[ "$(json_get "$MCP_VALUE" 0.id)" != "$GENERIC_RESOURCE_ID" || "$(json_get "$MCP_VALUE" 0.authority)" != "external" ]]; then
    die "the external resource reference was not returned for generic work"
fi

say "seeding one linked fact for bounded context verification"
GENERIC_FACT_ID="$(docker exec "$PG_CONTAINER" \
    psql -U stash_smoke -d stash_smoke -tAc \
    "WITH inserted AS (
         INSERT INTO facts (namespace_id, content, entity, property, value)
         VALUES (${PROJECT_NAMESPACE_ID}, 'The human review stays authoritative in Jira APP-12', 'jira:APP-12', 'authority', 'external')
         RETURNING id
     )
     INSERT INTO work_item_memory_links (work_item_id, memory_type, memory_id, relation)
     SELECT ${GENERIC_WORK_ITEM_ID}, 'fact', id, 'constraint' FROM inserted
     RETURNING memory_id" | awk 'NR == 1 { gsub(/[[:space:]]/, ""); print; exit }')"
if ! [[ "$GENERIC_FACT_ID" =~ ^[1-9][0-9]*$ ]]; then
    die "could not seed the linked work fact"
fi

say "spawning a prerequisite under the active generic attempt"
SPAWN_CONDITIONS='[{"kind":"custom","description":"The source fact is recorded","verification":{"check":"source and observed result are present"},"required":true}]'
mcp_call spawn_work "$(json_object \
    n attempt_id "$GENERIC_ATTEMPT_ID" \
    s lease_token "$GENERIC_LEASE_TOKEN" \
    s action_key "${ACTION_PREFIX}:spawn-research" \
    s relationship prerequisite \
    s title "Research the prerequisite" \
    s next_action "Read the official source and record the result" \
    j capabilities '["research"]' \
    j conditions "$SPAWN_CONDITIONS")"
GENERIC_PREREQUISITE_ID="$(json_get "$MCP_VALUE" work_item.id)"
GENERIC_PREREQUISITE_CONDITION_ID="$(json_get "$MCP_VALUE" preparation.completion_conditions.0.id)"
if [[ "$(json_get "$MCP_VALUE" edge.from_item_id)" != "$GENERIC_PREREQUISITE_ID" || "$(json_get "$MCP_VALUE" edge.to_item_id)" != "$GENERIC_WORK_ITEM_ID" || "$(json_get "$MCP_VALUE" edge.edge_type)" != "blocks" ]]; then
    die "spawn_work did not create the prerequisite blocker edge"
fi

mcp_call resume_work "$(json_object n work_item_id "$GENERIC_WORK_ITEM_ID")"
GENERIC_PARENT_CONDITION_ID="$(json_get "$MCP_VALUE" completion_conditions.0.id)"
if [[ "$(json_get "$MCP_VALUE" blockers.0.id)" != "$GENERIC_PREREQUISITE_ID" || "$(json_get "$MCP_VALUE" resources.0.id)" != "$GENERIC_RESOURCE_ID" ]]; then
    die "the generic work brief omitted its blocker or bounded resource"
fi
GENERIC_CONTEXT_DIGEST="$(json_get "$MCP_VALUE" context_digest)"
if [[ "$(json_get "$MCP_VALUE" shared_goal.id)" != "$ROOT_GOAL_ID" || "$(json_get "$MCP_VALUE" changed_facts.0.fact_id)" != "$GENERIC_FACT_ID" || "$(json_get "$MCP_VALUE" changed_facts.0.change)" != "added" ]]; then
    die "the generic work brief omitted its shared goal or changed fact"
fi
if [[ "$(json_get "$MCP_VALUE" context_window.next_query.known_context_digest)" != "$GENERIC_CONTEXT_DIGEST" ]]; then
    die "the generic work brief returned an invalid bounded-input continuation"
fi
GENERIC_INPUT_BYTES="$(json_get "$MCP_VALUE" context_window.input_bytes)"
GENERIC_INPUT_LIMIT="$(json_get "$MCP_VALUE" context_window.input_limit_bytes)"
if (( GENERIC_INPUT_BYTES > GENERIC_INPUT_LIMIT )); then
    die "the generic work brief exceeded its declared input limit: ${GENERIC_INPUT_BYTES} > ${GENERIC_INPUT_LIMIT}"
fi

mcp_call resume_work "$(json_object n work_item_id "$GENERIC_WORK_ITEM_ID" s known_context_digest "$GENERIC_CONTEXT_DIGEST")"
if [[ "$(json_get "$MCP_VALUE" unchanged)" != "true" || "$(json_get "$MCP_VALUE" context_window.next_query.known_context_digest)" != "$GENERIC_CONTEXT_DIGEST" ]]; then
    die "resume_work did not return the compact unchanged receipt"
fi

say "reading the bounded work and resource templates"
mcp_request "resources/read" "$(json_object s uri "stash://work/${GENERIC_WORK_ITEM_ID}/brief")"
if ! printf '%s' "$MCP_RESPONSE" | python3 -c '
import json, sys
response = json.load(sys.stdin)
brief = json.loads(response["result"]["contents"][0]["text"])
if not brief.get("blockers") or not brief.get("resources"):
    raise SystemExit(1)
'; then
    die "the work brief resource template omitted its blocker or resource"
fi
mcp_request "resources/read" "$(json_object s uri "stash://work-resource/${GENERIC_RESOURCE_ID}")"
if ! printf '%s' "$MCP_RESPONSE" | python3 -c '
import json, sys
response = json.load(sys.stdin)
resource = json.loads(response["result"]["contents"][0]["text"])
if resource.get("authority") != "external" or resource.get("source") != "jira":
    raise SystemExit(1)
'; then
    die "the work resource template did not return the external Jira pointer"
fi

mcp_call get_goal_map "$(json_object s namespace "$PROJECT_NAMESPACE")"
if [[ "$(json_get "$MCP_VALUE" goal_tree.root_goal_id)" != "$ROOT_GOAL_ID" || "$(json_get "$MCP_VALUE" resource_total)" != "1" ]]; then
    die "the owner goal map omitted the shared goal or external resource"
fi

say "recording the parent result and proving its prerequisite blocks completion"
mcp_call submit_work_evidence "$(json_object \
    n attempt_id "$GENERIC_ATTEMPT_ID" \
    s lease_token "$GENERIC_LEASE_TOKEN" \
    s evidence_type test \
    s summary "The generic Web MCP coordination path was observed" \
    s reference scripts/work_execution_smoke.sh \
    j payload '{"transport":"streamable-http","git_required":false}' \
    j condition_ids "[$GENERIC_PARENT_CONDITION_ID]" \
    s action_key "${ACTION_PREFIX}:generic-parent-evidence")"
GENERIC_PARENT_EVIDENCE_ID="$(json_get "$MCP_VALUE" id)"
mcp_call verify_work_condition "$(json_object \
    n attempt_id "$GENERIC_ATTEMPT_ID" \
    s lease_token "$GENERIC_LEASE_TOKEN" \
    n condition_id "$GENERIC_PARENT_CONDITION_ID" \
    s status passed \
    j evidence_ids "[$GENERIC_PARENT_EVIDENCE_ID]" \
    s action_key "${ACTION_PREFIX}:generic-parent-verify")"
mcp_call_expect_error finish_work "$(json_object \
    n attempt_id "$GENERIC_ATTEMPT_ID" \
    s lease_token "$GENERIC_LEASE_TOKEN" \
    s summary "The generic path was verified before its prerequisite finished" \
    s result "This finish must remain blocked" \
    s action_key "${ACTION_PREFIX}:generic-parent-finish-blocked")"
MCP_ERROR_LOWER="$(printf '%s' "$MCP_ERROR" | tr '[:upper:]' '[:lower:]')"
if ! [[ "$MCP_ERROR_LOWER" =~ block|prerequisite|unfinished ]]; then
    die "the parent finish failed for an unrelated reason: $MCP_ERROR"
fi

say "claiming and completing the spawned prerequisite"
GENERIC_RESEARCH_AGENT="smoke-research-${RUN_SUFFIX}"
mcp_call resume_project "$(json_object \
    s namespace "$PROJECT_NAMESPACE" \
    s agent_id "$GENERIC_RESEARCH_AGENT" \
    j capabilities '["research"]')"
if [[ "$(json_get "$MCP_VALUE" ready_work.0.id)" != "$GENERIC_PREREQUISITE_ID" ]]; then
    die "the research agent did not receive the runnable prerequisite"
fi
mcp_call claim_work "$(json_object \
    n work_item_id "$GENERIC_PREREQUISITE_ID" \
    s agent_id "$GENERIC_RESEARCH_AGENT" \
    n lease_seconds 600 \
    s action_key "${ACTION_PREFIX}:generic-child-claim")"
GENERIC_PREREQUISITE_ATTEMPT_ID="$(json_get "$MCP_VALUE" attempt.id)"
GENERIC_PREREQUISITE_LEASE_TOKEN="$(json_get "$MCP_VALUE" lease_token)"
mcp_call submit_work_evidence "$(json_object \
    n attempt_id "$GENERIC_PREREQUISITE_ATTEMPT_ID" \
    s lease_token "$GENERIC_PREREQUISITE_LEASE_TOKEN" \
    s evidence_type observation \
    s summary "The official source fact was recorded" \
    s reference "https://example.test/source" \
    j payload '{"observed":true}' \
    j condition_ids "[$GENERIC_PREREQUISITE_CONDITION_ID]" \
    s action_key "${ACTION_PREFIX}:generic-child-evidence")"
GENERIC_PREREQUISITE_EVIDENCE_ID="$(json_get "$MCP_VALUE" id)"
mcp_call verify_work_condition "$(json_object \
    n attempt_id "$GENERIC_PREREQUISITE_ATTEMPT_ID" \
    s lease_token "$GENERIC_PREREQUISITE_LEASE_TOKEN" \
    n condition_id "$GENERIC_PREREQUISITE_CONDITION_ID" \
    s status passed \
    j evidence_ids "[$GENERIC_PREREQUISITE_EVIDENCE_ID]" \
    s action_key "${ACTION_PREFIX}:generic-child-verify")"
mcp_call finish_work "$(json_object \
    n attempt_id "$GENERIC_PREREQUISITE_ATTEMPT_ID" \
    s lease_token "$GENERIC_PREREQUISITE_LEASE_TOKEN" \
    s summary "The source fact was checked" \
    s result "The required source fact was confirmed" \
    s action_key "${ACTION_PREFIX}:generic-child-finish")"

say "feeding the prerequisite result back into the parent and completing it"
mcp_call resume_work "$(json_object n work_item_id "$GENERIC_WORK_ITEM_ID")"
GENERIC_BLOCKER_COUNT="$(printf '%s' "$MCP_VALUE" | python3 -c 'import json, sys; print(len(json.load(sys.stdin).get("blockers", [])))')"
if [[ "$GENERIC_BLOCKER_COUNT" != "0" || "$(json_get "$MCP_VALUE" dependency_results.0.work_item.id)" != "$GENERIC_PREREQUISITE_ID" || "$(json_get "$MCP_VALUE" dependency_results.0.result)" != "The required source fact was confirmed" ]]; then
    die "the completed prerequisite result did not flow back into the parent brief"
fi
mcp_call finish_work "$(json_object \
    n attempt_id "$GENERIC_ATTEMPT_ID" \
    s lease_token "$GENERIC_LEASE_TOKEN" \
    s summary "The generic project path and prerequisite were verified" \
    s result "The parent received the bounded prerequisite result and completed" \
    s action_key "${ACTION_PREFIX}:generic-parent-finish")"
mcp_call resume_work "$(json_object n work_item_id "$GENERIC_WORK_ITEM_ID")"
if [[ "$(json_get "$MCP_VALUE" work_item.status)" != "done" ]]; then
    die "the generic parent did not finish after its prerequisite completed"
fi

say "checking the legacy worktree registration path"
mcp_call create_goal "$(json_object \
    s namespace "$PROJECT_NAMESPACE" \
    s content "Verify optional Git compatibility" \
    n priority 5)"
LEGACY_GOAL_ID="$(json_get "$MCP_VALUE" id)"
mcp_call set_project_goal "$(json_object \
    s namespace "$PROJECT_NAMESPACE" \
    n goal_id "$LEGACY_GOAL_ID" \
    s set_by smoke-owner)"
LEGACY_WORKTREE_PATH="/smoke/legacy-worktree-${RUN_SUFFIX}"
ARGS="$(json_object \
    s namespace "$PROJECT_NAMESPACE" \
    s repository "https://example.invalid/legacy.git" \
    s worktree_path "$LEGACY_WORKTREE_PATH" \
    s branch main \
    s head_sha legacy-head \
    s status clean \
    s agent_id legacy-smoke)"
mcp_call register_worktree "$ARGS"
LEGACY_WORKTREE_ID="$(json_get "$MCP_VALUE" id)"
ARGS="$(json_object \
    s namespace "$PROJECT_NAMESPACE" \
    s repository "https://example.invalid/legacy.git" \
    s worktree_path "$LEGACY_WORKTREE_PATH" \
    s branch main \
    s head_sha legacy-head-2 \
    s status dirty \
    s agent_id legacy-smoke)"
mcp_call register_worktree "$ARGS"
if [[ "$(json_get "$MCP_VALUE" id)" != "$LEGACY_WORKTREE_ID" ]]; then
    die "legacy register_worktree did not update the active path"
fi

REPOSITORY_INSTANCE_ID="$(python3 -c 'import uuid; print(uuid.uuid4())')"
WORKTREE_PATH="/smoke/worktree-${RUN_SUFFIX}"
GIT_COMMON_DIR="${WORKTREE_PATH}/.git"
GIT_DIR="$GIT_COMMON_DIR"
REMOTE_URL="git@github.com:example/work-execution-smoke.git"

say "binding and resolving the workspace"
ARGS="$(json_object \
    s cwd "$WORKTREE_PATH" \
    s repository_instance_id "$REPOSITORY_INSTANCE_ID" \
    s git_common_dir "$GIT_COMMON_DIR" \
    s git_dir "$GIT_DIR" \
    s worktree_path "$WORKTREE_PATH" \
    s remote_url "$REMOTE_URL" \
    s branch main \
    s head_sha 0123456789abcdef \
    s worktree_status clean \
    s agent_id "$AGENT_A" \
    s project_namespace "$PROJECT_NAMESPACE")"
mcp_call resolve_workspace "$ARGS"
WORKTREE_ID="$(json_get "$MCP_VALUE" worktree.id)"
if [[ "$(json_get "$MCP_VALUE" namespace.slug)" != "$PROJECT_NAMESPACE" ]]; then
    die "resolve_workspace returned the wrong project namespace"
fi

say "asserting a moved checkout keeps its worktree identity"
WORKTREE_PATH="/smoke/moved-worktree-${RUN_SUFFIX}"
GIT_COMMON_DIR="${WORKTREE_PATH}/.git"
GIT_DIR="$GIT_COMMON_DIR"
ARGS="$(json_object \
    s cwd "$WORKTREE_PATH" \
    s repository_instance_id "$REPOSITORY_INSTANCE_ID" \
    s git_common_dir "$GIT_COMMON_DIR" \
    s git_dir "$GIT_DIR" \
    s worktree_path "$WORKTREE_PATH" \
    s remote_url "$REMOTE_URL" \
    s branch main \
    s head_sha 0123456789abcdef \
    s worktree_status clean \
    s agent_id "$AGENT_A")"
mcp_call resolve_workspace "$ARGS"
if [[ "$(json_get "$MCP_VALUE" worktree.id)" != "$WORKTREE_ID" ]]; then
    die "moving the checkout created a duplicate worktree"
fi

say "reading the project-wide resume bundle"
mcp_call resume_workspace "$(json_object s namespace "$PROJECT_NAMESPACE" n worktree_id "$WORKTREE_ID" n recent_limit 20)"
if [[ "$(json_get "$MCP_VALUE" namespace)" != "$PROJECT_NAMESPACE" ]]; then
    die "resume_workspace returned the wrong project"
fi

ARGS="$(json_object \
    s namespace "$PROJECT_NAMESPACE" \
    s title "Exercise the complete work execution flow" \
    s description "Disposable issue used by the isolated work-execution smoke harness" \
    s issue_type task \
    s status ready \
    s reporter "$AGENT_A")"
mcp_call create_work_item "$ARGS"
WORK_ITEM_ID="$(json_get "$MCP_VALUE" id)"

say "preparing completion conditions"
CONDITIONS='[{"kind":"test","description":"The isolated work execution flow reaches completion with immutable evidence","verification":{"script":"scripts/work_execution_smoke.sh","expected":"exit code 0"},"required":true}]'
ARGS="$(json_object \
    n work_item_id "$WORK_ITEM_ID" \
    s next_action "Claim the prepared item and check lease exclusion" \
    j conditions "$CONDITIONS" \
    s action_key "${ACTION_PREFIX}:prepare")"
mcp_call prepare_work "$ARGS"

say "claiming the first leased attempt and its worktree atomically"
ARGS="$(json_object \
    n work_item_id "$WORK_ITEM_ID" \
    s cwd "$WORKTREE_PATH" \
    s repository_instance_id "$REPOSITORY_INSTANCE_ID" \
    s git_common_dir "$GIT_COMMON_DIR" \
    s git_dir "$GIT_DIR" \
    s worktree_path "$WORKTREE_PATH" \
    s remote_url "$REMOTE_URL" \
    s branch main \
    s head_sha 0123456789abcdef \
    s worktree_status clean \
    s agent_id "$AGENT_A" \
    n lease_seconds 600 \
    s action_key "${ACTION_PREFIX}:start-a")"
mcp_call claim_workspace "$ARGS"
ATTEMPT_A_ID="$(json_get "$MCP_VALUE" lease.attempt.id)"
LEASE_A_TOKEN="$(json_get "$MCP_VALUE" lease.lease_token)"

say "asserting a competing agent cannot claim or mutate the active lease"
ARGS="$(json_object \
    n work_item_id "$WORK_ITEM_ID" \
    s cwd "$WORKTREE_PATH" \
    s repository_instance_id "$REPOSITORY_INSTANCE_ID" \
    s git_common_dir "$GIT_COMMON_DIR" \
    s git_dir "$GIT_DIR" \
    s worktree_path "$WORKTREE_PATH" \
    s remote_url "$REMOTE_URL" \
    s branch main \
    s head_sha 0123456789abcdef \
    s worktree_status clean \
    s agent_id "$AGENT_B" \
    n lease_seconds 600 \
    s action_key "${ACTION_PREFIX}:start-b-conflict")"
mcp_call_expect_error claim_workspace "$ARGS"
MCP_ERROR_LOWER="$(printf '%s' "$MCP_ERROR" | tr '[:upper:]' '[:lower:]')"
if ! [[ "$MCP_ERROR_LOWER" =~ lease|active[[:space:]_-]*attempt|already[[:space:]_-]*active|claimed|owned ]]; then
    die "competing start failed for an unrelated reason: $MCP_ERROR"
fi

say "asserting the same worktree cannot claim a different item"
ARGS="$(json_object \
    s namespace "$PROJECT_NAMESPACE" \
    s title "Competing item for checkout exclusion" \
    s description "Must not share the active worktree" \
    s issue_type task \
    s status ready \
    s reporter "$AGENT_B")"
mcp_call create_work_item "$ARGS"
COMPETING_ITEM_ID="$(json_get "$MCP_VALUE" id)"
ARGS="$(json_object \
    n work_item_id "$COMPETING_ITEM_ID" \
    s next_action "Try to claim the already active worktree" \
    j conditions "$CONDITIONS" \
    s action_key "${ACTION_PREFIX}:prepare-competing")"
mcp_call prepare_work "$ARGS"
ARGS="$(json_object \
    n work_item_id "$COMPETING_ITEM_ID" \
    s cwd "$WORKTREE_PATH" \
    s repository_instance_id "$REPOSITORY_INSTANCE_ID" \
    s git_common_dir "$GIT_COMMON_DIR" \
    s git_dir "$GIT_DIR" \
    s worktree_path "$WORKTREE_PATH" \
    s remote_url "$REMOTE_URL" \
    s branch main \
    s head_sha 0123456789abcdef \
    s worktree_status clean \
    s agent_id "$AGENT_B" \
    n lease_seconds 600 \
    s action_key "${ACTION_PREFIX}:claim-competing-item")"
mcp_call_expect_error claim_workspace "$ARGS"
MCP_ERROR_LOWER="$(printf '%s' "$MCP_ERROR" | tr '[:upper:]' '[:lower:]')"
if ! [[ "$MCP_ERROR_LOWER" =~ worktree.*active|active.*worktree|already.*active ]]; then
    die "same-worktree claim failed for an unrelated reason: $MCP_ERROR"
fi

ARGS="$(json_object \
    n attempt_id "$ATTEMPT_A_ID" \
    s lease_token "invalid-${AGENT_B}" \
    s summary "A competing agent attempted to mutate the first attempt" \
    s result "This mutation must be rejected" \
    s next_action "Keep the original lease unchanged" \
    n lease_seconds 600 \
    s action_key "${ACTION_PREFIX}:checkpoint-b-conflict")"
mcp_call_expect_error checkpoint_work "$ARGS"
MCP_ERROR_LOWER="$(printf '%s' "$MCP_ERROR" | tr '[:upper:]' '[:lower:]')"
if ! [[ "$MCP_ERROR_LOWER" =~ lease|invalid|expired ]]; then
    die "competing lease mutation failed for an unrelated reason: $MCP_ERROR"
fi

say "recording and replaying one checkpoint action key"
CHECKPOINT_ARGS="$(json_object \
    n attempt_id "$ATTEMPT_A_ID" \
    s lease_token "$LEASE_A_TOKEN" \
    s summary "The first agent claimed the issue and observed the active lease" \
    s result "The competing start was rejected" \
    s next_action "Attach completion evidence" \
    n lease_seconds 600 \
    s action_key "${ACTION_PREFIX}:checkpoint")"
mcp_call checkpoint_work "$CHECKPOINT_ARGS"
FIRST_CHECKPOINT="$(canonical_json "$MCP_VALUE")"
FIRST_CHECKPOINT_ID="$(json_get "$MCP_VALUE" checkpoint.id)"
mcp_call checkpoint_work "$CHECKPOINT_ARGS"
SECOND_CHECKPOINT="$(canonical_json "$MCP_VALUE")"
SECOND_CHECKPOINT_ID="$(json_get "$MCP_VALUE" checkpoint.id)"
if [[ "$FIRST_CHECKPOINT_ID" != "$SECOND_CHECKPOINT_ID" || "$FIRST_CHECKPOINT" != "$SECOND_CHECKPOINT" ]]; then
    die "replaying one action_key created a different checkpoint receipt"
fi

say "attaching and verifying completion evidence"
mcp_call resume_work "$(json_object n work_item_id "$WORK_ITEM_ID")"
CONDITION_ID="$(json_get "$MCP_VALUE" completion_conditions.0.id)"

ARGS="$(json_object \
    n attempt_id "$ATTEMPT_A_ID" \
    s lease_token "$LEASE_A_TOKEN" \
    s evidence_type test \
    s summary "The active lease, conflict rejection, and idempotent replay were observed through MCP" \
    s reference scripts/work_execution_smoke.sh \
    j payload '{"transport":"streamable-http","isolated":true}' \
    j condition_ids "[$CONDITION_ID]" \
    s action_key "${ACTION_PREFIX}:evidence")"
mcp_call submit_work_evidence "$ARGS"
EVIDENCE_ID="$(json_get "$MCP_VALUE" id)"
EVIDENCE_DIGEST="$(json_get "$MCP_VALUE" content_digest)"
EVIDENCE_SUBMITTED_AT="$(json_get "$MCP_VALUE" submitted_at)"
if ! [[ "$EVIDENCE_DIGEST" =~ ^sha256:[0-9a-f]{64}$ ]] || [[ -z "$EVIDENCE_SUBMITTED_AT" ]]; then
    die "evidence provenance was not calculated by the server: digest=$EVIDENCE_DIGEST submitted_at=$EVIDENCE_SUBMITTED_AT"
fi

ARGS="$(json_object \
    n attempt_id "$ATTEMPT_A_ID" \
    s lease_token "$LEASE_A_TOKEN" \
    n condition_id "$CONDITION_ID" \
    s status passed \
    j evidence_ids "[$EVIDENCE_ID]" \
    s action_key "${ACTION_PREFIX}:verify")"
mcp_call verify_work_condition "$ARGS"

say "handing off the first attempt"
ARGS="$(json_object \
    n attempt_id "$ATTEMPT_A_ID" \
    s lease_token "$LEASE_A_TOKEN" \
    s summary "All completion evidence is attached and verified" \
    s result "The first lease conflict and idempotent replay checks passed" \
    s next_action "Reclaim the handed-off issue and finish it" \
    s action_key "${ACTION_PREFIX}:handoff")"
mcp_call handoff_work "$ARGS"

say "reading the durable handoff before reclaiming"
mcp_call resume_work "$(json_object n work_item_id "$WORK_ITEM_ID")"
HANDOFF_STATUS="$(json_get "$MCP_VALUE" latest_attempt.status)"
HANDOFF_NEXT_ACTION="$(json_get "$MCP_VALUE" latest_checkpoint.next_action)"
if [[ "$HANDOFF_STATUS" != "handed_off" || "$HANDOFF_NEXT_ACTION" != "Reclaim the handed-off issue and finish it" ]]; then
    die "handoff was not preserved by resume_work: status=$HANDOFF_STATUS next_action=$HANDOFF_NEXT_ACTION"
fi

mcp_call resume_workspace "$(json_object s namespace "$PROJECT_NAMESPACE" n worktree_id "$WORKTREE_ID" n recent_limit 20)"
WORKSPACE_HANDOFF_ITEM_ID="$(json_get "$MCP_VALUE" current_work.id)"
WORKSPACE_HANDOFF_NEXT_ACTION="$(json_get "$MCP_VALUE" next_action)"
if [[ "$WORKSPACE_HANDOFF_ITEM_ID" != "$WORK_ITEM_ID" || "$WORKSPACE_HANDOFF_NEXT_ACTION" != "Reclaim the handed-off issue and finish it" ]]; then
    die "handoff was not preserved by resume_workspace: work_item=$WORKSPACE_HANDOFF_ITEM_ID next_action=$WORKSPACE_HANDOFF_NEXT_ACTION"
fi

say "reclaiming the handed-off issue"
ARGS="$(json_object \
    n work_item_id "$WORK_ITEM_ID" \
    s cwd "$WORKTREE_PATH" \
    s repository_instance_id "$REPOSITORY_INSTANCE_ID" \
    s git_common_dir "$GIT_COMMON_DIR" \
    s git_dir "$GIT_DIR" \
    s worktree_path "$WORKTREE_PATH" \
    s remote_url "$REMOTE_URL" \
    s branch main \
    s head_sha 0123456789abcdef \
    s worktree_status clean \
    s agent_id "$AGENT_B" \
    n lease_seconds 600 \
    s action_key "${ACTION_PREFIX}:reclaim")"
mcp_call claim_workspace "$ARGS"
ATTEMPT_B_ID="$(json_get "$MCP_VALUE" lease.attempt.id)"
LEASE_B_TOKEN="$(json_get "$MCP_VALUE" lease.lease_token)"
if [[ "$ATTEMPT_A_ID" == "$ATTEMPT_B_ID" ]]; then
    die "the reclaimed start reused the handed-off attempt instead of creating a new attempt"
fi

say "finishing the reclaimed attempt"
ARGS="$(json_object \
    n attempt_id "$ATTEMPT_B_ID" \
    s lease_token "$LEASE_B_TOKEN" \
    s summary "The handed-off issue was reclaimed by the second agent" \
    s result "Every required condition has linked evidence and passed" \
    s action_key "${ACTION_PREFIX}:finish")"
mcp_call finish_work "$ARGS"

say "checking the final durable state"
mcp_call resume_work "$(json_object n work_item_id "$WORK_ITEM_ID")"
if [[ "$MCP_VALUE" == *"$ACTION_PREFIX"* ]]; then
    die "resume_work exposed a raw action key"
fi
FINAL_ITEM_STATUS="$(json_get "$MCP_VALUE" work_item.status)"
FINAL_ATTEMPT_STATUS="$(json_get "$MCP_VALUE" latest_attempt.status)"
FINAL_CONDITION_STATUS="$(json_get "$MCP_VALUE" completion_conditions.0.status)"
FINAL_EVIDENCE_TOTAL="$(json_get "$MCP_VALUE" totals.evidence)"
if [[ "$FINAL_ITEM_STATUS" != "done" || "$FINAL_ATTEMPT_STATUS" != "completed" || "$FINAL_CONDITION_STATUS" != "passed" ]]; then
    die "unexpected final state: item=$FINAL_ITEM_STATUS attempt=$FINAL_ATTEMPT_STATUS condition=$FINAL_CONDITION_STATUS"
fi
if [[ "$FINAL_EVIDENCE_TOTAL" != "1" ]]; then
    die "the brief did not preserve the full evidence total: evidence_total=$FINAL_EVIDENCE_TOTAL"
fi
mcp_call resume_work "$(json_object n work_item_id "$WORK_ITEM_ID" s detail full)"
if [[ "$(json_get "$MCP_VALUE" evidence.0.id)" != "$EVIDENCE_ID" || "$(json_get "$MCP_VALUE" truncated.evidence)" != "false" ]]; then
    die "the explicit full view did not preserve the evidence record"
fi

say "checking execution metrics"
METRICS="$(curl --fail --silent --show-error "${STASH_BASE_URL}/metrics")"
if ! printf '%s' "$METRICS" | grep -Fq 'stash_work_execution_transitions_total{action="claim_workspace",result="success"}'; then
    die "the successful workspace claim metric was not exported"
fi
if ! printf '%s' "$METRICS" | grep -Fq 'stash_work_execution_transitions_total{action="claim_workspace",result="rejected"}'; then
    die "the rejected competing workspace claim metric was not exported"
fi
if ! printf '%s' "$METRICS" | grep -Fq 'stash_work_execution_transitions_total{action="claim",result="success"}'; then
    die "the successful generic work claim metric was not exported"
fi
if ! printf '%s' "$METRICS" | grep -Fq 'stash_work_execution_transitions_total{action="spawn",result="success"}'; then
    die "the successful spawned work metric was not exported"
fi

say "passed: bounded changed-fact context, input continuation, generic Web MCP resume, prerequisite result flow, optional Git workspace binding, lease exclusion, handoff, evidence, and metrics were verified"
