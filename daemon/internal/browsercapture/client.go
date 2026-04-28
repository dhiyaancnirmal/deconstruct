package browsercapture

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type cdpClient struct {
	conn    *websocket.Conn
	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan map[string]any
	events  chan map[string]any
	done    chan struct{}
	errMu   sync.Mutex
	err     error
}

func dialCDP(ctx context.Context, endpoint string) (*cdpClient, error) {
	wsURL, err := discoverWebSocket(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	conn, _, err := (&websocket.Dialer{HandshakeTimeout: 15 * time.Second}).DialContext(ctx, wsURL, nil)
	if err != nil {
		return nil, err
	}
	client := &cdpClient{
		conn:    conn,
		pending: map[int64]chan map[string]any{},
		events:  make(chan map[string]any, 256),
		done:    make(chan struct{}),
	}
	go client.readLoop()
	return client, nil
}

func discoverWebSocket(ctx context.Context, endpoint string) (string, error) {
	endpoint = strings.TrimRight(endpoint, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/json/version", nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var version struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&version); err != nil {
		return "", err
	}
	if version.WebSocketDebuggerURL == "" {
		return "", fmt.Errorf("CDP endpoint did not expose webSocketDebuggerUrl")
	}
	return version.WebSocketDebuggerURL, nil
}

func (c *cdpClient) Close() error {
	return c.conn.Close()
}

func (c *cdpClient) Events() <-chan map[string]any {
	return c.events
}

func (c *cdpClient) Call(ctx context.Context, method string, params map[string]any) (map[string]any, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan map[string]any, 1)
	c.pending[id] = ch
	err := c.conn.WriteJSON(map[string]any{
		"id":     id,
		"method": method,
		"params": params,
	})
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	case <-c.done:
		if err := c.Err(); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("CDP connection closed")
	case response := <-ch:
		if rawError, ok := response["error"]; ok {
			return nil, fmt.Errorf("CDP error: %v", rawError)
		}
		return response, nil
	}
}

func (c *cdpClient) Err() error {
	c.errMu.Lock()
	defer c.errMu.Unlock()
	return c.err
}

func (c *cdpClient) readLoop() {
	defer close(c.done)
	defer close(c.events)
	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			c.errMu.Lock()
			c.err = err
			c.errMu.Unlock()
			return
		}
		var payload map[string]any
		if err := json.Unmarshal(message, &payload); err != nil {
			continue
		}
		if rawID, ok := payload["id"].(float64); ok {
			id := int64(rawID)
			c.mu.Lock()
			ch := c.pending[id]
			delete(c.pending, id)
			c.mu.Unlock()
			if ch != nil {
				ch <- payload
			}
			continue
		}
		if _, ok := payload["method"].(string); ok {
			select {
			case c.events <- payload:
			default:
			}
		}
	}
}
