package sms

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientSend(t *testing.T) {
	var got payload
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"id": 42}`))
	}))
	defer srv.Close()

	c := &Client{URL: srv.URL, Channel: "SENDERNAME", OrganizationID: "org-1", Token: "tok"}
	id, err := c.Send(context.Background(), "254743410697", "Your code is 123456", "req-1")
	if err != nil {
		t.Fatal(err)
	}
	if id != "42" || auth != "Bearer tok" {
		t.Fatalf("id=%q auth=%q", id, auth)
	}
	want := payload{Channel: "SENDERNAME", Destination: "254743410697", Content: "Your code is 123456", OrganizationID: "org-1", RequestID: "req-1"}
	if got != want {
		t.Fatalf("payload = %+v", got)
	}
}

func TestClientSendError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"Insufficient balance"}`, http.StatusBadRequest)
	}))
	defer srv.Close()
	c := &Client{URL: srv.URL}
	if _, err := c.Send(context.Background(), "254700000000", "x", "r"); err == nil {
		t.Fatal("expected error for 400")
	}
}
