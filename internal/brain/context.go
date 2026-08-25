package brain

import (
	"context"
	"fmt"
	"time"

	"github.com/alash3al/stash/internal/models"
	"github.com/jackc/pgx/v5"
)

// SetContext updates the working context for a namespace.
func (b *Brain) SetContext(ctx context.Context, namespaceSlug, focus string, expiresAt time.Time) error {
	if err := validatePath(namespaceSlug); err != nil {
		return err
	}
	if err := validateContent(focus); err != nil {
		return err
	}
	nsID, err := b.resolveNamespaceID(ctx, namespaceSlug)
	if err != nil {
		return err
	}

	// deleted_at 을 함께 비운다. 컨텍스트는 네임스페이스당 1행이라, 한 번 clear 한 뒤
	// 다시 set 하면 같은 행을 되살리는 형태가 된다. 이걸 빼먹으면 새 포커스를 넣어도
	// 소프트 삭제 표시가 남아 조회에서 계속 걸러진다.
	_, err = b.pool.Exec(ctx,
		`INSERT INTO contexts (namespace_id, focus, expires_at)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (namespace_id) DO UPDATE
		 SET focus = $2, expires_at = $3, updated_at = now(), deleted_at = NULL`,
		nsID, focus, expiresAt,
	)
	if err != nil {
		return fmt.Errorf("set context: %w", err)
	}
	return nil
}

// GetContext returns the working context for a namespace.
func (b *Brain) GetContext(ctx context.Context, namespaceSlug string) (*models.Context, error) {
	if err := validatePath(namespaceSlug); err != nil {
		return nil, err
	}
	nsID, err := b.resolveNamespaceID(ctx, namespaceSlug)
	if err != nil {
		return nil, err
	}

	var c models.Context
	err = b.pool.QueryRow(ctx,
		`SELECT namespace_id, focus, expires_at, created_at, updated_at
		 FROM contexts WHERE namespace_id = $1 AND deleted_at IS NULL AND expires_at > now()`,
		nsID,
	).Scan(&c.NamespaceID, &c.Focus, &c.ExpiresAt, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get context: %w", err)
	}
	return &c, nil
}

// ClearContext retires the working context for a namespace.
//
// Soft delete: the row is marked with deleted_at and stops being returned by
// GetContext, but the focus text survives as a record of what was being worked on.
// Calling SetContext again on the same namespace revives the row.
func (b *Brain) ClearContext(ctx context.Context, namespaceSlug string) error {
	if err := validatePath(namespaceSlug); err != nil {
		return err
	}
	nsID, err := b.resolveNamespaceID(ctx, namespaceSlug)
	if err != nil {
		return err
	}

	_, err = b.pool.Exec(ctx,
		"UPDATE contexts SET deleted_at = now(), updated_at = now() WHERE namespace_id = $1 AND deleted_at IS NULL",
		nsID,
	)
	if err != nil {
		return fmt.Errorf("clear context: %w", err)
	}
	return nil
}
