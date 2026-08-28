package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func TestStashSkillsProtocolSuccess(t *testing.T) {
	protocol, skill := testStashSkillsProtocol(t, defaultSkillPageSize, defaultDirectoryPageSize)

	list := extensionResult[skillsListResult](t, protocol, `{"jsonrpc":"2.0","id":1,"method":"skills/list","params":{}}`)
	if list.ResultType != "complete" || list.NextCursor != "" || len(list.Skills) != 1 {
		t.Fatalf("skills/list result = %#v", list)
	}
	listed := list.Skills[0]
	if listed.URI != stashWorkSkillURI {
		t.Fatalf("skill URI = %q, want %q", listed.URI, stashWorkSkillURI)
	}
	if listed.Frontmatter["name"] != "stash-work" {
		t.Fatalf("frontmatter name = %#v", listed.Frontmatter["name"])
	}
	for _, field := range []string{"description", "license", "compatibility", "metadata"} {
		if _, ok := listed.Frontmatter[field]; !ok {
			t.Errorf("frontmatter is missing %q: %#v", field, listed.Frontmatter)
		}
	}
	if len(listed.Resources) != len(skill.Files) {
		t.Fatalf("manifest has %d resources, embedded skill has %d files", len(listed.Resources), len(skill.Files))
	}

	get := extensionResult[skillsGetResult](t, protocol, `{"jsonrpc":"2.0","id":"get","method":"skills/get","params":{"uri":"skill://stash-work/SKILL.md"}}`)
	if get.ResultType != "complete" || !reflect.DeepEqual(get.Skill, listed) {
		t.Fatalf("skills/get entry differs from skills/list: get=%#v list=%#v", get.Skill, listed)
	}
}

func TestStashSkillsInitializeCapability(t *testing.T) {
	protocol, _ := testStashSkillsProtocol(t, defaultSkillPageSize, defaultDirectoryPageSize)
	mcpServer := server.NewMCPServer("test", "test")
	protocol.Register(mcpServer)

	ctx := withStashSkillsTransport(t.Context())
	response := mcpServer.HandleMessage(ctx, json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`))
	result := rpcResult[mcp.InitializeResult](t, response)
	extension, ok := result.Capabilities.Extensions[skillsExtensionName].(map[string]any)
	if !ok {
		t.Fatalf("initialize extensions = %#v", result.Capabilities.Extensions)
	}
	if directoryRead, ok := extension["directoryRead"].(bool); !ok || !directoryRead {
		t.Fatalf("directoryRead capability = %#v", extension["directoryRead"])
	}
	if result.Capabilities.Resources == nil {
		t.Fatal("registering skill files did not advertise base resource support")
	}

	listResponse := mcpServer.HandleMessage(t.Context(), json.RawMessage(`{"jsonrpc":"2.0","id":2,"method":"resources/list","params":{}}`))
	resources := rpcResult[mcp.ListResourcesResult](t, listResponse)
	if len(resources.Resources) != 3 {
		t.Fatalf("resources/list returned %d files, want 3: %#v", len(resources.Resources), resources.Resources)
	}
	assertResource(t, resources.Resources, stashWorkSkillURI, "stash-work", "text/markdown")
}

func TestStashSkillResourcesReadMatchManifest(t *testing.T) {
	protocol, skill := testStashSkillsProtocol(t, defaultSkillPageSize, defaultDirectoryPageSize)
	mcpServer := server.NewMCPServer("test", "test")
	protocol.Register(mcpServer)

	manifestByURI := make(map[string]skillResourceManifest, len(skill.Entry.Resources))
	for _, resource := range skill.Entry.Resources {
		manifestByURI[resource.URI] = resource
	}
	if len(manifestByURI) != len(skill.Files) {
		t.Fatalf("manifest URIs are not unique: %#v", skill.Entry.Resources)
	}

	for _, file := range skill.Files {
		t.Run(file.Path, func(t *testing.T) {
			request := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":%q}}`, file.Resource.URI)
			response := mcpServer.HandleMessage(t.Context(), json.RawMessage(request))
			result := rpcResult[mcp.ReadResourceResult](t, response)
			if len(result.Contents) != 1 {
				t.Fatalf("resources/read contents = %#v", result.Contents)
			}
			content, ok := result.Contents[0].(mcp.TextResourceContents)
			if !ok {
				t.Fatalf("resources/read content type = %T", result.Contents[0])
			}
			if content.URI != file.Resource.URI || content.MIMEType != file.Resource.MIMEType {
				t.Fatalf("resource metadata = %#v, want URI=%q MIME=%q", content, file.Resource.URI, file.Resource.MIMEType)
			}
			raw := []byte(content.Text)
			digest := sha256.Sum256(raw)
			manifest := manifestByURI[file.Resource.URI]
			if manifest.Size != int64(len(raw)) {
				t.Errorf("manifest size = %d, read size = %d", manifest.Size, len(raw))
			}
			if want := fmt.Sprintf("sha256:%x", digest); manifest.Digest != want {
				t.Errorf("manifest digest = %q, read digest = %q", manifest.Digest, want)
			}
		})
	}
}

