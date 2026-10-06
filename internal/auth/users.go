package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"
)

const (
	minLocalPasswordLen      = 8
	maxLocalPasswordLen      = 72 // bcrypt's input limit; longer input would be silently truncated
	localLoginFailureLimit   = 10
	localLoginFailureWindow  = 5 * time.Minute
	localLoginFailureEntries = 10000
	localPasswordCost        = 12

	identityPassword = "password"
	identityOIDC     = "oidc"
)

var (
	localUsernameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

	ErrUserNotFound        = errors.New("auth: user not found")
	ErrUserExists          = errors.New("auth: user already exists")
	ErrInvalidUsername     = errors.New("auth: username must be 1-64 lowercase letters, digits, dots, hyphens, or underscores")
	ErrWeakPassword        = fmt.Errorf("auth: password must be between %d and %d characters", minLocalPasswordLen, maxLocalPasswordLen)
	ErrAccountsUnavailable = errors.New("auth: user accounts need STASH_AUTH_MODE=token or oauth and a database")
	ErrLoginRejected       = errors.New("auth: invalid username or password")
	ErrLoginThrottled      = errors.New("auth: too many failed logins; try again in a few minutes")
	ErrUserDisabled        = errors.New("auth: this account is disabled")
	ErrNoPassword          = errors.New("auth: this account has no password")

	// localDummyHash keeps a login for an unknown username as slow as one for
	// a wrong password, so timing does not reveal which usernames exist.
	localDummyHash = mustHashPassword("stash-local-dummy-password-0")
)

// User is a person. The username is the session subject, so it is what
// namespaces, API tokens, and wiki authorship are keyed by.
type User struct {
	ID          int64      `json:"id"`
	Username    string     `json:"username"`
	DisplayName string     `json:"display_name"`
	IsAdmin     bool       `json:"is_admin"`
	Disabled    bool       `json:"disabled"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
	Identities  []Identity `json:"identities"`
}

// Identity is one way a user proves who they are: a password, or a subject
// at an OIDC issuer. Secrets never leave the auth package.
type Identity struct {
	ID         int64      `json:"id"`
	Kind       string     `json:"kind"`
	Issuer     string     `json:"issuer,omitempty"`
	Subject    string     `json:"subject,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// HasPassword reports whether the user can sign in with the login form.
func (u User) HasPassword() bool {
	for _, identity := range u.Identities {
		if identity.Kind == identityPassword {
			return true
		}
	}
	return false
}

// UserUpdate carries optional changes; nil keeps the stored value.
type UserUpdate struct {
	DisplayName *string
	IsAdmin     *bool
	Disabled    *bool
}

// oidcProfile is what an identity token tells us about a person beyond the
// subject. Only the display name is taken from it; the username stays the
// subject so a provisioned user keeps the namespaces it already had.
type oidcProfile struct {
	PreferredUsername string `json:"preferred_username"`
	Email             string `json:"email"`
	Name              string `json:"name"`
}

func (c oidcProfile) displayName() string {
	for _, candidate := range []string{c.Name, c.PreferredUsername, c.Email} {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			return candidate
		}
	}
	return ""
}

func mustHashPassword(password string) string {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), localPasswordCost)
	if err != nil {
		panic(err)
	}
	return string(hash)
}

func normalizeLocalUsername(raw string) (string, error) {
	username := strings.ToLower(strings.TrimSpace(raw))
	if !localUsernameRe.MatchString(username) {
		return "", ErrInvalidUsername
	}
	return username, nil
}

