// Package sms delivers text messages through the Peak messaging service.
package sms

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Sender delivers one SMS. requestID makes retries idempotent on the provider side.
type Sender interface {
	Send(ctx context.Context, phone, content, requestID string) (messageID string, err error)
}

// Client posts to the messaging service's "send" endpoint, e.g.
// https://messaging-peak-…run.app/api/v1/message/100/user/send
type Client struct {
	URL            string
	Channel        string
	OrganizationID string
	Token          string // optional bearer token
	HTTP           *http.Client
}

type payload struct {
	Channel        string `json:"channel"`
	Destination    string `json:"destination"`
	Content        string `json:"content"`
	OrganizationID string `json:"organization_id"`
	RequestID      string `json:"requestid"`
}

func (c *Client) Send(ctx context.Context, phone, content, requestID string) (string, error) {
	body, err := json.Marshal(payload{
		Channel:        c.Channel,
		Destination:    phone,
		Content:        content,
		OrganizationID: c.OrganizationID,
		RequestID:      requestID,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	res, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("sms request: %w", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return "", fmt.Errorf("sms api returned %d: %s", res.StatusCode, strings.TrimSpace(string(raw)))
	}
	return messageID(raw), nil
}

// messageID pulls an "id" out of the response if there is one, whatever its type.
func messageID(raw []byte) string {
	var v map[string]any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	for _, m := range []map[string]any{v, asMap(v["data"])} {
		if id, ok := m["id"]; ok && id != nil {
			return fmt.Sprint(id)
		}
	}
	return ""
}

func asMap(x any) map[string]any {
	m, _ := x.(map[string]any)
	return m
}

// LogSender is used in development: it writes the message to the log instead of sending it.
type LogSender struct{ Log *slog.Logger }

func (l LogSender) Send(_ context.Context, phone, content, requestID string) (string, error) {
	l.Log.Warn("SMS (log mode, not sent)", "to", phone, "message", content, "requestid", requestID)
	return "log-" + requestID, nil
}
