package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/ai-code-101/readly-api/internal/epub"
	"github.com/ai-code-101/readly-api/internal/imaging"
	"github.com/ai-code-101/readly-api/internal/store"
)

const thumbWidth = 400

func (s *Server) adminStats(w http.ResponseWriter, r *http.Request) {
	st, err := s.store.Stats(r.Context())
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	sub, err := s.store.SubscriptionStats(r.Context())
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		*store.Stats
		*store.SubscriptionStats
	}{st, sub})
}

// ---- categories -------------------------------------------------------------

func validateCategory(in *store.CategoryInput) string {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	if in.Name == "" {
		return "name is required"
	}
	return ""
}

func (s *Server) adminCreateCategory(w http.ResponseWriter, r *http.Request) {
	var in store.CategoryInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := validateCategory(&in); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	c, err := s.store.CreateCategory(r.Context(), in)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, viewCategory(*c))
}

func (s *Server) adminUpdateCategory(w http.ResponseWriter, r *http.Request) {
	id, ok := s.idParam(w, r)
	if !ok {
		return
	}
	var in store.CategoryInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := validateCategory(&in); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	c, err := s.store.UpdateCategory(r.Context(), id, in)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, viewCategory(*c))
}

func (s *Server) adminDeleteCategory(w http.ResponseWriter, r *http.Request) {
	id, ok := s.idParam(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteCategory(r.Context(), id); err != nil {
		s.storeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) adminSetCategoryImage(w http.ResponseWriter, r *http.Request) {
	id, ok := s.idParam(w, r)
	if !ok {
		return
	}
	if !s.parseMultipart(w, r, s.maxImageBytes) {
		return
	}
	data, present, err := formFile(r, "image", s.maxImageBytes)
	if err != nil || !present {
		writeError(w, http.StatusBadRequest, errMsg(err, `an "image" file is required`))
		return
	}
	mt, err := imaging.Sniff(data)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.SetCategoryImage(r.Context(), id, store.NewFile{MimeType: mt, Data: data}); err != nil {
		s.storeError(w, r, err)
		return
	}
	c, err := s.store.GetCategory(r.Context(), id)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, viewCategory(*c))
}

// ---- books ------------------------------------------------------------------

func (s *Server) adminListBooks(w http.ResponseWriter, r *http.Request) {
	f := parseBookFilter(r)
	f.IncludeDrafts = true
	f.Status = r.URL.Query().Get("status")
	if f.Sort == "" {
		f.Sort = "updated"
	}
	s.writeBookPage(w, r, f, adminViewBook)
}

func (s *Server) adminGetBook(w http.ResponseWriter, r *http.Request) {
	id, ok := s.idParam(w, r)
	if !ok {
		return
	}
	b, err := s.store.GetBook(r.Context(), id)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, adminViewBook(*b))
}

// adminInspectEPUB validates an EPUB and returns its metadata so the admin
// form can be pre-filled before the book is saved.
func (s *Server) adminInspectEPUB(w http.ResponseWriter, r *http.Request) {
	if !s.parseMultipart(w, r, s.maxEPUBBytes) {
		return
	}
	data, present, err := formFile(r, "epub", s.maxEPUBBytes)
	if err != nil || !present {
		writeError(w, http.StatusBadRequest, errMsg(err, `an "epub" file is required`))
		return
	}
	md, err := epub.Parse(data)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, md)
}

// adminCreateBook accepts multipart/form-data with:
//   - metadata: JSON-encoded BookInput (empty fields are filled from the EPUB)
//   - epub:     the book file (required)
//   - cover:    cover image (optional; falls back to the cover inside the EPUB)
func (s *Server) adminCreateBook(w http.ResponseWriter, r *http.Request) {
	if !s.parseMultipart(w, r, s.maxEPUBBytes+s.maxImageBytes) {
		return
	}
	var in store.BookInput
	if raw := r.FormValue("metadata"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &in); err != nil {
			writeError(w, http.StatusBadRequest, "metadata is not valid JSON: "+err.Error())
			return
		}
	}

	epubData, present, err := formFile(r, "epub", s.maxEPUBBytes)
	if err != nil || !present {
		writeError(w, http.StatusBadRequest, errMsg(err, `an "epub" file is required`))
		return
	}
	md, err := epub.Parse(epubData)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	fillFromEPUB(&in, md)
	if msg := in.Validate(); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if in.CategoryID != nil && !validUUID(*in.CategoryID) {
		writeError(w, http.StatusBadRequest, "category does not exist")
		return
	}

	files := store.BookFiles{
		EPUB:      &store.NewFile{Kind: "epub", MimeType: "application/epub+zip", Data: epubData},
		WordCount: md.WordCount,
	}
	coverData, present, err := formFile(r, "cover", s.maxImageBytes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !present && md.HasCover {
		coverData = md.Cover
	}
	if len(coverData) > 0 {
		if err := addCover(&files, coverData); err != nil {
			// A bad embedded cover shouldn't block the upload; a bad uploaded one should.
			if present {
				writeError(w, http.StatusBadRequest, "cover: "+err.Error())
				return
			}
			s.log.Warn("ignoring unreadable embedded cover", "title", in.Title, "err", err)
		}
	}

	b, err := s.store.CreateBook(r.Context(), in, files)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, adminViewBook(*b))
}