func hashLocalPassword(password string) (string, error) {
	if len(password) < minLocalPasswordLen || len(password) > maxLocalPasswordLen {
		return "", ErrWeakPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), localPasswordCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

// accountsEnabled reports whether this provider can store and check users:
// HTTP authentication is on and a database is attached.
func (p *Provider) accountsEnabled() bool {
	return p != nil && p.tokenPool != nil && p.Mode() != "stdio"
}

// LocalLoginAvailable reports whether the login page should offer a
// username/password form: at least one enabled account has a password.
func (p *Provider) LocalLoginAvailable(ctx context.Context) bool {
	if !p.accountsEnabled() {
		return false
	}
	var exists bool
	err := p.tokenPool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM user_identities i JOIN users u ON u.id = i.user_id
			WHERE i.kind = 'password' AND NOT u.disabled
		)`).Scan(&exists)
	return err == nil && exists
}

const userColumns = `u.id, u.username, u.display_name, u.is_admin, u.disabled, u.created_at, u.updated_at, u.last_login_at`

type userQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.IsAdmin, &u.Disabled, &u.CreatedAt, &u.UpdatedAt, &u.LastLoginAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrUserNotFound
	}
	u.Identities = []Identity{}
	return u, err
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// loadIdentities fills in the identities of every user in the slice.
func loadIdentities(ctx context.Context, q userQuerier, users []User) error {
	if len(users) == 0 {
		return nil
	}
	ids := make([]int64, len(users))
	index := map[int64]int{}
	for i, user := range users {
		ids[i] = user.ID
		index[user.ID] = i
	}
	rows, err := q.Query(ctx, `
		SELECT id, user_id, kind, issuer, subject, created_at, last_used_at
		FROM user_identities WHERE user_id = ANY($1) ORDER BY user_id, id`, ids)
	if err != nil {
		return fmt.Errorf("list identities: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var identity Identity
		var userID int64
		if err := rows.Scan(&identity.ID, &userID, &identity.Kind, &identity.Issuer, &identity.Subject, &identity.CreatedAt, &identity.LastUsedAt); err != nil {
			return fmt.Errorf("scan identity: %w", err)
		}
		if identity.Kind == identityPassword {
			identity.Subject = ""
		}
		if i, ok := index[userID]; ok {
			users[i].Identities = append(users[i].Identities, identity)
		}
	}
	return rows.Err()
}

func getUser(ctx context.Context, q userQuerier, username string) (User, error) {
	user, err := scanUser(q.QueryRow(ctx, `SELECT `+userColumns+` FROM users u WHERE u.username = $1`, strings.TrimSpace(username)))
	if err != nil {
		return User{}, err
	}
	users := []User{user}
	if err := loadIdentities(ctx, q, users); err != nil {
		return User{}, err
	}
	return users[0], nil
}

// CreateUser stores a person without any way to sign in yet; a password or
// an SSO identity is attached afterwards.
func (p *Provider) CreateUser(ctx context.Context, username, displayName string, admin bool) (User, error) {
	if !p.accountsEnabled() {
		return User{}, ErrAccountsUnavailable
	}
	username, err := normalizeLocalUsername(username)
	if err != nil {
		return User{}, err
	}
	return insertUser(ctx, p.tokenPool, username, displayName, admin)
}

func insertUser(ctx context.Context, q userQuerier, username, displayName string, admin bool) (User, error) {
	user, err := scanUser(q.QueryRow(ctx,
		`INSERT INTO users AS u (username, display_name, is_admin) VALUES ($1, $2, $3) RETURNING `+userColumns,
		username, strings.TrimSpace(displayName), admin))
	if isUniqueViolation(err) {
		return User{}, ErrUserExists
	}
	if err != nil {
		return User{}, fmt.Errorf("create user: %w", err)
	}
	return user, nil
}

// CreateLocalUser stores a person together with a password.
func (p *Provider) CreateLocalUser(ctx context.Context, username, password, displayName string, admin bool) (User, error) {
	if !p.accountsEnabled() {
		return User{}, ErrAccountsUnavailable
	}
	username, err := normalizeLocalUsername(username)
	if err != nil {
		return User{}, err
	}
	hash, err := hashLocalPassword(password)
	if err != nil {
		return User{}, err
	}
	tx, err := p.tokenPool.Begin(ctx)
	if err != nil {
		return User{}, fmt.Errorf("create user: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	user, err := insertUser(ctx, tx, username, displayName, admin)
	if err != nil {
		return User{}, err
	}
	if err := upsertPassword(ctx, tx, user.ID, username, hash); err != nil {
		return User{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, fmt.Errorf("create user: %w", err)
	}
	return getUser(ctx, p.tokenPool, username)
}

func upsertPassword(ctx context.Context, q userQuerier, userID int64, username, hash string) error {
	_, err := q.Exec(ctx, `
		INSERT INTO user_identities (user_id, kind, subject, secret_hash)
		VALUES ($1, 'password', $2, $3)
		ON CONFLICT (user_id) WHERE kind = 'password'
		DO UPDATE SET secret_hash = EXCLUDED.secret_hash, subject = EXCLUDED.subject, updated_at = now()`,
		userID, username, hash)
	if err != nil {
		return fmt.Errorf("store password: %w", err)
	}
	return nil
}

// GetUser returns one user with its identities.
func (p *Provider) GetUser(ctx context.Context, username string) (User, error) {
	if !p.accountsEnabled() {
		return User{}, ErrAccountsUnavailable
	}
	return getUser(ctx, p.tokenPool, username)
}

// ListUsers returns every user ordered by username, with identities.
func (p *Provider) ListUsers(ctx context.Context) ([]User, error) {
	if !p.accountsEnabled() {
		return nil, ErrAccountsUnavailable
	}
	rows, err := p.tokenPool.Query(ctx, `SELECT `+userColumns+` FROM users u ORDER BY u.username`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	users := []User{}
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := loadIdentities(ctx, p.tokenPool, users); err != nil {
		return nil, err
	}
	return users, nil
}

// SetLocalPassword gives a user a password, or replaces the one it has. An
// SSO-provisioned user can be given one as long as its username fits the
// login form.
func (p *Provider) SetLocalPassword(ctx context.Context, username, password string) error {
	if !p.accountsEnabled() {
		return ErrAccountsUnavailable
	}
	username, err := normalizeLocalUsername(username)
	if err != nil {
		return err
	}
	hash, err := hashLocalPassword(password)
	if err != nil {
		return err
	}
	user, err := getUser(ctx, p.tokenPool, username)
	if err != nil {
		return err
	}
	return upsertPassword(ctx, p.tokenPool, user.ID, username, hash)
}

// UpdateUser changes the flags and display name of a user.
func (p *Provider) UpdateUser(ctx context.Context, username string, update UserUpdate) (User, error) {
	if !p.accountsEnabled() {
		return User{}, ErrAccountsUnavailable
	}
	current, err := getUser(ctx, p.tokenPool, username)
	if err != nil {
		return User{}, err
	}
	if update.DisplayName != nil {
		current.DisplayName = strings.TrimSpace(*update.DisplayName)
	}
	if update.IsAdmin != nil {
		current.IsAdmin = *update.IsAdmin
	}
	if update.Disabled != nil {
		current.Disabled = *update.Disabled
	}
	if _, err := p.tokenPool.Exec(ctx,
		`UPDATE users SET display_name = $2, is_admin = $3, disabled = $4, updated_at = now() WHERE id = $1`,
		current.ID, current.DisplayName, current.IsAdmin, current.Disabled); err != nil {
		return User{}, fmt.Errorf("update user: %w", err)
	}
	return getUser(ctx, p.tokenPool, current.Username)
}

// DeleteUser removes a user and its identities and revokes its API tokens.
// Its memory stays under its username.
func (p *Provider) DeleteUser(ctx context.Context, username string) error {
	if !p.accountsEnabled() {
		return ErrAccountsUnavailable
	}
	tx, err := p.tokenPool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var deleted string
	err = tx.QueryRow(ctx, `DELETE FROM users WHERE username = $1 RETURNING username`, strings.TrimSpace(username)).Scan(&deleted)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrUserNotFound
	}
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE auth_tokens
		SET revoked_at = COALESCE(revoked_at, statement_timestamp()), expires_at = COALESCE(revoked_at, statement_timestamp())
		WHERE subject = $1`, deleted); err != nil {
		return fmt.Errorf("revoke tokens of deleted user: %w", err)
	}
	return tx.Commit(ctx)
}

