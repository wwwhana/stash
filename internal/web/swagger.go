package web

import (
	"io"
	"net/http"
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
    {"name": "Service", "description": "상태와 운영 정보"}
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
    "/auth/token": {
      "post": {
        "tags": ["Service"],
        "summary": "MCP 토큰 발급",
        "description": "현재 로그인한 주체의 Stash 토큰을 한 번 발급합니다. 토큰은 응답에만 포함됩니다.",
        "operationId": "authToken",
        "security": [{"bearerAuth": []}],
        "responses": {
          "200": {"description": "발급된 토큰", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/ApiToken"}}}},
          "401": {"$ref": "#/components/responses/Unauthorized"},
          "404": {"description": "인증이 꺼져 있음"},
          "503": {"description": "토큰 발급을 사용할 수 없음"}
        }
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
      "bearerAuth": {"type": "http", "scheme": "bearer", "bearerFormat": "Stash token or OAuth access token"}
    },
    "parameters": {
      "McpSessionId": {"name": "Mcp-Session-Id", "in": "header", "required": false, "schema": {"type": "string"}, "description": "Streamable HTTP 세션 ID"},
      "SseSessionId": {"name": "sessionId", "in": "query", "required": true, "schema": {"type": "string"}, "description": "SSE 연결에서 받은 세션 ID"}
    },
    "schemas": {
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
      "AuthStatus": {
        "type": "object",
        "required": ["auth_mode", "authenticated"],
        "properties": {
          "auth_mode": {"type": "string", "example": "token"},
          "authenticated": {"type": "boolean"},
          "user": {"type": "string"}
        }
      },
      "ApiToken": {
        "type": "object",
        "required": ["token", "token_type", "expires_in"],
        "properties": {
          "token": {"type": "string"},
          "token_type": {"type": "string", "example": "Bearer"},
          "expires_in": {"type": "integer", "format": "int64", "description": "유효 시간(초)"}
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
  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.11.10/swagger-ui.css">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.11.10/swagger-ui-bundle.js"></script>
  <script>
    window.onload = () => SwaggerUIBundle({
      url: '/openapi.json',
      dom_id: '#swagger-ui',
      deepLinking: true,
      displayRequestDuration: true,
      persistAuthorization: true,
      validatorUrl: null
    });
  </script>
</body>
</html>`

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

func staticDocumentHandler(contentType, body, name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", contentType)
		if r.Method == http.MethodHead {
			return
		}
		_, _ = io.WriteString(w, body)
	})
}
