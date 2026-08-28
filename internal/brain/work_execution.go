package brain

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/alash3al/stash/internal/models"
	"github.com/jackc/pgx/v5"
)

const (
	DefaultWorkLeaseDuration = 15 * time.Minute
	MaxWorkLeaseDuration     = 24 * time.Hour
	maxWorkConditions        = 100
	defaultResumeEventLimit  = 50
	maxResumeEventLimit      = 100
	resumeConditionLimit     = 100
	resumeEvidenceLimit      = 25
	resumeWorktreeLimit      = 20
	resumeMemoryLimit        = 50
	resumeMemoryContentLimit = 2048
	resumeBlockerLimit       = 50
)

var (
	ErrActiveWorkAttempt        = errors.New("brain: work item already has an active attempt")
	ErrWorkAttemptNotFound      = errors.New("brain: work attempt not found")
	ErrWorkAttemptLease         = errors.New("brain: work attempt lease is invalid or expired")
	ErrWorkAttemptTerminal      = errors.New("brain: completed or canceled work cannot start an attempt")
	ErrWorkConditionsMissing    = errors.New("brain: work requires at least one required completion condition")
	ErrWorkConditionsIncomplete = errors.New("brain: every required completion condition must be passed or waived with evidence")
	ErrWorkBlockersUnfinished   = errors.New("brain: unfinished blocking work prevents completion")
	ErrWorkConditionNotFound    = errors.New("brain: completion condition not found")
	ErrWorkEvidenceNotFound     = errors.New("brain: work evidence not found")
	ErrWorkActionConflict       = errors.New("brain: action key was already used for a different mutation")
)

// CompletionConditionInput replaces one active condition during PrepareWork.
type CompletionConditionInput struct {
	Kind         string          `json:"kind"`
	Description  string          `json:"description"`
	Required     bool            `json:"required"`
	Verification json.RawMessage `json:"verification"`
}

// WorkCheckpointInput is stored as one append-only row. NextAction is required
// for handoff so a later attempt has an explicit continuation point.
type WorkCheckpointInput struct {
	Summary    string `json:"summary"`
	Result     string `json:"result"`
	NextAction string `json:"next_action"`
}

// WorkFinishInput deliberately has no NextAction. Completion clears the
// persisted continuation point in the same transaction.
type WorkFinishInput struct {
	Summary string `json:"summary"`
	Result  string `json:"result"`
}

// WorkEvidenceInput records an observable result without constraining callers
// to one test runner, artifact store, or review system.
type WorkEvidenceInput struct {
	EvidenceType string          `json:"evidence_type"`
	Summary      string          `json:"summary"`
	Reference    string          `json:"reference"`
	Payload      json.RawMessage `json:"payload"`
}

const workAttemptColumns = `id, work_item_id, worktree_id, attempt_number, agent_id, principal_id, status,
	 lease_expires_at, started_at, ended_at, created_at, updated_at`

const workCheckpointColumns = `id, attempt_id, summary, result, next_action, created_at`

const workConditionColumns = `id, work_item_id, kind, description, verification, required, position, status,
 waiver_reason, verified_by_attempt_id, verified_at, superseded_at, created_at, updated_at`

const workEvidenceColumns = `id, work_item_id, attempt_id, evidence_type, summary, reference, payload,
	 content_digest, principal_id, worktree_head_sha, submitted_at, created_at`

var workConditionKinds = map[string]struct{}{
	"command": {},
	"test":    {},
	"http":    {},
	"file":    {},
	"build":   {},
	"ui":      {},
	"user":    {},
	"custom":  {},
}

func normalizeWorkLeaseDuration(duration time.Duration) (time.Duration, error) {
	if duration <= 0 {
		return DefaultWorkLeaseDuration, nil
	}
	if duration > MaxWorkLeaseDuration {
		return 0, fmt.Errorf("brain: work lease exceeds maximum duration %s", MaxWorkLeaseDuration)
	}
	return duration, nil
}

func validateCompletionConditionInputs(inputs []CompletionConditionInput) ([]CompletionConditionInput, error) {
	if len(inputs) == 0 || len(inputs) > maxWorkConditions {
		return nil, fmt.Errorf("brain: work must have between 1 and %d completion conditions", maxWorkConditions)
	}
	normalized := make([]CompletionConditionInput, len(inputs))
	required := 0
	for i, input := range inputs {
		kind := strings.TrimSpace(input.Kind)
		if _, ok := workConditionKinds[kind]; !ok {
			return nil, fmt.Errorf("brain: completion condition %d has invalid kind %q", i+1, kind)
		}
		description := strings.TrimSpace(input.Description)
		if err := validateContent(description); err != nil {
			return nil, fmt.Errorf("brain: completion condition %d: %w", i+1, err)
		}
		if len(input.Verification) > maxContentLen {
			return nil, fmt.Errorf("brain: completion condition %d verification: %w", i+1, ErrContentTooLong)
		}
		var verification map[string]json.RawMessage
		if len(input.Verification) == 0 || json.Unmarshal(input.Verification, &verification) != nil || len(verification) == 0 {
			return nil, fmt.Errorf("brain: completion condition %d verification must be a non-empty JSON object", i+1)
		}
		canonicalVerification, err := json.Marshal(verification)
		if err != nil {
			return nil, fmt.Errorf("brain: completion condition %d verification: %w", i+1, err)
		}
		normalized[i] = CompletionConditionInput{
			Kind: kind, Description: description, Required: input.Required, Verification: canonicalVerification,
		}
		if input.Required {
			required++
		}
	}
	if required == 0 {
		return nil, ErrWorkConditionsMissing
	}
	return normalized, nil
}

func validateCheckpointInput(input WorkCheckpointInput, requireNextAction bool) (WorkCheckpointInput, error) {
	input.Summary = strings.TrimSpace(input.Summary)
	input.Result = strings.TrimSpace(input.Result)
	input.NextAction = strings.TrimSpace(input.NextAction)
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "summary", value: input.Summary},
		{name: "result", value: input.Result},
		{name: "next action", value: input.NextAction},
	} {
		if len(field.value) > maxContentLen {
			return WorkCheckpointInput{}, fmt.Errorf("brain: work checkpoint %s: %w", field.name, ErrContentTooLong)
		}
	}
	if err := validateContent(input.Summary); err != nil {
		return WorkCheckpointInput{}, fmt.Errorf("brain: work checkpoint summary: %w", err)
	}
	if err := validateContent(input.Result); err != nil {
		return WorkCheckpointInput{}, fmt.Errorf("brain: work checkpoint result: %w", err)
	}
	if requireNextAction {
		if err := validateContent(input.NextAction); err != nil {
			return WorkCheckpointInput{}, fmt.Errorf("brain: handoff next action: %w", err)
		}
	}
	return input, nil
}

func validateWorkFinishInput(input WorkFinishInput) (WorkFinishInput, error) {
	input.Summary = strings.TrimSpace(input.Summary)
	input.Result = strings.TrimSpace(input.Result)
	if err := validateContent(input.Summary); err != nil {
		return WorkFinishInput{}, fmt.Errorf("brain: work completion summary: %w", err)
	}
	if err := validateContent(input.Result); err != nil {
		return WorkFinishInput{}, fmt.Errorf("brain: work completion result: %w", err)
	}
	return input, nil
}

func validateWorkNextAction(nextAction string) (string, error) {
	nextAction = strings.TrimSpace(nextAction)
	if err := validateContent(nextAction); err != nil {
		return "", fmt.Errorf("brain: work next action: %w", err)
	}
	return nextAction, nil
}

func validateWorkEvidenceInput(input WorkEvidenceInput) (WorkEvidenceInput, error) {
	input.EvidenceType = strings.TrimSpace(input.EvidenceType)
	input.Summary = strings.TrimSpace(input.Summary)
	input.Reference = strings.TrimSpace(input.Reference)
	if err := validateContent(input.EvidenceType); err != nil {
		return WorkEvidenceInput{}, fmt.Errorf("brain: evidence type: %w", err)
	}
	if err := validateContent(input.Summary); err != nil {
		return WorkEvidenceInput{}, fmt.Errorf("brain: evidence summary: %w", err)
	}
	if len(input.Reference) > maxContentLen {
		return WorkEvidenceInput{}, fmt.Errorf("brain: evidence reference: %w", ErrContentTooLong)
	}
	if input.Payload == nil {
		input.Payload = json.RawMessage(`{}`)
	}
	if len(input.Payload) > maxContentLen {
		return WorkEvidenceInput{}, fmt.Errorf("brain: evidence payload: %w", ErrContentTooLong)
	}
	if !json.Valid(input.Payload) {
		return WorkEvidenceInput{}, fmt.Errorf("brain: evidence payload must be valid JSON")
	}
	var payload any
	decoder := json.NewDecoder(bytes.NewReader(input.Payload))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return WorkEvidenceInput{}, fmt.Errorf("brain: evidence payload must be valid JSON")
	}
	canonicalPayload, err := json.Marshal(payload)
	if err != nil {
		return WorkEvidenceInput{}, fmt.Errorf("brain: canonicalize evidence payload: %w", err)
	}
	input.Payload = canonicalPayload
	return input, nil
}

func validateWorkPrincipalID(principalID string) (string, error) {
	if len(principalID) > maxContentLen {
		return "", fmt.Errorf("brain: work principal: %w", ErrContentTooLong)
	}
	return principalID, nil
}

func workEvidenceContentDigest(input WorkEvidenceInput) (string, error) {
	content, err := json.Marshal(struct {
		EvidenceType string          `json:"evidence_type"`
		Summary      string          `json:"summary"`
		Reference    string          `json:"reference"`
		Payload      json.RawMessage `json:"payload"`
	}{input.EvidenceType, input.Summary, input.Reference, input.Payload})
	if err != nil {
		return "", fmt.Errorf("marshal evidence digest content: %w", err)
	}
	digest := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func normalizeEvidenceIDs(ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("brain: condition verification requires evidence")
	}
	result := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("brain: evidence IDs must be positive")
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result, nil
}

func validateWorkCompletion(requiredCount, incompleteRequired int64, unfinishedBlocker bool) error {
	if requiredCount == 0 {
		return ErrWorkConditionsMissing
	}
	if incompleteRequired != 0 {
		return ErrWorkConditionsIncomplete
	}
	if unfinishedBlocker {
		return ErrWorkBlockersUnfinished
	}
	return nil
}

