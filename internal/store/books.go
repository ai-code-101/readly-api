package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ai-code-101/readly-api/internal/slug"
)

type CategoryRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type Book struct {
	ID             string       `json:"id"`
	Title          string       `json:"title"`
	Slug           string       `json:"slug"`
	Author         string       `json:"author"`
	Synopsis       string       `json:"synopsis"`
	Language       string       `json:"language"`
	GenreTag       string       `json:"genreTag"`
	Category       *CategoryRef `json:"category"`
	Rating         float64      `json:"rating"`
	PageCount      int          `json:"pageCount"`
	WordCount      int          `json:"wordCount"`
	IsFree         bool         `json:"isFree"`
	IsTrending     bool         `json:"isTrending"` // trending today (Africa/Nairobi)
	TrendingOn     *string      `json:"trendingOn"` // YYYY-MM-DD the book is (or was last) set to trend
	IsStaffPick    bool         `json:"isStaffPick"`
	IsBookOfTheDay bool         `json:"isBookOfTheDay"`
	Status         string       `json:"status"`
	OpenCount      int64        `json:"openCount"`
	HasEPUB        bool         `json:"hasEpub"`
	EPUBSize       int64        `json:"epubSize"`
	CoverVersion   string       `json:"-"`
	PublishedAt    *time.Time   `json:"publishedAt"`
	CreatedAt      time.Time    `json:"createdAt"`
	UpdatedAt      time.Time    `json:"updatedAt"`
}

// BookInput is the editable metadata of a book.
type BookInput struct {
	Title          string  `json:"title"`
	Author         string  `json:"author"`
	Synopsis       string  `json:"synopsis"`
	Language       string  `json:"language"`
	GenreTag       string  `json:"genreTag"`
	CategoryID     *string `json:"categoryId"`
	Rating         float64 `json:"rating"`
	PageCount      int     `json:"pageCount"`
	IsFree         bool    `json:"isFree"`
	IsTrending     bool    `json:"isTrending"`
	IsStaffPick    bool    `json:"isStaffPick"`
	IsBookOfTheDay bool    `json:"isBookOfTheDay"`
	Status         string  `json:"status"`
}

// Validate normalises the input and returns a user-facing error message, or "".
func (in *BookInput) Validate() string {
	in.Title = strings.TrimSpace(in.Title)
	in.Author = strings.TrimSpace(in.Author)
	in.GenreTag = strings.TrimSpace(in.GenreTag)
	in.Language = strings.TrimSpace(in.Language)
	if in.Language == "" {
		in.Language = "en"
	}
	if in.Status == "" {
		in.Status = "draft"
	}
	if in.CategoryID != nil && strings.TrimSpace(*in.CategoryID) == "" {
		in.CategoryID = nil
	}
	switch {
	case in.Title == "":
		return "title is required"
	case in.Rating < 0 || in.Rating > 5:
		return "rating must be between 0 and 5"
	case in.PageCount < 0:
		return "page count cannot be negative"
	case in.Status != "draft" && in.Status != "published":
		return `status must be "draft" or "published"`
	}
	return ""
}

// nairobiToday is today's date in Kenya, used for the per-day "trending" flag.
const nairobiToday = `(now() AT TIME ZONE 'Africa/Nairobi')::date`

const bookSelect = `
SELECT b.id, b.title, b.slug, b.author, b.synopsis, b.language, b.genre_tag,
       c.id, c.name, c.slug,
       b.rating::float8, b.page_count, b.word_count,
       b.is_free, coalesce(b.trending_on = ` + nairobiToday + `, false), to_char(b.trending_on, 'YYYY-MM-DD'),
       b.is_staff_pick, b.is_book_of_the_day,
       b.status, b.open_count,
       b.epub_file_id IS NOT NULL, coalesce(ef.size_bytes, 0),
       coalesce(left(cf.sha256, 12), ''),
       b.published_at, b.created_at, b.updated_at
FROM books b
LEFT JOIN categories c ON c.id = b.category_id
LEFT JOIN files ef ON ef.id = b.epub_file_id
LEFT JOIN files cf ON cf.id = b.cover_file_id`

