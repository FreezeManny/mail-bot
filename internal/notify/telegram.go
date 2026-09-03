package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func init() {
	Register("telegram", newTelegramNotifier)
}

type telegramNotifier struct {
	name     string
	botToken string
	chatID   string
	client   *http.Client
}

func newTelegramNotifier(name string, opts map[string]string) (Notifier, error) {
	tokenEnv := opts["bot_token_env"]
	chatEnv := opts["chat_id_env"]
	if tokenEnv == "" || chatEnv == "" {
		return nil, fmt.Errorf("telegram notifier %q: bot_token_env and chat_id_env are both required", name)
	}

	token := os.Getenv(tokenEnv)
	chatID := os.Getenv(chatEnv)
	if token == "" {
		return nil, fmt.Errorf("telegram notifier %q: env var %s is not set", name, tokenEnv)
	}
	if chatID == "" {
		return nil, fmt.Errorf("telegram notifier %q: env var %s is not set", name, chatEnv)
	}

	return &telegramNotifier{
		name:     name,
		botToken: token,
		chatID:   chatID,
		client:   &http.Client{Timeout: 10 * time.Second},
	}, nil
}

func (t *telegramNotifier) Name() string { return t.name }

func (t *telegramNotifier) Notify(ctx context.Context, ev Event) error {
	ts := ev.Time.Format("2006-01-02 15:04")

	var text string
	if ev.Message != "" {
		text = fmt.Sprintf("%s (%s): %s", ev.Account, ts, ev.Message)
	} else {
		text = fmt.Sprintf("%s (%s)\nSorted into: %s\nSubject: %s\nFrom: %s",
			ev.Account, ts, ev.Folder, ev.Subject, ev.Sender)
	}

	body, err := json.Marshal(map[string]string{
		"chat_id": t.chatID,
		"text":    text,
	})
	if err != nil {
		return fmt.Errorf("telegram: encoding request: %w", err)
	}

	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", t.botToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram: building request: %w", t.redact(err))
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: request failed: %w", t.redact(err))
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return t.redact(fmt.Errorf("telegram: unexpected status %d: %s", resp.StatusCode, respBody))
	}
	return nil
}

// redact replaces the bot token in err's message with a placeholder. The
// token is part of the request URL's path, and net/http embeds that URL in
// every transport error (*url.Error), so any error reaching a log has to
// come through here - the caller logs these, and a bot token in the
// container logs is a leaked credential.
func (t *telegramNotifier) redact(err error) error {
	msg := err.Error()
	redacted := strings.ReplaceAll(msg, t.botToken, "<bot-token>")
	if redacted == msg {
		return err
	}
	return errors.New(redacted)
}