func newWorkLeaseToken() (string, []byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("generate work lease token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(token))
	return token, hash[:], nil
}

func workLeaseTokenHash(token string) []byte {
	hash := sha256.Sum256([]byte(token))
	return hash[:]
}

type workActionReceipt struct {
	AttemptID *int64
	ResultID  *int64
	Response  json.RawMessage
}

func validateWorkActionKey(actionKey string) (string, error) {
	actionKey = strings.TrimSpace(actionKey)
	if actionKey == "" {
		return "", fmt.Errorf("brain: action key is required")
	}
	if len(actionKey) > 200 {
		return "", fmt.Errorf("brain: action key is too long")
	}
	return actionKey, nil
}

func workActionRequestHash(actionType string, request any) ([]byte, error) {
	data, err := json.Marshal(struct {
		ActionType string `json:"action_type"`
		Request    any    `json:"request"`
	}{ActionType: actionType, Request: request})
	if err != nil {
		return nil, fmt.Errorf("marshal work action request: %w", err)
	}
	hash := sha256.Sum256(data)
	return hash[:], nil
}

func workActionKeyDigest(actionKey string) string {
	hash := sha256.Sum256([]byte("stash-work-action:" + actionKey))
	return "sha256:" + hex.EncodeToString(hash[:])
}

func loadWorkActionReceipt(ctx context.Context, tx pgx.Tx, workItemID int64, attemptID *int64, actionType, actionKey string, requestHash []byte) (*workActionReceipt, error) {
	var receipt workActionReceipt
	var storedAttemptID *int64
	var storedActionType string
	var storedRequestHash []byte
	err := tx.QueryRow(ctx,
		`SELECT attempt_id, action_type, request_hash, result_id, response
		 FROM work_action_receipts
		 WHERE work_item_id = $1 AND action_key = $2
		 FOR UPDATE`,
		workItemID, workActionKeyDigest(actionKey),
	).Scan(&storedAttemptID, &storedActionType, &storedRequestHash, &receipt.ResultID, &receipt.Response)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read work action receipt: %w", err)
	}
	if storedActionType != actionType || !bytes.Equal(storedRequestHash, requestHash) ||
		(storedAttemptID == nil) != (attemptID == nil) ||
		(storedAttemptID != nil && *storedAttemptID != *attemptID) {
		return nil, ErrWorkActionConflict
	}
	receipt.AttemptID = storedAttemptID
	return &receipt, nil
}

func storeWorkActionReceipt(ctx context.Context, tx pgx.Tx, workItemID int64, attemptID *int64, actionType, actionKey string, requestHash []byte, resultID *int64, response any) error {
	data, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("marshal work action response: %w", err)
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO work_action_receipts
		    (work_item_id, attempt_id, action_key, action_type, request_hash, result_id, response)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		workItemID, attemptID, workActionKeyDigest(actionKey), actionType, requestHash, resultID, data,
	)
	if err != nil {
		return fmt.Errorf("store work action receipt: %w", err)
	}
	return nil
}

func decodeWorkActionResponse(receipt *workActionReceipt, target any) error {
	if receipt == nil {
		return pgx.ErrNoRows
	}
	if err := json.Unmarshal(receipt.Response, target); err != nil {
		return fmt.Errorf("decode work action receipt: %w", err)
	}
	return nil
}

