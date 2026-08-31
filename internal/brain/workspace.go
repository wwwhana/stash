package brain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/alash3al/stash/internal/models"
	"github.com/jackc/pgx/v5"
)

var (
	ErrWorkspaceBindingRequired  = fmt.Errorf("brain: workspace is not bound to a project namespace")
	ErrWorkspaceBindingConflict  = fmt.Errorf("brain: workspace identity points to more than one project namespace")
	ErrWorkspaceIdentityRequired = fmt.Errorf("brain: repository_instance_id, git_common_dir, git_dir, and worktree_path are required")
	ErrWorkspacePathConflict     = fmt.Errorf("brain: worktree path is already used by an active workspace")
)

const (
	workspaceResumeRecentLimit   = 20
	workspaceResumeRecentMax     = 100
	workspaceResumeGraphLimit    = 200
	workspaceResumeEdgeLimit     = 400
	workspaceResumeWorktreeLimit = 50
)

const workspaceRepositoryColumns = `id, namespace_id, repository_instance_id, provider, provider_repository_id,
 remote_url, remote_fingerprint, git_common_dir, last_seen_at, metadata,
 created_at, updated_at, deleted_at`

// WorkspaceIdentityInput contains facts observed by the local Git bridge. None
// of these caller-controlled values grant authorization; handlers must scope
// repository lookup to namespace IDs already authorized for the principal.
type WorkspaceIdentityInput struct {
	CWD                  string          `json:"cwd"`
	RepositoryInstanceID string          `json:"repository_instance_id"`
	Provider             string          `json:"repository_provider,omitempty"`
	ProviderRepositoryID string          `json:"repository_provider_id,omitempty"`
	RemoteURL            string          `json:"remote_url,omitempty"`
	GitCommonDir         string          `json:"git_common_dir"`
	GitDir               string          `json:"git_dir"`
	WorktreePath         string          `json:"worktree_path"`
	Branch               string          `json:"branch,omitempty"`
	HeadSHA              string          `json:"head_sha,omitempty"`
	Status               string          `json:"worktree_status"`
	AgentID              string          `json:"agent_id,omitempty"`
	Metadata             json.RawMessage `json:"metadata,omitempty"`
}

type normalizedWorkspaceIdentity struct {
	WorkspaceIdentityInput
	RemoteFingerprint string
	WorktreeSlot      string
	WorktreeKey       string
}