func scanBook(row pgx.Row) (*Book, error) {
	var b Book
	var catID, catName, catSlug *string
	err := row.Scan(&b.ID, &b.Title, &b.Slug, &b.Author, &b.Synopsis, &b.Language, &b.GenreTag,
		&catID, &catName, &catSlug,
		&b.Rating, &b.PageCount, &b.WordCount,
		&b.IsFree, &b.IsTrending, &b.TrendingOn, &b.IsStaffPick, &b.IsBookOfTheDay,
		&b.Status, &b.OpenCount,
		&b.HasEPUB, &b.EPUBSize, &b.CoverVersion,
		&b.PublishedAt, &b.CreatedAt, &b.UpdatedAt)
	if err != nil {
		return nil, notFoundIfMissing(err)
	}
	if catID != nil {
		b.Category = &CategoryRef{ID: *catID, Name: *catName, Slug: *catSlug}
	}
	return &b, nil
}

func collectBooks(rows pgx.Rows) ([]Book, error) {
	defer rows.Close()
	out := []Book{}
	for rows.Next() {
		b, err := scanBook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

type BookFilter struct {
	CategorySlug  string
	Query         string
	Sort          string // popular | new | rating | title
	Trending      bool
	StaffPick     bool
	Free          bool
	IncludeDrafts bool
	Status        string // optional exact status filter (admin only)
	Page          int
	PageSize      int
}

var sortClauses = map[string]string{
	"popular": "b.open_count DESC, b.rating DESC",
	"new":     "b.published_at DESC NULLS LAST, b.created_at DESC",
	"rating":  "b.rating DESC, b.open_count DESC",
	"title":   "lower(b.title) ASC",
	"updated": "b.updated_at DESC",
}

// ListBooks returns one page of books matching f, plus the total match count.
func (s *Store) ListBooks(ctx context.Context, f BookFilter) ([]Book, int, error) {
	var where []string
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if !f.IncludeDrafts {
		where = append(where, "b.status = 'published'")
	}
	if f.Status == "draft" || f.Status == "published" {
		where = append(where, "b.status = "+arg(f.Status))
	}
	if f.CategorySlug != "" {
		where = append(where, "c.slug = "+arg(f.CategorySlug))
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		p := arg("%" + escapeLike(q) + "%")
		where = append(where, fmt.Sprintf("(b.title ILIKE %[1]s OR b.author ILIKE %[1]s OR b.genre_tag ILIKE %[1]s OR c.name ILIKE %[1]s)", p))
	}
	if f.Trending {
		where = append(where, "b.trending_on = "+nairobiToday)
	}
	if f.StaffPick {
		where = append(where, "b.is_staff_pick")
	}
	if f.Free {
		where = append(where, "b.is_free")
	}
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = " WHERE " + strings.Join(where, " AND ")
	}

	var total int
	countSQL := `SELECT count(*) FROM books b LEFT JOIN categories c ON c.id = b.category_id` + whereSQL
	if err := s.pool.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	order, ok := sortClauses[f.Sort]
	if !ok {
		order = sortClauses["popular"]
	}
	if f.PageSize <= 0 || f.PageSize > 100 {
		f.PageSize = 12
	}
	if f.Page < 1 {
		f.Page = 1
	}
	limit, offset := arg(f.PageSize), arg((f.Page-1)*f.PageSize)
	rows, err := s.pool.Query(ctx,
		bookSelect+whereSQL+" ORDER BY "+order+", b.id LIMIT "+limit+" OFFSET "+offset, args...)
	if err != nil {
		return nil, 0, err
	}
	books, err := collectBooks(rows)
	return books, total, err
}

func (s *Store) GetBookBySlug(ctx context.Context, slug string, includeDrafts bool) (*Book, error) {
	q := bookSelect + ` WHERE b.slug = $1`
	if !includeDrafts {
		q += ` AND b.status = 'published'`
	}
	return scanBook(s.pool.QueryRow(ctx, q, slug))
}

func (s *Store) GetBook(ctx context.Context, id string) (*Book, error) {
	return scanBook(s.pool.QueryRow(ctx, bookSelect+` WHERE b.id = $1`, id))
}

// RelatedBooks returns other published books, preferring the same category.
func (s *Store) RelatedBooks(ctx context.Context, b *Book, limit int) ([]Book, error) {
	var catID *string
	if b.Category != nil {
		catID = &b.Category.ID
	}
	rows, err := s.pool.Query(ctx, bookSelect+`
		WHERE b.status = 'published' AND b.id <> $1
		ORDER BY (b.category_id IS NOT DISTINCT FROM $2) DESC, b.open_count DESC, b.rating DESC, b.id
		LIMIT $3`, b.ID, catID, limit)
	if err != nil {
		return nil, err
	}
	return collectBooks(rows)
}

func (s *Store) BookOfTheDay(ctx context.Context) (*Book, error) {
	return scanBook(s.pool.QueryRow(ctx,
		bookSelect+` WHERE b.status = 'published' AND b.is_book_of_the_day LIMIT 1`))
}

// BookFiles are the binary assets supplied when creating or updating a book.
type BookFiles struct {
	EPUB       *NewFile
	Cover      *NewFile
	CoverThumb *NewFile
	WordCount  int
}

func applyFlags(ctx context.Context, tx pgx.Tx, id string, in BookInput) error {
	if in.IsBookOfTheDay {
		if _, err := tx.Exec(ctx, `UPDATE books SET is_book_of_the_day = false WHERE is_book_of_the_day AND id <> $1`, id); err != nil {
			return err
		}
	}
	return nil
}

// CreateBook inserts a book with its EPUB and (optional) cover in one transaction.
func (s *Store) CreateBook(ctx context.Context, in BookInput, files BookFiles) (*Book, error) {
	var id string
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		sl, err := uniqueSlug(ctx, tx, "books", slug.Make(in.Title), "")
		if err != nil {
			return err
		}
		ids, err := insertBookFiles(ctx, tx, files)
		if err != nil {
			return err
		}
		if in.IsBookOfTheDay {
			if _, err := tx.Exec(ctx, `UPDATE books SET is_book_of_the_day = false WHERE is_book_of_the_day`); err != nil {
				return err
			}
		}
		return tx.QueryRow(ctx, `
			INSERT INTO books (title, slug, author, synopsis, language, genre_tag, category_id, rating,
			                   page_count, word_count, is_free, trending_on, is_staff_pick, is_book_of_the_day,
			                   status, published_at, epub_file_id, cover_file_id, cover_thumb_file_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, CASE WHEN $12::bool THEN `+nairobiToday+` END, $13, $14, $15,
			        CASE WHEN $15 = 'published' THEN now() END, $16, $17, $18)
			RETURNING id`,
			in.Title, sl, in.Author, in.Synopsis, in.Language, in.GenreTag, in.CategoryID, in.Rating,
			in.PageCount, files.WordCount, in.IsFree, in.IsTrending, in.IsStaffPick, in.IsBookOfTheDay,
			in.Status, ids[0], ids[1], ids[2],
		).Scan(&id)
	})
	if err != nil {
		return nil, mapWriteErr(err)
	}
	return s.GetBook(ctx, id)
}