func scanWorkAttempt(row pgx.Row) (*models.WorkAttempt, error) {
	var attempt models.WorkAttempt
	err := row.Scan(
		&attempt.ID, &attempt.WorkItemID, &attempt.WorktreeID, &attempt.AttemptNumber, &attempt.AgentID, &attempt.PrincipalID, &attempt.Status,
		&attempt.LeaseExpiresAt, &attempt.StartedAt, &attempt.EndedAt, &attempt.CreatedAt, &attempt.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &attempt, nil
}

func scanWorkCheckpoint(row pgx.Row) (*models.WorkCheckpoint, error) {
	var checkpoint models.WorkCheckpoint
	err := row.Scan(
		&checkpoint.ID, &checkpoint.AttemptID, &checkpoint.Summary, &checkpoint.Result,
		&checkpoint.NextAction, &checkpoint.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &checkpoint, nil
}

func scanWorkCondition(row pgx.Row) (*models.WorkCompletionCondition, error) {
	var condition models.WorkCompletionCondition
	err := row.Scan(
		&condition.ID, &condition.WorkItemID, &condition.Kind, &condition.Description, &condition.Verification, &condition.Required,
		&condition.Position, &condition.Status, &condition.WaiverReason, &condition.VerifiedByAttemptID,
		&condition.VerifiedAt, &condition.SupersededAt, &condition.CreatedAt, &condition.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &condition, nil
}

func scanWorkEvidence(row pgx.Row) (*models.WorkEvidence, error) {
	var evidence models.WorkEvidence
	err := row.Scan(
		&evidence.ID, &evidence.WorkItemID, &evidence.AttemptID, &evidence.EvidenceType,
		&evidence.Summary, &evidence.Reference, &evidence.Payload, &evidence.ContentDigest,
		&evidence.PrincipalID, &evidence.WorktreeHeadSHA, &evidence.SubmittedAt, &evidence.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &evidence, nil
}

func lockWorkItem(ctx context.Context, tx pgx.Tx, workItemID int64) (int64, string, error) {
	var namespaceID int64
	var status string
	err := tx.QueryRow(ctx,
		`SELECT namespace_id, status FROM work_items WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`,
		workItemID,
	).Scan(&namespaceID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", ErrWorkItemNotFound
	}
	if err != nil {
		return 0, "", fmt.Errorf("lock work item: %w", err)
	}
	return namespaceID, status, nil
}

func insertWorkExecutionEvent(ctx context.Context, tx pgx.Tx, namespaceID, workItemID int64, attemptID *int64, eventType, eventKey string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal work execution event: %w", err)
	}
	if attemptID == nil || strings.TrimSpace(eventKey) == "" {
		_, err = tx.Exec(ctx,
			`INSERT INTO work_events (namespace_id, work_item_id, attempt_id, event_type, event_key, payload)
			 VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6)`,
			namespaceID, workItemID, attemptID, eventType, strings.TrimSpace(eventKey), data,
		)
	} else {
		_, err = tx.Exec(ctx,
			`INSERT INTO work_events (namespace_id, work_item_id, attempt_id, event_type, event_key, payload)
			 VALUES ($1, $2, $3, $4, $5, $6)
			 ON CONFLICT (attempt_id, event_key)
			 WHERE attempt_id IS NOT NULL AND event_key IS NOT NULL DO NOTHING`,
			namespaceID, workItemID, attemptID, eventType, strings.TrimSpace(eventKey), data,
		)
	}
	if err != nil {
		return fmt.Errorf("record work execution event: %w", err)
	}
	return nil
}

func expireStaleWorkAttempts(ctx context.Context, tx pgx.Tx, namespaceID, workItemID int64) error {
	rows, err := tx.Query(ctx,
		`UPDATE work_attempts
		 SET status = 'expired', ended_at = clock_timestamp(), updated_at = now()
		 WHERE work_item_id = $1 AND status = 'active' AND lease_expires_at <= clock_timestamp()
		 RETURNING id`,
		workItemID,
	)
	if err != nil {
		return fmt.Errorf("expire stale work attempt: %w", err)
	}
	var expired []int64
	for rows.Next() {
		var attemptID int64
		if err := rows.Scan(&attemptID); err != nil {
			rows.Close()
			return fmt.Errorf("scan stale work attempt: %w", err)
		}
		expired = append(expired, attemptID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read stale work attempts: %w", err)
	}
	rows.Close()
	if len(expired) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx,
		`UPDATE work_attempt_lease_tokens
		 SET revoked_at = clock_timestamp()
		 WHERE attempt_id = ANY($1) AND revoked_at IS NULL`,
		expired,
	); err != nil {
		return fmt.Errorf("revoke stale work lease tokens: %w", err)
	}
	if err := setAvailableWorkItemStatus(ctx, tx, namespaceID, workItemID); err != nil {
		return err
	}
	for _, attemptID := range expired {
		id := attemptID
		if err := insertWorkExecutionEvent(ctx, tx, namespaceID, workItemID, &id, "work.attempt.expired", "attempt.expired", map[string]any{
			"attempt_id": attemptID,
		}); err != nil {
			return err
		}
	}
	return nil
}

func lockWorkGraph(ctx context.Context, tx pgx.Tx, namespaceID int64) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, namespaceID); err != nil {
		return fmt.Errorf("lock work graph: %w", err)
	}
	return nil
}

func lockWorkGraphForItem(ctx context.Context, tx pgx.Tx, workItemID int64) (int64, error) {
	var namespaceID int64
	err := tx.QueryRow(ctx,
		`SELECT namespace_id FROM work_items WHERE id = $1 AND deleted_at IS NULL`,
		workItemID,
	).Scan(&namespaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrWorkItemNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("read work item namespace before graph lock: %w", err)
	}
	if err := lockWorkGraph(ctx, tx, namespaceID); err != nil {
		return 0, err
	}
	return namespaceID, nil
}

func lockWorkGraphForAttempt(ctx context.Context, tx pgx.Tx, attemptID int64) (int64, error) {
	var namespaceID int64
	err := tx.QueryRow(ctx,
		`SELECT item.namespace_id
		 FROM work_attempts attempt
		 JOIN work_items item ON item.id = attempt.work_item_id AND item.deleted_at IS NULL
		 WHERE attempt.id = $1`,
		attemptID,
	).Scan(&namespaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrWorkAttemptLease
	}
	if err != nil {
		return 0, fmt.Errorf("read work attempt namespace before graph lock: %w", err)
	}
	if err := lockWorkGraph(ctx, tx, namespaceID); err != nil {
		return 0, err
	}
	return namespaceID, nil
}

func hasUnfinishedWorkBlockers(ctx context.Context, tx pgx.Tx, workItemID int64) (bool, error) {
	var blocked bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (
		    SELECT 1
		    FROM work_item_edges edge
		    JOIN work_items blocker ON blocker.id = edge.from_item_id
		    WHERE edge.to_item_id = $1 AND edge.edge_type = 'blocks' AND edge.deleted_at IS NULL
		      AND blocker.deleted_at IS NULL AND blocker.status NOT IN ('done', 'canceled')
		)`,
		workItemID,
	).Scan(&blocked); err != nil {
		return false, fmt.Errorf("check unfinished work blockers: %w", err)
	}
	return blocked, nil
}

func setAvailableWorkItemStatus(ctx context.Context, tx pgx.Tx, namespaceID, workItemID int64) error {
	if err := lockWorkGraph(ctx, tx, namespaceID); err != nil {
		return err
	}
	blocked, err := hasUnfinishedWorkBlockers(ctx, tx, workItemID)
	if err != nil {
		return err
	}
	status := "ready"
	if blocked {
		status = "blocked"
	}
	if _, err := tx.Exec(ctx,
		`UPDATE work_items SET status = $2, owner = '', updated_at = now()
		 WHERE id = $1 AND status NOT IN ('done', 'canceled')`,
		workItemID, status,
	); err != nil {
		return fmt.Errorf("release work item: %w", err)
	}
	return nil
}

type leasedWorkAttempt struct {
	Attempt     models.WorkAttempt
	NamespaceID int64
	LeaseValid  bool
}

func lockWorkAttemptForAction(ctx context.Context, tx pgx.Tx, attemptID int64, leaseToken string, principalIDs ...string) (*leasedWorkAttempt, error) {
	if attemptID <= 0 || strings.TrimSpace(leaseToken) == "" {
		return nil, ErrWorkAttemptLease
	}
	principalID := ""
	if len(principalIDs) > 0 {
		principalID = principalIDs[0]
	}
	var err error
	principalID, err = validateWorkPrincipalID(principalID)
	if err != nil {
		return nil, ErrWorkAttemptLease
	}
	var leased leasedWorkAttempt
	err = tx.QueryRow(ctx,
		`SELECT a.id, a.work_item_id, a.worktree_id, a.attempt_number, a.agent_id, a.principal_id, a.status,
		        a.lease_expires_at, a.started_at, a.ended_at, a.created_at, a.updated_at,
		        wi.namespace_id,
		        (token.revoked_at IS NULL AND a.status = 'active'
		         AND a.lease_expires_at > clock_timestamp() AND wi.status = 'doing')
		 FROM work_attempts a
		 JOIN work_items wi ON wi.id = a.work_item_id AND wi.deleted_at IS NULL
		 JOIN work_attempt_lease_tokens token
		   ON token.attempt_id = a.id AND token.principal_id = a.principal_id
		 WHERE a.id = $1 AND token.token_hash = $2 AND a.principal_id = $3
		 FOR UPDATE OF a, wi, token`,
		attemptID, workLeaseTokenHash(leaseToken), principalID,
	).Scan(
		&leased.Attempt.ID, &leased.Attempt.WorkItemID, &leased.Attempt.WorktreeID, &leased.Attempt.AttemptNumber,
		&leased.Attempt.AgentID, &leased.Attempt.PrincipalID, &leased.Attempt.Status, &leased.Attempt.LeaseExpiresAt,
		&leased.Attempt.StartedAt, &leased.Attempt.EndedAt, &leased.Attempt.CreatedAt,
		&leased.Attempt.UpdatedAt, &leased.NamespaceID, &leased.LeaseValid,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrWorkAttemptLease
	}
	if err != nil {
		return nil, fmt.Errorf("validate work attempt lease: %w", err)
	}
	return &leased, nil
}

// GetWorkAttempt returns attempt metadata without exposing the lease token hash.
func (b *Brain) GetWorkAttempt(ctx context.Context, attemptID int64) (*models.WorkAttempt, error) {
	attempt, err := scanWorkAttempt(b.pool.QueryRow(ctx,
		`SELECT `+workAttemptColumns+` FROM work_attempts WHERE id = $1`,
		attemptID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrWorkAttemptNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get work attempt: %w", err)
	}
	return attempt, nil
}

func insertWorkCheckpoint(ctx context.Context, tx pgx.Tx, attemptID int64, input WorkCheckpointInput) (*models.WorkCheckpoint, error) {
	checkpoint, err := scanWorkCheckpoint(tx.QueryRow(ctx,
		`INSERT INTO work_checkpoints (attempt_id, summary, result, next_action)
		 VALUES ($1, $2, $3, $4)
		 RETURNING `+workCheckpointColumns,
		attemptID, input.Summary, input.Result, input.NextAction,
	))
	if err != nil {
		return nil, fmt.Errorf("store work checkpoint: %w", err)
	}
	return checkpoint, nil
}

// PrepareWork atomically replaces the active completion conditions and stores
// exactly one concrete next action. A live attempt must hand off before another
// agent can change the definition of done.
func (b *Brain) PrepareWork(ctx context.Context, workItemID int64, nextAction string, inputs []CompletionConditionInput, actionKey string) (*models.WorkPreparation, error) {
	nextAction, err := validateWorkNextAction(nextAction)
	if err != nil {
		return nil, err
	}
	inputs, err = validateCompletionConditionInputs(inputs)
	if err != nil {
		return nil, err
	}
	actionKey, err = validateWorkActionKey(actionKey)
	if err != nil {
		return nil, err
	}
	requestHash, err := workActionRequestHash("prepare", struct {
		WorkItemID int64                      `json:"work_item_id"`
		NextAction string                     `json:"next_action"`
		Conditions []CompletionConditionInput `json:"conditions"`
	}{workItemID, nextAction, inputs})
	if err != nil {
		return nil, err
	}
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin prepare work: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	graphNamespaceID, err := lockWorkGraphForItem(ctx, tx, workItemID)
	if err != nil {
		return nil, err
	}
	namespaceID, status, err := lockWorkItem(ctx, tx, workItemID)
	if err != nil {
		return nil, err
	}
	if namespaceID != graphNamespaceID {
		return nil, fmt.Errorf("brain: work item namespace changed while preparing work")
	}
	receipt, err := loadWorkActionReceipt(ctx, tx, workItemID, nil, "prepare", actionKey, requestHash)
	if err != nil {
		return nil, err
	}
	if receipt != nil {
		var prepared models.WorkPreparation
		if err := decodeWorkActionResponse(receipt, &prepared); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit prepare work replay: %w", err)
		}
		return &prepared, nil
	}
	if status == "done" || status == "canceled" {
		return nil, ErrWorkAttemptTerminal
	}
	if err := expireStaleWorkAttempts(ctx, tx, namespaceID, workItemID); err != nil {
		return nil, err
	}
	var active bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM work_attempts WHERE work_item_id = $1 AND status = 'active')`,
		workItemID,
	).Scan(&active); err != nil {
		return nil, fmt.Errorf("check active work attempt: %w", err)
	}
	if active {
		return nil, ErrActiveWorkAttempt
	}
	if _, err := tx.Exec(ctx,
		`UPDATE work_completion_conditions
		 SET superseded_at = now(), updated_at = now()
		 WHERE work_item_id = $1 AND superseded_at IS NULL`,
		workItemID,
	); err != nil {
		return nil, fmt.Errorf("supersede work completion conditions: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO work_execution_states (work_item_id, current_next_action)
		 VALUES ($1, $2)
		 ON CONFLICT (work_item_id) DO UPDATE SET
		   current_next_action = EXCLUDED.current_next_action, updated_at = now()`,
		workItemID, nextAction,
	); err != nil {
		return nil, fmt.Errorf("store prepared next action: %w", err)
	}

	conditions := make([]models.WorkCompletionCondition, 0, len(inputs))
	for position, input := range inputs {
		condition, err := scanWorkCondition(tx.QueryRow(ctx,
			`INSERT INTO work_completion_conditions (work_item_id, kind, description, verification, required, position)
			 VALUES ($1, $2, $3, $4, $5, $6)
			 RETURNING `+workConditionColumns,
			workItemID, input.Kind, input.Description, input.Verification, input.Required, position,
		))
		if err != nil {
			return nil, fmt.Errorf("create work completion condition: %w", err)
		}
		condition.EvidenceIDs = []int64{}
		conditions = append(conditions, *condition)
	}
	prepared := &models.WorkPreparation{
		WorkItemID: workItemID, NextAction: nextAction, CompletionConditions: conditions,
	}
	if err := insertWorkExecutionEvent(ctx, tx, namespaceID, workItemID, nil, "work.prepared", workActionKeyDigest(actionKey), map[string]any{
		"condition_count": len(conditions),
		"next_action":     nextAction,
	}); err != nil {
		return nil, err
	}
	if err := storeWorkActionReceipt(ctx, tx, workItemID, nil, "prepare", actionKey, requestHash, nil, prepared); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit prepare work: %w", err)
	}
	return prepared, nil
}

// StartWorkAttempt starts a lease without an authenticated principal. This is
// the explicit auth-mode=none path used by trusted local callers.
func (b *Brain) StartWorkAttempt(ctx context.Context, workItemID int64, agentID string, worktreeID *int64, leaseDuration time.Duration, actionKey string) (*models.WorkAttemptLease, error) {
	return b.StartWorkAttemptForPrincipal(ctx, workItemID, agentID, "", worktreeID, leaseDuration, actionKey)
}

// StartWorkAttemptForPrincipal acquires the single active lease for a work
// item and binds it to the server-verified authentication principal. Stale
// leases are expired under the same work-item lock before a new attempt starts.
func (b *Brain) StartWorkAttemptForPrincipal(ctx context.Context, workItemID int64, agentID, principalID string, worktreeID *int64, leaseDuration time.Duration, actionKey string) (*models.WorkAttemptLease, error) {
	agentID = strings.TrimSpace(agentID)
	if err := validateContent(agentID); err != nil {
		return nil, fmt.Errorf("brain: work attempt agent: %w", err)
	}
	principalID, err := validateWorkPrincipalID(principalID)
	if err != nil {
		return nil, err
	}
	leaseDuration, err = normalizeWorkLeaseDuration(leaseDuration)
	if err != nil {
		return nil, err
	}
	actionKey, err = validateWorkActionKey(actionKey)
	if err != nil {
		return nil, err
	}
	if worktreeID != nil && *worktreeID <= 0 {
		return nil, ErrWorktreeNotFound
	}
	requestHash, err := workActionRequestHash("start", struct {
		WorkItemID    int64         `json:"work_item_id"`
		AgentID       string        `json:"agent_id"`
		PrincipalID   string        `json:"principal_id"`
		WorktreeID    *int64        `json:"worktree_id"`
		LeaseDuration time.Duration `json:"lease_duration"`
	}{workItemID, agentID, principalID, worktreeID, leaseDuration})
	if err != nil {
		return nil, err
	}
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin work attempt: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	graphNamespaceID, err := lockWorkGraphForItem(ctx, tx, workItemID)
	if err != nil {
		return nil, err
	}
	namespaceID, status, err := lockWorkItem(ctx, tx, workItemID)
	if err != nil {
		return nil, err
	}
	if namespaceID != graphNamespaceID {
		return nil, fmt.Errorf("brain: work item namespace changed while starting work")
	}
	receipt, err := loadWorkActionReceipt(ctx, tx, workItemID, nil, "start", actionKey, requestHash)
	if err != nil {
		return nil, err
	}
	if receipt != nil {
		if receipt.ResultID == nil {
			return nil, fmt.Errorf("brain: start action receipt has no attempt")
		}
		var active bool
		attempt, err := scanWorkAttempt(tx.QueryRow(ctx,
			`SELECT `+workAttemptColumns+` FROM work_attempts
			 WHERE id = $1 AND status = 'active' AND lease_expires_at > clock_timestamp()
			   AND principal_id = $2 AND $3 = 'doing'
			 FOR UPDATE`,
			*receipt.ResultID, principalID, status,
		))
		if err == nil {
			active = true
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("read replayed work attempt: %w", err)
		}
		if !active {
			if err := expireStaleWorkAttempts(ctx, tx, namespaceID, workItemID); err != nil {
				return nil, err
			}
			if err := tx.Commit(ctx); err != nil {
				return nil, fmt.Errorf("commit stale start replay: %w", err)
			}
			return nil, ErrWorkAttemptLease
		}
		token, tokenHash, err := newWorkLeaseToken()
		if err != nil {
			return nil, err
		}
		attempt, err = scanWorkAttempt(tx.QueryRow(ctx,
			`UPDATE work_attempts
			 SET lease_expires_at = greatest(lease_expires_at, clock_timestamp() + ($2 * interval '1 second')),
			     updated_at = now()
			 WHERE id = $1
			 RETURNING `+workAttemptColumns,
			attempt.ID, leaseDuration.Seconds(),
		))
		if err != nil {
			return nil, fmt.Errorf("extend replayed work lease: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO work_attempt_lease_tokens (attempt_id, principal_id, token_hash)
			 VALUES ($1, $2, $3)`,
			attempt.ID, principalID, tokenHash,
		); err != nil {
			return nil, fmt.Errorf("store replayed work lease token: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit work attempt replay: %w", err)
		}
		return &models.WorkAttemptLease{Attempt: *attempt, LeaseToken: token}, nil
	}
	if status == "done" || status == "canceled" {
		return nil, ErrWorkAttemptTerminal
	}
	if err := expireStaleWorkAttempts(ctx, tx, namespaceID, workItemID); err != nil {
		return nil, err
	}
	var active, prepared bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM work_attempts WHERE work_item_id = $1 AND status = 'active'),
		        EXISTS (SELECT 1 FROM work_completion_conditions
		                WHERE work_item_id = $1 AND superseded_at IS NULL AND required)
		        AND EXISTS (SELECT 1 FROM work_execution_states
		                    WHERE work_item_id = $1 AND current_next_action <> '')`,
		workItemID,
	).Scan(&active, &prepared); err != nil {
		return nil, fmt.Errorf("check work attempt readiness: %w", err)
	}
	if active {
		return nil, ErrActiveWorkAttempt
	}
	if !prepared {
		return nil, ErrWorkConditionsMissing
	}
	if err := lockWorkGraph(ctx, tx, namespaceID); err != nil {
		return nil, err
	}
	blocked, err := hasUnfinishedWorkBlockers(ctx, tx, workItemID)
	if err != nil {
		return nil, err
	}
	if blocked {
		if _, err := tx.Exec(ctx,
			`UPDATE work_items SET status = 'blocked', owner = '', updated_at = now() WHERE id = $1`,
			workItemID,
		); err != nil {
			return nil, fmt.Errorf("mark blocked work item: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit blocked work start: %w", err)
		}
		return nil, ErrWorkBlockersUnfinished
	}
	if worktreeID != nil {
		var worktreeNamespaceID int64
		if err := tx.QueryRow(ctx,
			`SELECT namespace_id FROM worktrees WHERE id = $1 AND deleted_at IS NULL`,
			*worktreeID,
		).Scan(&worktreeNamespaceID); errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrWorktreeNotFound
		} else if err != nil {
			return nil, fmt.Errorf("check attempt worktree: %w", err)
		} else if worktreeNamespaceID != namespaceID {
			return nil, fmt.Errorf("brain: work attempt worktree must share the work item namespace")
		}
	}
	var attemptNumber int
	if err := tx.QueryRow(ctx,
		`SELECT coalesce(max(attempt_number), 0) + 1 FROM work_attempts WHERE work_item_id = $1`,
		workItemID,
	).Scan(&attemptNumber); err != nil {
		return nil, fmt.Errorf("allocate work attempt number: %w", err)
	}
	token, tokenHash, err := newWorkLeaseToken()
	if err != nil {
		return nil, err
	}
	attempt, err := scanWorkAttempt(tx.QueryRow(ctx,
		`INSERT INTO work_attempts
		    (work_item_id, worktree_id, attempt_number, agent_id, principal_id, lease_expires_at, started_at)
		 VALUES ($1, $2, $3, $4, $5, clock_timestamp() + ($6 * interval '1 second'), clock_timestamp())
		 RETURNING `+workAttemptColumns,
		workItemID, worktreeID, attemptNumber, agentID, principalID, leaseDuration.Seconds(),
	))
	if err != nil {
		return nil, fmt.Errorf("start work attempt: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO work_attempt_lease_tokens (attempt_id, principal_id, token_hash)
		 VALUES ($1, $2, $3)`,
		attempt.ID, principalID, tokenHash,
	); err != nil {
		return nil, fmt.Errorf("store work lease token: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE work_items
		 SET status = 'doing', owner = $2, started_at = coalesce(started_at, now()),
		     completed_at = NULL, updated_at = now()
		 WHERE id = $1`,
		workItemID, agentID,
	); err != nil {
		return nil, fmt.Errorf("mark work item started: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE work_plan_items
		 SET started_by = CASE WHEN started_by = '' THEN $2 ELSE started_by END,
		     updated_at = now()
		 WHERE work_item_id = $1 AND kind = 'task'`,
		workItemID, agentID,
	); err != nil {
		return nil, fmt.Errorf("record plan task starter: %w", err)
	}
	if worktreeID != nil {
		if _, err := tx.Exec(ctx,
			`INSERT INTO work_item_worktrees (work_item_id, worktree_id, relation)
			 VALUES ($1, $2, 'active')
			 ON CONFLICT (work_item_id, worktree_id) DO UPDATE SET relation = 'active'`,
			workItemID, *worktreeID,
		); err != nil {
			return nil, fmt.Errorf("attach attempt worktree: %w", err)
		}
	}
	attemptID := attempt.ID
	// The start action key is a recovery credential: an exact replay issues
	// another valid token. Never expose it through resume_work events.
	if err := insertWorkExecutionEvent(ctx, tx, namespaceID, workItemID, &attemptID, "work.attempt.started", "attempt.started", map[string]any{
		"attempt_number":   attempt.AttemptNumber,
		"agent_id":         attempt.AgentID,
		"principal_id":     attempt.PrincipalID,
		"worktree_id":      attempt.WorktreeID,
		"lease_expires_at": attempt.LeaseExpiresAt,
	}); err != nil {
		return nil, err
	}
	if err := storeWorkActionReceipt(ctx, tx, workItemID, nil, "start", actionKey, requestHash, &attemptID, attempt); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit work attempt: %w", err)
	}
	return &models.WorkAttemptLease{Attempt: *attempt, LeaseToken: token}, nil
}

