package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ai-code-101/readly-api/internal/db"
	"github.com/ai-code-101/readly-api/internal/epub"
	"github.com/ai-code-101/readly-api/internal/httpapi"
	"github.com/ai-code-101/readly-api/internal/store"
)

const token = "test-admin-token"

// newServer connects to TEST_DATABASE_URL, resets the schema and returns a test server.
func newServer(t *testing.T) *httptest.Server {
	return newServerWith(t, httpapi.Options{})
}

func newServerWith(t *testing.T, opt httpapi.Options) *httptest.Server {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	opt.AdminToken, opt.MaxEPUBBytes, opt.MaxImageBytes = token, 10<<20, 5<<20
	api := httpapi.New(store.New(pool), opt)
	srv := httptest.NewServer(api.Routes())
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, srv *httptest.Server, method, path string, body io.Reader, contentType string, admin bool) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if admin {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, b
}

func multipartBody(t *testing.T, fields map[string]string, files map[string][]byte) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	for k, v := range files {
		fw, _ := mw.CreateFormFile(k, k+".bin")
		_, _ = fw.Write(v)
	}
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

func TestBookLifecycle(t *testing.T) {
	srv := newServer(t)

	// Admin endpoints require the token.
	if res, _ := do(t, srv, "GET", "/api/v1/admin/books", nil, "", false); res.StatusCode != 401 {
		t.Fatalf("unauthenticated admin request: %d", res.StatusCode)
	}

	// Create a category.
	res, b := do(t, srv, "POST", "/api/v1/admin/categories",
		strings.NewReader(`{"name":"Mystery","description":"Twists","sortOrder":1}`), "application/json", true)
	if res.StatusCode != 201 {
		t.Fatalf("create category: %d %s", res.StatusCode, b)
	}
	var cat struct{ ID, Slug string }
	_ = json.Unmarshal(b, &cat)
	if cat.Slug != "mystery" {
		t.Fatalf("slug = %q", cat.Slug)
	}

	// Inspect an EPUB.
	sample := epub.BuildSample("The Glass House", "Elena Vance")
	body, ct := multipartBody(t, nil, map[string][]byte{"epub": sample})
	res, b = do(t, srv, "POST", "/api/v1/admin/epub/inspect", body, ct, true)
	if res.StatusCode != 200 || !strings.Contains(string(b), `"title":"The Glass House"`) {
		t.Fatalf("inspect: %d %s", res.StatusCode, b)
	}

	// Rejects a non-EPUB.
	body, ct = multipartBody(t, nil, map[string][]byte{"epub": []byte("nope")})
	if res, _ = do(t, srv, "POST", "/api/v1/admin/books", body, ct, true); res.StatusCode != 400 {
		t.Fatalf("invalid epub accepted: %d", res.StatusCode)
	}

	// Create a draft book: title/author come from the EPUB, cover is extracted.
	meta := fmt.Sprintf(`{"categoryId":%q,"rating":4.9,"genreTag":"Literary Fiction","status":"draft"}`, cat.ID)
	body, ct = multipartBody(t, map[string]string{"metadata": meta}, map[string][]byte{"epub": sample})
	res, b = do(t, srv, "POST", "/api/v1/admin/books", body, ct, true)
	if res.StatusCode != 201 {
		t.Fatalf("create book: %d %s", res.StatusCode, b)
	}
	var book struct {
		ID, Slug, Title, Author, CoverURL, EPUBURL string
		Rating                                     float64
		PageCount                                  int
		Category                                   struct{ Slug string }
	}
	_ = json.Unmarshal(b, &book)
	if book.Title != "The Glass House" || book.Author != "Elena Vance" || book.Rating != 4.9 ||
		book.Category.Slug != "mystery" || book.CoverURL == "" || book.PageCount != 1 {
		t.Fatalf("unexpected book: %s", b)
	}

	// Drafts are hidden from the public API but visible to admins.
	if res, _ = do(t, srv, "GET", "/api/v1/books/"+book.Slug, nil, "", false); res.StatusCode != 404 {
		t.Fatalf("draft visible publicly: %d", res.StatusCode)
	}
	if _, b = do(t, srv, "GET", "/api/v1/admin/books?status=draft", nil, "", true); !strings.Contains(string(b), `"total":1`) {
		t.Fatalf("admin draft filter: %s", b)
	}
	if res, _ = do(t, srv, "GET", "/api/v1/admin/books/"+book.ID+"/epub", nil, "", true); res.StatusCode != 200 {
		t.Fatalf("admin epub preview: %d", res.StatusCode)
	}

	// Publish it.
	update := fmt.Sprintf(`{"title":"The Glass House","author":"Elena Vance","categoryId":%q,"rating":4.8,
		"pageCount":312,"status":"published","isTrending":true,"isBookOfTheDay":true}`, cat.ID)
	res, b = do(t, srv, "PUT", "/api/v1/admin/books/"+book.ID, strings.NewReader(update), "application/json", true)
	if res.StatusCode != 200 {
		t.Fatalf("update: %d %s", res.StatusCode, b)
	}

	// Public listing, filtering and detail.
	res, b = do(t, srv, "GET", "/api/v1/books?category=mystery&trending=true&q=glass", nil, "", false)
	if res.StatusCode != 200 || !strings.Contains(string(b), `"total":1`) {
		t.Fatalf("list: %d %s", res.StatusCode, b)
	}
	res, b = do(t, srv, "GET", "/api/v1/featured/book-of-the-day", nil, "", false)
	if res.StatusCode != 200 || !strings.Contains(string(b), `"pageCount":312`) {
		t.Fatalf("book of the day: %d %s", res.StatusCode, b)
	}
	res, b = do(t, srv, "GET", "/api/v1/categories", nil, "", false)
	if !strings.Contains(string(b), `"bookCount":1`) {
		t.Fatalf("categories: %s", b)
	}

	// The EPUB downloads byte-for-byte and counts as an open.
	res, b = do(t, srv, "GET", "/api/v1/books/"+book.Slug+"/epub", nil, "", false)
	if res.StatusCode != 200 || !bytes.Equal(b, sample) || res.Header.Get("Content-Type") != "application/epub+zip" {
		t.Fatalf("epub download: %d len=%d", res.StatusCode, len(b))
	}
	res, _ = do(t, srv, "GET", "/api/v1/books/"+book.Slug+"/cover?size=thumb", nil, "", false)
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("thumb: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	_, b = do(t, srv, "GET", "/api/v1/books/"+book.Slug, nil, "", false)
	if !strings.Contains(string(b), `"openCount":1`) {
		t.Fatalf("open not recorded: %s", b)
	}

	// A second book with the same title gets a distinct slug.
	body, ct = multipartBody(t, map[string]string{"metadata": `{"status":"published"}`}, map[string][]byte{"epub": sample})
	_, b = do(t, srv, "POST", "/api/v1/admin/books", body, ct, true)
	if !strings.Contains(string(b), `"slug":"the-glass-house-2"`) {
		t.Fatalf("duplicate slug: %s", b)
	}

	// Delete the first book; its files go with it.
	if res, _ = do(t, srv, "DELETE", "/api/v1/admin/books/"+book.ID, nil, "", true); res.StatusCode != 204 {
		t.Fatalf("delete: %d", res.StatusCode)
	}
	if res, _ = do(t, srv, "GET", "/api/v1/books/"+book.Slug+"/epub", nil, "", false); res.StatusCode != 404 {
		t.Fatalf("deleted epub still served: %d", res.StatusCode)
	}
}
