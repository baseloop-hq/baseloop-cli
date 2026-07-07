package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Envelope struct {
	OK      bool            `json:"ok"`
	Data    json.RawMessage `json:"data,omitempty"`
	Summary string          `json:"summary,omitempty"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Hint    string `json:"hint,omitempty"`
	} `json:"error,omitempty"`
	Meta json.RawMessage `json:"meta,omitempty"`
}

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func New(baseURL, token string) Client {
	return Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c Client) Get(path string) (Envelope, int, error) {
	return c.GetContext(context.Background(), path)
}

func (c Client) GetContext(ctx context.Context, path string) (Envelope, int, error) {
	return c.do(ctx, http.MethodGet, path, nil)
}

func (c Client) Post(path string, body any) (Envelope, int, error) {
	return c.PostContext(context.Background(), path, body)
}

func (c Client) PostContext(ctx context.Context, path string, body any) (Envelope, int, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return Envelope{}, 0, err
	}
	return c.do(ctx, http.MethodPost, path, bytes.NewReader(payload))
}

func (c Client) do(ctx context.Context, method, path string, body io.Reader) (Envelope, int, error) {
	if c.BaseURL == "" {
		return Envelope{}, 0, fmt.Errorf("missing API URL")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+"/"+strings.TrimLeft(path, "/"), body)
	if err != nil {
		return Envelope{}, 0, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return Envelope{}, 0, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return Envelope{}, res.StatusCode, err
	}
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return Envelope{}, res.StatusCode, fmt.Errorf("API returned non-JSON response: %s", strings.TrimSpace(string(data)))
	}
	return env, res.StatusCode, nil
}