// SeedLocalAdmin creates the first administrator from the environment. An
// existing user keeps its password, so a later change made in the console is
// not undone by every restart, but it is promoted and re-enabled in case the
// environment is the operator's way back in. A user that exists without a
// password (for example one provisioned by SSO) receives the password.
func (p *Provider) SeedLocalAdmin(ctx context.Context, username, password string) (bool, error) {
	if !p.accountsEnabled() {
		return false, ErrAccountsUnavailable
	}
	_, err := p.CreateLocalUser(ctx, username, password, "", true)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, ErrUserExists) {
		return false, err
	}
	admin, enabled := true, false
	user, err := p.UpdateUser(ctx, username, UserUpdate{IsAdmin: &admin, Disabled: &enabled})
	if err != nil {
		return false, err
	}
	if !user.HasPassword() {
		if err := p.SetLocalPassword(ctx, username, password); err != nil {
			return false, err
		}
	}
	return false, nil
}

// IsAdmin reports whether subject may use the operator pages: an enabled
// administrator in the users table, or a subject listed in
// STASH_ADMIN_SUBJECTS.
func (p *Provider) IsAdmin(ctx context.Context, subject string) bool {
	if p == nil {
		return false
	}
	if subjectListed(subject, p.config.AdminSubjects) {
		return true
	}
	if !p.accountsEnabled() {
		return false
	}
	var admin bool
	if err := p.tokenPool.QueryRow(ctx, `SELECT is_admin AND NOT disabled FROM users WHERE username = $1`, strings.TrimSpace(subject)).Scan(&admin); err != nil {
		return false
	}
	return admin
}