func TestStashSkillsUnknownURIReturnsInvalidParams(t *testing.T) {
	protocol, _ := testStashSkillsProtocol(t, defaultSkillPageSize, defaultDirectoryPageSize)

	requests := []string{
		`{"jsonrpc":"2.0","id":1,"method":"skills/get","params":{"uri":"skill://stash-work"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"skills/get","params":{"uri":"skill://unknown/SKILL.md"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"resources/directory/read","params":{"uri":"skill://stash-work/SKILL.md"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"resources/directory/read","params":{"uri":"skill://stash-work/missing"}}`,
	}
	for _, request := range requests {
		response, handled := protocol.HandleMessage(t.Context(), json.RawMessage(request))
		if !handled {
			t.Fatalf("request was not handled: %s", request)
		}
		rpcError, ok := response.(mcp.JSONRPCError)
		if !ok {
			t.Fatalf("response type = %T, want mcp.JSONRPCError", response)
		}
		if rpcError.Error.Code != mcp.INVALID_PARAMS {
			t.Errorf("error code = %d, want %d; request=%s", rpcError.Error.Code, mcp.INVALID_PARAMS, request)
		}
	}
}

func TestStashSkillsListCursorPagination(t *testing.T) {
	alpha := makeTestSkill(t, "alpha")
	beta := makeTestSkill(t, "beta")
	protocol, err := newSkillsProtocol([]servedSkill{beta, alpha}, 1, 10)
	if err != nil {
		t.Fatalf("new protocol: %v", err)
	}

	first := extensionResult[skillsListResult](t, protocol, `{"jsonrpc":"2.0","id":1,"method":"skills/list","params":{}}`)
	if len(first.Skills) != 1 || first.Skills[0].URI != "skill://alpha/SKILL.md" || first.NextCursor == "" {
		t.Fatalf("first page = %#v", first)
	}
	secondRequest := fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"skills/list","params":{"cursor":%q}}`, first.NextCursor)
	second := extensionResult[skillsListResult](t, protocol, secondRequest)
	if len(second.Skills) != 1 || second.Skills[0].URI != "skill://beta/SKILL.md" || second.NextCursor != "" {
		t.Fatalf("second page = %#v", second)
	}

	response, handled := protocol.HandleMessage(t.Context(), json.RawMessage(`{"jsonrpc":"2.0","id":3,"method":"skills/list","params":{"cursor":"not-a-cursor"}}`))
	if !handled {
		t.Fatal("skills/list with invalid cursor was not handled")
	}
	if rpcError, ok := response.(mcp.JSONRPCError); !ok || rpcError.Error.Code != mcp.INVALID_PARAMS {
		t.Fatalf("invalid cursor response = %#v", response)
	}
}

func TestStashSkillsDirectoryReadListsDirectChildrenAndPaginates(t *testing.T) {
	protocol, _ := testStashSkillsProtocol(t, defaultSkillPageSize, 1)

	first := extensionResult[directoryReadResult](t, protocol, `{"jsonrpc":"2.0","id":1,"method":"resources/directory/read","params":{"uri":"skill://stash-work"}}`)
	if len(first.Resources) != 1 || first.NextCursor == "" {
		t.Fatalf("first root directory page = %#v", first)
	}
	secondRequest := fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"resources/directory/read","params":{"uri":"skill://stash-work","cursor":%q}}`, first.NextCursor)
	second := extensionResult[directoryReadResult](t, protocol, secondRequest)
	if len(second.Resources) != 1 || second.NextCursor != "" {
		t.Fatalf("second root directory page = %#v", second)
	}
	rootChildren := append(first.Resources, second.Resources...)
	assertResource(t, rootChildren, stashWorkSkillURI, "stash-work", "text/markdown")
	assertResource(t, rootChildren, "skill://stash-work/references", "references", "inode/directory")
	for _, resource := range rootChildren {
		if strings.Contains(resource.URI, "protocol.md") || strings.Contains(resource.URI, "evidence.md") {
			t.Fatalf("root listing recursed into references: %#v", rootChildren)
		}
	}

	referenceProtocol, _ := testStashSkillsProtocol(t, defaultSkillPageSize, defaultDirectoryPageSize)
	references := extensionResult[directoryReadResult](t, referenceProtocol, `{"jsonrpc":"2.0","id":3,"method":"resources/directory/read","params":{"uri":"skill://stash-work/references"}}`)
	if len(references.Resources) != 2 || references.NextCursor != "" {
		t.Fatalf("references listing = %#v", references)
	}
	assertResource(t, references.Resources, "skill://stash-work/references/evidence.md", "evidence.md", "text/markdown")
	assertResource(t, references.Resources, "skill://stash-work/references/protocol.md", "protocol.md", "text/markdown")
}

