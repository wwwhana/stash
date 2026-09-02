package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/alash3al/stash/internal/models"
	"github.com/jackc/pgx/v5"
)

const (
	projectActiveWorkLimit = 3
	projectReadyWorkLimit  = 3
	maxWorkCapabilities    = 16
	maxWorkResourceURI     = 2048
	maxWorkResourceTitle   = 512
	maxWorkResourceSummary = 1000
	maxWorkResourceID      = 256
	maxWorkResourceActor   = 256
	maxWorkResourceMeta    = 16 * 1024
)

var capabilityNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

var workResourceKinds = map[string]struct{}{
	"git": {}, "document": {}, "url": {}, "browser": {}, "api": {},
	"dataset": {}, "device": {}, "ticket": {}, "file": {}, "other": {},
}

var workResourceRoles = map[string]struct{}{
	"input": {}, "target": {}, "output": {}, "evidence": {}, "reference": {},
}

var sensitiveResourceKeys = map[string]struct{}{
	"authorization": {}, "cookie": {}, "password": {}, "secret": {},
	"token": {}, "access_token": {}, "refresh_token": {}, "api_key": {}, "apikey": {},
	"accesstoken": {}, "refreshtoken": {}, "credential": {}, "credentials": {},
	"client_secret": {}, "clientsecret": {}, "private_key": {}, "privatekey": {},
}

const workResourceColumns = `id, namespace_id, resource_key, kind, source, authority,
 title, uri, summary, external_id, revision, content_digest, metadata, created_by,
 created_at, updated_at, deleted_at`

type WorkResourceInput struct {
	ResourceKey   string
	Kind          string
	Source        string
	Authority     string
	Title         string
	URI           string
	Summary       string
	ExternalID    string
	Revision      string
	ContentDigest string
	Metadata      json.RawMessage
	CreatedBy     string
	Role          string
	LinkedBy      string
}

type SpawnWorkInput struct {
	Title        string
	Description  string
	IssueType    string
	Priority     int
	Position     float64
	Reporter     string
	Relationship string
	NextAction   string
	Conditions   []CompletionConditionInput
	Capabilities []string
}

func isSensitiveResourceKey(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.NewReplacer("-", "_", ".", "_").Replace(normalized)
	if _, forbidden := sensitiveResourceKeys[normalized]; forbidden {
		return true
	}
	for _, suffix := range []string{
		"token", "secret", "password", "credential", "credentials", "privatekey", "apikey", "signature", "cookie",
	} {
		if strings.HasSuffix(normalized, suffix) {
			return true
		}
	}
	return strings.Contains(normalized, "authorization")
}

func NormalizeWorkCapabilities(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if !capabilityNamePattern.MatchString(value) {
			return nil, fmt.Errorf("brain: invalid work capability %q", value)
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
		if len(result) > maxWorkCapabilities {
			return nil, fmt.Errorf("brain: work has too many required capabilities (max %d)", maxWorkCapabilities)
		}
	}
	sort.Strings(result)
	return result, nil
}