func normalizeWorkspacePath(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, `\`, "/"))
	if value == "" {
		return ""
	}
	cleaned := path.Clean(value)
	if cleaned == "." && value != "." {
		return ""
	}
	return cleaned
}

// NormalizeWorkspaceRemoteURL strips credentials and transport-specific Git
// syntax so SSH and HTTPS forms of the same remote resolve to one safe value.
func NormalizeWorkspaceRemoteURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if !strings.Contains(raw, "://") {
		if at := strings.LastIndex(raw, "@"); at >= 0 {
			raw = raw[at+1:]
		}
		if colon := strings.Index(raw, ":"); colon > 0 && !strings.Contains(raw[:colon], "/") {
			raw = "ssh://" + raw[:colon] + "/" + raw[colon+1:]
		}
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("brain: parse remote URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("brain: remote URL must include a host")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return "", fmt.Errorf("brain: remote URL must include a host")
	}
	port := parsed.Port()
	if port != "" && !((parsed.Scheme == "https" && port == "443") || (parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "ssh" && port == "22")) {
		host += ":" + port
	}
	repositoryPath := strings.Trim(strings.ReplaceAll(parsed.Path, `\`, "/"), "/")
	repositoryPath = strings.TrimSuffix(repositoryPath, ".git")
	if repositoryPath == "" || strings.Contains(repositoryPath, "..") {
		return "", fmt.Errorf("brain: remote URL has no valid repository path")
	}
	return host + "/" + repositoryPath, nil
}

func workspaceRemoteFingerprint(remote string) string {
	if remote == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("stash-workspace-remote-v1\x00" + remote))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// WorkspaceWorktreeSlot returns the Git-managed identity inside one repository
// instance. It survives `git worktree move` and a move of the whole checkout.
func WorkspaceWorktreeSlot(gitCommonDir, gitDir string) (string, error) {
	common := normalizeWorkspacePath(gitCommonDir)
	dir := normalizeWorkspacePath(gitDir)
	if common == "" || dir == "" {
		return "", ErrWorkspaceIdentityRequired
	}
	if common == dir {
		return "main", nil
	}
	prefix := strings.TrimSuffix(common, "/") + "/worktrees/"
	if !strings.HasPrefix(dir, prefix) {
		return "", fmt.Errorf("brain: git_dir must be the common directory or one of its worktree entries")
	}
	slot := strings.TrimPrefix(dir, prefix)
	if slot == "" || strings.Contains(slot, "/") || slot == "." || slot == ".." {
		return "", fmt.Errorf("brain: invalid Git worktree entry")
	}
	return slot, nil
}

// WorkspaceWorktreeKey separates clones through repositoryInstanceID and
// remains stable when their display paths move.
func WorkspaceWorktreeKey(repositoryInstanceID, slot string) (string, error) {
	repositoryInstanceID = strings.TrimSpace(repositoryInstanceID)
	slot = strings.TrimSpace(slot)
	if repositoryInstanceID == "" || slot == "" {
		return "", ErrWorkspaceIdentityRequired
	}
	if len(repositoryInstanceID) > 256 || len(slot) > 256 {
		return "", ErrContentTooLong
	}
	sum := sha256.Sum256([]byte("stash-worktree-v1\x00" + repositoryInstanceID + "\x00" + slot))
	return "wtk_v1_" + hex.EncodeToString(sum[:]), nil
}

func normalizeWorkspaceIdentity(input WorkspaceIdentityInput) (normalizedWorkspaceIdentity, error) {
	input.RepositoryInstanceID = strings.TrimSpace(input.RepositoryInstanceID)
	input.Provider = strings.ToLower(strings.TrimSpace(input.Provider))
	input.ProviderRepositoryID = strings.TrimSpace(input.ProviderRepositoryID)
	input.GitCommonDir = normalizeWorkspacePath(input.GitCommonDir)
	input.GitDir = normalizeWorkspacePath(input.GitDir)
	input.WorktreePath = normalizeWorkspacePath(input.WorktreePath)
	input.CWD = normalizeWorkspacePath(input.CWD)
	input.Branch = strings.TrimSpace(input.Branch)
	input.HeadSHA = strings.TrimSpace(input.HeadSHA)
	input.AgentID = strings.TrimSpace(input.AgentID)
	input.Status = strings.TrimSpace(input.Status)
	if input.Status == "" {
		input.Status = "unknown"
	}
	if input.RepositoryInstanceID == "" || input.GitCommonDir == "" || input.GitDir == "" || input.WorktreePath == "" {
		return normalizedWorkspaceIdentity{}, ErrWorkspaceIdentityRequired
	}
	for label, value := range map[string]string{
		"repository_instance_id": input.RepositoryInstanceID,
		"git_common_dir":         input.GitCommonDir,
		"git_dir":                input.GitDir,
		"worktree_path":          input.WorktreePath,
		"branch":                 input.Branch,
		"head_sha":               input.HeadSHA,
		"agent_id":               input.AgentID,
	} {
		if len(value) > maxContentLen {
			return normalizedWorkspaceIdentity{}, fmt.Errorf("brain: %s: %w", label, ErrContentTooLong)
		}
	}
	if input.ProviderRepositoryID != "" && input.Provider == "" {
		return normalizedWorkspaceIdentity{}, fmt.Errorf("brain: provider is required with provider_repository_id")
	}
	if err := validateWorktreeStatus(input.Status); err != nil {
		return normalizedWorkspaceIdentity{}, err
	}
	if input.Metadata == nil {
		input.Metadata = json.RawMessage(`{}`)
	}
	if len(input.Metadata) > maxContentLen || !json.Valid(input.Metadata) {
		return normalizedWorkspaceIdentity{}, fmt.Errorf("brain: workspace metadata must be a valid JSON object within the content limit")
	}
	var metadataObject map[string]any
	if err := json.Unmarshal(input.Metadata, &metadataObject); err != nil || metadataObject == nil {
		return normalizedWorkspaceIdentity{}, fmt.Errorf("brain: workspace metadata must be a JSON object")
	}
	remote, err := NormalizeWorkspaceRemoteURL(input.RemoteURL)
	if err != nil {
		return normalizedWorkspaceIdentity{}, err
	}
	input.RemoteURL = remote
	slot, err := WorkspaceWorktreeSlot(input.GitCommonDir, input.GitDir)
	if err != nil {
		return normalizedWorkspaceIdentity{}, err
	}
	key, err := WorkspaceWorktreeKey(input.RepositoryInstanceID, slot)
	if err != nil {
		return normalizedWorkspaceIdentity{}, err
	}
	return normalizedWorkspaceIdentity{
		WorkspaceIdentityInput: input,
		RemoteFingerprint:      workspaceRemoteFingerprint(remote),
		WorktreeSlot:           slot,
		WorktreeKey:            key,
	}, nil
}

func scanWorkspaceRepository(row pgx.Row) (*models.WorkspaceRepository, error) {
	var repository models.WorkspaceRepository
	err := row.Scan(
		&repository.ID, &repository.NamespaceID, &repository.RepositoryInstanceID,
		&repository.Provider, &repository.ProviderRepositoryID, &repository.RemoteURL,
		&repository.RemoteFingerprint, &repository.GitCommonDir, &repository.LastSeenAt,
		&repository.Metadata, &repository.CreatedAt, &repository.UpdatedAt, &repository.DeletedAt,
	)
	if err != nil {
		return nil, err
	}
	return &repository, nil
}

func workspaceNamespaceSet(ids []int64) map[int64]struct{} {
	result := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id > 0 {
			result[id] = struct{}{}
		}
	}
	return result
}

func upsertWorkspaceRepositoryTx(ctx context.Context, tx pgx.Tx, allowedNamespaceIDs []int64, targetNamespaceID *int64, identity normalizedWorkspaceIdentity) (*models.WorkspaceRepository, error) {
	allowed := workspaceNamespaceSet(allowedNamespaceIDs)
	if len(allowed) == 0 {
		return nil, ErrNamespacesRequired
	}
	if targetNamespaceID != nil {
		if _, ok := allowed[*targetNamespaceID]; !ok {
			return nil, fmt.Errorf("brain: target workspace namespace is not authorized")
		}
	}

	rows, err := tx.Query(ctx,
		`SELECT `+workspaceRepositoryColumns+`
		 FROM workspace_repositories
		 WHERE namespace_id = ANY($1) AND deleted_at IS NULL
		   AND (repository_instance_id = $2
		        OR ($3 <> '' AND $4 <> '' AND provider = $3 AND provider_repository_id = $4)
		        OR ($5 <> '' AND remote_fingerprint = $5))
		 ORDER BY id FOR UPDATE`,
		allowedNamespaceIDs, identity.RepositoryInstanceID, identity.Provider,
		identity.ProviderRepositoryID, identity.RemoteFingerprint,
	)
	if err != nil {
		return nil, fmt.Errorf("find workspace repository binding: %w", err)
	}
	var matches []models.WorkspaceRepository
	for rows.Next() {
		repository, err := scanWorkspaceRepository(rows)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan workspace repository binding: %w", err)
		}
		matches = append(matches, *repository)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("read workspace repository bindings: %w", err)
	}
	rows.Close()

	matchedNamespaces := make(map[int64]struct{})
	for _, match := range matches {
		matchedNamespaces[match.NamespaceID] = struct{}{}
	}
	if len(matchedNamespaces) > 1 {
		return nil, ErrWorkspaceBindingConflict
	}
	if targetNamespaceID == nil {
		for namespaceID := range matchedNamespaces {
			resolved := namespaceID
			targetNamespaceID = &resolved
		}
		if targetNamespaceID == nil {
			return nil, ErrWorkspaceBindingRequired
		}
	} else if len(matchedNamespaces) == 1 {
		for namespaceID := range matchedNamespaces {
			if namespaceID != *targetNamespaceID {
				return nil, ErrWorkspaceBindingConflict
			}
		}
	}

	for _, match := range matches {
		if match.RepositoryInstanceID != identity.RepositoryInstanceID {
			continue
		}
		if match.ProviderRepositoryID != "" && identity.ProviderRepositoryID != "" &&
			(match.Provider != identity.Provider || match.ProviderRepositoryID != identity.ProviderRepositoryID) {
			return nil, ErrWorkspaceBindingConflict
		}
		return scanWorkspaceRepository(tx.QueryRow(ctx,
			`UPDATE workspace_repositories SET
			   provider = CASE WHEN $2 <> '' THEN $2 ELSE provider END,
			   provider_repository_id = CASE WHEN $3 <> '' THEN $3 ELSE provider_repository_id END,
			   remote_url = CASE WHEN $4 <> '' THEN $4 ELSE remote_url END,
			   remote_fingerprint = CASE WHEN $5 <> '' THEN $5 ELSE remote_fingerprint END,
			   git_common_dir = $6, last_seen_at = clock_timestamp(), metadata = $7,
			   updated_at = now(), deleted_at = NULL
			 WHERE id = $1 RETURNING `+workspaceRepositoryColumns,
			match.ID, identity.Provider, identity.ProviderRepositoryID, identity.RemoteURL,
			identity.RemoteFingerprint, identity.GitCommonDir, identity.Metadata,
		))
	}

	return scanWorkspaceRepository(tx.QueryRow(ctx,
		`INSERT INTO workspace_repositories
		    (namespace_id, repository_instance_id, provider, provider_repository_id,
		     remote_url, remote_fingerprint, git_common_dir, last_seen_at, metadata)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, clock_timestamp(), $8)
		 ON CONFLICT (namespace_id, repository_instance_id) WHERE deleted_at IS NULL DO UPDATE SET
		   provider = CASE WHEN EXCLUDED.provider <> '' THEN EXCLUDED.provider ELSE workspace_repositories.provider END,
		   provider_repository_id = CASE WHEN EXCLUDED.provider_repository_id <> '' THEN EXCLUDED.provider_repository_id ELSE workspace_repositories.provider_repository_id END,
		   remote_url = CASE WHEN EXCLUDED.remote_url <> '' THEN EXCLUDED.remote_url ELSE workspace_repositories.remote_url END,
		   remote_fingerprint = CASE WHEN EXCLUDED.remote_fingerprint <> '' THEN EXCLUDED.remote_fingerprint ELSE workspace_repositories.remote_fingerprint END,
		   git_common_dir = EXCLUDED.git_common_dir, last_seen_at = clock_timestamp(),
		   metadata = EXCLUDED.metadata, updated_at = now(), deleted_at = NULL
		 RETURNING `+workspaceRepositoryColumns,
		*targetNamespaceID, identity.RepositoryInstanceID, identity.Provider,
		identity.ProviderRepositoryID, identity.RemoteURL, identity.RemoteFingerprint,
		identity.GitCommonDir, identity.Metadata,
	))
}

func upsertWorkspaceWorktreeTx(ctx context.Context, tx pgx.Tx, repository *models.WorkspaceRepository, identity normalizedWorkspaceIdentity) (*models.Worktree, error) {
	if repository == nil {
		return nil, ErrWorkspaceBindingRequired
	}
	var existingID int64
	var previousPath string
	err := tx.QueryRow(ctx,
		`SELECT id, worktree_path FROM worktrees
		 WHERE namespace_id = $1 AND worktree_key = $2 AND deleted_at IS NULL FOR UPDATE`,
		repository.NamespaceID, identity.WorktreeKey,
	).Scan(&existingID, &previousPath)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("find stable worktree: %w", err)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx,
			`SELECT id, worktree_path FROM worktrees
			 WHERE namespace_id = $1 AND worktree_path = $2 AND deleted_at IS NULL FOR UPDATE`,
			repository.NamespaceID, identity.WorktreePath,
		).Scan(&existingID, &previousPath)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("find legacy worktree path: %w", err)
		}
		if err == nil {
			var currentKey *string
			var active bool
			if err := tx.QueryRow(ctx,
				`SELECT worktree_key,
				        EXISTS (SELECT 1 FROM work_attempts WHERE worktree_id = worktrees.id AND status = 'active')
				 FROM worktrees WHERE id = $1`, existingID,
			).Scan(&currentKey, &active); err != nil {
				return nil, fmt.Errorf("check legacy worktree identity: %w", err)
			}
			if currentKey != nil && *currentKey != identity.WorktreeKey {
				if active {
					return nil, ErrWorkspacePathConflict
				}
				if _, err := tx.Exec(ctx,
					`UPDATE worktrees SET status = 'removed', removed_at = clock_timestamp(),
					 deleted_at = clock_timestamp(), updated_at = now() WHERE id = $1`, existingID,
				); err != nil {
					return nil, fmt.Errorf("retire replaced worktree path: %w", err)
				}
				existingID = 0
			}
		}
	}

	repositoryLabel := identity.RemoteURL
	if repositoryLabel == "" {
		repositoryLabel = identity.GitCommonDir
	}
	if existingID == 0 {
		worktree, err := scanWorktree(tx.QueryRow(ctx,
			`INSERT INTO worktrees
			    (namespace_id, workspace_repository_id, worktree_key, repository, worktree_path,
			     git_dir, worktree_slot, branch, head_sha, status, agent_id, last_seen_at,
			     stale_at, missing_since, removed_at, metadata, deleted_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, clock_timestamp(),
			         CASE WHEN $10 = 'stale' THEN clock_timestamp() ELSE NULL END,
			         CASE WHEN $10 = 'missing' THEN clock_timestamp() ELSE NULL END,
			         CASE WHEN $10 = 'removed' THEN clock_timestamp() ELSE NULL END,
			         $12, NULL)
			 RETURNING `+worktreeColumns,
			repository.NamespaceID, repository.ID, identity.WorktreeKey, repositoryLabel,
			identity.WorktreePath, identity.GitDir, identity.WorktreeSlot, identity.Branch,
			identity.HeadSHA, identity.Status, identity.AgentID, identity.Metadata,
		))
		if err != nil {
			return nil, fmt.Errorf("create stable worktree: %w", err)
		}
		return worktree, nil
	}

	worktree, err := scanWorktree(tx.QueryRow(ctx,
		`UPDATE worktrees SET
		   workspace_repository_id = $2, worktree_key = $3, repository = $4,
		   worktree_path = $5, git_dir = $6, worktree_slot = $7, branch = $8,
		   head_sha = $9, status = $10, agent_id = $11, last_seen_at = clock_timestamp(),
		   stale_at = CASE WHEN $10 = 'stale' THEN coalesce(stale_at, clock_timestamp()) ELSE NULL END,
		   missing_since = CASE WHEN $10 = 'missing' THEN coalesce(missing_since, clock_timestamp()) ELSE NULL END,
		   removed_at = CASE WHEN $10 = 'removed' THEN coalesce(removed_at, clock_timestamp()) ELSE NULL END,
		   metadata = $12, updated_at = now(), deleted_at = NULL
		 WHERE id = $1 RETURNING `+worktreeColumns,
		existingID, repository.ID, identity.WorktreeKey, repositoryLabel, identity.WorktreePath,
		identity.GitDir, identity.WorktreeSlot, identity.Branch, identity.HeadSHA,
		identity.Status, identity.AgentID, identity.Metadata,
	))
	if err != nil {
		return nil, fmt.Errorf("update stable worktree: %w", err)
	}
	if previousPath != "" && previousPath != identity.WorktreePath {
		payload, _ := json.Marshal(map[string]string{"from": previousPath, "to": identity.WorktreePath})
		if _, err := tx.Exec(ctx,
			`INSERT INTO work_events (namespace_id, worktree_id, event_type, payload)
			 VALUES ($1, $2, 'worktree.moved', $3)`, repository.NamespaceID, worktree.ID, payload,
		); err != nil {
			return nil, fmt.Errorf("record worktree move: %w", err)
		}
	}
	return worktree, nil
}

func workspaceResolutionTx(ctx context.Context, tx pgx.Tx, repository *models.WorkspaceRepository, worktree *models.Worktree) (*models.WorkspaceResolution, error) {
	var namespace models.Namespace
	if err := tx.QueryRow(ctx,
		`SELECT id, slug, name, description, created_at, updated_at FROM namespaces WHERE id = $1`,
		repository.NamespaceID,
	).Scan(&namespace.ID, &namespace.Slug, &namespace.Name, &namespace.Description, &namespace.CreatedAt, &namespace.UpdatedAt); err != nil {
		return nil, fmt.Errorf("read workspace namespace: %w", err)
	}
	resolution := &models.WorkspaceResolution{Namespace: namespace, Repository: *repository, Worktree: *worktree}

	attempt, err := scanWorkAttempt(tx.QueryRow(ctx,
		`SELECT `+workAttemptColumns+` FROM work_attempts
		 WHERE worktree_id = $1 AND status = 'active' AND lease_expires_at > clock_timestamp()
		 ORDER BY started_at DESC, id DESC LIMIT 1`, worktree.ID,
	))
	if err == nil {
		resolution.ActiveAttempt = attempt
		item, itemErr := scanWorkItem(tx.QueryRow(ctx,
			`SELECT `+workItemColumns+` FROM work_items WHERE id = $1 AND deleted_at IS NULL`, attempt.WorkItemID,
		))
		if itemErr != nil {
			return nil, fmt.Errorf("read active workspace item: %w", itemErr)
		}
		resolution.ActiveWorkItem = item
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("read active workspace attempt: %w", err)
	}

	checkpoint, err := scanWorkCheckpoint(tx.QueryRow(ctx,
		`SELECT checkpoint.id, checkpoint.attempt_id, checkpoint.summary, checkpoint.result,
		        checkpoint.next_action, checkpoint.created_at
		 FROM work_checkpoints checkpoint
		 JOIN work_attempts attempt ON attempt.id = checkpoint.attempt_id
		 JOIN work_items item ON item.id = attempt.work_item_id
		 WHERE attempt.worktree_id = $1 AND item.namespace_id = $2 AND item.deleted_at IS NULL
		 ORDER BY checkpoint.created_at DESC, checkpoint.id DESC LIMIT 1`,
		worktree.ID, repository.NamespaceID,
	))
	if err == nil {
		resolution.LatestCheckpoint = checkpoint
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("read workspace checkpoint: %w", err)
	}
	if resolution.ActiveWorkItem != nil {
		if err := tx.QueryRow(ctx,
			`SELECT coalesce((SELECT current_next_action FROM work_execution_states WHERE work_item_id = $1), '')`,
			resolution.ActiveWorkItem.ID,
		).Scan(&resolution.NextAction); err != nil {
			return nil, fmt.Errorf("read active workspace next action: %w", err)
		}
	}
	if resolution.NextAction == "" && resolution.LatestCheckpoint != nil {
		resolution.NextAction = resolution.LatestCheckpoint.NextAction
	}
	return resolution, nil
}

// ResolveWorkspace binds or resolves a repository, upserts its current
// worktree, records a heartbeat, and returns the active continuation state.
func (b *Brain) ResolveWorkspace(ctx context.Context, allowedNamespaceIDs []int64, targetNamespaceID *int64, input WorkspaceIdentityInput) (*models.WorkspaceResolution, error) {
	identity, err := normalizeWorkspaceIdentity(input)
	if err != nil {
		return nil, err
	}
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin workspace resolution: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	repository, err := upsertWorkspaceRepositoryTx(ctx, tx, allowedNamespaceIDs, targetNamespaceID, identity)
	if err != nil {
		return nil, err
	}
	worktree, err := upsertWorkspaceWorktreeTx(ctx, tx, repository, identity)
	if err != nil {
		return nil, err
	}
	resolution, err := workspaceResolutionTx(ctx, tx, repository, worktree)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit workspace resolution: %w", err)
	}
	return resolution, nil
}

// ClaimWorkspace resolves the local checkout and starts its work lease in one
// transaction. A failed claim cannot leave a newly attached worktree without
// the corresponding attempt.
func (b *Brain) ClaimWorkspace(ctx context.Context, allowedNamespaceIDs []int64, targetNamespaceID *int64, input WorkspaceIdentityInput, workItemID int64, agentID, principalID string, leaseDuration time.Duration, actionKey string) (*models.WorkspaceClaim, error) {
	identity, err := normalizeWorkspaceIdentity(input)
	if err != nil {
		return nil, err
	}
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin workspace claim: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	repository, err := upsertWorkspaceRepositoryTx(ctx, tx, allowedNamespaceIDs, targetNamespaceID, identity)
	if err != nil {
		return nil, err
	}
	worktree, err := upsertWorkspaceWorktreeTx(ctx, tx, repository, identity)
	if err != nil {
		return nil, err
	}
	worktreeID := worktree.ID
	lease, err := b.startWorkAttemptForPrincipalTx(ctx, tx, workItemID, agentID, principalID, &worktreeID, leaseDuration, actionKey)
	if err != nil {
		if errors.Is(err, ErrWorkAttemptLease) || errors.Is(err, ErrWorkBlockersUnfinished) {
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return nil, fmt.Errorf("commit rejected workspace claim state: %w", commitErr)
			}
		}
		return nil, err
	}
	resolution, err := workspaceResolutionTx(ctx, tx, repository, worktree)
	if err != nil {
		return nil, err
	}
	claim := &models.WorkspaceClaim{Resolution: *resolution, Lease: *lease}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit workspace claim: %w", err)
	}
	if goalContext, contextErr := b.GetWorkGoalContext(ctx, workItemID); contextErr == nil {
		claim.Lease.GoalContext = goalContext
	}
	return claim, nil
}

// ReconcileWorkspaceWorktrees marks previously known worktrees missing after
// a complete `git worktree list` pass. It never treats a partial heartbeat as
// proof that another checkout disappeared.
func (b *Brain) ReconcileWorkspaceWorktrees(ctx context.Context, repositoryID int64, seenKeys []string) ([]models.Worktree, error) {
	if repositoryID <= 0 {
		return nil, ErrWorkspaceBindingRequired
	}
	seen := make([]string, 0, len(seenKeys))
	seenSet := make(map[string]struct{}, len(seenKeys))
	for _, key := range seenKeys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, err := hex.DecodeString(strings.TrimPrefix(key, "wtk_v1_")); !strings.HasPrefix(key, "wtk_v1_") || err != nil || len(key) != len("wtk_v1_")+64 {
			return nil, fmt.Errorf("brain: invalid worktree key")
		}
		if _, exists := seenSet[key]; exists {
			continue
		}
		seenSet[key] = struct{}{}
		seen = append(seen, key)
	}
	rows, err := b.pool.Query(ctx,
		`UPDATE worktrees
		 SET status = 'missing', missing_since = coalesce(missing_since, clock_timestamp()),
		     stale_at = NULL, updated_at = now()
		 WHERE workspace_repository_id = $1 AND deleted_at IS NULL
		   AND status NOT IN ('missing', 'merged', 'removed')
		   AND (worktree_key IS NULL OR NOT (worktree_key = ANY($2)))
		 RETURNING `+worktreeColumns,
		repositoryID, seen,
	)
	if err != nil {
		return nil, fmt.Errorf("reconcile workspace worktrees: %w", err)
	}
	return scanWorktreeRows(rows)
}

// MaintainWorkspaceLifecycle converts missed heartbeats to stale and worktrees
// confirmed missing by a full sync to removed after their retention period.
func (b *Brain) MaintainWorkspaceLifecycle(ctx context.Context, staleAfter, removeAfter time.Duration) (models.WorkspaceMaintenanceResult, error) {
	if staleAfter <= 0 || removeAfter <= 0 {
		return models.WorkspaceMaintenanceResult{}, fmt.Errorf("brain: workspace lifecycle durations must be greater than zero")
	}
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return models.WorkspaceMaintenanceResult{}, fmt.Errorf("begin workspace lifecycle maintenance: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result := models.WorkspaceMaintenanceResult{}
	staleTag, err := tx.Exec(ctx,
		`UPDATE worktrees
		 SET status = 'stale', stale_at = coalesce(stale_at, clock_timestamp()), updated_at = now()
		 WHERE workspace_repository_id IS NOT NULL AND deleted_at IS NULL
		   AND status IN ('unknown', 'clean', 'dirty')
		   AND last_seen_at < clock_timestamp() - ($1 * interval '1 second')`, staleAfter.Seconds(),
	)
	if err != nil {
		return models.WorkspaceMaintenanceResult{}, fmt.Errorf("mark stale worktrees: %w", err)
	}
	result.Stale = staleTag.RowsAffected()
	removedTag, err := tx.Exec(ctx,
		`UPDATE worktrees
		 SET status = 'removed', removed_at = coalesce(removed_at, clock_timestamp()), updated_at = now()
		 WHERE workspace_repository_id IS NOT NULL AND deleted_at IS NULL
		   AND status = 'missing' AND missing_since IS NOT NULL
		   AND missing_since < clock_timestamp() - ($1 * interval '1 second')`, removeAfter.Seconds(),
	)
	if err != nil {
		return models.WorkspaceMaintenanceResult{}, fmt.Errorf("retire missing worktrees: %w", err)
	}
	result.Removed = removedTag.RowsAffected()
	if err := tx.Commit(ctx); err != nil {
		return models.WorkspaceMaintenanceResult{}, fmt.Errorf("commit workspace lifecycle maintenance: %w", err)
	}
	return result, nil
}

func (b *Brain) workspaceResumeLimit(requested int) int {
	if requested <= 0 {
		requested = workspaceResumeRecentLimit
	}
	if requested > workspaceResumeRecentMax {
		requested = workspaceResumeRecentMax
	}
	return b.boundedWorkResumeLimit(requested)
}

func (b *Brain) listWorkspaceItemsByStatus(ctx context.Context, namespaceIDs []int64, status string, limit int) ([]models.WorkItem, error) {
	rows, err := b.pool.Query(ctx,
		`SELECT `+workItemColumns+` FROM work_items
		 WHERE namespace_id = ANY($1) AND status = $2 AND deleted_at IS NULL
		 ORDER BY priority DESC, updated_at DESC, position, id LIMIT $3`,
		namespaceIDs, status, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list workspace %s items: %w", status, err)
	}
	items, err := scanWorkItemRows(rows)
	if err != nil {
		return nil, err
	}
	return b.attachWorktreeIDs(ctx, items)
}

func (b *Brain) boundedWorkspaceGraph(ctx context.Context, namespaceIDs []int64) (models.WorkGraph, int, int, error) {
	nodeLimit := b.boundedWorkResumeLimit(workspaceResumeGraphLimit)
	edgeLimit := b.boundedWorkResumeLimit(workspaceResumeEdgeLimit)
	worktreeLimit := b.boundedWorkResumeLimit(workspaceResumeWorktreeLimit)
	var nodeTotal, edgeTotal int
	if err := b.pool.QueryRow(ctx,
		`SELECT
		    (SELECT count(*) FROM work_items WHERE namespace_id = ANY($1) AND deleted_at IS NULL),
		    (SELECT count(*) FROM work_item_edges edge
		     JOIN work_items source_item ON source_item.id = edge.from_item_id AND source_item.deleted_at IS NULL
		     JOIN work_items target_item ON target_item.id = edge.to_item_id AND target_item.deleted_at IS NULL
		     WHERE edge.namespace_id = ANY($1) AND edge.deleted_at IS NULL)`, namespaceIDs,
	).Scan(&nodeTotal, &edgeTotal); err != nil {
		return models.WorkGraph{}, 0, 0, fmt.Errorf("count workspace graph: %w", err)
	}
	nodeRows, err := b.pool.Query(ctx,
		`SELECT `+workItemColumns+` FROM work_items
		 WHERE namespace_id = ANY($1) AND deleted_at IS NULL
		 ORDER BY CASE status
		   WHEN 'doing' THEN 0 WHEN 'blocked' THEN 1 WHEN 'ready' THEN 2
		   WHEN 'review' THEN 3 WHEN 'backlog' THEN 4 WHEN 'done' THEN 5 ELSE 6 END,
		   priority DESC, position, id LIMIT $2`, namespaceIDs, nodeLimit,
	)
	if err != nil {
		return models.WorkGraph{}, 0, 0, fmt.Errorf("list bounded workspace graph items: %w", err)
	}
	nodes, err := scanWorkItemRows(nodeRows)
	if err != nil {
		return models.WorkGraph{}, 0, 0, err
	}
	nodes, err = b.attachWorktreeIDs(ctx, nodes)
	if err != nil {
		return models.WorkGraph{}, 0, 0, err
	}
	edges := make([]models.WorkItemEdge, 0)
	if len(nodes) > 0 {
		nodeIDs := make([]int64, 0, len(nodes))
		for _, node := range nodes {
			nodeIDs = append(nodeIDs, node.ID)
		}
		edgeRows, err := b.pool.Query(ctx,
			`SELECT edge.id, edge.namespace_id, edge.from_item_id, edge.to_item_id,
			        edge.edge_type, edge.created_at, edge.deleted_at
			 FROM work_item_edges edge
			 WHERE edge.namespace_id = ANY($1) AND edge.deleted_at IS NULL
			   AND edge.from_item_id = ANY($2) AND edge.to_item_id = ANY($2)
			 ORDER BY edge.id LIMIT $3`, namespaceIDs, nodeIDs, edgeLimit,
		)
		if err != nil {
			return models.WorkGraph{}, 0, 0, fmt.Errorf("list bounded workspace graph edges: %w", err)
		}
		edges, err = scanWorkItemEdges(edgeRows)
		if err != nil {
			return models.WorkGraph{}, 0, 0, err
		}
	}
	worktreeRows, err := b.pool.Query(ctx,
		`SELECT `+worktreeColumns+` FROM worktrees
		 WHERE namespace_id = ANY($1) AND deleted_at IS NULL
		 ORDER BY last_seen_at DESC NULLS LAST, id DESC LIMIT $2`, namespaceIDs, worktreeLimit,
	)
	if err != nil {
		return models.WorkGraph{}, 0, 0, fmt.Errorf("list bounded workspace worktrees: %w", err)
	}
	worktrees, err := scanWorktreeRows(worktreeRows)
	if err != nil {
		return models.WorkGraph{}, 0, 0, err
	}
	return models.WorkGraph{Nodes: nodes, Edges: edges, Worktrees: worktrees}, nodeTotal, edgeTotal, nil
}

// ResumeWorkspace returns one bounded project snapshot suitable for a new
// session. It uses only persisted state and never invokes the embedding or
// reasoning model.
func (b *Brain) ResumeWorkspace(ctx context.Context, namespaceSlug string, namespaceID int64, worktreeID *int64, principalID string, recentLimit int) (*models.WorkspaceResumeBundle, error) {
	if err := validatePath(namespaceSlug); err != nil {
		return nil, err
	}
	namespace, err := b.GetNamespace(ctx, namespaceSlug)
	if err != nil {
		return nil, err
	}
	if namespace.ID != namespaceID {
		return nil, fmt.Errorf("brain: workspace namespace changed while resuming")
	}
	namespaceIDs, err := b.resolveNamespaceIDs(ctx, []string{namespaceSlug})
	if err != nil {
		return nil, err
	}
	limit := b.workspaceResumeLimit(recentLimit)
	bundle := &models.WorkspaceResumeBundle{
		Namespace:       *namespace,
		Doing:           make([]models.WorkItem, 0),
		Blocked:         make([]models.WorkItem, 0),
		RecentDecisions: make([]models.WorkPlanDecision, 0),
		RecentFailures:  make([]models.Failure, 0),
	}
	bundle.WorkPlan, err = b.GetWorkPlan(ctx, namespaceID)
	if err != nil {
		return nil, err
	}
	bundle.GoalTree = bundle.WorkPlan.GoalTree
	bundle.Totals.Goals = len(bundle.GoalTree.Goals)
	// The top-level goal tree is authoritative in this response. Avoid sending
	// the same tree a second time inside the plan snapshot.
	bundle.WorkPlan.GoalTree = models.GoalTree{RootGoalID: bundle.GoalTree.RootGoalID, Goals: []models.GoalProgress{}}
	bundle.Graph, bundle.Totals.GraphNodes, bundle.Totals.GraphEdges, err = b.boundedWorkspaceGraph(ctx, namespaceIDs)
	if err != nil {
		return nil, err
	}
	bundle.Doing, err = b.listWorkspaceItemsByStatus(ctx, namespaceIDs, "doing", limit)
	if err != nil {
		return nil, err
	}
	bundle.Blocked, err = b.listWorkspaceItemsByStatus(ctx, namespaceIDs, "blocked", limit)
	if err != nil {
		return nil, err
	}
	bundle.RecentDecisions, err = b.ListWorkPlanDecisions(ctx, namespaceID, Pagination{Limit: limit})
	if err != nil {
		return nil, err
	}
	bundle.RecentFailures, err = b.ListFailures(ctx, []string{namespaceSlug}, nil, Pagination{Limit: limit})
	if err != nil {
		return nil, err
	}
	bundle.ProjectContext, err = b.GetContext(ctx, namespaceSlug)
	if err != nil {
		return nil, err
	}
	if err := b.pool.QueryRow(ctx,
		`SELECT
		    (SELECT count(*) FROM work_items WHERE namespace_id = ANY($1) AND status = 'doing' AND deleted_at IS NULL),
		    (SELECT count(*) FROM work_items WHERE namespace_id = ANY($1) AND status = 'blocked' AND deleted_at IS NULL),
		    (SELECT count(*) FROM work_plan_decisions WHERE namespace_id = $2 AND deleted_at IS NULL),
		    (SELECT count(*) FROM failures WHERE namespace_id = ANY($1) AND deleted_at IS NULL)`,
		namespaceIDs, namespaceID,
	).Scan(&bundle.Totals.Doing, &bundle.Totals.Blocked, &bundle.Totals.Decisions, &bundle.Totals.Failures); err != nil {
		return nil, fmt.Errorf("count workspace resume records: %w", err)
	}
	bundle.Truncated.Doing = bundle.Totals.Doing > len(bundle.Doing)
	bundle.Truncated.Goals = bundle.Totals.Goals > len(bundle.GoalTree.Goals)
	bundle.Truncated.Blocked = bundle.Totals.Blocked > len(bundle.Blocked)
	bundle.Truncated.Graph = bundle.Totals.GraphNodes > len(bundle.Graph.Nodes) || bundle.Totals.GraphEdges > len(bundle.Graph.Edges)
	bundle.Truncated.Decisions = bundle.Totals.Decisions > len(bundle.RecentDecisions)
	bundle.Truncated.Failures = bundle.Totals.Failures > len(bundle.RecentFailures)

	var currentWorkItemID int64
	if worktreeID != nil {
		state, err := b.WorkspaceStateByWorktree(ctx, *worktreeID)
		if err != nil {
			return nil, err
		}
		if state.Namespace.ID != namespaceID {
			return nil, fmt.Errorf("brain: worktree is outside the requested project namespace")
		}
		bundle.Worktree = &state.Worktree
		bundle.LatestCheckpoint = state.LatestCheckpoint
		bundle.NextAction = state.NextAction
		if state.ActiveWorkItem != nil {
			currentWorkItemID = state.ActiveWorkItem.ID
		} else {
			latestAttempt, latestErr := scanWorkAttempt(b.pool.QueryRow(ctx,
				`SELECT `+workAttemptColumns+` FROM work_attempts
				 WHERE worktree_id = $1 ORDER BY started_at DESC, id DESC LIMIT 1`, *worktreeID,
			))
			if latestErr != nil && !errors.Is(latestErr, pgx.ErrNoRows) {
				return nil, fmt.Errorf("read latest workspace attempt: %w", latestErr)
			}
			if latestErr == nil && (latestAttempt.Status == "handed_off" || latestAttempt.Status == "expired") {
				currentWorkItemID = latestAttempt.WorkItemID
			}
		}
	} else {
		activeRows, err := b.pool.Query(ctx,
			`SELECT DISTINCT attempt.work_item_id
			 FROM work_attempts attempt
			 JOIN work_items item ON item.id = attempt.work_item_id
			 WHERE item.namespace_id = ANY($1) AND item.deleted_at IS NULL
			   AND attempt.principal_id = $2
			   AND attempt.status = 'active' AND attempt.lease_expires_at > clock_timestamp()
			 ORDER BY attempt.work_item_id LIMIT 2`, namespaceIDs, principalID,
		)
		if err != nil {
			return nil, fmt.Errorf("find active workspace work: %w", err)
		}
		activeIDs := make([]int64, 0, 2)
		for activeRows.Next() {
			var id int64
			if err := activeRows.Scan(&id); err != nil {
				activeRows.Close()
				return nil, fmt.Errorf("scan active workspace work: %w", err)
			}
			activeIDs = append(activeIDs, id)
		}
		if err := activeRows.Err(); err != nil {
			activeRows.Close()
			return nil, fmt.Errorf("read active workspace work: %w", err)
		}
		activeRows.Close()
		if len(activeIDs) == 1 {
			currentWorkItemID = activeIDs[0]
		}
	}
	if currentWorkItemID > 0 {
		bundle.CurrentWork, err = b.GetWorkResumeBundle(ctx, currentWorkItemID, limit)
		if err != nil {
			return nil, err
		}
		if bundle.NextAction == "" {
			bundle.NextAction = bundle.CurrentWork.NextAction
		}
	}
	if bundle.NextAction == "" {
		if err := b.pool.QueryRow(ctx,
			`SELECT state.current_next_action
			 FROM work_items item
			 JOIN work_execution_states state ON state.work_item_id = item.id AND state.current_next_action <> ''
			 WHERE item.namespace_id = ANY($1) AND item.deleted_at IS NULL
			   AND item.status IN ('ready', 'backlog')
			   AND NOT EXISTS (
			       SELECT 1 FROM work_item_edges edge
			       JOIN work_items blocker ON blocker.id = edge.from_item_id AND blocker.deleted_at IS NULL
			       WHERE edge.to_item_id = item.id AND edge.edge_type = 'blocks' AND edge.deleted_at IS NULL
			         AND blocker.status NOT IN ('done', 'canceled')
			   )
			 ORDER BY item.priority DESC, item.position, item.id LIMIT 1`, namespaceIDs,
		).Scan(&bundle.NextAction); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("read workspace next action: %w", err)
		}
	}
	return bundle, nil
}

// WorkspaceStateByWorktree reads the project binding and current attempt for a
// registered stable worktree without refreshing its heartbeat.
func (b *Brain) WorkspaceStateByWorktree(ctx context.Context, worktreeID int64) (*models.WorkspaceResolution, error) {
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin workspace state read: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	worktree, err := scanWorktree(tx.QueryRow(ctx,
		`SELECT `+worktreeColumns+` FROM worktrees WHERE id = $1 AND deleted_at IS NULL`, worktreeID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrWorktreeNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read workspace worktree: %w", err)
	}
	if worktree.WorkspaceRepositoryID == nil {
		return nil, ErrWorkspaceBindingRequired
	}
	repository, err := scanWorkspaceRepository(tx.QueryRow(ctx,
		`SELECT `+workspaceRepositoryColumns+` FROM workspace_repositories
		 WHERE id = $1 AND namespace_id = $2 AND deleted_at IS NULL`,
		*worktree.WorkspaceRepositoryID, worktree.NamespaceID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrWorkspaceBindingRequired
	}
	if err != nil {
		return nil, fmt.Errorf("read workspace repository: %w", err)
	}
	resolution, err := workspaceResolutionTx(ctx, tx, repository, worktree)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit workspace state read: %w", err)
	}
	return resolution, nil
}
