package httpapi_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/ai-code-101/readly-api/internal/epub"
	"github.com/ai-code-101/readly-api/internal/httpapi"
)

// fakeSMS records messages instead of sending them.
type fakeSMS struct {
	mu   sync.Mutex
	msgs map[string]string
}

func (f *fakeSMS) Send(_ context.Context, phone, content, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs[phone] = content
	return "1", nil
}

func (f *fakeSMS) code(t *testing.T, phone string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := regexp.MustCompile(`\b(\d{6})\b`).FindStringSubmatch(f.msgs[phone])
	if m == nil {
		t.Fatalf("no code sent to %s (messages: %v)", phone, f.msgs)
	}
	return m[1]
}

// client keeps cookies, like a browser.
func client(t *testing.T) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

func call(t *testing.T, c *http.Client, srv *httptest.Server, method, path, body string) (int, string) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, srv.URL+path, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if strings.HasPrefix(path, "/api/v1/admin") {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestSubscribeWithOTP(t *testing.T) {
	smsFake := &fakeSMS{msgs: map[string]string{}}
	srv := newServerWith(t, httpapi.Options{SMS: smsFake})
	c := client(t)

	// Not signed in yet.
	if code, _ := call(t, c, srv, "GET", "/api/v1/me", ""); code != 401 {
		t.Fatalf("/me before login: %d", code)
	}
	// Invalid numbers are rejected.
	if code, _ := call(t, c, srv, "POST", "/api/v1/auth/otp/request", `{"phone":"12345"}`); code != 400 {
		t.Fatalf("bad phone: %d", code)
	}
	// Request a code; 0743… is normalised to 254743….
	code, body := call(t, c, srv, "POST", "/api/v1/auth/otp/request", `{"phone":"0743 410 697","purpose":"subscribe"}`)
	if code != 200 || !strings.Contains(body, `"phone":"254743410697"`) {
		t.Fatalf("request otp: %d %s", code, body)
	}
	if strings.Contains(body, smsFake.code(t, "254743410697")) {
		t.Fatal("the code must never be returned by the API")
	}
	// Resend cooldown.
	if code, _ := call(t, c, srv, "POST", "/api/v1/auth/otp/request", `{"phone":"254743410697"}`); code != 429 {
		t.Fatalf("cooldown: %d", code)
	}
	// A wrong code fails without signing in.
	if code, _ := call(t, c, srv, "POST", "/api/v1/auth/otp/verify", `{"phone":"0743410697","code":"000000"}`); code != 400 {
		t.Fatalf("wrong code: %d", code)
	}
	// The right code subscribes and signs in.
	otpCode := smsFake.code(t, "254743410697")
	code, body = call(t, c, srv, "POST", "/api/v1/auth/otp/verify", fmt.Sprintf(`{"phone":"0743410697","code":%q}`, otpCode))
	if code != 200 || !strings.Contains(body, `"subscribed":true`) || !strings.Contains(body, `"amountKes":10`) {
		t.Fatalf("verify: %d %s", code, body)
	}
	// The code is single-use.
	if code, _ := call(t, c, srv, "POST", "/api/v1/auth/otp/verify", fmt.Sprintf(`{"phone":"0743410697","code":%q}`, otpCode)); code != 400 {
		t.Fatalf("reused code: %d", code)
	}
	// The session cookie now identifies the reader.
	code, body = call(t, c, srv, "GET", "/api/v1/me", "")
	if code != 200 || !strings.Contains(body, `"phone":"254743410697"`) || !strings.Contains(body, `"subscribed":true`) {
		t.Fatalf("/me: %d %s", code, body)
	}

	// Reading progress is stored per reader.
	sample := epub.BuildSample("Synced Book", "A. Writer")
	mb, ct := multipartBody(t, map[string]string{"metadata": `{"status":"published"}`}, map[string][]byte{"epub": sample})
	if res, b := do(t, srv, "POST", "/api/v1/admin/books", mb, ct, true); res.StatusCode != 201 {
		t.Fatalf("create book: %d %s", res.StatusCode, b)
	}
	if code, body := call(t, c, srv, "PUT", "/api/v1/me/progress/synced-book", `{"cfi":"epubcfi(/6/4!/4/2)","percent":0.25,"chapter":"Chapter 1"}`); code != 204 {
		t.Fatalf("put progress: %d %s", code, body)
	}
	code, body = call(t, c, srv, "GET", "/api/v1/me/progress", "")
	if code != 200 || !strings.Contains(body, `"percent":0.25`) || !strings.Contains(body, `"slug":"synced-book"`) {
		t.Fatalf("list progress: %d %s", code, body)
	}
	if code, _ := call(t, c, srv, "PUT", "/api/v1/me/progress/no-such-book", `{"cfi":"x","percent":0.1}`); code != 404 {
		t.Fatalf("progress for missing book: %d", code)
	}

	// The admin sees today's subscriber and the counts.
	code, body = call(t, c, srv, "GET", "/api/v1/admin/subscribers", "")
	if code != 200 || !strings.Contains(body, `"phone":"254743410697"`) || !strings.Contains(body, `"activeNow":true`) {
		t.Fatalf("admin subscribers: %d %s", code, body)
	}
	code, body = call(t, c, srv, "GET", "/api/v1/admin/stats", "")
	if code != 200 || !strings.Contains(body, `"activeSubscribers":1`) || !strings.Contains(body, `"books":1`) {
		t.Fatalf("admin stats: %d %s", code, body)
	}

	// Logging out ends the session.
	if code, _ := call(t, c, srv, "POST", "/api/v1/auth/logout", ""); code != 204 {
		t.Fatalf("logout: %d", code)
	}
	if code, _ := call(t, c, srv, "GET", "/api/v1/me", ""); code != 401 {
		t.Fatalf("/me after logout: %d", code)
	}
}

func TestDailyTrending(t *testing.T) {
	srv := newServer(t)
	c := client(t)
	mb, ct := multipartBody(t, map[string]string{"metadata": `{"status":"published"}`}, map[string][]byte{"epub": epub.BuildSample("Hot Book", "X")})
	res, b := do(t, srv, "POST", "/api/v1/admin/books", mb, ct, true)
	if res.StatusCode != 201 {
		t.Fatalf("create: %s", b)
	}
	id := regexp.MustCompile(`"id":"([^"]+)"`).FindStringSubmatch(string(b))[1]

	// Trending today -> appears in the public trending list.
	if code, body := call(t, c, srv, "PUT", "/api/v1/admin/books/"+id+"/trending", `{}`); code != 200 || !strings.Contains(body, `"isTrending":true`) {
		t.Fatalf("set trending: %d %s", code, body)
	}
	if _, body := call(t, c, srv, "GET", "/api/v1/books?trending=true", ""); !strings.Contains(body, `"total":1`) {
		t.Fatalf("trending list: %s", body)
	}
	// Trending on another day -> not trending today.
	if code, body := call(t, c, srv, "PUT", "/api/v1/admin/books/"+id+"/trending", `{"date":"2020-01-01"}`); code != 200 || !strings.Contains(body, `"isTrending":false`) {
		t.Fatalf("past trending: %d %s", code, body)
	}
	if _, body := call(t, c, srv, "GET", "/api/v1/books?trending=true", ""); !strings.Contains(body, `"total":0`) {
		t.Fatalf("expired trending still listed: %s", body)
	}
	if _, body := call(t, c, srv, "GET", "/api/v1/admin/trending?date=2020-01-01", ""); !strings.Contains(body, `"title":"Hot Book"`) {
		t.Fatalf("admin trending by date: %s", body)
	}
	// Remove.
	if code, body := call(t, c, srv, "PUT", "/api/v1/admin/books/"+id+"/trending", `{"off":true}`); code != 200 || !strings.Contains(body, `"trendingOn":null`) {
		t.Fatalf("remove trending: %d %s", code, body)
	}
}