func fillFromEPUB(in *store.BookInput, md *epub.Metadata) {
	if strings.TrimSpace(in.Title) == "" {
		in.Title = md.Title
	}
	if strings.TrimSpace(in.Author) == "" {
		in.Author = md.Author
	}
	if strings.TrimSpace(in.Synopsis) == "" {
		in.Synopsis = md.Description
	}
	if strings.TrimSpace(in.Language) == "" && md.Language != "" {
		in.Language = md.Language
	}
	if in.PageCount == 0 {
		in.PageCount = md.PageCount
	}
}

func addCover(files *store.BookFiles, data []byte) error {
	mt, err := imaging.Sniff(data)
	if err != nil {
		return err
	}
	thumb, err := imaging.Thumbnail(data, thumbWidth)
	if err != nil {
		return err
	}
	files.Cover = &store.NewFile{Kind: "cover", MimeType: mt, Data: data}
	files.CoverThumb = &store.NewFile{Kind: "cover_thumb", MimeType: "image/jpeg", Data: thumb}
	return nil
}

func (s *Server) adminUpdateBook(w http.ResponseWriter, r *http.Request) {
	id, ok := s.idParam(w, r)
	if !ok {
		return
	}
	var in store.BookInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.Validate(); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if in.CategoryID != nil && !validUUID(*in.CategoryID) {
		writeError(w, http.StatusBadRequest, "category does not exist")
		return
	}
	b, err := s.store.UpdateBook(r.Context(), id, in)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, adminViewBook(*b))
}

func (s *Server) adminDeleteBook(w http.ResponseWriter, r *http.Request) {
	id, ok := s.idParam(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteBook(r.Context(), id); err != nil {
		s.storeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) adminReplaceEPUB(w http.ResponseWriter, r *http.Request) {
	id, ok := s.idParam(w, r)
	if !ok {
		return
	}
	if !s.parseMultipart(w, r, s.maxEPUBBytes) {
		return
	}
	data, present, err := formFile(r, "epub", s.maxEPUBBytes)
	if err != nil || !present {
		writeError(w, http.StatusBadRequest, errMsg(err, `an "epub" file is required`))
		return
	}
	md, err := epub.Parse(data)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	b, err := s.store.ReplaceBookFiles(r.Context(), id, store.BookFiles{
		EPUB:      &store.NewFile{Kind: "epub", MimeType: "application/epub+zip", Data: data},
		WordCount: md.WordCount,
	})
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, adminViewBook(*b))
}

func (s *Server) adminReplaceCover(w http.ResponseWriter, r *http.Request) {
	id, ok := s.idParam(w, r)
	if !ok {
		return
	}
	if !s.parseMultipart(w, r, s.maxImageBytes) {
		return
	}
	data, present, err := formFile(r, "cover", s.maxImageBytes)
	if err != nil || !present {
		writeError(w, http.StatusBadRequest, errMsg(err, `a "cover" file is required`))
		return
	}
	var files store.BookFiles
	if err := addCover(&files, data); err != nil {
		writeError(w, http.StatusBadRequest, "cover: "+err.Error())
		return
	}
	b, err := s.store.ReplaceBookFiles(r.Context(), id, files)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, adminViewBook(*b))
}

func (s *Server) adminBookCover(w http.ResponseWriter, r *http.Request) {
	s.adminBookFile(w, r, map[bool]string{true: "thumb", false: "cover"}[r.URL.Query().Get("size") == "thumb"])
}

func (s *Server) adminBookEPUB(w http.ResponseWriter, r *http.Request) { s.adminBookFile(w, r, "epub") }

func (s *Server) adminBookFile(w http.ResponseWriter, r *http.Request, kind string) {
	id, ok := s.idParam(w, r)
	if !ok {
		return
	}
	b, err := s.store.GetBook(r.Context(), id)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	fileID, err := s.store.BookFileID(r.Context(), b.Slug, kind, true)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	s.serveFile(w, r, fileID, b.Slug+map[bool]string{true: ".epub", false: ""}[kind == "epub"], r.URL.Query().Get("v") != "")
}

// ---- request helpers --------------------------------------------------------

func (s *Server) idParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !validUUID(id) {
		writeError(w, http.StatusNotFound, "not found")
		return "", false
	}
	return id, true
}

// parseMultipart caps the body at limit (+1 MiB for form overhead) and parses it.
func (s *Server) parseMultipart(w http.ResponseWriter, r *http.Request, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit+(1<<20))
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("upload too large (max %d MB)", limit>>20))
		} else {
			writeError(w, http.StatusBadRequest, "expected multipart/form-data: "+err.Error())
		}
		return false
	}
	return true
}

// formFile reads an uploaded file field. present is false when the field was omitted.
func formFile(r *http.Request, field string, limit int64) (data []byte, present bool, err error) {
	f, hdr, err := r.FormFile(field)
	if errors.Is(err, http.ErrMissingFile) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	if hdr.Size > limit {
		return nil, true, fmt.Errorf("%s is too large (max %d MB)", field, limit>>20)
	}
	data, err = io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, true, err
	}
	if len(data) == 0 {
		return nil, false, nil
	}
	return data, true, nil
}

func errMsg(err error, fallback string) string {
	if err != nil {
		return err.Error()
	}
	return fallback
}
