package web

import (
	"io"
	"net/http"
	"strings"
)

const openAPISpec = `{
  "openapi": "3.0.3",
  "info": {
    "title": "Stash API",
    "description": "Stash MCP와 상태 확인용 HTTP API입니다.",
    "version": "0.2.8"
  },
  "servers": [{"url": "/"}],
  "tags": [
    {"name": "MCP", "description": "Stash 도구 호출"},
    {"name": "Service", "description": "상태와 운영 정보"},
    {"name": "Admin", "description": "운영자 전용 설정. X-Stash-Admin-Token 또는 STASH_ADMIN_SUBJECTS 로그인 주체가 필요하며, STASH_AUTH_MODE=none에서는 열려 있습니다."}
  ],
  "paths": {
    "/mcp": {
      "post": {
        "tags": ["MCP"],
        "summary": "MCP 요청 보내기",
        "description": "Streamable HTTP 전송으로 JSON-RPC 요청을 보냅니다. tools/list로 현재 도구 목록을 확인할 수 있습니다.",
        "operationId": "mcpPost",
        "security": [{"bearerAuth": []}],
        "parameters": [{"$ref": "#/components/parameters/McpSessionId"}],
        "requestBody": {
          "required": true,
          "content": {
            "application/json": {
              "schema": {"$ref": "#/components/schemas/JsonRpcRequest"},
              "examples": {
                "initialize": {
                  "value": {
                    "jsonrpc": "2.0",
                    "id": 1,
                    "method": "initialize",
                    "params": {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "swagger-ui", "version": "1"}}
                  }
                },
                "listTools": {
                  "value": {"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}}
                }
              }
            }
          }
        },
        "responses": {
          "200": {
            "description": "JSON-RPC 응답 또는 서버 전송 이벤트",
            "content": {
              "application/json": {"schema": {"$ref": "#/components/schemas/JsonRpcResponse"}},
              "text/event-stream": {"schema": {"type": "string"}}
            }
          },
          "202": {"description": "응답 없는 JSON-RPC 알림 수락"},
          "400": {"$ref": "#/components/responses/BadRequest"},
          "401": {"$ref": "#/components/responses/Unauthorized"}
        }
      },
      "get": {
        "tags": ["MCP"],
        "summary": "MCP 이벤트 스트림 열기",
        "operationId": "mcpGet",
        "security": [{"bearerAuth": []}],
        "parameters": [{"$ref": "#/components/parameters/McpSessionId"}],
        "responses": {
          "200": {"description": "서버 전송 이벤트", "content": {"text/event-stream": {"schema": {"type": "string"}}}},
          "401": {"$ref": "#/components/responses/Unauthorized"}
        }
      },
      "delete": {
        "tags": ["MCP"],
        "summary": "MCP 세션 닫기",
        "operationId": "mcpDelete",
        "security": [{"bearerAuth": []}],
        "parameters": [{"$ref": "#/components/parameters/McpSessionId"}],
        "responses": {
          "200": {"description": "세션이 닫힘"},
          "401": {"$ref": "#/components/responses/Unauthorized"},
          "404": {"description": "세션을 찾을 수 없음"}
        }
      }
    },
    "/sse": {
      "get": {
        "tags": ["MCP"],
        "summary": "SSE 연결 열기",
        "description": "구형 SSE 전송을 사용하는 MCP 클라이언트용 연결입니다.",
        "operationId": "sseGet",
        "security": [{"bearerAuth": []}],
        "responses": {
          "200": {"description": "서버 전송 이벤트", "content": {"text/event-stream": {"schema": {"type": "string"}}}},
          "401": {"$ref": "#/components/responses/Unauthorized"}
        }
      }
    },
    "/message": {
      "post": {
        "tags": ["MCP"],
        "summary": "SSE 세션에 JSON-RPC 보내기",
        "operationId": "sseMessagePost",
        "security": [{"bearerAuth": []}],
        "parameters": [{"$ref": "#/components/parameters/SseSessionId"}],
        "requestBody": {
          "required": true,
          "content": {"application/json": {"schema": {"$ref": "#/components/schemas/JsonRpcRequest"}}}
        },
        "responses": {
          "200": {"description": "메시지 수락"},
          "400": {"$ref": "#/components/responses/BadRequest"},
          "401": {"$ref": "#/components/responses/Unauthorized"},
          "404": {"description": "세션을 찾을 수 없음"}
        }
      }
    },
    "/healthz": {
      "get": {
        "tags": ["Service"],
        "summary": "서비스 상태 확인",
        "operationId": "health",
        "responses": {
          "200": {"description": "서비스 정상", "content": {"text/plain": {"schema": {"type": "string", "example": "ok"}}}},
          "503": {"description": "서비스를 사용할 수 없음"}
        }
      }
    },
    "/readyz": {
      "get": {
        "tags": ["Service"],
        "summary": "준비 상태 확인",
        "operationId": "ready",
        "responses": {
          "200": {"description": "요청을 처리할 준비가 됨", "content": {"text/plain": {"schema": {"type": "string", "example": "ready"}}}},
          "503": {"description": "아직 준비되지 않음"}
        }
      }
    },
    "/metrics": {
      "get": {
        "tags": ["Service"],
        "summary": "Prometheus 메트릭 읽기",
        "operationId": "metrics",
        "security": [{"bearerAuth": []}],
        "responses": {
          "200": {"description": "Prometheus 텍스트 형식", "content": {"text/plain": {"schema": {"type": "string"}}}},
          "401": {"$ref": "#/components/responses/Unauthorized"}
        }
      }
    },
    "/auth/status": {
      "get": {
        "tags": ["Service"],
        "summary": "인증 상태 읽기",
        "operationId": "authStatus",
        "responses": {
          "200": {"description": "현재 인증 상태", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/AuthStatus"}}}}
        }
      }
    },
    "/auth/login": {
      "get": {
        "tags": ["Service"],
        "summary": "로그인 페이지",
        "description": "provider를 생략하면 비밀번호 계정이 있을 때 아이디/비밀번호 폼을, 아니면 토큰 폼 또는 SSO 리다이렉트를 보여 줍니다.",
        "operationId": "authLoginPage",
        "parameters": [{"name": "provider", "in": "query", "schema": {"type": "string", "enum": ["local", "token", "oidc"]}}],
        "responses": {"200": {"description": "HTML 로그인 폼", "content": {"text/html": {"schema": {"type": "string"}}}}, "302": {"description": "SSO 제공자로 리다이렉트"}}
      },
      "post": {
        "tags": ["Service"],
        "summary": "콘솔 로그인",
        "description": "username/password 또는 token 폼 필드를 받아 세션 쿠키를 발급합니다. 실패하면 401과 X-Stash-Login-Error(invalid|throttled) 헤더를 돌려줍니다.",
        "operationId": "authLogin",
        "requestBody": {"content": {"application/x-www-form-urlencoded": {"schema": {"type": "object", "properties": {"username": {"type": "string"}, "password": {"type": "string", "format": "password"}, "token": {"type": "string", "description": "API 토큰 로그인에만 사용"}}}}}},
        "responses": {"303": {"description": "로그인 성공, 세션 쿠키 설정 후 / 로 이동"}, "401": {"description": "자격 증명이 틀리거나 잠시 차단됨", "headers": {"X-Stash-Login-Error": {"schema": {"type": "string", "enum": ["invalid", "throttled"]}}}}}
      }
    },
    "/auth/password": {
      "post": {
        "tags": ["Service"],
        "summary": "내 비밀번호 변경",
        "description": "브라우저 세션으로만 호출할 수 있습니다. 현재 비밀번호가 맞아야 하며, Bearer 토큰 요청은 거부됩니다.",
        "operationId": "authPassword",
        "requestBody": {"required": true, "content": {"application/json": {"schema": {"type": "object", "required": ["current_password", "new_password"], "properties": {"current_password": {"type": "string", "format": "password"}, "new_password": {"type": "string", "format": "password", "minLength": 8, "maxLength": 72}}}}}},
        "responses": {"204": {"description": "변경 완료"}, "400": {"$ref": "#/components/responses/BadRequest"}, "401": {"description": "세션이 없거나 현재 비밀번호가 틀림"}, "403": {"description": "비밀번호가 없는 계정(SSO 전용)이거나 교차 출처 요청"}, "404": {"description": "계정 기능을 사용할 수 없음"}, "429": {"description": "로그인 실패가 너무 많음"}}
      }
    },
    "/auth/token": {
      "post": {
        "tags": ["Service"],
        "summary": "MCP 토큰 발급",
        "description": "현재 로그인한 주체의 API 토큰을 발급합니다. 무제한 또는 유효 시간을 지정할 수 있으며 원문은 발급 응답에만 포함됩니다.",
        "operationId": "authToken",
        "security": [{"bearerAuth": []}],
        "requestBody": {"content": {"application/x-www-form-urlencoded": {"schema": {"type": "object", "properties": {
          "name": {"type": "string", "maxLength": 120, "description": "토큰 이름 (선택)"},
          "expires_in": {"type": "integer", "format": "int64", "minimum": 0, "maximum": 9223372036, "default": 0, "description": "유효 시간(초). 생략하거나 비워 두거나 0을 지정하면 무제한이며 토큰 저장소가 필요합니다."}
        }}}}},
        "responses": {
          "200": {"description": "발급된 토큰", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/ApiToken"}}}},
          "400": {"$ref": "#/components/responses/BadRequest"},
          "401": {"$ref": "#/components/responses/Unauthorized"},
          "404": {"description": "인증이 꺼져 있음"},
          "503": {"description": "토큰 발급을 사용할 수 없음"}
        }
      }
    },
    "/auth/tokens": {
      "get": {
        "tags": ["Service"],
        "summary": "API 토큰 목록",
        "operationId": "authTokens",
        "security": [{"bearerAuth": []}],
        "responses": {
          "200": {"description": "현재 주체가 발급한 토큰 메타데이터", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/ApiTokenList"}}}},
          "401": {"$ref": "#/components/responses/Unauthorized"},
          "503": {"description": "토큰 관리가 준비되지 않음"}
        }
      }
    },
    "/auth/tokens/{id}/revoke": {
      "post": {
        "tags": ["Service"],
        "summary": "API 토큰 폐기",
        "operationId": "revokeAuthToken",
        "security": [{"bearerAuth": []}],
        "parameters": [{"name": "id", "in": "path", "required": true, "schema": {"type": "integer", "format": "int64"}}],
        "responses": {
          "200": {"description": "토큰 폐기 완료. 만료일을 폐기 시각으로 즉시 갱신합니다.", "content": {"application/json": {"schema": {"type": "object", "properties": {"id": {"type": "integer", "format": "int64"}, "revoked": {"type": "boolean"}, "revoked_at": {"type": "string", "format": "date-time"}, "expires_at": {"type": "string", "format": "date-time"}}}}}},
          "401": {"$ref": "#/components/responses/Unauthorized"},
          "404": {"description": "토큰을 찾을 수 없음"}
        }
      }
    },
    "/admin/users": {
      "get": {
        "tags": ["Admin"], "summary": "사용자 목록", "description": "사용자와 각자의 인증 수단(비밀번호, SSO subject)을 돌려줍니다. actor는 호출한 관리자입니다.", "operationId": "adminUsers",
        "security": [{"adminToken": []}, {"bearerAuth": []}],
        "responses": {"200": {"description": "사용자 목록", "content": {"application/json": {"schema": {"type": "object", "properties": {"users": {"type": "array", "items": {"$ref": "#/components/schemas/User"}}, "actor": {"type": "string"}}}}}}}
      },
      "post": {
        "tags": ["Admin"], "summary": "사용자 만들기", "description": "password를 주면 비밀번호 로그인 계정, 생략하면 SSO나 나중의 비밀번호 설정을 기다리는 계정입니다.", "operationId": "adminCreateUser",
        "security": [{"adminToken": []}, {"bearerAuth": []}],
        "requestBody": {"required": true, "content": {"application/json": {"schema": {"type": "object", "required": ["username"], "properties": {"username": {"type": "string", "pattern": "^[a-z0-9][a-z0-9._-]{0,63}$"}, "display_name": {"type": "string"}, "password": {"type": "string", "format": "password", "minLength": 8, "maxLength": 72}, "is_admin": {"type": "boolean"}}}}}},
        "responses": {"201": {"description": "만든 사용자", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/User"}}}}, "400": {"$ref": "#/components/responses/BadRequest"}, "409": {"description": "같은 아이디가 이미 있음"}}
      }
    },
    "/admin/users/{username}": {
      "parameters": [{"name": "username", "in": "path", "required": true, "schema": {"type": "string"}}],
      "put": {
        "tags": ["Admin"], "summary": "사용자 수정", "description": "표시 이름, 관리자 여부, 비활성화를 바꿉니다. 자기 자신의 관리자 권한을 빼거나 자신을 비활성화할 수는 없습니다(409).", "operationId": "adminUpdateUser",
        "security": [{"adminToken": []}, {"bearerAuth": []}],
        "requestBody": {"required": true, "content": {"application/json": {"schema": {"type": "object", "properties": {"display_name": {"type": "string"}, "is_admin": {"type": "boolean"}, "disabled": {"type": "boolean", "description": "true면 다음 요청부터 세션과 토큰이 거부됨"}}}}}},
        "responses": {"200": {"description": "수정된 사용자", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/User"}}}}, "404": {"description": "사용자를 찾을 수 없음"}, "409": {"description": "자기 자신을 잠그는 변경"}}
      },
      "delete": {
        "tags": ["Admin"], "summary": "사용자 삭제", "description": "인증 수단을 함께 지우고 그 사용자의 API 토큰을 모두 폐기합니다. 기억과 위키는 남습니다.", "operationId": "adminDeleteUser",
        "security": [{"adminToken": []}, {"bearerAuth": []}],
        "responses": {"200": {"description": "삭제 완료"}, "404": {"description": "사용자를 찾을 수 없음"}, "409": {"description": "자기 자신은 삭제할 수 없음"}}
      }
    },
    "/admin/users/{username}/password": {
      "post": {
        "tags": ["Admin"], "summary": "사용자 비밀번호 설정", "description": "비밀번호가 없던(SSO 전용) 사용자에게 붙이거나 기존 비밀번호를 바꿉니다.", "operationId": "adminSetUserPassword",
        "security": [{"adminToken": []}, {"bearerAuth": []}],
        "parameters": [{"name": "username", "in": "path", "required": true, "schema": {"type": "string"}}],
        "requestBody": {"required": true, "content": {"application/json": {"schema": {"type": "object", "required": ["password"], "properties": {"password": {"type": "string", "format": "password", "minLength": 8, "maxLength": 72}}}}}},
        "responses": {"200": {"description": "수정된 사용자", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/User"}}}}, "400": {"$ref": "#/components/responses/BadRequest"}, "404": {"description": "사용자를 찾을 수 없음"}}
      }
    },
    "/admin/users/{username}/tokens": {
      "get": {
        "tags": ["Admin"], "summary": "사용자의 API 토큰", "description": "생성일 역순으로 돌려줍니다. 원문은 포함되지 않습니다.", "operationId": "adminUserTokens",
        "security": [{"adminToken": []}, {"bearerAuth": []}],
        "parameters": [{"name": "username", "in": "path", "required": true, "schema": {"type": "string"}}],
        "responses": {"200": {"description": "토큰 목록", "content": {"application/json": {"schema": {"type": "object", "properties": {"username": {"type": "string"}, "tokens": {"type": "array", "items": {"$ref": "#/components/schemas/ApiTokenMetadata"}}}}}}}}
      }
    },
    "/admin/users/{username}/tokens/{id}/revoke": {
      "post": {
        "tags": ["Admin"], "summary": "사용자의 API 토큰 폐기", "operationId": "adminRevokeUserToken",
        "security": [{"adminToken": []}, {"bearerAuth": []}],
        "parameters": [{"name": "username", "in": "path", "required": true, "schema": {"type": "string"}}, {"name": "id", "in": "path", "required": true, "schema": {"type": "integer", "format": "int64"}}],
        "responses": {"200": {"description": "폐기 완료", "content": {"application/json": {"schema": {"type": "object", "properties": {"id": {"type": "integer", "format": "int64"}, "revoked": {"type": "boolean"}, "revoked_at": {"type": "string", "format": "date-time"}, "expires_at": {"type": "string", "format": "date-time"}}}}}}, "404": {"description": "토큰을 찾을 수 없음"}}
      }
    },
    "/admin/sso/status": {
      "get": {
        "tags": ["Admin"],
        "summary": "SSO 제공자 상태",
        "description": "저장된 OIDC 제공자와 로드 상태(ready/error/disabled), 시크릿 저장 가능 여부, 환경 변수 발급자를 돌려줍니다.",
        "operationId": "adminSSOStatus",
        "security": [{"adminToken": []}, {"bearerAuth": []}],
        "responses": {"200": {"description": "SSO 상태", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/SSOStatus"}}}}, "401": {"$ref": "#/components/responses/Unauthorized"}}
      }
    },
    "/admin/sso/providers": {
      "get": {
        "tags": ["Admin"], "summary": "SSO 제공자 목록", "operationId": "adminSSOProviders",
        "security": [{"adminToken": []}, {"bearerAuth": []}],
        "responses": {"200": {"description": "제공자 목록", "content": {"application/json": {"schema": {"type": "array", "items": {"$ref": "#/components/schemas/SSOProvider"}}}}}}
      },
      "post": {
        "tags": ["Admin"], "summary": "SSO 제공자 등록",
        "description": "클라이언트 시크릿은 STASH_SECRETS_KEY로 봉인해 저장합니다. 저장 직후 OIDC 검색(discovery)을 실행하고 결과를 status에 반영합니다.",
        "operationId": "adminSSOCreateProvider",
        "security": [{"adminToken": []}, {"bearerAuth": []}],
        "requestBody": {"required": true, "content": {"application/json": {"schema": {"$ref": "#/components/schemas/SSOProviderInput"}}}},
        "responses": {"201": {"description": "등록된 제공자", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/SSOProvider"}}}}, "400": {"$ref": "#/components/responses/BadRequest"}, "409": {"description": "같은 식별자가 이미 있음"}}
      }
    },
    "/admin/sso/providers/{id}": {
      "parameters": [{"name": "id", "in": "path", "required": true, "schema": {"type": "integer", "format": "int64"}}],
      "put": {
        "tags": ["Admin"], "summary": "SSO 제공자 수정", "description": "client_secret을 생략하거나 비우면 저장된 시크릿을 유지합니다.", "operationId": "adminSSOUpdateProvider",
        "security": [{"adminToken": []}, {"bearerAuth": []}],
        "requestBody": {"required": true, "content": {"application/json": {"schema": {"$ref": "#/components/schemas/SSOProviderInput"}}}},
        "responses": {"200": {"description": "수정된 제공자", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/SSOProvider"}}}}, "404": {"description": "제공자를 찾을 수 없음"}}
      },
      "delete": {
        "tags": ["Admin"], "summary": "SSO 제공자 삭제", "description": "이 제공자로 로그인했던 사용자와 identity 행은 남습니다.", "operationId": "adminSSODeleteProvider",
        "security": [{"adminToken": []}, {"bearerAuth": []}],
        "responses": {"200": {"description": "삭제 완료"}, "404": {"description": "제공자를 찾을 수 없음"}}
      }
    },
    "/admin/sso/providers/{id}/test": {
      "post": {
        "tags": ["Admin"], "summary": "SSO 제공자 연결 확인", "description": "저장된 설정으로 OIDC 검색 문서를 읽어 봅니다. 로그인 화면에 반영하지는 않습니다.", "operationId": "adminSSOTestProvider",
        "security": [{"adminToken": []}, {"bearerAuth": []}],
        "parameters": [{"name": "id", "in": "path", "required": true, "schema": {"type": "integer", "format": "int64"}}],
        "responses": {"200": {"description": "확인 결과", "content": {"application/json": {"schema": {"type": "object", "properties": {"ok": {"type": "boolean"}, "error": {"type": "string"}}}}}}}
      }
    },
    "/admin/sso/import-environment": {
      "post": {
        "tags": ["Admin"], "summary": "환경 변수 SSO 설정 가져오기", "description": "STASH_AUTH_ISSUER/CLIENT_ID/CLIENT_SECRET/REDIRECT_URL을 테이블에 한 번 저장합니다. 서버 시작 시에도 자동으로 실행됩니다.", "operationId": "adminSSOImportEnvironment",
        "security": [{"adminToken": []}, {"bearerAuth": []}],
        "responses": {"200": {"description": "가져올 것이 없음"}, "201": {"description": "저장됨", "content": {"application/json": {"schema": {"type": "object", "properties": {"imported": {"type": "boolean"}, "provider": {"$ref": "#/components/schemas/SSOProvider"}}}}}}}
      }
    },
    "/admin/llm/status": {
      "get": {
        "tags": ["Admin"],
        "summary": "모델 라우팅 상태",
        "description": "기능별로 어떤 프로바이더와 모델이 쓰이는지, 등록된 프로바이더와 지정 목록을 돌려줍니다.",
        "operationId": "adminLlmStatus",
        "security": [{"adminToken": []}, {"bearerAuth": []}],
        "responses": {
          "200": {"description": "라우팅 상태", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/LlmStatus"}}}},
          "401": {"$ref": "#/components/responses/Unauthorized"},
          "503": {"description": "관리 기능이 설정되지 않음"}
        }
      }
    },
    "/admin/llm/providers": {
      "get": {
        "tags": ["Admin"], "summary": "프로바이더 목록", "operationId": "adminLlmListProviders",
        "security": [{"adminToken": []}, {"bearerAuth": []}],
        "responses": {"200": {"description": "프로바이더 목록", "content": {"application/json": {"schema": {"type": "array", "items": {"$ref": "#/components/schemas/LlmProvider"}}}}}}
      },
      "post": {
        "tags": ["Admin"], "summary": "프로바이더 등록",
        "description": "OpenAI 호환 엔드포인트를 등록합니다. api_key는 STASH_SECRETS_KEY로 봉인되어 저장되며 다시 읽을 수 없습니다.",
        "operationId": "adminLlmCreateProvider",
        "security": [{"adminToken": []}, {"bearerAuth": []}],
        "requestBody": {"required": true, "content": {"application/json": {"schema": {"$ref": "#/components/schemas/LlmProviderInput"}}}},
        "responses": {
          "201": {"description": "등록된 프로바이더", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/LlmProvider"}}}},
          "400": {"$ref": "#/components/responses/BadRequest"},
          "409": {"description": "이름 중복"},
          "412": {"description": "STASH_SECRETS_KEY가 없어 API 키를 저장할 수 없음"}
        }
      }
    },
    "/admin/llm/providers/{id}": {
      "parameters": [{"name": "id", "in": "path", "required": true, "schema": {"type": "integer", "format": "int64"}}],
      "put": {
        "tags": ["Admin"], "summary": "프로바이더 수정", "description": "보낸 필드만 바뀝니다. api_key를 빈 문자열로 보내면 키를 지웁니다.",
        "operationId": "adminLlmUpdateProvider",
        "security": [{"adminToken": []}, {"bearerAuth": []}],
        "requestBody": {"required": true, "content": {"application/json": {"schema": {"$ref": "#/components/schemas/LlmProviderInput"}}}},
        "responses": {"200": {"description": "수정된 프로바이더", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/LlmProvider"}}}}, "404": {"description": "프로바이더 없음"}}
      },
      "delete": {
        "tags": ["Admin"], "summary": "프로바이더 삭제", "operationId": "adminLlmDeleteProvider",
        "security": [{"adminToken": []}, {"bearerAuth": []}],
        "responses": {"200": {"description": "삭제됨"}, "404": {"description": "프로바이더 없음"}, "409": {"description": "기능에 지정되어 있어 삭제할 수 없음"}}
      }
    },
    "/admin/llm/probe": {
      "post": {
        "tags": ["Admin"], "summary": "연결 확인과 모델 목록",
        "description": "엔드포인트의 /models를 호출해 연결·자격 증명을 확인하고 모델 ID를 돌려줍니다. provider_id를 주면 저장된 키를 사용합니다.",
        "operationId": "adminLlmProbe",
        "security": [{"adminToken": []}, {"bearerAuth": []}],
        "requestBody": {"required": true, "content": {"application/json": {"schema": {"type": "object", "properties": {"provider_id": {"type": "integer", "format": "int64"}, "base_url": {"type": "string"}, "api_key": {"type": "string"}}}}}},
        "responses": {"200": {"description": "확인 결과", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/LlmProbeResult"}}}}}
      }
    },
    "/admin/llm/assignments/{feature}": {
      "parameters": [{"name": "feature", "in": "path", "required": true, "schema": {"type": "string", "enum": ["embedding", "consolidation", "plan_validation", "wiki"]}}],
      "put": {
        "tags": ["Admin"], "summary": "기능에 프로바이더·모델 지정", "operationId": "adminLlmSetAssignment",
        "security": [{"adminToken": []}, {"bearerAuth": []}],
        "requestBody": {"required": true, "content": {"application/json": {"schema": {"$ref": "#/components/schemas/LlmAssignmentInput"}}}},
        "responses": {"200": {"description": "저장된 지정", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/LlmAssignment"}}}}, "400": {"$ref": "#/components/responses/BadRequest"}}
      },
      "delete": {
        "tags": ["Admin"], "summary": "지정 해제", "description": "기능을 STASH_OPENAI_* 환경 프로바이더로 되돌립니다.", "operationId": "adminLlmClearAssignment",
        "security": [{"adminToken": []}, {"bearerAuth": []}],
        "responses": {"200": {"description": "해제됨"}}
      }
    },
    "/admin/llm/import-environment": {
      "post": {
        "tags": ["Admin"], "summary": "환경 설정 가져오기",
        "description": "STASH_OPENAI_* 설정을 'environment' 프로바이더로 저장하고, 다른 프로바이더가 지정되지 않은 기능을 그 프로바이더에 지정합니다.",
        "operationId": "adminLlmImportEnvironment",
        "security": [{"adminToken": []}, {"bearerAuth": []}],
        "responses": {"200": {"description": "가져온 프로바이더와 지정"}}
      }
    },
    "/openapi.json": {
      "get": {
        "tags": ["Service"],
        "summary": "OpenAPI 문서 읽기",
        "operationId": "openapi",
        "responses": {
          "200": {"description": "OpenAPI 3.0 문서", "content": {"application/json": {"schema": {"type": "object"}}}}
        }
      }
    }
  },
  "components": {
    "securitySchemes": {
      "bearerAuth": {"type": "http", "scheme": "bearer", "bearerFormat": "Stash API token (stash_api_…) stored in the database"},
      "adminToken": {"type": "apiKey", "in": "header", "name": "X-Stash-Admin-Token"}
    },
    "parameters": {
      "McpSessionId": {"name": "Mcp-Session-Id", "in": "header", "required": false, "schema": {"type": "string"}, "description": "Streamable HTTP 세션 ID"},
      "SseSessionId": {"name": "sessionId", "in": "query", "required": true, "schema": {"type": "string"}, "description": "SSE 연결에서 받은 세션 ID"}
    },
    "schemas": {
      "LlmProvider": {
        "type": "object",
        "properties": {
          "id": {"type": "integer", "format": "int64"}, "name": {"type": "string"}, "kind": {"type": "string", "enum": ["openai_compatible"]},
          "base_url": {"type": "string"}, "has_api_key": {"type": "boolean"}, "request_timeout_seconds": {"type": "integer"},
          "enabled": {"type": "boolean"}, "created_at": {"type": "string", "format": "date-time"}, "updated_at": {"type": "string", "format": "date-time"}
        }
      },
      "LlmProviderInput": {
        "type": "object",
        "properties": {
          "name": {"type": "string", "pattern": "^[a-z0-9][a-z0-9_-]{0,63}$"}, "kind": {"type": "string", "enum": ["openai_compatible"]},
          "base_url": {"type": "string"}, "api_key": {"type": "string", "description": "봉인되어 저장됩니다. 빈 문자열은 키 삭제입니다."},
          "request_timeout_seconds": {"type": "integer", "minimum": 1}, "enabled": {"type": "boolean"}
        }
      },
      "LlmAssignmentInput": {
        "type": "object", "required": ["provider_id", "model"],
        "properties": {
          "provider_id": {"type": "integer", "format": "int64"}, "model": {"type": "string"},
          "dimensions": {"type": "integer", "description": "embedding 기능에만 필요 (1~2000)"},
          "context_tokens": {"type": "integer", "minimum": 0}, "reserved_tokens": {"type": "integer", "minimum": 0}
        }
      },
      "LlmAssignment": {
        "allOf": [{"$ref": "#/components/schemas/LlmAssignmentInput"}, {"type": "object", "properties": {"feature": {"type": "string"}, "updated_at": {"type": "string", "format": "date-time"}}}]
      },
      "LlmRoute": {
        "type": "object",
        "properties": {
          "feature": {"type": "string"}, "kind": {"type": "string", "enum": ["embedding", "reasoning"]},
          "source": {"type": "string", "enum": ["database", "environment", "none"]},
          "provider_id": {"type": "integer", "format": "int64"}, "provider_name": {"type": "string"}, "model": {"type": "string"},
          "dimensions": {"type": "integer"}, "context_tokens": {"type": "integer"}, "reserved_tokens": {"type": "integer"},
          "available": {"type": "boolean"}, "error": {"type": "string"}
        }
      },
      "LlmStatus": {
        "type": "object",
        "properties": {
          "features": {"type": "array", "items": {"type": "object", "properties": {"feature": {"type": "string"}, "kind": {"type": "string"}, "description": {"type": "string"}}}},
          "routes": {"type": "array", "items": {"$ref": "#/components/schemas/LlmRoute"}},
          "providers": {"type": "array", "items": {"$ref": "#/components/schemas/LlmProvider"}},
          "assignments": {"type": "array", "items": {"$ref": "#/components/schemas/LlmAssignment"}},
          "version": {"type": "integer", "format": "int64"}, "secrets_enabled": {"type": "boolean"}, "environment_base_url": {"type": "string"}
        }
      },
      "LlmProbeResult": {
        "type": "object",
        "properties": {"ok": {"type": "boolean"}, "status_code": {"type": "integer"}, "latency_ms": {"type": "integer"}, "models": {"type": "array", "items": {"type": "string"}}, "error": {"type": "string"}}
      },
      "JsonRpcRequest": {
        "type": "object",
        "required": ["jsonrpc", "method"],
        "properties": {
          "jsonrpc": {"type": "string", "enum": ["2.0"]},
          "id": {"oneOf": [{"type": "string"}, {"type": "number"}], "nullable": true},
          "method": {"type": "string", "example": "tools/list"},
          "params": {"type": "object", "additionalProperties": true}
        },
        "additionalProperties": false
      },
      "JsonRpcResponse": {"type": "object", "description": "MCP JSON-RPC 응답. 메서드에 따라 result 또는 error가 포함됩니다.", "additionalProperties": true},
      "User": {
        "type": "object",
        "properties": {
          "id": {"type": "integer", "format": "int64"}, "username": {"type": "string", "description": "세션 주체이자 네임스페이스·토큰의 소유자 키"},
          "display_name": {"type": "string"}, "is_admin": {"type": "boolean"}, "disabled": {"type": "boolean"},
          "created_at": {"type": "string", "format": "date-time"}, "updated_at": {"type": "string", "format": "date-time"}, "last_login_at": {"type": "string", "format": "date-time", "nullable": true},
          "identities": {"type": "array", "items": {"type": "object", "properties": {"id": {"type": "integer", "format": "int64"}, "kind": {"type": "string", "enum": ["password", "oidc"]}, "issuer": {"type": "string"}, "subject": {"type": "string"}, "created_at": {"type": "string", "format": "date-time"}, "last_used_at": {"type": "string", "format": "date-time", "nullable": true}}}}
        }
      },
      "ApiTokenMetadata": {
        "type": "object",
        "properties": {
          "id": {"type": "integer", "format": "int64"}, "name": {"type": "string"},
          "created_at": {"type": "string", "format": "date-time"}, "expires_at": {"type": "string", "format": "date-time", "nullable": true},
          "last_used_at": {"type": "string", "format": "date-time", "nullable": true}, "revoked_at": {"type": "string", "format": "date-time", "nullable": true}
        }
      },
      "SSOProvider": {
        "type": "object",
        "properties": {
          "id": {"type": "integer", "format": "int64", "description": "0이면 환경 변수에서만 읽은 미저장 제공자"},
          "slug": {"type": "string"}, "display_name": {"type": "string"}, "issuer": {"type": "string"}, "client_id": {"type": "string"},
          "has_client_secret": {"type": "boolean"}, "redirect_url": {"type": "string"}, "enabled": {"type": "boolean"},
          "source": {"type": "string", "enum": ["environment", "console"]},
          "status": {"type": "string", "enum": ["ready", "error", "disabled"]}, "error": {"type": "string"},
          "created_at": {"type": "string", "format": "date-time"}, "updated_at": {"type": "string", "format": "date-time"}
        }
      },
      "SSOProviderInput": {
        "type": "object",
        "properties": {
          "slug": {"type": "string", "pattern": "^[a-z0-9][a-z0-9_-]{0,63}$"}, "display_name": {"type": "string"},
          "issuer": {"type": "string", "format": "uri"}, "client_id": {"type": "string"}, "client_secret": {"type": "string", "format": "password", "description": "등록 시 필수, 수정 시 비우면 유지"},
          "redirect_url": {"type": "string", "format": "uri"}, "enabled": {"type": "boolean"}
        }
      },
      "SSOStatus": {
        "type": "object",
        "properties": {
          "providers": {"type": "array", "items": {"$ref": "#/components/schemas/SSOProvider"}},
          "secrets_enabled": {"type": "boolean"}, "cookie_secure": {"type": "boolean"},
          "environment_issuer": {"type": "string"}, "callback_path": {"type": "string", "example": "/auth/callback"}
        }
      },
      "AuthStatus": {
        "type": "object",
        "required": ["auth_mode", "authenticated"],
        "properties": {
          "auth_mode": {"type": "string", "example": "token"},
          "authenticated": {"type": "boolean"},
          "user": {"type": "string", "description": "세션 주체. 사용자 테이블의 username이며 네임스페이스와 토큰의 소유자 키"},
          "admin": {"type": "boolean", "description": "서버 설정 페이지를 열 수 있는지 (users.is_admin 또는 STASH_ADMIN_SUBJECTS)"},
          "has_password": {"type": "boolean", "description": "로그인한 사용자가 비밀번호를 가졌는지 (비밀번호 변경 가능 여부)"},
          "local_login": {"type": "boolean", "description": "아이디/비밀번호 로그인 폼을 보여 줄지"},
          "sso_login": {"type": "boolean", "description": "SSO(OIDC) 로그인이 설정됐는지"},
          "sso_providers": {"type": "array", "description": "로그인 버튼으로 보여 줄 제공자", "items": {"type": "object", "properties": {"slug": {"type": "string"}, "name": {"type": "string"}}}}
        }
      },
      "ApiToken": {
        "type": "object",
        "required": ["token", "token_type", "expires_in"],
        "properties": {
          "id": {"type": "integer", "format": "int64"},
          "name": {"type": "string"},
          "token": {"type": "string"},
          "token_type": {"type": "string", "example": "Bearer"},
          "expires_in": {"type": "integer", "format": "int64", "description": "유효 시간(초). 0이면 폐기할 때까지 유효함"},
          "expires_at": {"type": "string", "format": "date-time", "nullable": true},
          "created_at": {"type": "string", "format": "date-time"}
        }
      },
      "ApiTokenList": {
        "type": "object",
        "required": ["tokens"],
        "properties": {
          "tokens": {"type": "array", "items": {"$ref": "#/components/schemas/ApiTokenMetadata"}}
        }
      },
      "ApiTokenMetadata": {
        "type": "object",
        "required": ["id", "name", "created_at"],
        "properties": {
          "id": {"type": "integer", "format": "int64"},
          "name": {"type": "string"},
          "created_at": {"type": "string", "format": "date-time"},
          "expires_at": {"type": "string", "format": "date-time", "nullable": true, "description": "만료 시각. null이면 무제한"},
          "last_used_at": {"type": "string", "format": "date-time", "nullable": true},
          "revoked_at": {"type": "string", "format": "date-time", "nullable": true}
        }
      },
      "Error": {
        "type": "object",
        "properties": {"error": {"type": "string"}, "error_description": {"type": "string"}}
      }
    },
    "responses": {
      "BadRequest": {"description": "요청이 올바르지 않음", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Error"}}}},
      "Unauthorized": {"description": "인증이 필요함", "headers": {"WWW-Authenticate": {"schema": {"type": "string"}}}, "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Error"}}}}
    }
  }
}`