func TestStashSkillsListDoesNotReadResourceBodies(t *testing.T) {
	protocol, _ := testStashSkillsProtocol(t, defaultSkillPageSize, defaultDirectoryPageSize)
	readCount := 0
	protocol.onResourceRead = func(string) { readCount++ }
	mcpServer := server.NewMCPServer("test", "test")
	protocol.Register(mcpServer)

	response, handled := protocol.HandleMessage(t.Context(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"skills/list","params":{}}`))
	if !handled {
		t.Fatal("skills/list was not handled")
	}
	if readCount != 0 {
		t.Fatalf("skills/list invoked %d resources/read handlers", readCount)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal skills/list response: %v", err)
	}
	if strings.Contains(string(encoded), "# Stash Work") {
		t.Fatalf("skills/list eagerly included SKILL.md content: %s", encoded)
	}

	mcpServer.HandleMessage(t.Context(), json.RawMessage(`{"jsonrpc":"2.0","id":2,"method":"resources/read","params":{"uri":"skill://stash-work/SKILL.md"}}`))
	if readCount != 1 {
		t.Fatalf("resources/read handler count = %d, want 1", readCount)
	}
}

func TestStashSkillLimitsAreEnforced(t *testing.T) {
	frontmatter := testFrontmatter("limits")
	tooMany := make([]skillFileSource, 0, maxSkillFiles+1)
	tooMany = append(tooMany, skillFileSource{Path: "SKILL.md", MIMEType: "text/markdown", Content: []byte("skill")})
	for i := 1; i <= maxSkillFiles; i++ {
		tooMany = append(tooMany, skillFileSource{Path: fmt.Sprintf("references/%03d.md", i), MIMEType: "text/markdown"})
	}
	if _, err := newServedSkill("skill://limits", frontmatter, tooMany[:maxSkillFiles]); err != nil {
		t.Fatalf("exact %d-file limit was rejected: %v", maxSkillFiles, err)
	}
	if _, err := newServedSkill("skill://limits", frontmatter, tooMany); err == nil || !strings.Contains(err.Error(), "maximum") {
		t.Fatalf("file-count limit error = %v", err)
	}

	skillBytes := []byte("skill")
	atLimit := []skillFileSource{
		{Path: "SKILL.md", MIMEType: "text/markdown", Content: skillBytes},
		{Path: "references/large.md", MIMEType: "text/markdown", Content: make([]byte, int(maxSkillContentSize)-len(skillBytes))},
	}
	if _, err := newServedSkill("skill://limits", frontmatter, atLimit); err != nil {
		t.Fatalf("exact %d-byte limit was rejected: %v", maxSkillContentSize, err)
	}
	tooLarge := []skillFileSource{
		{Path: "SKILL.md", MIMEType: "text/markdown", Content: skillBytes},
		{Path: "references/large.md", MIMEType: "text/markdown", Content: make([]byte, maxSkillContentSize)},
	}
	if _, err := newServedSkill("skill://limits", frontmatter, tooLarge); err == nil || !strings.Contains(err.Error(), "exceed") {
		t.Fatalf("content-size limit error = %v", err)
	}
}

func TestBundledStashSkillAddsNoExecutionSurface(t *testing.T) {
	_, skill := testStashSkillsProtocol(t, defaultSkillPageSize, defaultDirectoryPageSize)
	if _, exists := skill.Entry.Frontmatter["allowed-tools"]; exists {
		t.Fatal("remote skill must not grant allowed-tools")
	}
	for _, file := range skill.Files {
		if !strings.HasSuffix(file.Path, ".md") {
			t.Fatalf("bundled skill contains non-document file %q", file.Path)
		}
		if strings.HasPrefix(file.Path, "scripts/") {
			t.Fatalf("bundled skill contains script %q", file.Path)
		}
	}
}

func testStashSkillsProtocol(t *testing.T, skillPageSize, directoryPageSize int) (*skillsProtocol, servedSkill) {
	t.Helper()
	skill, err := loadEmbeddedStashWorkSkill()
	if err != nil {
		t.Fatalf("load embedded skill: %v", err)
	}
	protocol, err := newSkillsProtocol([]servedSkill{skill}, skillPageSize, directoryPageSize)
	if err != nil {
		t.Fatalf("new skills protocol: %v", err)
	}
	return protocol, skill
}

func makeTestSkill(t *testing.T, name string) servedSkill {
	t.Helper()
	skill, err := newServedSkill("skill://"+name, testFrontmatter(name), []skillFileSource{{
		Path:     "SKILL.md",
		MIMEType: "text/markdown",
		Content:  []byte("---\nname: " + name + "\ndescription: Test skill " + name + "\n---\n"),
	}})
	if err != nil {
		t.Fatalf("make test skill %q: %v", name, err)
	}
	return skill
}

func testFrontmatter(name string) map[string]any {
	return map[string]any{"name": name, "description": "Test skill " + name}
}

func extensionResult[T any](t *testing.T, protocol *skillsProtocol, request string) T {
	t.Helper()
	response, handled := protocol.HandleMessage(context.Background(), json.RawMessage(request))
	if !handled {
		t.Fatalf("extension request was not handled: %s", request)
	}
	return rpcResult[T](t, response)
}

func rpcResult[T any](t *testing.T, response mcp.JSONRPCMessage) T {
	t.Helper()
	rpcResponse, ok := response.(mcp.JSONRPCResponse)
	if !ok {
		t.Fatalf("response type = %T, want mcp.JSONRPCResponse; value=%#v", response, response)
	}
	result, ok := rpcResponse.Result.(T)
	if !ok {
		t.Fatalf("result type = %T, want requested type; value=%#v", rpcResponse.Result, rpcResponse.Result)
	}
	return result
}

func assertResource(t *testing.T, resources []mcp.Resource, uri, name, mimeType string) {
	t.Helper()
	for _, resource := range resources {
		if resource.URI == uri {
			if resource.Name != name || resource.MIMEType != mimeType {
				t.Fatalf("resource %q = %#v, want name=%q MIME=%q", uri, resource, name, mimeType)
			}
			return
		}
	}
	t.Fatalf("resource %q not found in %#v", uri, resources)
}
