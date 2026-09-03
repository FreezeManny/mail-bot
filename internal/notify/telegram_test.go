package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const testBotToken = "123456:SECRET-TOKEN-VALUE"

func testNotifier(rt http.RoundTripper) *telegramNotifier {
	return &telegramNotifier{
		name:     "telegram-main",
		botToken: testBotToken,
		chatID:   "chat123",
		client:   &http.Client{Transport: rt},
	}
}

func sortedEvent() Event {
	return Event{
		Account: "gmx",
		Subject: "Your statement",
		Sender:  "service@paypal.de",
		Folder:  "Sort/PayPal",
		Time:    time.Date(2026, 9, 2, 14, 5, 0, 0, time.UTC),
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("simulated dial failure")
}

type stubTransport struct {
	status int
	body   string
	seen   *http.Request
	sent   []byte
}

func (s *stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	s.seen = req
	if req.Body != nil {
		s.sent, _ = io.ReadAll(req.Body)
	}
	return &http.Response{
		StatusCode: s.status,
		Body:       io.NopCloser(strings.NewReader(s.body)),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

// The bot token sits in the request URL's path, and net/http embeds that
// URL in every transport error. The caller logs whatever Notify returns,
// so a raw error here would put a live credential into the container logs.
func TestNotifyDoesNotLeakBotTokenOnTransportError(t *testing.T) {
	err := testNotifier(failingTransport{}).Notify(context.Background(), sortedEvent())
	if err == nil {
		t.Fatal("Notify() error = nil, want a transport error")
	}
	if strings.Contains(err.Error(), testBotToken) {
		t.Errorf("Notify() error leaks the bot token: %v", err)
	}
	if !strings.Contains(err.Error(), "<bot-token>") {
		t.Errorf("Notify() error = %v, want the token replaced by <bot-token>", err)
	}
}

// A proxy or gateway error page can echo the request URL back in the body.
func TestNotifyDoesNotLeakBotTokenFromResponseBody(t *testing.T) {
	body := "Bad Gateway while calling /bot" + testBotToken + "/sendMessage"
	err := testNotifier(&stubTransport{status: 502, body: body}).Notify(context.Background(), sortedEvent())
	if err == nil {
		t.Fatal("Notify() error = nil, want an error for status 502")
	}
	if strings.Contains(err.Error(), testBotToken) {
		t.Errorf("Notify() error leaks the bot token: %v", err)
	}
}

func TestNotifyMessageNamesTheDestinationFolder(t *testing.T) {
	rt := &stubTransport{status: 200, body: `{"ok":true}`}
	if err := testNotifier(rt).Notify(context.Background(), sortedEvent()); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}

	var payload struct {
		ChatID string `json:"chat_id"`
		Text   string `json:"text"`
	}
	if err := json.Unmarshal(rt.sent, &payload); err != nil {
		t.Fatalf("request body is not the expected JSON: %v", err)
	}
	if payload.ChatID != "chat123" {
		t.Errorf("chat_id = %q, want chat123", payload.ChatID)
	}
	// Which folder the mail was sorted into is the point of the message.
	for _, want := range []string{"gmx", "2026-09-02 14:05", "Sort/PayPal", "Your statement", "service@paypal.de"} {
		if !strings.Contains(payload.Text, want) {
			t.Errorf("text %q is missing %q", payload.Text, want)
		}
	}
}

func TestNotifyStatusMessage(t *testing.T) {
	rt := &stubTransport{status: 200, body: `{"ok":true}`}
	ev := Event{Account: "gmx", Message: "connection restored", Time: sortedEvent().Time}
	if err := testNotifier(rt).Notify(context.Background(), ev); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}
	var payload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(rt.sent, &payload); err != nil {
		t.Fatal(err)
	}
	if want := "gmx (2026-09-02 14:05): connection restored"; payload.Text != want {
		t.Errorf("text = %q, want %q", payload.Text, want)
	}
}

func TestBuildRejectsDuplicateNames(t *testing.T) {
	t.Setenv("TEST_TOKEN", "t")
	t.Setenv("TEST_CHAT", "c")
	cfgs := []RawConfig{
		{Name: "a", Type: "telegram", Options: map[string]string{"bot_token_env": "TEST_TOKEN", "chat_id_env": "TEST_CHAT"}},
		{Name: "a", Type: "telegram", Options: map[string]string{"bot_token_env": "TEST_TOKEN", "chat_id_env": "TEST_CHAT"}},
	}
	if _, err := Build(cfgs); err == nil {
		t.Fatal("Build() error = nil, want error for duplicate notifier name")
	}
}
