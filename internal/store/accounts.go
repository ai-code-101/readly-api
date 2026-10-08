package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type User struct {
	ID          string     `json:"id"`
	Phone       string     `json:"phone"`
	CreatedAt   time.Time  `json:"createdAt"`
	LastLoginAt *time.Time `json:"lastLoginAt"`
}

type Subscription struct {
	ID        string    `json:"id"`
	UserID    string    `json:"userId"`
	Plan      string    `json:"plan"`
	AmountKES int       `json:"amountKes"`
	Source    string    `json:"source"`
	StartsAt  time.Time `json:"startsAt"`
	EndsAt    time.Time `json:"endsAt"`
	CreatedAt time.Time `json:"createdAt"`
}

// ---- OTP ------------------------------------------------------------------------

type OTPRequest struct {
	ID        int64
	Phone     string
	Purpose   string
	CodeHash  string
	Attempts  int
	ExpiresAt time.Time
	CreatedAt time.Time
}

// OTPRateInfo summarises recent OTP activity, used for cooldowns and caps.
type OTPRateInfo struct {
	LastSentAt      *time.Time
	SentLastHour    int
	SentFromIPLastH int
}

func (s *Store) OTPRate(ctx context.Context, phone, ip string) (OTPRateInfo, error) {
	var r OTPRateInfo
	err := s.pool.QueryRow(ctx, `
		SELECT (SELECT max(created_at) FROM otp_requests WHERE phone = $1),
		       (SELECT count(*) FROM otp_requests WHERE phone = $1 AND created_at > now() - interval '1 hour')::int,
		       (SELECT count(*) FROM otp_requests WHERE ip = $2 AND ip <> '' AND created_at > now() - interval '1 hour')::int`,
		phone, ip).Scan(&r.LastSentAt, &r.SentLastHour, &r.SentFromIPLastH)
	return r, err
}

// CreateOTP stores a new code and invalidates earlier unused codes for the same phone and purpose.
func (s *Store) CreateOTP(ctx context.Context, phone, purpose, codeHash, requestID, ip string, ttl time.Duration) (int64, error) {
	var id int64
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`UPDATE otp_requests SET used_at = now() WHERE phone = $1 AND purpose = $2 AND used_at IS NULL`, phone, purpose); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			INSERT INTO otp_requests (phone, purpose, code_hash, request_id, ip, expires_at)
			VALUES ($1, $2, $3, $4, $5, now() + $6::interval) RETURNING id`,
			phone, purpose, codeHash, requestID, ip, ttl.String()).Scan(&id)
	})
	return id, err
}

func (s *Store) SetOTPMessageID(ctx context.Context, id int64, msgID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE otp_requests SET sms_message_id = $2 WHERE id = $1`, id, msgID)
	return err
}

// DeleteOTP removes a code that could not be delivered so it doesn't block a resend.
func (s *Store) DeleteOTP(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM otp_requests WHERE id = $1`, id)
	return err
}

// LatestActiveOTP returns the newest unused, unexpired code for phone+purpose.
func (s *Store) LatestActiveOTP(ctx context.Context, phone, purpose string) (*OTPRequest, error) {
	var o OTPRequest
	err := s.pool.QueryRow(ctx, `
		SELECT id, phone, purpose, code_hash, attempts, expires_at, created_at FROM otp_requests
		WHERE phone = $1 AND purpose = $2 AND used_at IS NULL AND expires_at > now()
		ORDER BY created_at DESC LIMIT 1`, phone, purpose,
	).Scan(&o.ID, &o.Phone, &o.Purpose, &o.CodeHash, &o.Attempts, &o.ExpiresAt, &o.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &o, err
}

// CountOTPAttempt records a verification attempt (counted before checking, to stop brute force).
func (s *Store) CountOTPAttempt(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE otp_requests SET attempts = attempts + 1 WHERE id = $1`, id)
	return err
}