// UpdateBook replaces the book's metadata. The slug is kept stable so links don't break.
func (s *Store) UpdateBook(ctx context.Context, id string, in BookInput) (*Book, error) {
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if err := applyFlags(ctx, tx, id, in); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE books SET title = $2, author = $3, synopsis = $4, language = $5, genre_tag = $6,
			       category_id = $7, rating = $8, page_count = $9, is_free = $10,
			       trending_on = CASE WHEN $11::bool THEN `+nairobiToday+`
			                          WHEN trending_on > `+nairobiToday+` THEN trending_on END,
			       is_staff_pick = $12, is_book_of_the_day = $13, status = $14,
			       published_at = CASE WHEN $14 = 'published' THEN coalesce(published_at, now()) END,
			       updated_at = now()
			WHERE id = $1`,
			id, in.Title, in.Author, in.Synopsis, in.Language, in.GenreTag, in.CategoryID, in.Rating,
			in.PageCount, in.IsFree, in.IsTrending, in.IsStaffPick, in.IsBookOfTheDay, in.Status)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return nil, mapWriteErr(err)
	}
	return s.GetBook(ctx, id)
}

// ReplaceBookFiles swaps the EPUB and/or cover of an existing book and removes the old files.
func (s *Store) ReplaceBookFiles(ctx context.Context, id string, files BookFiles) (*Book, error) {
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var oldEPUB, oldCover, oldThumb *string
		err := tx.QueryRow(ctx,
			`SELECT epub_file_id, cover_file_id, cover_thumb_file_id FROM books WHERE id = $1 FOR UPDATE`, id,
		).Scan(&oldEPUB, &oldCover, &oldThumb)
		if err != nil {
			return notFoundIfMissing(err)
		}
		ids, err := insertBookFiles(ctx, tx, files)
		if err != nil {
			return err
		}
		var stale []*string
		if files.EPUB != nil {
			if _, err := tx.Exec(ctx,
				`UPDATE books SET epub_file_id = $2, word_count = $3, updated_at = now() WHERE id = $1`,
				id, ids[0], files.WordCount); err != nil {
				return err
			}
			stale = append(stale, oldEPUB)
		}
		if files.Cover != nil {
			if _, err := tx.Exec(ctx,
				`UPDATE books SET cover_file_id = $2, cover_thumb_file_id = $3, updated_at = now() WHERE id = $1`,
				id, ids[1], ids[2]); err != nil {
				return err
			}
			stale = append(stale, oldCover, oldThumb)
		}
		return deleteFiles(ctx, tx, stale...)
	})
	if err != nil {
		return nil, err
	}
	return s.GetBook(ctx, id)
}

func insertBookFiles(ctx context.Context, tx pgx.Tx, files BookFiles) ([3]*string, error) {
	var ids [3]*string
	for i, f := range []*NewFile{files.EPUB, files.Cover, files.CoverThumb} {
		if f == nil {
			continue
		}
		id, err := insertFile(ctx, tx, *f)
		if err != nil {
			return ids, err
		}
		ids[i] = &id
	}
	return ids, nil
}

func (s *Store) DeleteBook(ctx context.Context, id string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		var e, c, t *string
		err := tx.QueryRow(ctx,
			`DELETE FROM books WHERE id = $1 RETURNING epub_file_id, cover_file_id, cover_thumb_file_id`, id,
		).Scan(&e, &c, &t)
		if err != nil {
			return notFoundIfMissing(err)
		}
		return deleteFiles(ctx, tx, e, c, t)
	})
}

// BookFileID returns the id of a published book's asset: kind is "epub", "cover" or "thumb".
// When includeDrafts is false, drafts are treated as missing.
func (s *Store) BookFileID(ctx context.Context, slug, kind string, includeDrafts bool) (string, error) {
	col := map[string]string{"epub": "epub_file_id", "cover": "cover_file_id", "thumb": "coalesce(cover_thumb_file_id, cover_file_id)"}[kind]
	if col == "" {
		return "", ErrNotFound
	}
	q := `SELECT ` + col + ` FROM books WHERE slug = $1`
	if !includeDrafts {
		q += ` AND status = 'published'`
	}
	var id *string
	if err := s.pool.QueryRow(ctx, q, slug).Scan(&id); err != nil {
		return "", notFoundIfMissing(err)
	}
	if id == nil {
		return "", ErrNotFound
	}
	return *id, nil
}

// SetTrending marks a book as trending on the given day (YYYY-MM-DD), or clears it when day is nil.
func (s *Store) SetTrending(ctx context.Context, id string, day *string) (*Book, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE books SET trending_on = $2::date, updated_at = now() WHERE id = $1`, id, day)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.GetBook(ctx, id)
}

