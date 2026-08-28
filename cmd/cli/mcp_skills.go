package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	stashwork "github.com/alash3al/stash/internal/skills/stash-work"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	yaml "go.yaml.in/yaml/v2"
)

const (
	stashWorkSkillRootURI = "skill://stash-work"
	stashWorkSkillURI     = stashWorkSkillRootURI + "/SKILL.md"
	skillsExtensionName   = "io.modelcontextprotocol/skills"

	maxSkillFiles            = 512
	maxSkillContentSize      = int64(16 * 1024 * 1024)
	defaultSkillPageSize     = 50
	defaultDirectoryPageSize = 100
)

var agentSkillNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type skillResourceManifest struct {
	URI    string `json:"uri"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

type skillEntry struct {
	URI         string                  `json:"uri"`
	Frontmatter map[string]any          `json:"frontmatter"`
	Resources   []skillResourceManifest `json:"resources"`
}

type skillFileSource struct {
	Path     string
	MIMEType string
	Content  []byte
}

type skillFile struct {
	Path     string
	Content  []byte
	Resource mcp.Resource
	Manifest skillResourceManifest
}

type servedSkill struct {
	RootURI     string
	Entry       skillEntry
	Files       []skillFile
	Directories map[string][]mcp.Resource
}

type skillsProtocol struct {
	skills            []servedSkill
	skillsByURI       map[string]servedSkill
	directories       map[string][]mcp.Resource
	skillPageSize     int
	directoryPageSize int
	onResourceRead    func(string)
}

type skillsListResult struct {
	ResultType string       `json:"resultType"`
	Skills     []skillEntry `json:"skills"`
	NextCursor string       `json:"nextCursor,omitempty"`
}

type skillsGetResult struct {
	ResultType string     `json:"resultType"`
	Skill      skillEntry `json:"skill"`
}

type directoryReadResult struct {
	ResultType string         `json:"resultType"`
	Resources  []mcp.Resource `json:"resources"`
	NextCursor string         `json:"nextCursor,omitempty"`
}

type extensionRequestEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type skillsListParams struct {
	Cursor string `json:"cursor,omitempty"`
}

type skillsGetParams struct {
	URI string `json:"uri"`
}

type directoryReadParams struct {
	URI    string `json:"uri"`
	Cursor string `json:"cursor,omitempty"`
}

var bundledStashSkills = mustLoadStashSkillsProtocol()

func mustLoadStashSkillsProtocol() *skillsProtocol {
	skill, err := loadEmbeddedStashWorkSkill()
	if err != nil {
		panic(fmt.Sprintf("load embedded stash-work skill: %v", err))
	}
	protocol, err := newSkillsProtocol([]servedSkill{skill}, defaultSkillPageSize, defaultDirectoryPageSize)
	if err != nil {
		panic(fmt.Sprintf("build stash skills protocol: %v", err))
	}
	return protocol
}

// registerStashSkills registers ordinary resources/read handlers on every MCP
// transport. The initialize hook advertises the extension only when the
// Streamable HTTP request passed through the custom-method dispatcher.
func registerStashSkills(mcpServer *server.MCPServer) {
	bundledStashSkills.Register(mcpServer)
}

// handleStashSkillsMessage is the custom-method seam for mcp-go v0.49.0.
// That release uses a closed switch inside MCPServer.HandleMessage and has no
// custom request registration API. A transport dispatcher must call this
// function before MCPServer.HandleMessage and return the response when handled
// is true. The isolated handler adds no tool, script, or execution surface.
func handleStashSkillsMessage(ctx context.Context, message json.RawMessage) (response mcp.JSONRPCMessage, handled bool) {
	return bundledStashSkills.HandleMessage(ctx, message)
}

func loadEmbeddedStashWorkSkill() (servedSkill, error) {
	contentFS := stashwork.Files()
	files := make([]skillFileSource, 0, 4)
	var skillMarkdown []byte
	err := fs.WalkDir(contentFS, ".", func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		content, err := fs.ReadFile(contentFS, filePath)
		if err != nil {
			return fmt.Errorf("read %s: %w", filePath, err)
		}
		if filePath == "SKILL.md" {
			skillMarkdown = content
		}
		files = append(files, skillFileSource{
			Path:     filePath,
			MIMEType: skillMIMEType(filePath),
			Content:  content,
		})
		return nil
	})
	if err != nil {
		return servedSkill{}, err
	}
	if skillMarkdown == nil {
		return servedSkill{}, fmt.Errorf("SKILL.md is required")
	}
	frontmatter, err := parseSkillFrontmatter(skillMarkdown)
	if err != nil {
		return servedSkill{}, fmt.Errorf("parse SKILL.md frontmatter: %w", err)
	}
	return newServedSkill(stashWorkSkillRootURI, frontmatter, files)
}

func skillMIMEType(filePath string) string {
	switch strings.ToLower(path.Ext(filePath)) {
	case ".md", ".markdown":
		return "text/markdown"
	case ".json":
		return "application/json"
	default:
		return "text/plain"
	}
}

func parseSkillFrontmatter(content []byte) (map[string]any, error) {
	lines := bytes.Split(content, []byte("\n"))
	if len(lines) < 3 || string(bytes.TrimSpace(lines[0])) != "---" {
		return nil, fmt.Errorf("frontmatter must start with ---")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if string(bytes.TrimSpace(lines[i])) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, fmt.Errorf("frontmatter closing delimiter is missing")
	}

	var decoded any
	if err := yaml.Unmarshal(bytes.Join(lines[1:end], []byte("\n")), &decoded); err != nil {
		return nil, err
	}
	normalized, err := normalizeYAMLValue(decoded)
	if err != nil {
		return nil, err
	}
	frontmatter, ok := normalized.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("frontmatter must be an object")
	}
	if err := validateSkillFrontmatter(frontmatter); err != nil {
		return nil, err
	}
	return frontmatter, nil
}

func normalizeYAMLValue(value any) (any, error) {
	switch typed := value.(type) {
	case map[any]any:
		result := make(map[string]any, len(typed))
		for key, child := range typed {
			keyString, ok := key.(string)
			if !ok {
				return nil, fmt.Errorf("frontmatter keys must be strings")
			}
			normalized, err := normalizeYAMLValue(child)
			if err != nil {
				return nil, err
			}
			result[keyString] = normalized
		}
		return result, nil
	case []any:
		result := make([]any, len(typed))
		for i, child := range typed {
			normalized, err := normalizeYAMLValue(child)
			if err != nil {
				return nil, err
			}
			result[i] = normalized
		}
		return result, nil
	default:
		return typed, nil
	}
}

func validateSkillFrontmatter(frontmatter map[string]any) error {
	name, ok := frontmatter["name"].(string)
	if !ok || name == "" {
		return fmt.Errorf("name must be a non-empty string")
	}
	if len(name) > 64 || !agentSkillNamePattern.MatchString(name) {
		return fmt.Errorf("name %q does not follow Agent Skills naming rules", name)
	}
	description, ok := frontmatter["description"].(string)
	if !ok || description == "" || utf8.RuneCountInString(description) > 1024 {
		return fmt.Errorf("description must contain between 1 and 1024 characters")
	}
	if compatibility, exists := frontmatter["compatibility"]; exists {
		text, ok := compatibility.(string)
		if !ok || text == "" || utf8.RuneCountInString(text) > 500 {
			return fmt.Errorf("compatibility must contain between 1 and 500 characters")
		}
	}
	if metadata, exists := frontmatter["metadata"]; exists {
		values, ok := metadata.(map[string]any)
		if !ok {
			return fmt.Errorf("metadata must be an object")
		}
		for key, value := range values {
			if _, ok := value.(string); !ok {
				return fmt.Errorf("metadata value %q must be a string", key)
			}
		}
	}
	return nil
}

func newServedSkill(rootURI string, frontmatter map[string]any, sources []skillFileSource) (servedSkill, error) {
	name, _ := frontmatter["name"].(string)
	description, _ := frontmatter["description"].(string)
	if err := validateSkillFrontmatter(frontmatter); err != nil {
		return servedSkill{}, err
	}
	uriName, err := skillNameFromRootURI(rootURI)
	if err != nil {
		return servedSkill{}, err
	}
	if uriName != name {
		return servedSkill{}, fmt.Errorf("skill URI final segment %q does not match frontmatter name %q", uriName, name)
	}
	if len(sources) > maxSkillFiles {
		return servedSkill{}, fmt.Errorf("skill has %d files; maximum is %d", len(sources), maxSkillFiles)
	}

	files := make([]skillFile, 0, len(sources))
	seenPaths := make(map[string]struct{}, len(sources))
	var totalSize int64
	hasSkillMarkdown := false
	for _, source := range sources {
		if err := validateSkillFilePath(source.Path); err != nil {
			return servedSkill{}, err
		}
		if _, exists := seenPaths[source.Path]; exists {
			return servedSkill{}, fmt.Errorf("duplicate skill file %q", source.Path)
		}
		seenPaths[source.Path] = struct{}{}
		if source.Path == "SKILL.md" {
			hasSkillMarkdown = true
		}

		content := append([]byte(nil), source.Content...)
		size := int64(len(content))
		if size > maxSkillContentSize-totalSize {
			return servedSkill{}, fmt.Errorf("skill files exceed %d bytes", maxSkillContentSize)
		}
		totalSize += size
		digest := sha256.Sum256(content)
		uri := rootURI + "/" + source.Path
		resourceName := path.Base(source.Path)
		resourceDescription := ""
		if source.Path == "SKILL.md" {
			resourceName = name
			resourceDescription = description
		}
		resource := mcp.Resource{
			URI:         uri,
			Name:        resourceName,
			Description: resourceDescription,
			MIMEType:    source.MIMEType,
		}
		files = append(files, skillFile{
			Path:     source.Path,
			Content:  content,
			Resource: resource,
			Manifest: skillResourceManifest{
				URI:    uri,
				Digest: fmt.Sprintf("sha256:%x", digest),
				Size:   size,
			},
		})
	}
	if !hasSkillMarkdown {
		return servedSkill{}, fmt.Errorf("SKILL.md is required")
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	manifest := make([]skillResourceManifest, len(files))
	for i := range files {
		manifest[i] = files[i].Manifest
	}
	entry := skillEntry{
		URI:         rootURI + "/SKILL.md",
		Frontmatter: cloneStringMap(frontmatter),
		Resources:   manifest,
	}
	return servedSkill{
		RootURI:     rootURI,
		Entry:       entry,
		Files:       files,
		Directories: buildSkillDirectories(rootURI, files),
	}, nil
}

func skillNameFromRootURI(rootURI string) (string, error) {
	parsed, err := url.Parse(rootURI)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("invalid skill root URI %q", rootURI)
	}
	if strings.HasSuffix(rootURI, "/") {
		return "", fmt.Errorf("skill root URI must not have a trailing slash")
	}
	segments := []string{parsed.Host}
	if trimmed := strings.Trim(parsed.Path, "/"); trimmed != "" {
		segments = append(segments, strings.Split(trimmed, "/")...)
	}
	return segments[len(segments)-1], nil
}

func validateSkillFilePath(filePath string) error {
	if filePath == "" || strings.HasPrefix(filePath, "/") || strings.Contains(filePath, `\`) || path.Clean(filePath) != filePath || filePath == "." || strings.HasPrefix(filePath, "../") {
		return fmt.Errorf("invalid skill file path %q", filePath)
	}
	return nil
}

func buildSkillDirectories(rootURI string, files []skillFile) map[string][]mcp.Resource {
	children := map[string]map[string]mcp.Resource{
		rootURI: {},
	}
	for _, file := range files {
		parts := strings.Split(file.Path, "/")
		directoryURI := rootURI
		for i, part := range parts {
			childURI := directoryURI + "/" + part
			if i == len(parts)-1 {
				children[directoryURI][childURI] = file.Resource
				continue
			}
			directory := mcp.Resource{URI: childURI, Name: part, MIMEType: "inode/directory"}
			children[directoryURI][childURI] = directory
			if _, exists := children[childURI]; !exists {
				children[childURI] = make(map[string]mcp.Resource)
			}
			directoryURI = childURI
		}
	}

	directories := make(map[string][]mcp.Resource, len(children))
	for uri, childMap := range children {
		listed := make([]mcp.Resource, 0, len(childMap))
		for _, resource := range childMap {
			listed = append(listed, resource)
		}
		sort.Slice(listed, func(i, j int) bool { return listed[i].URI < listed[j].URI })
		directories[uri] = listed
	}
	return directories
}

func newSkillsProtocol(skills []servedSkill, skillPageSize, directoryPageSize int) (*skillsProtocol, error) {
	if skillPageSize <= 0 || directoryPageSize <= 0 {
		return nil, fmt.Errorf("pagination limits must be positive")
	}
	protocol := &skillsProtocol{
		skills:            append([]servedSkill(nil), skills...),
		skillsByURI:       make(map[string]servedSkill, len(skills)),
		directories:       make(map[string][]mcp.Resource),
		skillPageSize:     skillPageSize,
		directoryPageSize: directoryPageSize,
	}
	sort.Slice(protocol.skills, func(i, j int) bool { return protocol.skills[i].Entry.URI < protocol.skills[j].Entry.URI })
	for _, skill := range protocol.skills {
		if _, exists := protocol.skillsByURI[skill.Entry.URI]; exists {
			return nil, fmt.Errorf("duplicate skill URI %q", skill.Entry.URI)
		}
		if len(skill.Files) > maxSkillFiles || len(skill.Entry.Resources) != len(skill.Files) {
			return nil, fmt.Errorf("skill %q has an invalid resource manifest", skill.Entry.URI)
		}
		var totalSize int64
		for i, file := range skill.Files {
			digest := sha256.Sum256(file.Content)
			if file.Manifest.Size != int64(len(file.Content)) || file.Manifest.Digest != fmt.Sprintf("sha256:%x", digest) || skill.Entry.Resources[i] != file.Manifest {
				return nil, fmt.Errorf("skill %q manifest does not match embedded bytes", skill.Entry.URI)
			}
			if file.Manifest.Size > maxSkillContentSize-totalSize {
				return nil, fmt.Errorf("skill %q exceeds %d bytes", skill.Entry.URI, maxSkillContentSize)
			}
			totalSize += file.Manifest.Size
		}
		protocol.skillsByURI[skill.Entry.URI] = skill
		for uri, resources := range skill.Directories {
			if _, exists := protocol.directories[uri]; exists {
				return nil, fmt.Errorf("duplicate skill directory URI %q", uri)
			}
			protocol.directories[uri] = append([]mcp.Resource(nil), resources...)
		}
	}
	return protocol, nil
}

// Register attaches the base resource handlers and a transport-aware
// initialize capability hook that mcp-go v0.49.0 can expose natively.
func (p *skillsProtocol) Register(mcpServer *server.MCPServer) {
	if mcpServer == nil {
		panic("register Stash skills on a nil MCP server")
	}
	hooks := mcpServer.GetHooks()
	if hooks == nil {
		hooks = &server.Hooks{}
		server.WithHooks(hooks)(mcpServer)
	}
	hooks.AddAfterInitialize(func(ctx context.Context, _ any, _ *mcp.InitializeRequest, result *mcp.InitializeResult) {
		if !stashSkillsTransportAvailable(ctx) {
			return
		}
		if result.Capabilities.Extensions == nil {
			result.Capabilities.Extensions = make(map[string]any)
		}
		result.Capabilities.Extensions[skillsExtensionName] = map[string]any{"directoryRead": true}
	})

	for _, skill := range p.skills {
		for _, registeredFile := range skill.Files {
			file := registeredFile
			mcpServer.AddResource(file.Resource, func(_ context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
				if request.Params.URI != file.Resource.URI {
					return nil, fmt.Errorf("resource URI %q does not match registered URI %q", request.Params.URI, file.Resource.URI)
				}
				if p.onResourceRead != nil {
					p.onResourceRead(file.Resource.URI)
				}
				return []mcp.ResourceContents{mcp.TextResourceContents{
					URI:      file.Resource.URI,
					MIMEType: file.Resource.MIMEType,
					Text:     string(file.Content),
				}}, nil
			})
		}
	}
}

// HandleMessage implements the SEP-2640 custom methods without fetching any
// resource body. Standard resources/read remains registered through mcp-go.
func (p *skillsProtocol) HandleMessage(_ context.Context, message json.RawMessage) (mcp.JSONRPCMessage, bool) {
	var envelope extensionRequestEnvelope
	if err := json.Unmarshal(message, &envelope); err != nil {
		return nil, false
	}
	switch envelope.Method {
	case "skills/list", "skills/get", "resources/directory/read":
	default:
		return nil, false
	}

	requestID, err := extensionRequestID(envelope.ID)
	if err != nil || envelope.JSONRPC != mcp.JSONRPC_VERSION || requestID.IsNil() {
		return mcp.NewJSONRPCError(requestID, mcp.INVALID_REQUEST, "Invalid JSON-RPC request", nil), true
	}

	switch envelope.Method {
	case "skills/list":
		var params skillsListParams
		if err := decodeExtensionParams(envelope.Params, &params); err != nil {
			return invalidSkillsParams(requestID, "Invalid skills/list parameters"), true
		}
		entries := make([]skillEntry, len(p.skills))
		for i := range p.skills {
			entries[i] = p.skills[i].Entry
		}
		page, nextCursor, err := paginateSkillValues(entries, params.Cursor, p.skillPageSize, "skills/list")
		if err != nil {
			return invalidSkillsParams(requestID, err.Error()), true
		}
		return mcp.NewJSONRPCResultResponse(requestID, skillsListResult{ResultType: "complete", Skills: page, NextCursor: nextCursor}), true
	case "skills/get":
		var params skillsGetParams
		if err := decodeExtensionParams(envelope.Params, &params); err != nil {
			return invalidSkillsParams(requestID, "Invalid skills/get parameters"), true
		}
		skill, exists := p.skillsByURI[params.URI]
		if !exists {
			return invalidSkillsParams(requestID, fmt.Sprintf("Unknown skill URI %q", params.URI)), true
		}
		return mcp.NewJSONRPCResultResponse(requestID, skillsGetResult{ResultType: "complete", Skill: skill.Entry}), true
	case "resources/directory/read":
		var params directoryReadParams
		if err := decodeExtensionParams(envelope.Params, &params); err != nil {
			return invalidSkillsParams(requestID, "Invalid resources/directory/read parameters"), true
		}
		resources, exists := p.directories[params.URI]
		if !exists {
			return invalidSkillsParams(requestID, fmt.Sprintf("Unknown directory URI %q", params.URI)), true
		}
		page, nextCursor, err := paginateSkillValues(resources, params.Cursor, p.directoryPageSize, "resources/directory/read:"+params.URI)
		if err != nil {
			return invalidSkillsParams(requestID, err.Error()), true
		}
		return mcp.NewJSONRPCResultResponse(requestID, directoryReadResult{ResultType: "complete", Resources: page, NextCursor: nextCursor}), true
	default:
		panic("unreachable extension method")
	}
}

func extensionRequestID(raw json.RawMessage) (mcp.RequestId, error) {
	if len(raw) == 0 {
		return mcp.NewRequestId(nil), nil
	}
	var requestID mcp.RequestId
	if err := json.Unmarshal(raw, &requestID); err != nil {
		return mcp.NewRequestId(nil), err
	}
	return requestID, nil
}

func decodeExtensionParams(raw json.RawMessage, target any) error {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		raw = []byte("{}")
	}
	return json.Unmarshal(raw, target)
}

func invalidSkillsParams(id mcp.RequestId, message string) mcp.JSONRPCError {
	return mcp.NewJSONRPCError(id, mcp.INVALID_PARAMS, message, nil)
}

func paginateSkillValues[T any](values []T, cursor string, pageSize int, scope string) ([]T, string, error) {
	start, err := decodeSkillsCursor(cursor, scope)
	if err != nil || start > len(values) {
		return nil, "", fmt.Errorf("Invalid pagination cursor")
	}
	end := start + pageSize
	if end > len(values) {
		end = len(values)
	}
	page := append([]T(nil), values[start:end]...)
	nextCursor := ""
	if end < len(values) {
		nextCursor = encodeSkillsCursor(scope, end)
	}
	return page, nextCursor, nil
}

func encodeSkillsCursor(scope string, offset int) string {
	plain := scope + "\x00" + strconv.Itoa(offset)
	return base64.RawURLEncoding.EncodeToString([]byte(plain))
}

func decodeSkillsCursor(cursor, scope string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, err
	}
	prefix := scope + "\x00"
	if !strings.HasPrefix(string(decoded), prefix) {
		return 0, fmt.Errorf("cursor scope does not match")
	}
	offset, err := strconv.Atoi(strings.TrimPrefix(string(decoded), prefix))
	if err != nil || offset < 0 {
		return 0, fmt.Errorf("invalid cursor offset")
	}
	return offset, nil
}

func cloneStringMap(source map[string]any) map[string]any {
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		switch typed := value.(type) {
		case map[string]any:
			cloned[key] = cloneStringMap(typed)
		case []any:
			cloned[key] = append([]any(nil), typed...)
		default:
			cloned[key] = typed
		}
	}
	return cloned
}
