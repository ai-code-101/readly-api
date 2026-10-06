// Package httpapi exposes the REST API consumed by the reader and admin apps.
package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/ai-code-101/readly-api/internal/store"
)

type Server struct {
	store          *store.Store
	adminToken     string
	allowedOrigins map[string]bool
	maxEPUBBytes   int64
	maxImageBytes  int64
	log            *slog.Logger
}

type Options struct {
	AdminToken     string
	AllowedOrigins []string
	MaxEPUBBytes   int64
	MaxImageBytes  int64
	Logger         *slog.Logger
}

func New(st *store.Store, opt Options) *Server {
	origins := make(map[string]bool, len(opt.AllowedOrigins))
	for _, o := range opt.AllowedOrigins {
		origins[o] = true
	}
	if opt.Logger == nil {
		opt.Logger = slog.Default()
	}
	return &Server{
		store:          st,
		adminToken:     opt.AdminToken,
		allowedOrigins: origins,
		maxEPUBBytes:   opt.MaxEPUBBytes,
		maxImageBytes:  opt.MaxImageBytes,
		log:            opt.Logger,
	}
}

func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, s.requestLogger, middleware.Recoverer, s.cors)

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/categories", s.listCategories)
		r.Get("/categories/{slug}", s.getCategory)
		r.Get("/categories/{slug}/image", s.categoryImage)

		r.Get("/books", s.listBooks)
		r.Get("/featured/book-of-the-day", s.bookOfTheDay)
		r.Get("/books/{slug}", s.getBook)
		r.Get("/books/{slug}/related", s.relatedBooks)
		r.Get("/books/{slug}/cover", s.bookCover)
		r.Get("/books/{slug}/epub", s.bookEPUB)

		r.Route("/admin", func(r chi.Router) {
			r.Use(s.requireAdmin)
			r.Get("/stats", s.adminStats)

			r.Get("/categories", s.listCategories)
			r.Post("/categories", s.adminCreateCategory)
			r.Put("/categories/{id}", s.adminUpdateCategory)
			r.Delete("/categories/{id}", s.adminDeleteCategory)
			r.Put("/categories/{id}/image", s.adminSetCategoryImage)

			r.Post("/epub/inspect", s.adminInspectEPUB)

			r.Get("/books", s.adminListBooks)
			r.Post("/books", s.adminCreateBook)
			r.Get("/books/{id}", s.adminGetBook)
			r.Put("/books/{id}", s.adminUpdateBook)
			r.Delete("/books/{id}", s.adminDeleteBook)
			r.Put("/books/{id}/epub", s.adminReplaceEPUB)
			r.Put("/books/{id}/cover", s.adminReplaceCover)
			r.Get("/books/{id}/cover", s.adminBookCover)
			r.Get("/books/{id}/epub", s.adminBookEPUB)
		})
	})
	return r
}

// ---- middleware -------------------------------------------------------------

func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		s.log.Info("request", "method", r.Method, "path", r.URL.Path, "status", ww.Status(),
			"bytes", ww.BytesWritten(), "duration", time.Since(start).Round(time.Millisecond))
	})
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && s.allowedOrigins[origin] {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Add("Vary", "Origin")
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			h.Set("Access-Control-Expose-Headers", "Content-Length, ETag")
			h.Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireAdmin checks the shared admin bearer token. This is a placeholder
// until admin accounts are added together with user login.
func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(token), []byte(s.adminToken)) != 1 {
			writeError(w, http.StatusUnauthorized, "missing or invalid admin token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---- helpers ----------------------------------------------------------------

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func validUUID(s string) bool { return uuidRE.MatchString(s) }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// storeError writes the HTTP response for a store-layer error.
func (s *Server) storeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "already exists")
	case errors.Is(err, store.ErrInvalidCategory):
		writeError(w, http.StatusBadRequest, "category does not exist")
	default:
		s.log.Error("internal error", "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}