// TrendingOn lists books set to trend on the given day (YYYY-MM-DD), including drafts.
func (s *Store) TrendingOn(ctx context.Context, day string) ([]Book, error) {
	rows, err := s.pool.Query(ctx, bookSelect+` WHERE b.trending_on = $1::date ORDER BY lower(b.title)`, day)
	if err != nil {
		return nil, err
	}
	return collectBooks(rows)
}

// RecordOpen bumps the popularity counter used by the "Most Popular" sort.
func (s *Store) RecordOpen(ctx context.Context, slug string) error {
	_, err := s.pool.Exec(ctx, `UPDATE books SET open_count = open_count + 1 WHERE slug = $1`, slug)
	return err
}

type Stats struct {
	Books          int   `json:"books"`
	PublishedBooks int   `json:"publishedBooks"`
	DraftBooks     int   `json:"draftBooks"`
	FreeBooks      int   `json:"freeBooks"`
	Categories     int   `json:"categories"`
	TotalOpens     int64 `json:"totalOpens"`
	StorageBytes   int64 `json:"storageBytes"`
}

func (s *Store) Stats(ctx context.Context) (*Stats, error) {
	var st Stats
	err := s.pool.QueryRow(ctx, `
		SELECT count(*)::int,
		       count(*) FILTER (WHERE status = 'published')::int,
		       count(*) FILTER (WHERE status = 'draft')::int,
		       count(*) FILTER (WHERE is_free)::int,
		       (SELECT count(*) FROM categories)::int,
		       coalesce(sum(open_count), 0)::bigint,
		       (SELECT coalesce(sum(size_bytes), 0) FROM files)::bigint
		FROM books`).Scan(&st.Books, &st.PublishedBooks, &st.DraftBooks, &st.FreeBooks,
		&st.Categories, &st.TotalOpens, &st.StorageBytes)
	return &st, err
}

// ErrInvalidCategory is returned when a book references a category that doesn't exist.
var ErrInvalidCategory = fmt.Errorf("category does not exist")

func mapWriteErr(err error) error {
	if isForeignKeyViolation(err) {
		return ErrInvalidCategory
	}
	return err
}
