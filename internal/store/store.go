// Package store contains all Postgres access for Readly.
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("already exists")
)

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// querier is satisfied by both *pgxpool.Pool and pgx.Tx.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func (s *Store) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

// ---- files ---------------------------------------------------------------

type File struct {
	ID        string
	Kind      string
	MimeType  string
	Size      int64
	SHA256    string
	Data      []byte
	CreatedAt time.Time
}

// NewFile describes a binary asset to be stored.
type NewFile struct {
	Kind     string
	MimeType string
	Data     []byte
}

func insertFile(ctx context.Context, q querier, f NewFile) (string, error) {
	sum := sha256.Sum256(f.Data)
	var id string
	err := q.QueryRow(ctx,
		`INSERT INTO files (kind, mime_type, size_bytes, sha256, data) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		f.Kind, f.MimeType, len(f.Data), hex.EncodeToString(sum[:]), f.Data,
	).Scan(&id)
	return id, err
}

func deleteFiles(ctx context.Context, q querier, ids ...*string) error {
	for _, id := range ids {
		if id == nil {
			continue
		}
		if _, err := q.Exec(ctx, `DELETE FROM files WHERE id = $1`, *id); err != nil {
			return err
		}
	}
	return nil
}

// GetFile loads a file including its bytes.
func (s *Store) GetFile(ctx context.Context, id string) (*File, error) {
	var f File
	err := s.pool.QueryRow(ctx,
		`SELECT id, kind, mime_type, size_bytes, sha256, data, created_at FROM files WHERE id = $1`, id,
	).Scan(&f.ID, &f.Kind, &f.MimeType, &f.Size, &f.SHA256, &f.Data, &f.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &f, err
}

// ---- helpers --------------------------------------------------------------

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func isInvalidUUID(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "22P02"
}

// notFoundIfMissing maps "no rows" and malformed UUIDs to ErrNotFound.
func notFoundIfMissing(err error) error {
	if errors.Is(err, pgx.ErrNoRows) || isInvalidUUID(err) {
		return ErrNotFound
	}
	return err
}

// uniqueSlug returns base, or base-2, base-3 … whichever is not yet used in table.
func uniqueSlug(ctx context.Context, q querier, table, base, excludeID string) (string, error) {
	rows, err := q.Query(ctx,
		fmt.Sprintf(`SELECT slug FROM %s WHERE (slug = $1 OR slug LIKE $2) AND ($3 = '' OR id::text <> $3)`, table),
		base, escapeLike(base)+"-%", excludeID)
	if err != nil {
		return "", err
	}
	taken, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", err
	}
	used := make(map[string]bool, len(taken))
	for _, t := range taken {
		used[t] = true
	}
	if !used[base] {
		return base, nil
	}
	for i := 2; ; i++ {
		if c := fmt.Sprintf("%s-%d", base, i); !used[c] {
			return c, nil
		}
	}
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}
