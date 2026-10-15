package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ai-code-101/readly-api/internal/account"
	"github.com/ai-code-101/readly-api/internal/store"
)

const (
	sessionCookie = "readly_session"
	sessionTTL    = 90 * 24 * time.Hour
	nairobiTZ     = "Africa/Nairobi"
)

var nairobi = func() *time.Location {
	loc, err := time.LoadLocation(nairobiTZ)
	if err != nil {
		return time.FixedZone("EAT", 3*60*60)
	}
	return loc
}()

type plan struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	PriceKES int    `json:"priceKes"`
	Hours    int    `json:"hours"`
	Method   string `json:"method"`
	FreeBook int    `json:"freeBooks"`
}

func (s *Server) plans(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, []plan{{
		ID: "daily_airtime", Name: "Daily", PriceKES: s.subPriceKES, Hours: s.subHours, Method: "airtime", FreeBook: 5,
	}})
}

type meResponse struct {
	User         *store.User         `json:"user"`
	Subscription *store.Subscription `json:"subscription"` // the active one, or null
	Subscribed   bool                `json:"subscribed"`
}

func (s *Server) meFor(ctx context.Context, u *store.User) (meResponse, error) {
	sub, err := s.store.ActiveSubscription(ctx, u.ID)
	if errors.Is(err, store.ErrNotFound) {
		return meResponse{User: u}, nil
	}
	if err != nil {
		return meResponse{}, err
	}
	return meResponse{User: u, Subscription: sub, Subscribed: true}, nil
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if token := sessionToken(r); token != "" {
		_ = s.store.DeleteSession(r.Context(), account.HashToken(token))
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.cookieSecure, SameSite: http.SameSiteLaxMode})
	w.WriteHeader(http.StatusNoContent)
}

// ---- signed-in endpoints -------------------------------------------------------

type ctxKey struct{}

func sessionToken(r *http.Request) string {
	if t, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && t != "" {
		return t
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		return c.Value
	}
	return ""
}

func (s *Server) requireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := sessionToken(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, "not signed in")
			return
		}
		u, err := s.store.UserBySession(r.Context(), account.HashToken(token))
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusUnauthorized, "session expired, please sign in again")
			return
		} else if err != nil {
			s.storeError(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
	})
}

func currentUser(r *http.Request) *store.User { return r.Context().Value(ctxKey{}).(*store.User) }

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	resp, err := s.meFor(r.Context(), currentUser(r))
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, resp)
}

type progressView struct {
	store.Progress
	ThumbURL string `json:"thumbUrl"`
}

func viewProgress(p store.Progress) progressView {
	v := progressView{Progress: p}
	if p.CoverVer != "" {
		v.ThumbURL = "/api/v1/books/" + p.BookSlug + "/cover?size=thumb&v=" + p.CoverVer
	}
	return v
}

func (s *Server) listProgress(w http.ResponseWriter, r *http.Request) {
	ps, err := s.store.ListProgress(r.Context(), currentUser(r).ID)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	out := make([]progressView, len(ps))
	for i, p := range ps {
		out[i] = viewProgress(p)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getProgress(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetProgress(r.Context(), currentUser(r).ID, chi.URLParam(r, "slug"))
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, viewProgress(*p))
}

type progressBody struct {
	CFI       string     `json:"cfi"`
	Percent   float64    `json:"percent"`
	Chapter   string     `json:"chapter"`
	UpdatedAt *time.Time `json:"updatedAt"`
}

func (s *Server) putProgress(w http.ResponseWriter, r *http.Request) {
	var in progressBody
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.CFI == "" || len(in.CFI) > 2000 || in.Percent < 0 || in.Percent > 1 {
		writeError(w, http.StatusBadRequest, "invalid progress")
		return
	}
	at := time.Now()
	if in.UpdatedAt != nil && in.UpdatedAt.Before(at.Add(time.Minute)) {
		at = *in.UpdatedAt
	}
	if len(in.Chapter) > 300 {
		in.Chapter = in.Chapter[:300]
	}
	if err := s.store.SaveProgress(r.Context(), currentUser(r).ID, chi.URLParam(r, "slug"), in.CFI, in.Percent, in.Chapter, at); err != nil {
		s.storeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- admin: subscribers & trending -------------------------------------------------

func today() string { return time.Now().In(nairobi).Format(time.DateOnly) }

func dayParam(r *http.Request) (string, bool) {
	d := r.URL.Query().Get("date")
	if d == "" {
		return today(), true
	}
	_, err := time.Parse(time.DateOnly, d)
	return d, err == nil
}

func (s *Server) adminSubscribers(w http.ResponseWriter, r *http.Request) {
	day, ok := dayParam(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "date must be YYYY-MM-DD")
		return
	}
	rows, err := s.store.SubscribersOn(r.Context(), day)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"date": day, "items": rows})
}

func (s *Server) adminTrending(w http.ResponseWriter, r *http.Request) {
	day, ok := dayParam(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "date must be YYYY-MM-DD")
		return
	}
	books, err := s.store.TrendingOn(r.Context(), day)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"date": day, "today": today(), "items": viewBooks(books, adminViewBook)})
}

// adminSetTrending sets ({"date":"YYYY-MM-DD"}, default today) or clears ({"date":null}) a book's trending day.
func (s *Server) adminSetTrending(w http.ResponseWriter, r *http.Request) {
	id, ok := s.idParam(w, r)
	if !ok {
		return
	}
	var in struct {
		Date *string `json:"date"`
		Off  bool    `json:"off"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	var day *string
	if !in.Off {
		d := today()
		if in.Date != nil && *in.Date != "" {
			if _, err := time.Parse(time.DateOnly, *in.Date); err != nil {
				writeError(w, http.StatusBadRequest, "date must be YYYY-MM-DD")
				return
			}
			d = *in.Date
		}
		day = &d
	}
	b, err := s.store.SetTrending(r.Context(), id, day)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, adminViewBook(*b))
}