// CheckpointWorkAttempt extends the lease and stores summary, result, and next
// action in one transaction.
func (b *Brain) CheckpointWorkAttempt(ctx context.Context, attemptID int64, leaseToken string, input WorkCheckpointInput, leaseDuration time.Duration, actionKey string) (*models.WorkCheckpointReceipt, error) {
	return b.CheckpointWorkAttemptForPrincipal(ctx, attemptID, leaseToken, "", input, leaseDuration, actionKey)
}

// CheckpointWorkAttemptForPrincipal applies a checkpoint only for the principal
// bound to the supplied lease token.
func (b *Brain) CheckpointWorkAttemptForPrincipal(ctx context.Context, attemptID int64, leaseToken, principalID string, input WorkCheckpointInput, leaseDuration time.Duration, actionKey string) (*models.WorkCheckpointReceipt, error) {
	input, err := validateCheckpointInput(input, true)
	if err != nil {
		return nil, err
	}
	leaseDuration, err = normalizeWorkLeaseDuration(leaseDuration)
	if err != nil {
		return nil, err
	}
	actionKey, err = validateWorkActionKey(actionKey)
	if err != nil {
		return nil, err
	}
	requestHash, err := workActionRequestHash("checkpoint", struct {
		AttemptID     int64               `json:"attempt_id"`
		Input         WorkCheckpointInput `json:"input"`
		LeaseDuration time.Duration       `json:"lease_duration"`
	}{attemptID, input, leaseDuration})
	if err != nil {
		return nil, err
	}
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin work checkpoint: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	leased, err := lockWorkAttemptForAction(ctx, tx, attemptID, leaseToken, principalID)
	if err != nil {
		return nil, err
	}
	receipt, err := loadWorkActionReceipt(ctx, tx, leased.Attempt.WorkItemID, &attemptID, "checkpoint", actionKey, requestHash)
	if err != nil {
		return nil, err
	}
	if receipt != nil {
		var replay models.WorkCheckpointReceipt
		if err := decodeWorkActionResponse(receipt, &replay); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit work checkpoint replay: %w", err)
		}
		return &replay, nil
	}
	if !leased.LeaseValid {
		return nil, ErrWorkAttemptLease
	}
	checkpoint, err := insertWorkCheckpoint(ctx, tx, attemptID, input)
	if err != nil {
		return nil, err
	}
	var leaseExpiresAt time.Time
	if err := tx.QueryRow(ctx,
		`UPDATE work_attempts
		 SET lease_expires_at = greatest(lease_expires_at, clock_timestamp() + ($2 * interval '1 second')),
		     updated_at = now()
		 WHERE id = $1
		 RETURNING lease_expires_at`,
		attemptID, leaseDuration.Seconds(),
	).Scan(&leaseExpiresAt); err != nil {
		return nil, fmt.Errorf("extend work attempt lease: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO work_execution_states (work_item_id, current_next_action)
		 VALUES ($1, $2)
		 ON CONFLICT (work_item_id) DO UPDATE SET
		   current_next_action = EXCLUDED.current_next_action, updated_at = now()`,
		leased.Attempt.WorkItemID, checkpoint.NextAction,
	); err != nil {
		return nil, fmt.Errorf("store checkpoint next action: %w", err)
	}
	result := &models.WorkCheckpointReceipt{Checkpoint: *checkpoint, LeaseExpiresAt: leaseExpiresAt}
	if err := insertWorkExecutionEvent(ctx, tx, leased.NamespaceID, leased.Attempt.WorkItemID, &attemptID, "work.attempt.checkpointed", workActionKeyDigest(actionKey), map[string]any{
		"checkpoint_id":    checkpoint.ID,
		"lease_expires_at": leaseExpiresAt,
	}); err != nil {
		return nil, err
	}
	checkpointID := checkpoint.ID
	if err := storeWorkActionReceipt(ctx, tx, leased.Attempt.WorkItemID, &attemptID, "checkpoint", actionKey, requestHash, &checkpointID, result); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit work checkpoint: %w", err)
	}
	return result, nil
}

// RenewWorkAttemptLease extends an active lease without creating a checkpoint.
func (b *Brain) RenewWorkAttemptLease(ctx context.Context, attemptID int64, leaseToken string, leaseDuration time.Duration, actionKey string) (*models.WorkAttempt, error) {
	return b.RenewWorkAttemptLeaseForPrincipal(ctx, attemptID, leaseToken, "", leaseDuration, actionKey)
}

// RenewWorkAttemptLeaseForPrincipal extends only the authenticated principal's
// active lease.
func (b *Brain) RenewWorkAttemptLeaseForPrincipal(ctx context.Context, attemptID int64, leaseToken, principalID string, leaseDuration time.Duration, actionKey string) (*models.WorkAttempt, error) {
	leaseDuration, err := normalizeWorkLeaseDuration(leaseDuration)
	if err != nil {
		return nil, err
	}
	actionKey, err = validateWorkActionKey(actionKey)
	if err != nil {
		return nil, err
	}
	requestHash, err := workActionRequestHash("renew", struct {
		AttemptID     int64         `json:"attempt_id"`
		LeaseDuration time.Duration `json:"lease_duration"`
	}{attemptID, leaseDuration})
	if err != nil {
		return nil, err
	}
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin work lease renewal: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	leased, err := lockWorkAttemptForAction(ctx, tx, attemptID, leaseToken, principalID)
	if err != nil {
		return nil, err
	}
	receipt, err := loadWorkActionReceipt(ctx, tx, leased.Attempt.WorkItemID, &attemptID, "renew", actionKey, requestHash)
	if err != nil {
		return nil, err
	}
	if receipt != nil {
		var replay models.WorkAttempt
		if err := decodeWorkActionResponse(receipt, &replay); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit work lease renewal replay: %w", err)
		}
		return &replay, nil
	}
	if !leased.LeaseValid {
		return nil, ErrWorkAttemptLease
	}
	attempt, err := scanWorkAttempt(tx.QueryRow(ctx,
		`UPDATE work_attempts
		 SET lease_expires_at = greatest(lease_expires_at, clock_timestamp() + ($2 * interval '1 second')),
		     updated_at = now()
		 WHERE id = $1
		 RETURNING `+workAttemptColumns,
		attemptID, leaseDuration.Seconds(),
	))
	if err != nil {
		return nil, fmt.Errorf("renew work attempt lease: %w", err)
	}
	if err := insertWorkExecutionEvent(ctx, tx, leased.NamespaceID, leased.Attempt.WorkItemID, &attemptID, "work.attempt.renewed", workActionKeyDigest(actionKey), map[string]any{
		"lease_expires_at": attempt.LeaseExpiresAt,
	}); err != nil {
		return nil, err
	}
	if err := storeWorkActionReceipt(ctx, tx, leased.Attempt.WorkItemID, &attemptID, "renew", actionKey, requestHash, &attemptID, attempt); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit work lease renewal: %w", err)
	}
	return attempt, nil
}

// SubmitWorkEvidence stores immutable evidence and links it to active
// completion conditions in the same transaction.
func (b *Brain) SubmitWorkEvidence(ctx context.Context, attemptID int64, leaseToken string, input WorkEvidenceInput, conditionIDs []int64, actionKey string) (*models.WorkEvidence, error) {
	return b.SubmitWorkEvidenceForPrincipal(ctx, attemptID, leaseToken, "", input, conditionIDs, actionKey)
}

// SubmitWorkEvidenceForPrincipal records server-derived provenance for the
// authenticated principal bound to the lease.
func (b *Brain) SubmitWorkEvidenceForPrincipal(ctx context.Context, attemptID int64, leaseToken, principalID string, input WorkEvidenceInput, conditionIDs []int64, actionKey string) (*models.WorkEvidence, error) {
	input, err := validateWorkEvidenceInput(input)
	if err != nil {
		return nil, err
	}
	contentDigest, err := workEvidenceContentDigest(input)
	if err != nil {
		return nil, err
	}
	conditionIDs, err = normalizeEvidenceIDs(conditionIDs)
	if err != nil {
		return nil, fmt.Errorf("brain: evidence condition IDs: %w", err)
	}
	actionKey, err = validateWorkActionKey(actionKey)
	if err != nil {
		return nil, err
	}
	requestHash, err := workActionRequestHash("evidence", struct {
		AttemptID    int64             `json:"attempt_id"`
		Input        WorkEvidenceInput `json:"input"`
		ConditionIDs []int64           `json:"condition_ids"`
	}{attemptID, input, conditionIDs})
	if err != nil {
		return nil, err
	}
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin work evidence: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	leased, err := lockWorkAttemptForAction(ctx, tx, attemptID, leaseToken, principalID)
	if err != nil {
		return nil, err
	}
	receipt, err := loadWorkActionReceipt(ctx, tx, leased.Attempt.WorkItemID, &attemptID, "evidence", actionKey, requestHash)
	if err != nil {
		return nil, err
	}
	if receipt != nil {
		var replay models.WorkEvidence
		if err := decodeWorkActionResponse(receipt, &replay); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit work evidence replay: %w", err)
		}
		return &replay, nil
	}
	if !leased.LeaseValid {
		return nil, ErrWorkAttemptLease
	}
	var conditionCount int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM work_completion_conditions
		 WHERE work_item_id = $1 AND superseded_at IS NULL AND id = ANY($2)`,
		leased.Attempt.WorkItemID, conditionIDs,
	).Scan(&conditionCount); err != nil {
		return nil, fmt.Errorf("check evidence completion conditions: %w", err)
	}
	if conditionCount != len(conditionIDs) {
		return nil, ErrWorkConditionNotFound
	}
	evidence, err := scanWorkEvidence(tx.QueryRow(ctx,
		`INSERT INTO work_evidence
		    (work_item_id, attempt_id, evidence_type, summary, reference, payload,
		     content_digest, principal_id, worktree_head_sha)
		 SELECT $1, attempt.id, $3, $4, $5, $6, $7, attempt.principal_id,
		        coalesce(tree.head_sha, '')
		 FROM work_attempts attempt
		 LEFT JOIN worktrees tree ON tree.id = attempt.worktree_id AND tree.deleted_at IS NULL
		 WHERE attempt.id = $2
		 RETURNING `+workEvidenceColumns,
		leased.Attempt.WorkItemID, attemptID, input.EvidenceType, input.Summary, input.Reference, input.Payload, contentDigest,
	))
	if err != nil {
		return nil, fmt.Errorf("submit work evidence: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO work_condition_evidence
		    (condition_id, evidence_id, work_item_id, linked_by_attempt_id)
		 SELECT condition_id, $2, $3, $4 FROM unnest($1::bigint[]) AS condition_id`,
		conditionIDs, evidence.ID, leased.Attempt.WorkItemID, attemptID,
	); err != nil {
		return nil, fmt.Errorf("link submitted evidence to conditions: %w", err)
	}
	evidence.ConditionIDs = conditionIDs
	if err := insertWorkExecutionEvent(ctx, tx, leased.NamespaceID, leased.Attempt.WorkItemID, &attemptID, "work.evidence.submitted", workActionKeyDigest(actionKey), map[string]any{
		"evidence_id":       evidence.ID,
		"evidence_type":     evidence.EvidenceType,
		"condition_ids":     conditionIDs,
		"content_digest":    evidence.ContentDigest,
		"principal_id":      evidence.PrincipalID,
		"worktree_head_sha": evidence.WorktreeHeadSHA,
	}); err != nil {
		return nil, err
	}
	evidenceID := evidence.ID
	if err := storeWorkActionReceipt(ctx, tx, leased.Attempt.WorkItemID, &attemptID, "evidence", actionKey, requestHash, &evidenceID, evidence); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit work evidence: %w", err)
	}
	return evidence, nil
}

// VerifyWorkCondition marks a condition passed or waived only when every
// supplied evidence row is already linked to that condition.
func (b *Brain) VerifyWorkCondition(ctx context.Context, attemptID int64, leaseToken string, conditionID int64, status string, evidenceIDs []int64, waiverReason, actionKey string) (*models.WorkCompletionCondition, error) {
	return b.VerifyWorkConditionForPrincipal(ctx, attemptID, leaseToken, "", conditionID, status, evidenceIDs, waiverReason, actionKey)
}

// VerifyWorkConditionForPrincipal verifies a condition only for the principal
// bound to the supplied lease.
func (b *Brain) VerifyWorkConditionForPrincipal(ctx context.Context, attemptID int64, leaseToken, principalID string, conditionID int64, status string, evidenceIDs []int64, waiverReason, actionKey string) (*models.WorkCompletionCondition, error) {
	status = strings.TrimSpace(status)
	if status != "passed" && status != "waived" {
		return nil, fmt.Errorf("brain: condition verification status must be passed or waived")
	}
	waiverReason = strings.TrimSpace(waiverReason)
	if status == "waived" {
		if err := validateContent(waiverReason); err != nil {
			return nil, fmt.Errorf("brain: condition waiver reason: %w", err)
		}
	} else {
		waiverReason = ""
	}
	evidenceIDs, err := normalizeEvidenceIDs(evidenceIDs)
	if err != nil {
		return nil, err
	}
	actionKey, err = validateWorkActionKey(actionKey)
	if err != nil {
		return nil, err
	}
	requestHash, err := workActionRequestHash("verify", struct {
		AttemptID    int64   `json:"attempt_id"`
		ConditionID  int64   `json:"condition_id"`
		Status       string  `json:"status"`
		EvidenceIDs  []int64 `json:"evidence_ids"`
		WaiverReason string  `json:"waiver_reason"`
	}{attemptID, conditionID, status, evidenceIDs, waiverReason})
	if err != nil {
		return nil, err
	}
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin condition verification: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	leased, err := lockWorkAttemptForAction(ctx, tx, attemptID, leaseToken, principalID)
	if err != nil {
		return nil, err
	}
	receipt, err := loadWorkActionReceipt(ctx, tx, leased.Attempt.WorkItemID, &attemptID, "verify", actionKey, requestHash)
	if err != nil {
		return nil, err
	}
	if receipt != nil {
		var replay models.WorkCompletionCondition
		if err := decodeWorkActionResponse(receipt, &replay); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit condition verification replay: %w", err)
		}
		return &replay, nil
	}
	if !leased.LeaseValid {
		return nil, ErrWorkAttemptLease
	}
	var conditionWorkItemID int64
	err = tx.QueryRow(ctx,
		`SELECT work_item_id FROM work_completion_conditions
		 WHERE id = $1 AND superseded_at IS NULL FOR UPDATE`,
		conditionID,
	).Scan(&conditionWorkItemID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrWorkConditionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock work completion condition: %w", err)
	}
	if conditionWorkItemID != leased.Attempt.WorkItemID {
		return nil, ErrWorkConditionNotFound
	}
	var evidenceCount int
	if err := tx.QueryRow(ctx,
		`SELECT count(*)
		 FROM work_condition_evidence linked
		 JOIN work_evidence evidence ON evidence.id = linked.evidence_id
		 WHERE linked.condition_id = $1 AND linked.work_item_id = $2
		   AND evidence.work_item_id = $2 AND evidence.id = ANY($3)`,
		conditionID, leased.Attempt.WorkItemID, evidenceIDs,
	).Scan(&evidenceCount); err != nil {
		return nil, fmt.Errorf("check condition evidence: %w", err)
	}
	if evidenceCount != len(evidenceIDs) {
		return nil, ErrWorkEvidenceNotFound
	}
	condition, err := scanWorkCondition(tx.QueryRow(ctx,
		`UPDATE work_completion_conditions
		 SET status = $2, waiver_reason = $3, verified_by_attempt_id = $4,
		     verified_at = now(), updated_at = now()
		 WHERE id = $1
		 RETURNING `+workConditionColumns,
		conditionID, status, waiverReason, attemptID,
	))
	if err != nil {
		return nil, fmt.Errorf("verify work completion condition: %w", err)
	}
	condition.EvidenceIDs = evidenceIDs
	if err := insertWorkExecutionEvent(ctx, tx, leased.NamespaceID, leased.Attempt.WorkItemID, &attemptID, "work.condition.verified", workActionKeyDigest(actionKey), map[string]any{
		"condition_id": condition.ID,
		"status":       condition.Status,
		"evidence_ids": evidenceIDs,
	}); err != nil {
		return nil, err
	}
	conditionResultID := condition.ID
	if err := storeWorkActionReceipt(ctx, tx, leased.Attempt.WorkItemID, &attemptID, "verify", actionKey, requestHash, &conditionResultID, condition); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit condition verification: %w", err)
	}
	return condition, nil
}

// HandoffWorkAttempt stores a final checkpoint with a required next action,
// ends the lease, and makes the work item available for a later attempt.
func (b *Brain) HandoffWorkAttempt(ctx context.Context, attemptID int64, leaseToken string, input WorkCheckpointInput, actionKey string) (*models.WorkAttempt, error) {
	return b.HandoffWorkAttemptForPrincipal(ctx, attemptID, leaseToken, "", input, actionKey)
}

// HandoffWorkAttemptForPrincipal hands off only the authenticated principal's
// attempt and revokes every token issued for that attempt.
func (b *Brain) HandoffWorkAttemptForPrincipal(ctx context.Context, attemptID int64, leaseToken, principalID string, input WorkCheckpointInput, actionKey string) (*models.WorkAttempt, error) {
	input, err := validateCheckpointInput(input, true)
	if err != nil {
		return nil, err
	}
	actionKey, err = validateWorkActionKey(actionKey)
	if err != nil {
		return nil, err
	}
	requestHash, err := workActionRequestHash("handoff", struct {
		AttemptID int64               `json:"attempt_id"`
		Input     WorkCheckpointInput `json:"input"`
	}{attemptID, input})
	if err != nil {
		return nil, err
	}
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin work handoff: %w", err)
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
		return nil, fmt.Errorf("brain: work attempt namespace changed while handing off")
	}
	receipt, err := loadWorkActionReceipt(ctx, tx, leased.Attempt.WorkItemID, &attemptID, "handoff", actionKey, requestHash)
	if err != nil {
		return nil, err
	}
	if receipt != nil {
		var replay models.WorkAttempt
		if err := decodeWorkActionResponse(receipt, &replay); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit work handoff replay: %w", err)
		}
		return &replay, nil
	}
	if !leased.LeaseValid {
		return nil, ErrWorkAttemptLease
	}
	checkpoint, err := insertWorkCheckpoint(ctx, tx, attemptID, input)
	if err != nil {
		return nil, err
	}
	attempt, err := scanWorkAttempt(tx.QueryRow(ctx,
		`UPDATE work_attempts
		 SET status = 'handed_off', lease_expires_at = clock_timestamp(),
		     ended_at = clock_timestamp(), updated_at = now()
		 WHERE id = $1
		 RETURNING `+workAttemptColumns,
		attemptID,
	))
	if err != nil {
		return nil, fmt.Errorf("end handed-off work attempt: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE work_attempt_lease_tokens
		 SET revoked_at = clock_timestamp()
		 WHERE attempt_id = $1 AND revoked_at IS NULL`,
		attemptID,
	); err != nil {
		return nil, fmt.Errorf("revoke handed-off work lease tokens: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO work_execution_states (work_item_id, current_next_action)
		 VALUES ($1, $2)
		 ON CONFLICT (work_item_id) DO UPDATE SET
		   current_next_action = EXCLUDED.current_next_action, updated_at = now()`,
		leased.Attempt.WorkItemID, checkpoint.NextAction,
	); err != nil {
		return nil, fmt.Errorf("store handoff next action: %w", err)
	}
	if err := setAvailableWorkItemStatus(ctx, tx, leased.NamespaceID, leased.Attempt.WorkItemID); err != nil {
		return nil, err
	}
	if err := insertWorkExecutionEvent(ctx, tx, leased.NamespaceID, leased.Attempt.WorkItemID, &attemptID, "work.attempt.handed_off", workActionKeyDigest(actionKey), map[string]any{
		"checkpoint_id": checkpoint.ID,
		"next_action":   checkpoint.NextAction,
	}); err != nil {
		return nil, err
	}
	if err := storeWorkActionReceipt(ctx, tx, leased.Attempt.WorkItemID, &attemptID, "handoff", actionKey, requestHash, &attemptID, attempt); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit work handoff: %w", err)
	}
	return attempt, nil
}

// FinishWorkAttempt checks conditions, linked evidence, and blocking items in
// the same transaction that completes the attempt and work item.
func (b *Brain) FinishWorkAttempt(ctx context.Context, attemptID int64, leaseToken string, input WorkFinishInput, actionKey string) (*models.WorkAttempt, error) {
	return b.FinishWorkAttemptForPrincipal(ctx, attemptID, leaseToken, "", input, actionKey)
}

// FinishWorkAttemptForPrincipal completes only the authenticated principal's
// attempt and revokes every token issued for that attempt.
func (b *Brain) FinishWorkAttemptForPrincipal(ctx context.Context, attemptID int64, leaseToken, principalID string, input WorkFinishInput, actionKey string) (*models.WorkAttempt, error) {
	input, err := validateWorkFinishInput(input)
	if err != nil {
		return nil, err
	}
	actionKey, err = validateWorkActionKey(actionKey)
	if err != nil {
		return nil, err
	}
	requestHash, err := workActionRequestHash("finish", struct {
		AttemptID int64           `json:"attempt_id"`
		Input     WorkFinishInput `json:"input"`
	}{attemptID, input})
	if err != nil {
		return nil, err
	}
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin work completion: %w", err)
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
		return nil, fmt.Errorf("brain: work attempt namespace changed while completing")
	}
	receipt, err := loadWorkActionReceipt(ctx, tx, leased.Attempt.WorkItemID, &attemptID, "finish", actionKey, requestHash)
	if err != nil {
		return nil, err
	}
	if receipt != nil {
		var replay models.WorkAttempt
		if err := decodeWorkActionResponse(receipt, &replay); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit work completion replay: %w", err)
		}
		return &replay, nil
	}
	if !leased.LeaseValid {
		return nil, ErrWorkAttemptLease
	}
	if err := lockWorkGraph(ctx, tx, leased.NamespaceID); err != nil {
		return nil, err
	}
	var requiredCount, incompleteRequired int64
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE condition.required),
		        count(*) FILTER (
		            WHERE condition.required AND (
		                condition.status NOT IN ('passed', 'waived') OR
		                NOT EXISTS (
		                    SELECT 1
		                    FROM work_condition_evidence linked
		                    JOIN work_evidence evidence ON evidence.id = linked.evidence_id
		                    WHERE linked.condition_id = condition.id
		                      AND linked.work_item_id = condition.work_item_id
		                      AND evidence.work_item_id = condition.work_item_id
		                )
		            )
		        )
		 FROM work_completion_conditions condition
		 WHERE condition.work_item_id = $1 AND condition.superseded_at IS NULL`,
		leased.Attempt.WorkItemID,
	).Scan(&requiredCount, &incompleteRequired); err != nil {
		return nil, fmt.Errorf("check work completion conditions: %w", err)
	}
	rows, err := tx.Query(ctx,
		`SELECT blocker.id
		 FROM work_item_edges edge
		 JOIN work_items blocker ON blocker.id = edge.from_item_id
		 WHERE edge.to_item_id = $1 AND edge.edge_type = 'blocks' AND edge.deleted_at IS NULL
		   AND blocker.deleted_at IS NULL AND blocker.status NOT IN ('done', 'canceled')
		 FOR UPDATE OF blocker`,
		leased.Attempt.WorkItemID,
	)
	if err != nil {
		return nil, fmt.Errorf("check unfinished blocking work: %w", err)
	}
	unfinishedBlocker := rows.Next()
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("read unfinished blocking work: %w", err)
	}
	rows.Close()
	if err := validateWorkCompletion(requiredCount, incompleteRequired, unfinishedBlocker); err != nil {
		return nil, err
	}
	checkpoint, err := insertWorkCheckpoint(ctx, tx, attemptID, WorkCheckpointInput{
		Summary: input.Summary, Result: input.Result, NextAction: "",
	})
	if err != nil {
		return nil, err
	}
	attempt, err := scanWorkAttempt(tx.QueryRow(ctx,
		`UPDATE work_attempts
		 SET status = 'completed', lease_expires_at = clock_timestamp(),
		     ended_at = clock_timestamp(), updated_at = now()
		 WHERE id = $1
		 RETURNING `+workAttemptColumns,
		attemptID,
	))
	if err != nil {
		return nil, fmt.Errorf("complete work attempt: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE work_attempt_lease_tokens
		 SET revoked_at = clock_timestamp()
		 WHERE attempt_id = $1 AND revoked_at IS NULL`,
		attemptID,
	); err != nil {
		return nil, fmt.Errorf("revoke completed work lease tokens: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE work_items
		 SET status = 'done', completed_at = now(), updated_at = now()
		 WHERE id = $1`,
		leased.Attempt.WorkItemID,
	); err != nil {
		return nil, fmt.Errorf("complete work item: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE work_plan_items
		 SET completed_by = $2, updated_at = now()
		 WHERE work_item_id = $1 AND kind = 'task'`,
		leased.Attempt.WorkItemID, leased.Attempt.AgentID,
	); err != nil {
		return nil, fmt.Errorf("record plan task completer: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO work_execution_states (work_item_id, current_next_action)
		 VALUES ($1, '')
		 ON CONFLICT (work_item_id) DO UPDATE SET current_next_action = '', updated_at = now()`,
		leased.Attempt.WorkItemID,
	); err != nil {
		return nil, fmt.Errorf("clear completed work next action: %w", err)
	}
	if err := insertWorkExecutionEvent(ctx, tx, leased.NamespaceID, leased.Attempt.WorkItemID, &attemptID, "work.attempt.completed", workActionKeyDigest(actionKey), map[string]any{
		"checkpoint_id": checkpoint.ID,
	}); err != nil {
		return nil, err
	}
	if err := storeWorkActionReceipt(ctx, tx, leased.Attempt.WorkItemID, &attemptID, "finish", actionKey, requestHash, &attemptID, attempt); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit work completion: %w", err)
	}
	return attempt, nil
}

// GetWorkResumeBundle returns the durable state needed by a later agent. The
// latest checkpoint is selected across all attempts so a handed-off next
// action is retained even if a newer attempt expires before checkpointing.
func (b *Brain) boundedWorkResumeLimit(limit int) int {
	if limit <= 0 {
		return 1
	}
	if b.config.MaxResultSize > 0 && limit > b.config.MaxResultSize {
		return b.config.MaxResultSize
	}
	return limit
}

func (b *Brain) GetWorkResumeBundle(ctx context.Context, workItemID int64, recentEventLimit int) (*models.WorkResumeBundle, error) {
	if recentEventLimit <= 0 {
		recentEventLimit = defaultResumeEventLimit
	}
	if recentEventLimit > maxResumeEventLimit {
		recentEventLimit = maxResumeEventLimit
	}
	recentEventLimit = b.boundedWorkResumeLimit(recentEventLimit)
	conditionLimit := b.boundedWorkResumeLimit(resumeConditionLimit)
	evidenceLimit := b.boundedWorkResumeLimit(resumeEvidenceLimit)
	worktreeLimit := b.boundedWorkResumeLimit(resumeWorktreeLimit)
	memoryLimit := b.boundedWorkResumeLimit(resumeMemoryLimit)
	blockerLimit := b.boundedWorkResumeLimit(resumeBlockerLimit)
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin work resume read: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	graphNamespaceID, err := lockWorkGraphForItem(ctx, tx, workItemID)
	if err != nil {
		return nil, err
	}
	namespaceID, _, err := lockWorkItem(ctx, tx, workItemID)
	if err != nil {
		return nil, err
	}
	if namespaceID != graphNamespaceID {
		return nil, fmt.Errorf("brain: work item namespace changed while resuming work")
	}
	if err := expireStaleWorkAttempts(ctx, tx, namespaceID, workItemID); err != nil {
		return nil, err
	}

	item, err := scanWorkItem(tx.QueryRow(ctx,
		`SELECT `+workItemColumns+` FROM work_items WHERE id = $1 AND deleted_at IS NULL`,
		workItemID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrWorkItemNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read work item for resume: %w", err)
	}
	if err := tx.QueryRow(ctx,
		`SELECT coalesce(array_agg(limited.worktree_id ORDER BY limited.worktree_id), '{}'::bigint[])
		 FROM (
		     SELECT linked.worktree_id
		     FROM work_item_worktrees linked
		     JOIN worktrees tree ON tree.id = linked.worktree_id
		     WHERE linked.work_item_id = $1 AND tree.namespace_id = $2 AND tree.deleted_at IS NULL
		     ORDER BY linked.created_at DESC, linked.worktree_id DESC
		     LIMIT $3
		 ) limited`,
		workItemID, namespaceID, worktreeLimit,
	).Scan(&item.WorktreeIDs); err != nil {
		return nil, fmt.Errorf("read work item worktree IDs for resume: %w", err)
	}

	bundle := &models.WorkResumeBundle{
		WorkItem:             *item,
		CompletionConditions: make([]models.WorkCompletionCondition, 0),
		Evidence:             make([]models.WorkEvidence, 0),
		WorktreeLinks:        make([]models.WorktreeLink, 0),
		MemoryLinks:          make([]models.WorkMemorySnapshot, 0),
		Blockers:             make([]models.WorkItem, 0),
		RecentEvents:         make([]models.WorkEvent, 0),
	}
	if err := tx.QueryRow(ctx,
		`SELECT
		    (SELECT count(*) FROM work_completion_conditions condition
		     WHERE condition.work_item_id = $1 AND condition.superseded_at IS NULL),
		    (SELECT count(*) FROM work_evidence evidence WHERE evidence.work_item_id = $1),
		    (SELECT count(*) FROM work_item_worktrees linked
		     JOIN worktrees tree ON tree.id = linked.worktree_id
		     WHERE linked.work_item_id = $1 AND tree.namespace_id = $2 AND tree.deleted_at IS NULL),
		    (SELECT count(*) FROM work_item_memory_links linked
		     WHERE linked.work_item_id = $1
		       AND (
		           (linked.memory_type = 'episode' AND EXISTS (
		               SELECT 1 FROM episodes memory
		               WHERE memory.id = linked.memory_id AND memory.namespace_id = $2 AND memory.deleted_at IS NULL
		           )) OR
		           (linked.memory_type = 'fact' AND EXISTS (
		               SELECT 1 FROM facts memory
		               WHERE memory.id = linked.memory_id AND memory.namespace_id = $2
		                 AND memory.deleted_at IS NULL AND memory.valid_until IS NULL
		           )) OR
		           (linked.memory_type = 'hypothesis' AND EXISTS (
		               SELECT 1 FROM hypotheses memory
		               WHERE memory.id = linked.memory_id AND memory.namespace_id = $2 AND memory.deleted_at IS NULL
		           )) OR
		           (linked.memory_type = 'failure' AND EXISTS (
		               SELECT 1 FROM failures memory
		               WHERE memory.id = linked.memory_id AND memory.namespace_id = $2 AND memory.deleted_at IS NULL
		           )) OR
		           (linked.memory_type = 'goal' AND EXISTS (
		               SELECT 1 FROM goals memory
		               WHERE memory.id = linked.memory_id AND memory.namespace_id = $2 AND memory.deleted_at IS NULL
		           ))
		       )),
		    (SELECT count(*) FROM work_item_edges edge
		     JOIN work_items blocker ON blocker.id = edge.from_item_id
		     WHERE edge.to_item_id = $1 AND edge.edge_type = 'blocks' AND edge.deleted_at IS NULL
		       AND edge.namespace_id = $2 AND blocker.namespace_id = $2
		       AND blocker.deleted_at IS NULL AND blocker.status NOT IN ('done', 'canceled')),
		    (SELECT count(*) FROM work_events event
		     WHERE event.work_item_id = $1 AND event.namespace_id = $2
		       AND (event.attempt_id IS NULL OR EXISTS (
		           SELECT 1 FROM work_attempts attempt
		           WHERE attempt.id = event.attempt_id AND attempt.work_item_id = $1
		       )))`,
		workItemID, namespaceID,
	).Scan(
		&bundle.Totals.CompletionConditions,
		&bundle.Totals.Evidence,
		&bundle.Totals.WorktreeLinks,
		&bundle.Totals.MemoryLinks,
		&bundle.Totals.Blockers,
		&bundle.Totals.RecentEvents,
	); err != nil {
		return nil, fmt.Errorf("read work resume collection totals: %w", err)
	}
	if err := tx.QueryRow(ctx,
		`SELECT coalesce((SELECT current_next_action FROM work_execution_states WHERE work_item_id = $1), '')`,
		workItemID,
	).Scan(&bundle.NextAction); err != nil {
		return nil, fmt.Errorf("read current work next action: %w", err)
	}

	latestAttempt, err := scanWorkAttempt(tx.QueryRow(ctx,
		`SELECT `+workAttemptColumns+` FROM work_attempts
		 WHERE work_item_id = $1 ORDER BY attempt_number DESC LIMIT 1`,
		workItemID,
	))
	if err == nil {
		bundle.LatestAttempt = latestAttempt
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("read latest work attempt: %w", err)
	}

	latestCheckpoint, err := scanWorkCheckpoint(tx.QueryRow(ctx,
		`SELECT checkpoint.id, checkpoint.attempt_id, checkpoint.summary, checkpoint.result,
		        checkpoint.next_action, checkpoint.created_at
		 FROM work_checkpoints checkpoint
		 JOIN work_attempts attempt ON attempt.id = checkpoint.attempt_id
		 WHERE attempt.work_item_id = $1
		 ORDER BY checkpoint.created_at DESC, checkpoint.id DESC LIMIT 1`,
		workItemID,
	))
	if err == nil {
		bundle.LatestCheckpoint = latestCheckpoint
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("read latest work checkpoint: %w", err)
	}

	conditionRows, err := tx.Query(ctx,
		`SELECT condition.id, condition.work_item_id, condition.kind, condition.description,
		        condition.verification, condition.required, condition.position, condition.status, condition.waiver_reason,
		        condition.verified_by_attempt_id, condition.verified_at, condition.superseded_at,
		        condition.created_at, condition.updated_at,
		        coalesce(ARRAY(
		            SELECT linked_evidence.id
		            FROM work_condition_evidence linked
		            JOIN work_evidence linked_evidence
		              ON linked_evidence.id = linked.evidence_id
		             AND linked_evidence.work_item_id = condition.work_item_id
		            WHERE linked.condition_id = condition.id
		              AND linked.work_item_id = condition.work_item_id
		            ORDER BY linked_evidence.submitted_at DESC, linked_evidence.id DESC
		            LIMIT $3
		        ), '{}'::bigint[])
		 FROM work_completion_conditions condition
		 WHERE condition.work_item_id = $1 AND condition.superseded_at IS NULL
		 ORDER BY condition.position, condition.id
		 LIMIT $2`,
		workItemID, conditionLimit, evidenceLimit,
	)
	if err != nil {
		return nil, fmt.Errorf("read work completion conditions: %w", err)
	}
	for conditionRows.Next() {
		var condition models.WorkCompletionCondition
		if err := conditionRows.Scan(
			&condition.ID, &condition.WorkItemID, &condition.Kind, &condition.Description, &condition.Verification, &condition.Required,
			&condition.Position, &condition.Status, &condition.WaiverReason,
			&condition.VerifiedByAttemptID, &condition.VerifiedAt, &condition.SupersededAt,
			&condition.CreatedAt, &condition.UpdatedAt, &condition.EvidenceIDs,
		); err != nil {
			conditionRows.Close()
			return nil, fmt.Errorf("scan work completion condition: %w", err)
		}
		bundle.CompletionConditions = append(bundle.CompletionConditions, condition)
	}
	if err := conditionRows.Err(); err != nil {
		conditionRows.Close()
		return nil, fmt.Errorf("read work completion condition rows: %w", err)
	}
	conditionRows.Close()

	evidenceRows, err := tx.Query(ctx,
		`SELECT evidence.id, evidence.work_item_id, evidence.attempt_id, evidence.evidence_type,
		        evidence.summary, evidence.reference, evidence.payload, evidence.content_digest,
		        evidence.principal_id, evidence.worktree_head_sha, evidence.submitted_at, evidence.created_at,
		        coalesce(
		            array_agg(linked_condition.id ORDER BY linked_condition.id)
		                FILTER (WHERE linked_condition.id IS NOT NULL),
		            '{}'::bigint[]
		        )
	 FROM work_evidence evidence
	 LEFT JOIN work_condition_evidence linked
	        ON linked.evidence_id = evidence.id AND linked.work_item_id = evidence.work_item_id
	 LEFT JOIN work_completion_conditions linked_condition
	        ON linked_condition.id = linked.condition_id
	       AND linked_condition.work_item_id = evidence.work_item_id
		 WHERE evidence.work_item_id = $1
		 GROUP BY evidence.id
		 ORDER BY evidence.submitted_at DESC, evidence.id DESC
		 LIMIT $2`,
		workItemID, evidenceLimit,
	)
	if err != nil {
		return nil, fmt.Errorf("read work evidence: %w", err)
	}
	for evidenceRows.Next() {
		var evidence models.WorkEvidence
		if err := evidenceRows.Scan(
			&evidence.ID, &evidence.WorkItemID, &evidence.AttemptID, &evidence.EvidenceType,
			&evidence.Summary, &evidence.Reference, &evidence.Payload, &evidence.ContentDigest,
			&evidence.PrincipalID, &evidence.WorktreeHeadSHA, &evidence.SubmittedAt,
			&evidence.CreatedAt, &evidence.ConditionIDs,
		); err != nil {
			evidenceRows.Close()
			return nil, fmt.Errorf("scan work evidence: %w", err)
		}
		bundle.Evidence = append(bundle.Evidence, evidence)
	}
	if err := evidenceRows.Err(); err != nil {
		evidenceRows.Close()
		return nil, fmt.Errorf("read work evidence rows: %w", err)
	}
	evidenceRows.Close()

	worktreeRows, err := tx.Query(ctx,
		`SELECT linked.work_item_id, linked.relation, linked.created_at,
		        tree.id, tree.namespace_id, tree.repository, tree.worktree_path, tree.branch,
		        tree.head_sha, tree.status, tree.agent_id, tree.last_seen_at, tree.metadata,
		        tree.created_at, tree.updated_at, tree.deleted_at
		 FROM work_item_worktrees linked
		 JOIN worktrees tree ON tree.id = linked.worktree_id AND tree.deleted_at IS NULL
		 WHERE linked.work_item_id = $1 AND tree.namespace_id = $2
		 ORDER BY linked.created_at DESC, tree.id DESC
		 LIMIT $3`,
		workItemID, namespaceID, worktreeLimit,
	)
	if err != nil {
		return nil, fmt.Errorf("read worktree links for resume: %w", err)
	}
	for worktreeRows.Next() {
		var link models.WorktreeLink
		if err := worktreeRows.Scan(
			&link.WorkItemID, &link.Relation, &link.LinkedAt,
			&link.Worktree.ID, &link.Worktree.NamespaceID, &link.Worktree.Repository,
			&link.Worktree.WorktreePath, &link.Worktree.Branch, &link.Worktree.HeadSHA,
			&link.Worktree.Status, &link.Worktree.AgentID, &link.Worktree.LastSeenAt,
			&link.Worktree.Metadata, &link.Worktree.CreatedAt, &link.Worktree.UpdatedAt,
			&link.Worktree.DeletedAt,
		); err != nil {
			worktreeRows.Close()
			return nil, fmt.Errorf("scan worktree link for resume: %w", err)
		}
		bundle.WorktreeLinks = append(bundle.WorktreeLinks, link)
	}
	if err := worktreeRows.Err(); err != nil {
		worktreeRows.Close()
		return nil, fmt.Errorf("read worktree link rows: %w", err)
	}
	worktreeRows.Close()

	memoryRows, err := tx.Query(ctx,
		`WITH snapshots AS (
		    SELECT linked.work_item_id, linked.memory_type, linked.memory_id, linked.relation,
		           left(memory.content, $4) AS content, 'recorded'::text AS status,
		           char_length(memory.content) > $4 AS content_truncated, linked.created_at AS linked_at
		    FROM work_item_memory_links linked
		    JOIN episodes memory ON linked.memory_type = 'episode' AND memory.id = linked.memory_id
		    WHERE linked.work_item_id = $1 AND memory.namespace_id = $2 AND memory.deleted_at IS NULL
		    UNION ALL
		    SELECT linked.work_item_id, linked.memory_type, linked.memory_id, linked.relation,
		           left(memory.content, $4), 'active'::text,
		           char_length(memory.content) > $4, linked.created_at
		    FROM work_item_memory_links linked
		    JOIN facts memory ON linked.memory_type = 'fact' AND memory.id = linked.memory_id
		    WHERE linked.work_item_id = $1 AND memory.namespace_id = $2
		      AND memory.deleted_at IS NULL AND memory.valid_until IS NULL
		    UNION ALL
		    SELECT linked.work_item_id, linked.memory_type, linked.memory_id, linked.relation,
		           left(memory.content, $4), memory.status,
		           char_length(memory.content) > $4, linked.created_at
		    FROM work_item_memory_links linked
		    JOIN hypotheses memory ON linked.memory_type = 'hypothesis' AND memory.id = linked.memory_id
		    WHERE linked.work_item_id = $1 AND memory.namespace_id = $2 AND memory.deleted_at IS NULL
		    UNION ALL
		    SELECT linked.work_item_id, linked.memory_type, linked.memory_id, linked.relation,
		           left(memory.content, $4), 'recorded'::text,
		           char_length(memory.content) > $4, linked.created_at
		    FROM work_item_memory_links linked
		    JOIN failures memory ON linked.memory_type = 'failure' AND memory.id = linked.memory_id
		    WHERE linked.work_item_id = $1 AND memory.namespace_id = $2 AND memory.deleted_at IS NULL
		    UNION ALL
		    SELECT linked.work_item_id, linked.memory_type, linked.memory_id, linked.relation,
		           left(memory.content, $4), memory.status,
		           char_length(memory.content) > $4, linked.created_at
		    FROM work_item_memory_links linked
		    JOIN goals memory ON linked.memory_type = 'goal' AND memory.id = linked.memory_id
		    WHERE linked.work_item_id = $1 AND memory.namespace_id = $2 AND memory.deleted_at IS NULL
		)
		SELECT work_item_id, memory_type, memory_id, relation, content, status, content_truncated, linked_at
		FROM snapshots
		ORDER BY linked_at DESC, memory_type, memory_id
		LIMIT $3`,
		workItemID, namespaceID, memoryLimit, resumeMemoryContentLimit,
	)
	if err != nil {
		return nil, fmt.Errorf("read memory links for resume: %w", err)
	}
	for memoryRows.Next() {
		var link models.WorkMemorySnapshot
		if err := memoryRows.Scan(
			&link.WorkItemID, &link.MemoryType, &link.MemoryID, &link.Relation,
			&link.Content, &link.Status, &link.ContentTruncated, &link.LinkedAt,
		); err != nil {
			memoryRows.Close()
			return nil, fmt.Errorf("scan memory link for resume: %w", err)
		}
		bundle.MemoryLinks = append(bundle.MemoryLinks, link)
	}
	if err := memoryRows.Err(); err != nil {
		memoryRows.Close()
		return nil, fmt.Errorf("read memory link rows: %w", err)
	}
	memoryRows.Close()

	blockerRows, err := tx.Query(ctx,
		`SELECT blocker.id, blocker.namespace_id, blocker.goal_id, blocker.parent_id,
		        blocker.issue_key, blocker.issue_type, blocker.labels, blocker.reporter,
		        blocker.title, blocker.description, blocker.status, blocker.priority,
		        blocker.position, blocker.owner, blocker.due_at, blocker.started_at,
		        blocker.completed_at, blocker.created_at, blocker.updated_at, blocker.deleted_at
		 FROM work_item_edges edge
		 JOIN work_items blocker ON blocker.id = edge.from_item_id
		 WHERE edge.to_item_id = $1 AND edge.edge_type = 'blocks' AND edge.deleted_at IS NULL
		   AND edge.namespace_id = $2 AND blocker.namespace_id = $2
		   AND blocker.deleted_at IS NULL AND blocker.status NOT IN ('done', 'canceled')
		 ORDER BY blocker.priority DESC, blocker.position, blocker.id
		 LIMIT $3`,
		workItemID, namespaceID, blockerLimit,
	)
	if err != nil {
		return nil, fmt.Errorf("read work blockers for resume: %w", err)
	}
	bundle.Blockers, err = scanWorkItemRows(blockerRows)
	if err != nil {
		return nil, err
	}

	eventRows, err := tx.Query(ctx,
		`SELECT id, namespace_id, worktree_id, work_item_id, attempt_id, event_type,
		        event_key, payload, occurred_at, created_at
		 FROM work_events event
		 WHERE event.work_item_id = $1 AND event.namespace_id = $2
		   AND (event.attempt_id IS NULL OR EXISTS (
		       SELECT 1 FROM work_attempts attempt
		       WHERE attempt.id = event.attempt_id AND attempt.work_item_id = $1
		   ))
		 ORDER BY event.occurred_at DESC, event.id DESC LIMIT $3`,
		workItemID, namespaceID, recentEventLimit,
	)
	if err != nil {
		return nil, fmt.Errorf("read recent work events for resume: %w", err)
	}
	for eventRows.Next() {
		var event models.WorkEvent
		if err := eventRows.Scan(
			&event.ID, &event.NamespaceID, &event.WorktreeID, &event.WorkItemID, &event.AttemptID,
			&event.EventType, &event.EventKey, &event.Payload, &event.OccurredAt, &event.CreatedAt,
		); err != nil {
			eventRows.Close()
			return nil, fmt.Errorf("scan recent work event for resume: %w", err)
		}
		bundle.RecentEvents = append(bundle.RecentEvents, event)
	}
	if err := eventRows.Err(); err != nil {
		eventRows.Close()
		return nil, fmt.Errorf("read recent work event rows: %w", err)
	}
	eventRows.Close()

	bundle.Truncated.CompletionConditions = bundle.Totals.CompletionConditions > len(bundle.CompletionConditions)
	bundle.Truncated.Evidence = bundle.Totals.Evidence > len(bundle.Evidence)
	bundle.Truncated.WorktreeLinks = bundle.Totals.WorktreeLinks > len(bundle.WorktreeLinks)
	bundle.Truncated.MemoryLinks = bundle.Totals.MemoryLinks > len(bundle.MemoryLinks)
	for _, memory := range bundle.MemoryLinks {
		bundle.Truncated.MemoryLinks = bundle.Truncated.MemoryLinks || memory.ContentTruncated
	}
	bundle.Truncated.Blockers = bundle.Totals.Blockers > len(bundle.Blockers)
	bundle.Truncated.RecentEvents = bundle.Totals.RecentEvents > len(bundle.RecentEvents)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit work resume read: %w", err)
	}
	return bundle, nil
}
