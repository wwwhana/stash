package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/alash3al/stash/internal/bootstrap"
	"github.com/alash3al/stash/internal/brain"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func registerMemoryDetailTool(s *server.MCPServer, bc *bootstrap.Context) {
	s.AddTool(mcp.NewTool("get_memory_context",
		mcp.WithDescription("Read a memory's original sources, including records without a ticket. Continue with next_offset and snapshot while has_more."),
		mcp.WithString("namespace", mcp.Required()), mcp.WithString("memory_type", mcp.Required(), mcp.Enum("fact", "episode", "hypothesis", "failure")), mcp.WithNumber("memory_id", mcp.Required()),
		mcp.WithNumber("offset", mcp.DefaultNumber(0)), mcp.WithNumber("limit", mcp.DefaultNumber(100)), mcp.WithString("snapshot"), mcp.WithReadOnlyHintAnnotation(true),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, namespaceID, err := exactNamespaceID(ctx, bc, request.GetString("namespace", "/"))
		if err != nil {
			return nil, err
		}
		id, err := requiredPositiveID(request, "memory_id")
		if err != nil {
			return nil, err
		}
		value, err := bc.Brain.MemoryContext(ctx, namespaceID, request.GetString("memory_type", ""), id)
		if err != nil {
			return nil, err
		}
		return goalMapPageResult(bc, value, request.GetInt("offset", 0), request.GetInt("limit", 100), request.GetString("snapshot", ""))
	})
	s.AddTool(mcp.NewTool("list_memories",
		mcp.WithDescription("Browse namespace memory without requiring a ticket or goal. Original content is available with get_memory."),
		mcp.WithString("namespace", mcp.Required()), mcp.WithString("memory_type"), mcp.WithString("q"), mcp.WithString("status"),
		mcp.WithNumber("offset", mcp.DefaultNumber(0)), mcp.WithNumber("limit", mcp.DefaultNumber(100)), mcp.WithReadOnlyHintAnnotation(true),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, namespaceID, err := exactNamespaceID(ctx, bc, request.GetString("namespace", "/"))
		if err != nil {
			return nil, err
		}
		items, err := bc.Brain.ListMemory(ctx, namespaceID, request.GetString("memory_type", ""), request.GetString("q", ""), request.GetString("status", ""), brain.Pagination{Limit: request.GetInt("limit", 100), Offset: request.GetInt("offset", 0)})
		if err != nil {
			return nil, err
		}
		return jsonToolResult(bc, items, request.GetInt("offset", 0))
	})
	s.AddTool(mcp.NewTool("get_memory",
		mcp.WithDescription("Read one original memory field in bounded pages. Continue with next_offset while has_more; preserve snapshot."),
		mcp.WithString("namespace", mcp.Required()),
		mcp.WithString("memory_type", mcp.Required(), mcp.Enum("fact", "episode", "hypothesis", "failure")),
		mcp.WithNumber("memory_id", mcp.Required()),
		mcp.WithString("field", mcp.Enum("content", "reason", "lesson", "verification_plan"), mcp.DefaultString("content")),
		mcp.WithNumber("offset", mcp.DefaultNumber(0)), mcp.WithNumber("limit", mcp.DefaultNumber(1000)),
		mcp.WithString("snapshot"), mcp.WithReadOnlyHintAnnotation(true),
	), func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, namespaceID, err := exactNamespaceID(ctx, bc, request.GetString("namespace", "/"))
		if err != nil {
			return nil, err
		}
		id, err := requiredPositiveID(request, "memory_id")
		if err != nil {
			return nil, err
		}
		kind, field := request.GetString("memory_type", ""), request.GetString("field", "content")
		values := map[string]string{}
		var recordNamespace int64
		status := "recorded"
		switch kind {
		case "episode":
			record, e := bc.Brain.GetEpisode(ctx, id)
			if e != nil {
				return nil, e
			}
			recordNamespace = record.NamespaceID
			values["content"] = record.Content
		case "fact":
			record, e := bc.Brain.GetFact(ctx, id)
			if e != nil {
				return nil, e
			}
			recordNamespace = record.NamespaceID
			values["content"] = record.Content
			status = "active"
		case "hypothesis":
			record, e := bc.Brain.GetHypothesis(ctx, id)
			if e != nil {
				return nil, e
			}
			recordNamespace = record.NamespaceID
			values["content"], values["verification_plan"] = record.Content, record.VerificationPlan
			status = record.Status
		case "failure":
			record, e := bc.Brain.GetFailure(ctx, id)
			if e != nil {
				return nil, e
			}
			recordNamespace = record.NamespaceID
			values["content"], values["reason"], values["lesson"] = record.Content, record.Reason, record.Lesson
		default:
			return nil, fmt.Errorf("invalid memory type")
		}
		if recordNamespace != namespaceID {
			return nil, fmt.Errorf("memory not found in this workspace")
		}
		content, ok := values[field]
		if !ok {
			return nil, fmt.Errorf("field is unavailable for this memory type")
		}
		encoded, err := json.Marshal(values)
		if err != nil {
			return nil, err
		}
		digest := fmt.Sprintf("%x", sha256.Sum256(encoded))
		if snapshot := request.GetString("snapshot", ""); snapshot != "" && snapshot != digest {
			return mcp.NewToolResultError("기억이 변경되었습니다. 다시 여세요."), nil
		}
		offset, limit := request.GetInt("offset", 0), request.GetInt("limit", 1000)
		runes := []rune(content)
		if offset < 0 || offset > len(runes) || limit < 1 || limit > 1000 {
			return nil, fmt.Errorf("invalid memory page")
		}
		end := min(offset+limit, len(runes))
		for {
			value := map[string]any{"memory_type": kind, "memory_id": id, "field": field, "content": string(runes[offset:end]), "status": status, "has_more": end < len(runes), "next_offset": end, "snapshot": digest}
			payload, e := json.Marshal(value)
			if e != nil {
				return nil, e
			}
			if len(payload) <= mcpMaxResponseBytes(bc) {
				return textToolResult(payload), nil
			}
			if end-offset <= 1 {
				return mcp.NewToolResultError("기억을 표시하기에 서버 응답 제한이 너무 작습니다."), nil
			}
			end = offset + (end-offset)/2
		}
	})
}