// UseOTP marks a code used; it reports false if it was already used (atomic "use once").
func (s *Store) UseOTP(ctx context.Context, id int64) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE otp_requests SET used_at = now() WHERE id = $1 AND used_at IS NULL`, id)
	return tag.RowsAffected() == 1, err
}

// ---- users & sessions -------------------------------------------------------------

// UpsertUser returns the user for phone, creating it on first login, and stamps last_login_at.
func (s *Store) UpsertUser(ctx context.Context, phone string) (*User, error) {
	var u User
	err := s.pool.QueryRow(ctx, `
		INSERT INTO users (phone, last_login_at) VALUES ($1, now())
		ON CONFLICT (phone) DO UPDATE SET last_login_at = now()
		RETURNING id, phone, created_at, last_login_at`, phone,
	).Scan(&u.ID, &u.Phone, &u.CreatedAt, &u.LastLoginAt)
	return &u, err
}

func (s *Store) CreateSession(ctx context.Context, userID, tokenHash string, ttl time.Duration) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO sessions (user_id, token_hash, expires_at) VALUES ($1, $2, now() + $3::interval)`,
		userID, tokenHash, ttl.String())
	return err
}

// UserBySession resolves a session token hash to its user and refreshes last_seen_at.
func (s *Store) UserBySession(ctx context.Context, tokenHash string) (*User, error) {
	var u User
	err := s.pool.QueryRow(ctx, `
		UPDATE sessions SET last_seen_at = now()
		FROM users u
		WHERE sessions.token_hash = $1 AND sessions.expires_at > now() AND u.id = sessions.user_id
		RETURNING u.id, u.phone, u.created_at, u.last_login_at`, tokenHash,
	).Scan(&u.ID, &u.Phone, &u.CreatedAt, &u.LastLoginAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &u, err
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	return err
}

// ---- subscriptions ----------------------------------------------------------------

const subscriptionCols = `id, user_id, plan, amount_kes, source, starts_at, ends_at, created_at`

func scanSubscription(row pgx.Row) (*Subscription, error) {
	var x Subscription
	err := row.Scan(&x.ID, &x.UserID, &x.Plan, &x.AmountKES, &x.Source, &x.StartsAt, &x.EndsAt, &x.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &x, err
}

// ActiveSubscription returns the subscription covering now, if any.
func (s *Store) ActiveSubscription(ctx context.Context, userID string) (*Subscription, error) {
	return scanSubscription(s.pool.QueryRow(ctx, `SELECT `+subscriptionCols+` FROM subscriptions
		WHERE user_id = $1 AND starts_at <= now() AND ends_at > now() ORDER BY ends_at DESC LIMIT 1`, userID))
}

// AddSubscription grants a new period of length d. Renewing early stacks the new
// period after the current one, so already-paid time is never lost.
func (s *Store) AddSubscription(ctx context.Context, userID, plan string, amountKES int, source string, d time.Duration) (*Subscription, error) {
	return scanSubscription(s.pool.QueryRow(ctx, `
		WITH start AS (
			SELECT greatest(now(), coalesce((SELECT max(ends_at) FROM subscriptions WHERE user_id = $1), now())) AS t
		)
		INSERT INTO subscriptions (user_id, plan, amount_kes, source, starts_at, ends_at)
		SELECT $1, $2, $3, $4, t, t + $5::interval FROM start
		RETURNING `+subscriptionCols, userID, plan, amountKES, source, d.String()))
}

// SubscriberRow is one line of the admin "subscribers" list.
type SubscriberRow struct {
	UserID             string     `json:"userId"`
	Phone              string     `json:"phone"`
	JoinedAt           time.Time  `json:"joinedAt"`
	LastLoginAt        *time.Time `json:"lastLoginAt"`
	PeriodStartsAt     time.Time  `json:"periodStartsAt"`
	PeriodEndsAt       time.Time  `json:"periodEndsAt"`
	ActiveNow          bool       `json:"activeNow"`
	TotalSubscriptions int        `json:"totalSubscriptions"`
	BooksInProgress    int        `json:"booksInProgress"`
}

// SubscribersOn lists users whose subscription overlapped the given Nairobi calendar day.
func (s *Store) SubscribersOn(ctx context.Context, day string) ([]SubscriberRow, error) {
	rows, err := s.pool.Query(ctx, `
		WITH bounds AS (
			SELECT ($1::date)::timestamp AT TIME ZONE 'Africa/Nairobi' AS d0,
			       ($1::date + 1)::timestamp AT TIME ZONE 'Africa/Nairobi' AS d1
		), periods AS (
			SELECT DISTINCT ON (x.user_id) x.user_id, x.starts_at, x.ends_at
			FROM subscriptions x, bounds
			WHERE x.starts_at < bounds.d1 AND x.ends_at > bounds.d0
			ORDER BY x.user_id, x.ends_at DESC
		)
		SELECT u.id, u.phone, u.created_at, u.last_login_at, p.starts_at, p.ends_at,
		       EXISTS (SELECT 1 FROM subscriptions a WHERE a.user_id = u.id AND a.starts_at <= now() AND a.ends_at > now()),
		       (SELECT count(*) FROM subscriptions c WHERE c.user_id = u.id)::int,
		       (SELECT count(*) FROM reading_progress r WHERE r.user_id = u.id)::int
		FROM periods p JOIN users u ON u.id = p.user_id
		ORDER BY p.starts_at DESC`, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SubscriberRow{}
	for rows.Next() {
		var r SubscriberRow
		if err := rows.Scan(&r.UserID, &r.Phone, &r.JoinedAt, &r.LastLoginAt, &r.PeriodStartsAt, &r.PeriodEndsAt,
			&r.ActiveNow, &r.TotalSubscriptions, &r.BooksInProgress); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---- reading progress -------------------------------------------------------------

type Progress struct {
	BookSlug  string    `json:"slug"`
	Title     string    `json:"title"`
	Author    string    `json:"author"`
	CFI       string    `json:"cfi"`
	Percent   float64   `json:"percent"`
	Chapter   string    `json:"chapter"`
	UpdatedAt time.Time `json:"updatedAt"`
	CoverVer  string    `json:"-"`
}

const progressSelect = `
SELECT b.slug, b.title, b.author, p.cfi, p.percent::float8, p.chapter, p.updated_at, coalesce(left(f.sha256, 12), '')
FROM reading_progress p
JOIN books b ON b.id = p.book_id
LEFT JOIN files f ON f.id = b.cover_file_id`

func collectProgress(rows pgx.Rows) ([]Progress, error) {
	defer rows.Close()
	out := []Progress{}
	for rows.Next() {
		var p Progress
		if err := rows.Scan(&p.BookSlug, &p.Title, &p.Author, &p.CFI, &p.Percent, &p.Chapter, &p.UpdatedAt, &p.CoverVer); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) ListProgress(ctx context.Context, userID string) ([]Progress, error) {
	rows, err := s.pool.Query(ctx, progressSelect+` WHERE p.user_id = $1 AND b.status = 'published' ORDER BY p.updated_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	return collectProgress(rows)
}

func (s *Store) GetProgress(ctx context.Context, userID, slug string) (*Progress, error) {
	rows, err := s.pool.Query(ctx, progressSelect+` WHERE p.user_id = $1 AND b.slug = $2`, userID, slug)
	if err != nil {
		return nil, err
	}
	ps, err := collectProgress(rows)
	if err != nil {
		return nil, err
	}
	if len(ps) == 0 {
		return nil, ErrNotFound
	}
	return &ps[0], nil
}

// SaveProgress upserts the reader's position; older writes (by client timestamp) never overwrite newer ones.
func (s *Store) SaveProgress(ctx context.Context, userID, slug, cfi string, percent float64, chapter string, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO reading_progress (user_id, book_id, cfi, percent, chapter, updated_at)
		SELECT $1, b.id, $3, $4, $5, $6 FROM books b WHERE b.slug = $2
		ON CONFLICT (user_id, book_id) DO UPDATE
		SET cfi = EXCLUDED.cfi, percent = EXCLUDED.percent, chapter = EXCLUDED.chapter, updated_at = EXCLUDED.updated_at
		WHERE reading_progress.updated_at <= EXCLUDED.updated_at`,
		userID, slug, cfi, percent, chapter, at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// Either the book doesn't exist or a newer position is already stored.
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM books WHERE slug = $1)`, slug).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
	}
	return nil
}

// SubscriptionStats are the subscriber numbers on the admin dashboard.
type SubscriptionStats struct {
	ActiveSubscribers  int `json:"activeSubscribers"`
	SubscriptionsToday int `json:"subscriptionsToday"`
	Users              int `json:"users"`
}

func (s *Store) SubscriptionStats(ctx context.Context) (*SubscriptionStats, error) {
	var st SubscriptionStats
	err := s.pool.QueryRow(ctx, `
		SELECT (SELECT count(DISTINCT user_id) FROM subscriptions WHERE starts_at <= now() AND ends_at > now())::int,
		       (SELECT count(*) FROM subscriptions
		         WHERE (created_at AT TIME ZONE 'Africa/Nairobi')::date = `+nairobiToday+`)::int,
		       (SELECT count(*) FROM users)::int`).Scan(&st.ActiveSubscribers, &st.SubscriptionsToday, &st.Users)
	return &st, err
}
