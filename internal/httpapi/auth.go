package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ai-code-101/readly-api/internal/otp"
	"github.com/ai-code-101/readly-api/internal/store"
)

const (
	sessionCookie   = "readly_session"
	sessionTTL      = 90 * 24 * time.Hour
	otpTTL          = 5 * time.Minute
	otpResendAfter  = 60 * time.Second
	otpMaxPerHour   = 5  // per phone number
	otpMaxPerIPHour = 20 // per client IP
	otpMaxAttempts  = 5
	nairobiTZ       = "Africa/Nairobi"
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

type otpRequestBody struct {
	Phone   string `json:"phone"`
	Purpose string `json:"purpose"` // "subscribe" (default) or "login"
}

func normalizePurpose(p string) (string, bool) {
	switch p {
	case "", "subscribe":
		return "subscribe", true
	case "login":
		return "login", true
	}
	return "", false
}

// requestOTP generates a code, stores its hash and sends it by SMS.
func (s *Server) requestOTP(w http.ResponseWriter, r *http.Request) {
	var in otpRequestBody
	if !decodeJSON(w, r, &in) {
		return
	}
	purpose, ok := normalizePurpose(in.Purpose)
	if !ok {
		writeError(w, http.StatusBadRequest, `purpose must be "subscribe" or "login"`)
		return
	}
	phone, err := otp.NormalizePhone(in.Phone)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ip := clientIP(r)

	rate, err := s.store.OTPRate(r.Context(), phone, ip)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	if rate.LastSentAt != nil {
		if wait := otpResendAfter - time.Since(*rate.LastSentAt); wait > 0 {
			w.Header().Set("Retry-After", fmt.Sprint(int(wait.Seconds())+1))
			writeError(w, http.StatusTooManyRequests, fmt.Sprintf("Please wait %d seconds before requesting another code.", int(wait.Seconds())+1))
			return
		}
	}
	if rate.SentLastHour >= otpMaxPerHour || rate.SentFromIPLastH >= otpMaxPerIPHour {
		writeError(w, http.StatusTooManyRequests, "Too many codes requested. Please try again later.")
		return
	}

	code, err := otp.NewCode()
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	requestID := uuid.NewString()
	id, err := s.store.CreateOTP(r.Context(), phone, purpose, otp.HashCode(s.otpSecret, phone, purpose, code), requestID, ip, otpTTL)
	if err != nil {
		s.storeError(w, r, err)
		return
	}

	msg := fmt.Sprintf("Your Readly verification code is %s. It expires in %d minutes. Do not share it.", code, int(otpTTL.Minutes()))
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	msgID, err := s.sms.Send(ctx, phone, msg, requestID)
	if err != nil {
		s.log.Error("otp sms failed", "phone", otp.Mask(phone), "requestid", requestID, "err", err)
		_ = s.store.DeleteOTP(context.WithoutCancel(r.Context()), id)
		writeError(w, http.StatusBadGateway, "We couldn't send the code right now. Please try again.")
		return
	}
	if msgID != "" {
		_ = s.store.SetOTPMessageID(r.Context(), id, msgID)
	}
	s.log.Info("otp sent", "phone", otp.Mask(phone), "purpose", purpose, "requestid", requestID)
	writeJSON(w, http.StatusOK, map[string]any{
		"requestId": requestID, "phone": phone, "expiresIn": int(otpTTL.Seconds()), "resendIn": int(otpResendAfter.Seconds()),
	})
}

type otpVerifyBody struct {
	Phone   string `json:"phone"`
	Code    string `json:"code"`
	Purpose string `json:"purpose"`
}

type meResponse struct {
	User         *store.User         `json:"user"`
	Subscription *store.Subscription `json:"subscription"` // the active one, or null
	Subscribed   bool                `json:"subscribed"`
}

// verifyOTP checks the code, signs the reader in and (for purpose "subscribe")
// grants a subscription period. Airtime billing is not wired up yet, so a
// verified phone number is currently all that is needed to subscribe.
func (s *Server) verifyOTP(w http.ResponseWriter, r *http.Request) {
	var in otpVerifyBody
	if !decodeJSON(w, r, &in) {
		return
	}
	purpose, ok := normalizePurpose(in.Purpose)
	if !ok {
		writeError(w, http.StatusBadRequest, `purpose must be "subscribe" or "login"`)
		return
	}
	phone, err := otp.NormalizePhone(in.Phone)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	const invalid = "That code is incorrect or has expired."
	code := strings.TrimSpace(in.Code)

	rec, err := s.store.LatestActiveOTP(r.Context(), phone, purpose)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusBadRequest, invalid)
		return
	} else if err != nil {
		s.storeError(w, r, err)
		return
	}
	if rec.Attempts >= otpMaxAttempts {
		writeError(w, http.StatusTooManyRequests, "Too many wrong attempts. Please request a new code.")
		return
	}
	if err := s.store.CountOTPAttempt(r.Context(), rec.ID); err != nil {
		s.storeError(w, r, err)
		return
	}
	if !otp.Equal(otp.HashCode(s.otpSecret, phone, purpose, code), rec.CodeHash) {
		writeError(w, http.StatusBadRequest, invalid)
		return
	}
	if used, err := s.store.UseOTP(r.Context(), rec.ID); err != nil {
		s.storeError(w, r, err)
		return
	} else if !used {
		writeError(w, http.StatusBadRequest, invalid)
		return
	}

	user, err := s.store.UpsertUser(r.Context(), phone)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	token, hash, err := otp.NewSessionToken()
	if err == nil {
		err = s.store.CreateSession(r.Context(), user.ID, hash, sessionTTL)
	}
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: s.cookieSecure,
		SameSite: http.SameSiteLaxMode, MaxAge: int(sessionTTL.Seconds()),
	})

	if purpose == "subscribe" {
		if _, err := s.store.AddSubscription(r.Context(), user.ID, "daily_airtime", s.subPriceKES, "otp",
			time.Duration(s.subHours)*time.Hour); err != nil {
			s.storeError(w, r, err)
			return
		}
		s.log.Info("subscription granted", "phone", otp.Mask(phone), "hours", s.subHours)
	}
	resp, err := s.meFor(r.Context(), user)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		meResponse
		Token string `json:"token"` // for non-browser clients; browsers use the cookie
	}{resp, token})
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
		_ = s.store.DeleteSession(r.Context(), otp.HashToken(token))
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
		u, err := s.store.UserBySession(r.Context(), otp.HashToken(token))
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

func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
