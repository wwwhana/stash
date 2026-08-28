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
    "create_work_item": {"namespace", "title"},
    "resume_work": {"work_item_id"},
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

say "starting the first leased attempt"
ARGS="$(json_object \
    n work_item_id "$WORK_ITEM_ID" \
    s agent_id "$AGENT_A" \
    n lease_seconds 600 \
    s action_key "${ACTION_PREFIX}:start-a")"
mcp_call start_work "$ARGS"
ATTEMPT_A_ID="$(json_get "$MCP_VALUE" attempt.id)"
LEASE_A_TOKEN="$(json_get "$MCP_VALUE" lease_token)"

say "asserting a competing agent cannot claim or mutate the active lease"
ARGS="$(json_object \
    n work_item_id "$WORK_ITEM_ID" \
    s agent_id "$AGENT_B" \
    n lease_seconds 600 \
    s action_key "${ACTION_PREFIX}:start-b-conflict")"
mcp_call_expect_error start_work "$ARGS"
MCP_ERROR_LOWER="$(printf '%s' "$MCP_ERROR" | tr '[:upper:]' '[:lower:]')"
if ! [[ "$MCP_ERROR_LOWER" =~ lease|active[[:space:]_-]*attempt|already[[:space:]_-]*active|claimed|owned ]]; then
    die "competing start failed for an unrelated reason: $MCP_ERROR"
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

say "reclaiming the handed-off issue"
ARGS="$(json_object \
    n work_item_id "$WORK_ITEM_ID" \
    s agent_id "$AGENT_B" \
    n lease_seconds 600 \
    s action_key "${ACTION_PREFIX}:reclaim")"
mcp_call start_work "$ARGS"
ATTEMPT_B_ID="$(json_get "$MCP_VALUE" attempt.id)"
LEASE_B_TOKEN="$(json_get "$MCP_VALUE" lease_token)"
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
FINAL_EVIDENCE_TRUNCATED="$(json_get "$MCP_VALUE" truncated.evidence)"
if [[ "$FINAL_ITEM_STATUS" != "done" || "$FINAL_ATTEMPT_STATUS" != "completed" || "$FINAL_CONDITION_STATUS" != "passed" ]]; then
    die "unexpected final state: item=$FINAL_ITEM_STATUS attempt=$FINAL_ATTEMPT_STATUS condition=$FINAL_CONDITION_STATUS"
fi
if [[ "$FINAL_EVIDENCE_TOTAL" != "1" || "$FINAL_EVIDENCE_TRUNCATED" != "false" ]]; then
    die "resume totals/truncation were not preserved: evidence_total=$FINAL_EVIDENCE_TOTAL evidence_truncated=$FINAL_EVIDENCE_TRUNCATED"
fi

say "checking execution metrics"
METRICS="$(curl --fail --silent --show-error "${STASH_BASE_URL}/metrics")"
if ! printf '%s' "$METRICS" | grep -Fq 'stash_work_execution_transitions_total{action="start",result="success"}'; then
    die "the successful start execution metric was not exported"
fi
if ! printf '%s' "$METRICS" | grep -Fq 'stash_work_execution_transitions_total{action="start",result="rejected"}'; then
    die "the rejected competing start execution metric was not exported"
fi

say "passed: full flow, lease conflict, replay, provenance, bounded resume, and execution metrics were verified"
