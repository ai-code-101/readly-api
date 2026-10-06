package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ai-code-101/readly-api/internal/slug"
)

type Category struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Slug         string    `json:"slug"`
	Description  string    `json:"description"`
	SortOrder    int       `json:"sortOrder"`
	BookCount    int       `json:"bookCount"`
	ImageVersion string    `json:"-"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type CategoryInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	SortOrder   int    `json:"sortOrder"`
}

const categorySelect = `
SELECT c.id, c.name, c.slug, c.description, c.sort_order,
       (SELECT count(*) FROM books b WHERE b.category_id = c.id AND b.status = 'published')::int,
       coalesce(left(f.sha256, 12), ''), c.created_at, c.updated_at
FROM categories c
LEFT JOIN files f ON f.id = c.image_file_id`

func scanCategory(row pgx.Row) (*Category, error) {
	var c Category
	err := row.Scan(&c.ID, &c.Name, &c.Slug, &c.Description, &c.SortOrder, &c.BookCount,
		&c.ImageVersion, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, notFoundIfMissing(err)
	}
	return &c, nil
}

func (s *Store) ListCategories(ctx context.Context) ([]Category, error) {
	rows, err := s.pool.Query(ctx, categorySelect+` ORDER BY c.sort_order, lower(c.name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Category{}
	for rows.Next() {
		c, err := scanCategory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *Store) GetCategoryBySlug(ctx context.Context, slug string) (*Category, error) {
	return scanCategory(s.pool.QueryRow(ctx, categorySelect+` WHERE c.slug = $1`, slug))
}

func (s *Store) GetCategory(ctx context.Context, id string) (*Category, error) {
	return scanCategory(s.pool.QueryRow(ctx, categorySelect+` WHERE c.id = $1`, id))
}

func (s *Store) CreateCategory(ctx context.Context, in CategoryInput) (*Category, error) {
	var id string
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		sl, err := uniqueSlug(ctx, tx, "categories", slug.Make(in.Name), "")
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx,
			`INSERT INTO categories (name, slug, description, sort_order) VALUES ($1, $2, $3, $4) RETURNING id`,
			in.Name, sl, in.Description, in.SortOrder).Scan(&id)
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrConflict
		}
		return nil, err
	}
	return s.GetCategory(ctx, id)
}

func (s *Store) UpdateCategory(ctx context.Context, id string, in CategoryInput) (*Category, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE categories SET name = $2, description = $3, sort_order = $4, updated_at = now() WHERE id = $1`,
		id, in.Name, in.Description, in.SortOrder)
	if err != nil {
		return nil, notFoundIfMissing(err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.GetCategory(ctx, id)
}

// DeleteCategory removes the category; its books become uncategorised.
func (s *Store) DeleteCategory(ctx context.Context, id string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		var imageID *string
		err := tx.QueryRow(ctx, `DELETE FROM categories WHERE id = $1 RETURNING image_file_id`, id).Scan(&imageID)
		if err != nil {
			return notFoundIfMissing(err)
		}
		return deleteFiles(ctx, tx, imageID)
	})
}

// SetCategoryImage stores a new image for the category, replacing any old one.
func (s *Store) SetCategoryImage(ctx context.Context, id string, img NewFile) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		var old *string
		if err := tx.QueryRow(ctx, `SELECT image_file_id FROM categories WHERE id = $1 FOR UPDATE`, id).Scan(&old); err != nil {
			return notFoundIfMissing(err)
		}
		img.Kind = "category_image"
		newID, err := insertFile(ctx, tx, img)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE categories SET image_file_id = $2, updated_at = now() WHERE id = $1`, id, newID); err != nil {
			return err
		}
		return deleteFiles(ctx, tx, old)
	})
}

// CategoryImageFileID returns the file id of a category's image.
func (s *Store) CategoryImageFileID(ctx context.Context, slug string) (string, error) {
	var id *string
	if err := s.pool.QueryRow(ctx, `SELECT image_file_id FROM categories WHERE slug = $1`, slug).Scan(&id); err != nil {
		return "", notFoundIfMissing(err)
	}
	if id == nil {
		return "", ErrNotFound
	}
	return *id, nil
}