const swaggerUIPage = `<!doctype html>
<html lang="ko">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Stash API 문서</title>
  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.32.14/swagger-ui.css" integrity="sha384-fgyWYkUAamzuI8mJFu/xpRP0JWCJRwkwUwsYDoOYVHUJ8NQE5cENn8ib3ppwFFSX" crossorigin="anonymous">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.32.14/swagger-ui-bundle.js" integrity="sha384-Dt83RhU85ZmX7werw9uTFCzmauXUoSyx3pdzTQMABtsnFmooJy4Vz9/ACh7n5m1A" crossorigin="anonymous"></script>
  <script src="/swagger-init.js"></script>
</body>
</html>`

const swaggerInitScript = `window.addEventListener('load', () => SwaggerUIBundle({
  url: '/openapi.json',
  dom_id: '#swagger-ui',
  deepLinking: true,
  displayRequestDuration: true,
  persistAuthorization: false,
  validatorUrl: null
}));`

// OpenAPIHandler serves the stable HTTP contract without requiring an API
// credential. Operations that expose data remain protected by their own
// authentication middleware.
func OpenAPIHandler() http.Handler {
	return staticDocumentHandler("application/json; charset=utf-8", openAPISpec, "openapi.json")
}

// SwaggerUIHandler serves a small pinned Swagger UI shell. The specification
// itself stays same-origin at /openapi.json so deployments can also consume it
// without loading the optional UI assets.
func SwaggerUIHandler() http.Handler {
	return staticDocumentHandler("text/html; charset=utf-8", swaggerUIPage, "swagger.html")
}

func SwaggerInitHandler() http.Handler {
	return staticDocumentHandler("text/javascript; charset=utf-8", swaggerInitScript, "swagger-init.js")
}

func staticDocumentHandler(contentType, body, name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		if strings.HasPrefix(contentType, "text/html") {
			w.Header().Set("Content-Security-Policy", "default-src 'none'; connect-src 'self'; img-src data:; script-src 'self' https://cdn.jsdelivr.net; style-src 'unsafe-inline' https://cdn.jsdelivr.net; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		}
		if r.Method == http.MethodHead {
			return
		}
		_, _ = io.WriteString(w, body)
	})
}
