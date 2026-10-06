package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/ai-code-101/readly-api/internal/store"
)

func (s *Server) listCategories(w http.ResponseWriter, r *http.Request) {
	cats, err := s.store.ListCategories(r.Context())
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	out := make([]categoryView, len(cats))
	for i, c := range cats {
		out[i] = viewCategory(c)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getCategory(w http.ResponseWriter, r *http.Request) {
	c, err := s.store.GetCategoryBySlug(r.Context(), chi.URLParam(r, "slug"))
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, viewCategory(*c))
}

func (s *Server) categoryImage(w http.ResponseWriter, r *http.Request) {
	id, err := s.store.CategoryImageFileID(r.Context(), chi.URLParam(r, "slug"))
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	s.serveFile(w, r, id, "category-image", true)
}

func parseBookFilter(r *http.Request) store.BookFilter {
	q := r.URL.Query()
	atoi := func(k string) int { n, _ := strconv.Atoi(q.Get(k)); return n }
	flag := func(k string) bool { b, _ := strconv.ParseBool(q.Get(k)); return b }
	return store.BookFilter{
		CategorySlug: q.Get("category"),
		Query:        q.Get("q"),
		Sort:         q.Get("sort"),
		Trending:     flag("trending"),
		StaffPick:    flag("staffPick"),
		Free:         flag("free"),
		Page:         atoi("page"),
		PageSize:     atoi("pageSize"),
	}
}

func (s *Server) listBooks(w http.ResponseWriter, r *http.Request) {
	f := parseBookFilter(r)
	s.writeBookPage(w, r, f, viewBook)
}

func (s *Server) writeBookPage(w http.ResponseWriter, r *http.Request, f store.BookFilter, view func(store.Book) bookView) {
	books, total, err := s.store.ListBooks(r.Context(), f)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize <= 0 || f.PageSize > 100 {
		f.PageSize = 12
	}
	writeJSON(w, http.StatusOK, page[bookView]{Items: viewBooks(books, view), Total: total, Page: f.Page, PageSize: f.PageSize})
}

func (s *Server) bookOfTheDay(w http.ResponseWriter, r *http.Request) {
	b, err := s.store.BookOfTheDay(r.Context())
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, viewBook(*b))
}

func (s *Server) getBook(w http.ResponseWriter, r *http.Request) {
	b, err := s.store.GetBookBySlug(r.Context(), chi.URLParam(r, "slug"), false)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, viewBook(*b))
}

func (s *Server) relatedBooks(w http.ResponseWriter, r *http.Request) {
	b, err := s.store.GetBookBySlug(r.Context(), chi.URLParam(r, "slug"), false)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 24 {
		limit = 8
	}
	related, err := s.store.RelatedBooks(r.Context(), b, limit)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, viewBooks(related, viewBook))
}

func (s *Server) bookCover(w http.ResponseWriter, r *http.Request) {
	kind := "cover"
	if r.URL.Query().Get("size") == "thumb" {
		kind = "thumb"
	}
	id, err := s.store.BookFileID(r.Context(), chi.URLParam(r, "slug"), kind, false)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	s.serveFile(w, r, id, "cover", r.URL.Query().Get("v") != "")
}

// bookEPUB streams the book file to the reader.
//
// TODO(paywall): when user accounts land, check the reader's entitlement here
// (5 free books on the free plan, unlimited with an active M-Pesa subscription)
// before serving the file.
func (s *Server) bookEPUB(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	id, err := s.store.BookFileID(r.Context(), slug, "epub", false)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	// Count a fresh open, not conditional revalidations or range continuations.
	if r.Header.Get("If-None-Match") == "" && r.Header.Get("Range") == "" {
		if err := s.store.RecordOpen(context.WithoutCancel(r.Context()), slug); err != nil {
			s.log.Warn("record open failed", "slug", slug, "err", err)
		}
	}
	s.serveFile(w, r, id, slug+".epub", false)
}

// serveFile writes a stored file with ETag/Range support. Versioned URLs
// (those carrying ?v=<hash>) are cached for a year.
func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, id, name string, immutable bool) {
	f, err := s.store.GetFile(r.Context(), id)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", f.MimeType)
	h.Set("ETag", `"`+f.SHA256+`"`)
	h.Set("X-Content-Type-Options", "nosniff")
	switch {
	case f.Kind == "epub":
		h.Set("Cache-Control", "private, no-cache")
	case immutable:
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	default:
		h.Set("Cache-Control", "public, max-age=300")
	}
	http.ServeContent(w, r, name, f.CreatedAt, bytes.NewReader(f.Data))
}