// HasPassword reports whether subject is an enabled user with a password,
// which is what the console needs to offer a password change.
func (p *Provider) HasPassword(ctx context.Context, subject string) bool {
	if !p.accountsEnabled() {
		return false
	}
	var ok bool
	err := p.tokenPool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM user_identities i JOIN users u ON u.id = i.user_id
			WHERE u.username = $1 AND i.kind = 'password' AND NOT u.disabled
		)`, strings.TrimSpace(subject)).Scan(&ok)
	return err == nil && ok
}

// userDisabled reports whether subject names a user that an administrator
// switched off. Subjects without a user row (legacy token subjects) are not
// affected, and a storage error does not lock anyone out.
func (p *Provider) userDisabled(ctx context.Context, subject string) bool {
	if !p.accountsEnabled() {
		return false
	}
	var disabled bool
	if err := p.tokenPool.QueryRow(ctx, `SELECT disabled FROM users WHERE username = $1`, strings.TrimSpace(subject)).Scan(&disabled); err != nil {
		return false
	}
	return disabled
}

func subjectListed(subject, configured string) bool {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return false
	}
	for _, candidate := range strings.Split(configured, ",") {
		if subject == strings.TrimSpace(candidate) {
			return true
		}
	}
	return false
}

// resolveOIDCUser maps a verified identity-token subject to a user and
// returns the username that becomes the session subject. An unknown subject
// is provisioned on first login, with the subject itself as username so the
// namespaces it used before the users table existed stay its own. Without
// a database the subject is used directly, as it always was.
func (p *Provider) resolveOIDCUser(ctx context.Context, issuer, subject string, profile oidcProfile) (string, error) {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return "", errors.New("identity token has no subject")
	}
	if !p.accountsEnabled() {
		return subject, nil
	}
	tx, err := p.tokenPool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve SSO user: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var identityID int64
	var username string
	var disabled bool
	err = tx.QueryRow(ctx, `
		SELECT i.id, u.username, u.disabled
		FROM user_identities i JOIN users u ON u.id = i.user_id
		WHERE i.kind = 'oidc' AND i.issuer = $1 AND i.subject = $2`, issuer, subject).Scan(&identityID, &username, &disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		// A user row with the same name (seeded, CLI-created, or named after
		// the subject by an earlier deployment) is the same person: the
		// subject was already its session name.
		err = tx.QueryRow(ctx, `
			INSERT INTO users (username, display_name) VALUES ($1, $2)
			ON CONFLICT (username) DO UPDATE SET updated_at = now()
			RETURNING username, disabled`, subject, profile.displayName()).Scan(&username, &disabled)
		if err != nil {
			return "", fmt.Errorf("provision SSO user: %w", err)
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO user_identities (user_id, kind, issuer, subject)
			SELECT id, 'oidc', $2, $3 FROM users WHERE username = $1
			ON CONFLICT (kind, issuer, subject) DO UPDATE SET updated_at = now()
			RETURNING id`, username, issuer, subject).Scan(&identityID)
	}
	if err != nil {
		return "", fmt.Errorf("resolve SSO user: %w", err)
	}
	if disabled {
		return "", ErrUserDisabled
	}
	if _, err := tx.Exec(ctx, `UPDATE user_identities SET last_used_at = clock_timestamp() WHERE id = $1`, identityID); err != nil {
		return "", fmt.Errorf("record SSO login: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET last_login_at = clock_timestamp() WHERE username = $1`, username); err != nil {
		return "", fmt.Errorf("record SSO login: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("resolve SSO user: %w", err)
	}
	return username, nil
}

// loginFailure counts failed password attempts for one username from one
// client within a fixed window.
type loginFailure struct {
	count   int
	started time.Time
}

func loginClientKey(r *http.Request, username string) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	return host + "|" + username
}

func (p *Provider) loginThrottled(key string, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, ok := p.loginFailures[key]
	if !ok || now.Sub(entry.started) >= localLoginFailureWindow {
		return false
	}
	return entry.count >= localLoginFailureLimit
}