func setWorkCapabilitiesTx(ctx context.Context, tx pgx.Tx, workItemID int64, capabilities []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM work_item_capabilities WHERE work_item_id = $1`, workItemID); err != nil {
		return fmt.Errorf("clear work capabilities: %w", err)
	}
	for _, capability := range capabilities {
		if _, err := tx.Exec(ctx,
			`INSERT INTO work_item_capabilities (work_item_id, capability) VALUES ($1, $2)`,
			workItemID, capability,
		); err != nil {
			return fmt.Errorf("store work capability: %w", err)
		}
	}
	return nil
}

func (b *Brain) SetWorkItemCapabilities(ctx context.Context, workItemID int64, capabilities []string) ([]string, error) {
	capabilities, err := NormalizeWorkCapabilities(capabilities)
	if err != nil {
		return nil, err
	}
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin work capability update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, _, err := lockWorkItem(ctx, tx, workItemID); err != nil {
		return nil, err
	}
	if err := setWorkCapabilitiesTx(ctx, tx, workItemID, capabilities); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit work capability update: %w", err)
	}
	return capabilities, nil
}

func (b *Brain) CreateWorkItemWithCapabilities(ctx context.Context, namespaceID int64, input WorkItemInput, capabilities []string) (*models.WorkItem, error) {
	capabilities, err := NormalizeWorkCapabilities(capabilities)
	if err != nil {
		return nil, err
	}
	return b.createWorkItemWithCapabilities(ctx, namespaceID, input, capabilities)
}

func (b *Brain) attachWorkCapabilities(ctx context.Context, items []models.WorkItem) ([]models.WorkItem, error) {
	if len(items) == 0 {
		return items, nil
	}
	ids := make([]int64, 0, len(items))
	byID := make(map[int64]int, len(items))
	for index := range items {
		ids = append(ids, items[index].ID)
		byID[items[index].ID] = index
		items[index].RequiredCapabilities = []string{}
	}
	rows, err := b.pool.Query(ctx,
		`SELECT work_item_id, array_agg(capability ORDER BY capability)
		 FROM work_item_capabilities WHERE work_item_id = ANY($1) GROUP BY work_item_id`, ids,
	)
	if err != nil {
		return nil, fmt.Errorf("list work capabilities: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var workItemID int64
		var capabilities []string
		if err := rows.Scan(&workItemID, &capabilities); err != nil {
			return nil, fmt.Errorf("scan work capabilities: %w", err)
		}
		if index, ok := byID[workItemID]; ok {
			items[index].RequiredCapabilities = capabilities
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read work capabilities: %w", err)
	}
	return items, nil
}

func validateResourceMetadata(metadata json.RawMessage) (json.RawMessage, error) {
	if len(metadata) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if len(metadata) > maxWorkResourceMeta {
		return nil, fmt.Errorf("brain: work resource metadata exceeds %d bytes", maxWorkResourceMeta)
	}
	var value map[string]any
	if err := json.Unmarshal(metadata, &value); err != nil || value == nil {
		return nil, fmt.Errorf("brain: work resource metadata must be a JSON object")
	}
	var check func(any) error
	check = func(current any) error {
		switch value := current.(type) {
		case map[string]any:
			for key, child := range value {
				if isSensitiveResourceKey(key) {
					return fmt.Errorf("brain: work resource metadata must not contain secret field %q", key)
				}
				if err := check(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range value {
				if err := check(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := check(value); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("brain: encode work resource metadata: %w", err)
	}
	return canonical, nil
}

func validateWorkResourceURI(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) > maxWorkResourceURI {
		return "", fmt.Errorf("brain: work resource URI exceeds %d characters", maxWorkResourceURI)
	}
	if raw == "" {
		return "", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" {
		return "", fmt.Errorf("brain: work resource URI must be absolute")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "data", "javascript", "vbscript":
		return "", fmt.Errorf("brain: work resource URI scheme %q is not allowed", parsed.Scheme)
	}
	if parsed.User != nil {
		return "", fmt.Errorf("brain: work resource URI must not contain credentials")
	}
	for key := range parsed.Query() {
		if isSensitiveResourceKey(key) || strings.Contains(strings.ToLower(key), "signature") {
			return "", fmt.Errorf("brain: work resource URI must not contain secret query field %q", key)
		}
	}
	fragment := strings.ToLower(parsed.Fragment)
	for _, marker := range []string{"access_token=", "refresh_token=", "api_key=", "signature=", "password=", "secret="} {
		if strings.Contains(fragment, marker) {
			return "", fmt.Errorf("brain: work resource URI must not contain secrets in its fragment")
		}
	}
	return raw, nil
}

func normalizeWorkResourceInput(input WorkResourceInput) (WorkResourceInput, error) {
	input.ResourceKey = strings.TrimSpace(input.ResourceKey)
	input.Kind = strings.ToLower(strings.TrimSpace(input.Kind))
	input.Source = strings.ToLower(strings.TrimSpace(input.Source))
	input.Authority = strings.ToLower(strings.TrimSpace(input.Authority))
	input.Title = strings.TrimSpace(input.Title)
	input.Summary = strings.TrimSpace(input.Summary)
	input.ExternalID = strings.TrimSpace(input.ExternalID)
	input.Revision = strings.TrimSpace(input.Revision)
	input.ContentDigest = strings.TrimSpace(input.ContentDigest)
	input.CreatedBy = strings.TrimSpace(input.CreatedBy)
	input.LinkedBy = strings.TrimSpace(input.LinkedBy)
	input.Role = strings.ToLower(strings.TrimSpace(input.Role))
	if input.Source == "" {
		input.Source = "stash"
	}
	if input.Authority == "" {
		input.Authority = "stash"
	}
	if input.Role == "" {
		input.Role = "reference"
	}
	if input.ResourceKey == "" || len(input.ResourceKey) > 256 {
		return input, fmt.Errorf("brain: work resource key is required and must not exceed 256 characters")
	}
	if _, ok := workResourceKinds[input.Kind]; !ok {
		return input, fmt.Errorf("brain: invalid work resource kind %q", input.Kind)
	}
	if !capabilityNamePattern.MatchString(input.Source) {
		return input, fmt.Errorf("brain: invalid work resource source %q", input.Source)
	}
	if input.Authority != "stash" && input.Authority != "external" {
		return input, fmt.Errorf("brain: invalid work resource authority %q", input.Authority)
	}
	if _, ok := workResourceRoles[input.Role]; !ok {
		return input, fmt.Errorf("brain: invalid work resource role %q", input.Role)
	}
	if err := validateContent(input.Title); err != nil {
		return input, fmt.Errorf("brain: work resource title: %w", err)
	}
	if len(input.Title) > maxWorkResourceTitle {
		return input, fmt.Errorf("brain: work resource title exceeds %d bytes", maxWorkResourceTitle)
	}
	if len(input.Summary) > maxWorkResourceSummary {
		return input, fmt.Errorf("brain: work resource summary exceeds %d bytes", maxWorkResourceSummary)
	}
	if len(input.ExternalID) > maxWorkResourceID || len(input.Revision) > maxWorkResourceID {
		return input, fmt.Errorf("brain: work resource external ID and revision must not exceed %d bytes", maxWorkResourceID)
	}
	if len(input.CreatedBy) > maxWorkResourceActor || len(input.LinkedBy) > maxWorkResourceActor {
		return input, fmt.Errorf("brain: work resource actor must not exceed %d bytes", maxWorkResourceActor)
	}
	uri, err := validateWorkResourceURI(input.URI)
	if err != nil {
		return input, err
	}
	input.URI = uri
	metadata, err := validateResourceMetadata(input.Metadata)
	if err != nil {
		return input, err
	}
	input.Metadata = metadata
	if input.ContentDigest != "" {
		matched, _ := regexp.MatchString(`^sha256:[0-9a-f]{64}$`, input.ContentDigest)
		if !matched {
			return input, fmt.Errorf("brain: work resource content digest must be sha256:<64 lowercase hex characters>")
		}
	}
	return input, nil
}

func scanWorkResource(row pgx.Row) (*models.WorkResource, error) {
	var resource models.WorkResource
	err := row.Scan(
		&resource.ID, &resource.NamespaceID, &resource.ResourceKey, &resource.Kind,
		&resource.Source, &resource.Authority, &resource.Title, &resource.URI,
		&resource.Summary, &resource.ExternalID, &resource.Revision, &resource.ContentDigest,
		&resource.Metadata, &resource.CreatedBy, &resource.CreatedAt, &resource.UpdatedAt,
		&resource.DeletedAt,
	)
	return &resource, err
}

// AttachWorkResource upserts a bounded reference and links it to a work item
// in one transaction. The stable resource key makes Web MCP retries safe.
func (b *Brain) AttachWorkResource(ctx context.Context, workItemID int64, input WorkResourceInput) (*models.WorkResourceAttachment, error) {
	input, err := normalizeWorkResourceInput(input)
	if err != nil {
		return nil, err
	}
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin work resource attachment: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	namespaceID, _, err := lockWorkItem(ctx, tx, workItemID)
	if err != nil {
		return nil, err
	}
	resource, err := scanWorkResource(tx.QueryRow(ctx,
		`INSERT INTO work_resources
		    (namespace_id, resource_key, kind, source, authority, title, uri, summary,
		     external_id, revision, content_digest, metadata, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		 ON CONFLICT (namespace_id, resource_key) WHERE deleted_at IS NULL DO UPDATE SET
		   kind = EXCLUDED.kind, source = EXCLUDED.source, authority = EXCLUDED.authority,
		   title = EXCLUDED.title, uri = EXCLUDED.uri, summary = EXCLUDED.summary,
		   external_id = EXCLUDED.external_id, revision = EXCLUDED.revision,
		   content_digest = EXCLUDED.content_digest, metadata = EXCLUDED.metadata,
		   updated_at = now()
		 RETURNING `+workResourceColumns,
		namespaceID, input.ResourceKey, input.Kind, input.Source, input.Authority,
		input.Title, input.URI, input.Summary, input.ExternalID, input.Revision,
		input.ContentDigest, input.Metadata, input.CreatedBy,
	))
	if err != nil {
		return nil, fmt.Errorf("upsert work resource: %w", err)
	}
	var link models.WorkResourceLink
	if err := tx.QueryRow(ctx,
		`INSERT INTO work_resource_links (namespace_id, work_item_id, resource_id, role, linked_by)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (work_item_id, resource_id, role) DO UPDATE SET linked_by = EXCLUDED.linked_by
		 RETURNING id, namespace_id, work_item_id, resource_id, role, linked_by, created_at`,
		namespaceID, workItemID, resource.ID, input.Role, input.LinkedBy,
	).Scan(&link.ID, &link.NamespaceID, &link.WorkItemID, &link.ResourceID, &link.Role, &link.LinkedBy, &link.CreatedAt); err != nil {
		return nil, fmt.Errorf("link work resource: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit work resource attachment: %w", err)
	}
	return &models.WorkResourceAttachment{Resource: *resource, Link: link}, nil
}

func (b *Brain) GetWorkResource(ctx context.Context, resourceID int64) (*models.WorkResource, error) {
	resource, err := scanWorkResource(b.pool.QueryRow(ctx,
		`SELECT `+workResourceColumns+` FROM work_resources WHERE id = $1 AND deleted_at IS NULL`, resourceID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("brain: work resource not found")
	}
	if err != nil {
		return nil, fmt.Errorf("get work resource: %w", err)
	}
	return resource, nil
}

func (b *Brain) ListWorkResourceRefs(ctx context.Context, workItemID int64, page Pagination) ([]models.WorkResourceRef, error) {
	page = b.sanitizePage(page)
	rows, err := b.pool.Query(ctx,
		`SELECT resource.id, resource.resource_key, resource.kind, resource.source, resource.authority,
		        resource.title, resource.uri, resource.summary, resource.external_id, resource.revision,
		        resource.content_digest, linked.role
		 FROM work_resource_links linked
		 JOIN work_items item ON item.id = linked.work_item_id AND item.deleted_at IS NULL
		 JOIN work_resources resource ON resource.id = linked.resource_id
		   AND resource.namespace_id = item.namespace_id AND resource.deleted_at IS NULL
		 WHERE linked.work_item_id = $1
		 ORDER BY CASE linked.role WHEN 'input' THEN 0 WHEN 'target' THEN 1 WHEN 'reference' THEN 2 WHEN 'output' THEN 3 ELSE 4 END,
		          linked.created_at DESC, linked.id DESC
		 LIMIT $2 OFFSET $3`, workItemID, page.Limit, page.Offset,
	)
	if err != nil {
		return nil, fmt.Errorf("list work resources: %w", err)
	}
	defer rows.Close()
	resources := make([]models.WorkResourceRef, 0)
	for rows.Next() {
		var resource models.WorkResourceRef
		if err := rows.Scan(
			&resource.ID, &resource.ResourceKey, &resource.Kind, &resource.Source, &resource.Authority,
			&resource.Title, &resource.URI, &resource.Summary, &resource.ExternalID, &resource.Revision,
			&resource.ContentDigest, &resource.Role,
		); err != nil {
			return nil, fmt.Errorf("scan work resource reference: %w", err)
		}
		resources = append(resources, resource)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read work resource references: %w", err)
	}
	return resources, nil
}

func (b *Brain) expireProjectAttempts(ctx context.Context, namespaceID int64) error {
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin project lease cleanup: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockWorkGraph(ctx, tx, namespaceID); err != nil {
		return err
	}
	rows, err := tx.Query(ctx,
		`SELECT attempt.work_item_id
		 FROM work_attempts attempt
		 JOIN work_items item ON item.id = attempt.work_item_id
		 WHERE item.namespace_id = $1 AND item.deleted_at IS NULL
		   AND attempt.status = 'active' AND attempt.lease_expires_at <= clock_timestamp()
		 ORDER BY attempt.work_item_id FOR UPDATE OF attempt`, namespaceID,
	)
	if err != nil {
		return fmt.Errorf("list expired project attempts: %w", err)
	}
	workItemIDs := make([]int64, 0)
	for rows.Next() {
		var workItemID int64
		if err := rows.Scan(&workItemID); err != nil {
			rows.Close()
			return fmt.Errorf("scan expired project attempt: %w", err)
		}
		workItemIDs = append(workItemIDs, workItemID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read expired project attempts: %w", err)
	}
	rows.Close()
	for _, workItemID := range workItemIDs {
		if err := b.expireStaleWorkAttempts(ctx, tx, namespaceID, workItemID); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit project lease cleanup: %w", err)
	}
	return nil
}

func scanAgentWorkRows(rows pgx.Rows) ([]models.AgentWorkItem, error) {
	defer rows.Close()
	items := make([]models.AgentWorkItem, 0)
	for rows.Next() {
		var item models.AgentWorkItem
		if err := rows.Scan(
			&item.ID, &item.GoalID, &item.IssueKey, &item.Title, &item.Status, &item.Owner,
			&item.NextAction, &item.RequiredCapabilities,
		); err != nil {
			return nil, fmt.Errorf("scan project work: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read project work: %w", err)
	}
	return items, nil
}

func compactProjectWorkItems(items []models.AgentWorkItem) []models.AgentWorkItem {
	for index := range items {
		items[index].IssueKey = compactGoalMapText(items[index].IssueKey, 96)
		items[index].Title = compactGoalMapText(items[index].Title, 256)
		items[index].Owner = compactGoalMapText(items[index].Owner, 128)
		items[index].NextAction = compactGoalMapText(items[index].NextAction, 384)
	}
	return items
}

// ResumeProject returns a compact, Git-independent starting point for any Web
// MCP agent. Capabilities are routing hints; authorization remains tied to the
// authenticated namespace and principal.
func (b *Brain) ResumeProject(ctx context.Context, namespaceSlug string, namespaceID int64, principalID, agentID string, capabilities []string, filterCapabilities bool) (*models.ProjectResumeBrief, error) {
	if err := validatePath(namespaceSlug); err != nil {
		return nil, err
	}
	capabilities, err := NormalizeWorkCapabilities(capabilities)
	if err != nil {
		return nil, err
	}
	agentID = strings.TrimSpace(agentID)
	if agentID != "" {
		if err := validateContent(agentID); err != nil {
			return nil, fmt.Errorf("brain: project resume agent: %w", err)
		}
	}
	principalID, err = validateWorkPrincipalID(principalID)
	if err != nil {
		return nil, err
	}
	if err := b.expireProjectAttempts(ctx, namespaceID); err != nil {
		return nil, err
	}
	namespace, err := b.GetNamespace(ctx, namespaceSlug)
	if err != nil {
		return nil, err
	}
	if namespace.ID != namespaceID {
		return nil, fmt.Errorf("brain: project namespace changed while resuming")
	}
	brief := &models.ProjectResumeBrief{
		NamespaceID: namespaceID, Namespace: namespaceSlug,
		ActiveWork: []models.AgentWorkItem{}, ReadyWork: []models.AgentWorkItem{},
	}
	tree, err := b.GetProjectGoalTree(ctx, namespaceID)
	if err != nil {
		return nil, err
	}
	if tree.RootGoalID != nil {
		for _, goal := range tree.Goals {
			if goal.ID == *tree.RootGoalID {
				root := compactGoalBrief(goal)
				brief.SharedGoal = &root
				break
			}
		}
	}
	goalIDs := make([]int64, 0, len(tree.Goals))
	for _, goal := range tree.Goals {
		goalIDs = append(goalIDs, goal.ID)
	}
	if err := b.pool.QueryRow(ctx,
		`SELECT
		   (SELECT count(*) FROM goals WHERE namespace_id = $1 AND deleted_at IS NULL),
		   count(*) FILTER (WHERE status IN ('ready', 'backlog')),
		   count(*) FILTER (WHERE status IN ('doing', 'review')),
		   count(*) FILTER (WHERE status = 'blocked'),
		   count(*) FILTER (WHERE status IN ('done', 'canceled'))
		 FROM work_items WHERE namespace_id = $1 AND deleted_at IS NULL`, namespaceID,
	).Scan(&brief.Counts.Goals, &brief.Counts.Ready, &brief.Counts.Doing, &brief.Counts.Blocked, &brief.Counts.Done); err != nil {
		return nil, fmt.Errorf("count project work: %w", err)
	}
	activeRows, err := b.pool.Query(ctx,
		`SELECT item.id, item.goal_id, item.issue_key, item.title, item.status, item.owner,
		        coalesce(nullif(state.current_next_action, ''), checkpoint.next_action, ''),
		        coalesce(ARRAY(SELECT capability FROM work_item_capabilities required
		                       WHERE required.work_item_id = item.id ORDER BY capability), '{}'::text[])
		 FROM work_attempts attempt
		 JOIN work_items item ON item.id = attempt.work_item_id AND item.deleted_at IS NULL
		 LEFT JOIN work_execution_states state ON state.work_item_id = item.id
		 LEFT JOIN LATERAL (
		   SELECT saved.next_action FROM work_checkpoints saved
		   WHERE saved.attempt_id = attempt.id ORDER BY saved.created_at DESC, saved.id DESC LIMIT 1
		 ) checkpoint ON true
		 WHERE item.namespace_id = $1 AND attempt.status = 'active'
		   AND attempt.lease_expires_at > clock_timestamp() AND attempt.principal_id = $2
		   AND ($3 = '' OR attempt.agent_id = $3)
		 ORDER BY attempt.started_at, attempt.id LIMIT $4`,
		namespaceID, principalID, agentID, projectActiveWorkLimit+1,
	)
	if err != nil {
		return nil, fmt.Errorf("list active project work: %w", err)
	}
	active, err := scanAgentWorkRows(activeRows)
	if err != nil {
		return nil, err
	}
	if len(active) > projectActiveWorkLimit {
		brief.MoreActive = true
		active = active[:projectActiveWorkLimit]
	}
	brief.ActiveWork = compactProjectWorkItems(active)

	readyRows, err := b.pool.Query(ctx,
		`SELECT item.id, item.goal_id, item.issue_key, item.title, item.status, item.owner,
		        state.current_next_action,
		        coalesce(ARRAY(SELECT capability FROM work_item_capabilities required
		                       WHERE required.work_item_id = item.id ORDER BY capability), '{}'::text[])
		 FROM work_items item
		 JOIN work_execution_states state ON state.work_item_id = item.id AND state.current_next_action <> ''
		 WHERE item.namespace_id = $1 AND item.deleted_at IS NULL AND item.status IN ('ready', 'backlog')
		   AND EXISTS (SELECT 1 FROM work_completion_conditions condition
		               WHERE condition.work_item_id = item.id AND condition.superseded_at IS NULL AND condition.required)
		   AND NOT EXISTS (SELECT 1 FROM work_attempts attempt
		                   WHERE attempt.work_item_id = item.id AND attempt.status = 'active')
		   AND NOT EXISTS (
		     SELECT 1 FROM work_item_edges edge
		     JOIN work_items blocker ON blocker.id = edge.from_item_id AND blocker.deleted_at IS NULL
		     WHERE edge.to_item_id = item.id AND edge.edge_type = 'blocks' AND edge.deleted_at IS NULL
		       AND blocker.status NOT IN ('done', 'canceled')
		   )
		   AND (NOT $2 OR NOT EXISTS (
		     SELECT 1 FROM work_item_capabilities required
		     WHERE required.work_item_id = item.id AND NOT (required.capability = ANY($3))
		   ))
		   AND (NOT $5 OR item.goal_id IS NULL OR item.goal_id = ANY($6))
		 ORDER BY item.priority DESC, item.position, item.id LIMIT $4`,
		namespaceID, filterCapabilities, capabilities, projectReadyWorkLimit+1,
		tree.RootGoalID != nil, goalIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("list ready project work: %w", err)
	}
	ready, err := scanAgentWorkRows(readyRows)
	if err != nil {
		return nil, err
	}
	if len(ready) > projectReadyWorkLimit {
		brief.MoreReady = true
		ready = ready[:projectReadyWorkLimit]
	}
	brief.ReadyWork = compactProjectWorkItems(ready)
	if len(brief.ActiveWork) > 0 {
		brief.NextAction = brief.ActiveWork[0].NextAction
	} else if len(brief.ReadyWork) > 0 {
		brief.NextAction = brief.ReadyWork[0].NextAction
	}
	return brief, nil
}

// SpawnWorkForPrincipal lets the current lease owner decompose work while
// preserving hierarchy, blockers, completion conditions, and retry safety.
func (b *Brain) SpawnWorkForPrincipal(ctx context.Context, attemptID int64, leaseToken, principalID string, input SpawnWorkInput, actionKey string) (*models.SpawnedWork, error) {
	input.Title = strings.TrimSpace(input.Title)
	input.Description = strings.TrimSpace(input.Description)
	input.IssueType = strings.TrimSpace(input.IssueType)
	input.Reporter = strings.TrimSpace(input.Reporter)
	input.Relationship = strings.TrimSpace(input.Relationship)
	if input.IssueType == "" {
		input.IssueType = "task"
	}
	if input.Relationship == "" {
		input.Relationship = "child"
	}
	if input.Relationship != "child" && input.Relationship != "prerequisite" && input.Relationship != "related" {
		return nil, fmt.Errorf("brain: invalid spawned work relationship %q", input.Relationship)
	}
	if err := validateContent(input.Title); err != nil {
		return nil, fmt.Errorf("brain: spawned work title: %w", err)
	}
	if len(input.Description) > maxContentLen {
		return nil, ErrContentTooLong
	}
	if err := validateWorkItemType(input.IssueType); err != nil {
		return nil, err
	}
	if err := validatePosition(input.Position); err != nil {
		return nil, err
	}
	input.NextAction, _ = strings.CutSuffix(strings.TrimSpace(input.NextAction), "\n")
	var err error
	input.NextAction, err = validateWorkNextAction(input.NextAction)
	if err != nil {
		return nil, err
	}
	input.Conditions, err = validateCompletionConditionInputs(input.Conditions)
	if err != nil {
		return nil, err
	}
	input.Capabilities, err = NormalizeWorkCapabilities(input.Capabilities)
	if err != nil {
		return nil, err
	}
	actionKey, err = validateWorkActionKey(actionKey)
	if err != nil {
		return nil, err
	}
	requestHash, err := workActionRequestHash("spawn", struct {
		AttemptID int64          `json:"attempt_id"`
		Input     SpawnWorkInput `json:"input"`
	}{AttemptID: attemptID, Input: input})
	if err != nil {
		return nil, err
	}
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin spawned work: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	graphNamespaceID, err := lockWorkGraphForAttempt(ctx, tx, attemptID)
	if err != nil {
		return nil, err
	}
	leased, err := lockWorkAttemptForAction(ctx, tx, attemptID, leaseToken, principalID)
	if err != nil {
		return nil, err
	}
	if leased.NamespaceID != graphNamespaceID {
		return nil, fmt.Errorf("brain: work attempt namespace changed while spawning work")
	}
	receipt, err := loadWorkActionReceipt(ctx, tx, leased.Attempt.WorkItemID, &attemptID, "spawn", actionKey, requestHash)
	if err != nil {
		return nil, err
	}
	if receipt != nil {
		var replay models.SpawnedWork
		if err := decodeWorkActionResponse(receipt, &replay); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit spawned work replay: %w", err)
		}
		return &replay, nil
	}
	if !leased.LeaseValid {
		return nil, ErrWorkAttemptLease
	}
	parent, err := scanWorkItem(tx.QueryRow(ctx,
		`SELECT `+workItemColumns+` FROM work_items WHERE id = $1 AND deleted_at IS NULL`,
		leased.Attempt.WorkItemID,
	))
	if err != nil {
		return nil, fmt.Errorf("read parent work for decomposition: %w", err)
	}
	var parentID *int64
	if input.Relationship == "child" {
		value := parent.ID
		parentID = &value
	}
	child, err := b.insertWorkItem(ctx, tx, leased.NamespaceID, WorkItemInput{
		GoalID: parent.GoalID, ParentID: parentID, IssueType: input.IssueType,
		Reporter: input.Reporter, Title: input.Title, Description: input.Description,
		Status: "ready", Priority: input.Priority, Position: input.Position,
	})
	if err != nil {
		return nil, err
	}
	if err := setWorkCapabilitiesTx(ctx, tx, child.ID, input.Capabilities); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO work_execution_states (work_item_id, current_next_action) VALUES ($1, $2)`,
		child.ID, input.NextAction,
	); err != nil {
		return nil, fmt.Errorf("prepare spawned work action: %w", err)
	}
	conditions := make([]models.WorkCompletionCondition, 0, len(input.Conditions))
	for position, conditionInput := range input.Conditions {
		condition, err := scanWorkCondition(tx.QueryRow(ctx,
			`INSERT INTO work_completion_conditions
			    (work_item_id, kind, description, verification, required, position)
			 VALUES ($1, $2, $3, $4, $5, $6)
			 RETURNING `+workConditionColumns,
			child.ID, conditionInput.Kind, conditionInput.Description, conditionInput.Verification,
			conditionInput.Required, position,
		))
		if err != nil {
			return nil, fmt.Errorf("prepare spawned work condition: %w", err)
		}
		condition.EvidenceIDs = []int64{}
		conditions = append(conditions, *condition)
	}
	edgeType := "blocks"
	if input.Relationship == "related" {
		edgeType = "relates_to"
	}
	var edge models.WorkItemEdge
	if err := tx.QueryRow(ctx,
		`INSERT INTO work_item_edges (namespace_id, from_item_id, to_item_id, edge_type)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, namespace_id, from_item_id, to_item_id, edge_type, created_at, deleted_at`,
		leased.NamespaceID, child.ID, parent.ID, edgeType,
	).Scan(&edge.ID, &edge.NamespaceID, &edge.FromItemID, &edge.ToItemID, &edge.EdgeType, &edge.CreatedAt, &edge.DeletedAt); err != nil {
		return nil, fmt.Errorf("link spawned work: %w", err)
	}
	child.RequiredCapabilities = append([]string(nil), input.Capabilities...)
	result := &models.SpawnedWork{
		WorkItem: *child, Relationship: input.Relationship, Edge: &edge,
		RequiredCapabilities: append([]string(nil), input.Capabilities...),
		Preparation: models.WorkPreparation{
			WorkItemID: child.ID, NextAction: input.NextAction, CompletionConditions: conditions,
		},
	}
	if err := insertWorkExecutionEvent(ctx, tx, leased.NamespaceID, parent.ID, &attemptID, "work.spawned", workActionKeyDigest(actionKey), map[string]any{
		"spawned_work_item_id": child.ID, "relationship": input.Relationship,
		"required_capabilities": input.Capabilities,
	}); err != nil {
		return nil, err
	}
	childID := child.ID
	if err := storeWorkActionReceipt(ctx, tx, parent.ID, &attemptID, "spawn", actionKey, requestHash, &childID, result); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit spawned work: %w", err)
	}
	return result, nil
}

func (b *Brain) SpawnWork(ctx context.Context, attemptID int64, leaseToken string, input SpawnWorkInput, actionKey string) (*models.SpawnedWork, error) {
	return b.SpawnWorkForPrincipal(ctx, attemptID, leaseToken, "", input, actionKey)
}