func (p *Provider) recordLoginFailure(key string, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.loginFailures == nil {
		p.loginFailures = map[string]*loginFailure{}
	}
	if len(p.loginFailures) >= localLoginFailureEntries {
		for k, entry := range p.loginFailures {
			if now.Sub(entry.started) >= localLoginFailureWindow {
				delete(p.loginFailures, k)
			}
		}
	}
	entry, ok := p.loginFailures[key]
	if !ok || now.Sub(entry.started) >= localLoginFailureWindow {
		p.loginFailures[key] = &loginFailure{count: 1, started: now}
		return
	}
	entry.count++
}

func (p *Provider) clearLoginFailures(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.loginFailures, key)
}

// authenticateLocal checks a password. It costs the same for an unknown
// username, an account without a password, a disabled account, and a wrong
// password.
func (p *Provider) authenticateLocal(ctx context.Context, r *http.Request, username, password string) (string, error) {
	if !p.accountsEnabled() {
		return "", ErrAccountsUnavailable
	}
	normalized := strings.ToLower(strings.TrimSpace(username))
	key := loginClientKey(r, normalized)
	now := time.Now()
	if p.loginThrottled(key, now) {
		return "", ErrLoginThrottled
	}
	var identityID int64
	var hash string
	var disabled bool
	lookupErr := p.tokenPool.QueryRow(ctx, `
		SELECT i.id, i.secret_hash, u.disabled
		FROM user_identities i JOIN users u ON u.id = i.user_id
		WHERE u.username = $1 AND i.kind = 'password'`, normalized).Scan(&identityID, &hash, &disabled)
	if lookupErr != nil || disabled {
		hash = localDummyHash
	}
	compareErr := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	if lookupErr != nil || disabled || compareErr != nil {
		p.recordLoginFailure(key, now)
		return "", ErrLoginRejected
	}
	p.clearLoginFailures(key)
	_, _ = p.tokenPool.Exec(ctx, `UPDATE user_identities SET last_used_at = clock_timestamp() WHERE id = $1`, identityID)
	_, _ = p.tokenPool.Exec(ctx, `UPDATE users SET last_login_at = clock_timestamp() WHERE username = $1`, normalized)
	return normalized, nil
}

// handleLocalLogin is the username/password branch of HandleLogin.
func (p *Provider) handleLocalLogin(w http.ResponseWriter, r *http.Request) {
	username := r.PostFormValue("username")
	password := r.PostFormValue("password")
	subject, err := p.authenticateLocal(r.Context(), r, username, password)
	if err != nil {
		if errors.Is(err, ErrAccountsUnavailable) {
			writeTokenLoginPage(w, false, p.browserLoginConfigured())
			return
		}
		log.Printf("local login rejected for %q: %v", strings.ToLower(strings.TrimSpace(username)), err)
		writeLoginPage(w, loginPageOptions{Failed: true, Throttled: errors.Is(err, ErrLoginThrottled), OAuthEnabled: p.browserLoginConfigured(), LocalEnabled: true})
		return
	}
	p.setSessionCookie(w, subject, time.Now().Add(p.sessionTTL()), true)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// HandlePassword lets a signed-in user change their own password. It needs
// the current password, a browser session (not a bearer token), and a
// same-origin request.
func (p *Provider) HandlePassword(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	if p == nil || !p.accountsEnabled() {
		http.Error(w, `{"error":"user accounts are not available"}`, http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := browserRequestProtection.Check(r); err != nil {
		http.Error(w, `{"error":"cross-origin request denied"}`, http.StatusForbidden)
		return
	}
	subject, err := p.VerifyRequest(r)
	if err != nil || subject == "" || bearerToken(r) != "" {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	var input struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&input); err != nil {
		http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
		return
	}
	if !p.HasPassword(r.Context(), subject) {
		http.Error(w, `{"error":"the signed-in account has no password"}`, http.StatusForbidden)
		return
	}
	if _, err := p.authenticateLocal(r.Context(), r, subject, input.CurrentPassword); err != nil {
		status := http.StatusUnauthorized
		if errors.Is(err, ErrLoginThrottled) {
			status = http.StatusTooManyRequests
		}
		http.Error(w, `{"error":"current password is wrong"}`, status)
		return
	}
	if err := p.SetLocalPassword(r.Context(), subject, input.NewPassword); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrWeakPassword) {
			status = http.StatusBadRequest
		}
		http.Error(w, `{"error":"`+strings.TrimPrefix(err.Error(), "auth: ")+`"}`, status)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
