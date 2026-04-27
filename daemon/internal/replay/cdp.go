package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type cdpConn struct {
	conn *websocket.Conn
	mu   sync.Mutex
	next int64
}

func dialWebSocket(ctx context.Context, wsURL string) (*cdpConn, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return nil, err
	}
	return &cdpConn{conn: conn}, nil
}

func (c *cdpConn) Close() error {
	return c.conn.Close()
}

func (c *cdpConn) Call(ctx context.Context, method string, params map[string]any) (map[string]any, error) {
	c.mu.Lock()
	c.next++
	id := c.next
	request := map[string]any{
		"id":     id,
		"method": method,
		"params": params,
	}
	if err := c.conn.WriteJSON(request); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	c.mu.Unlock()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			return nil, err
		}
		var response map[string]any
		if err := json.Unmarshal(message, &response); err != nil {
			return nil, err
		}
		if responseID, ok := response["id"].(float64); !ok || int64(responseID) != id {
			continue
		}
		if rawError, ok := response["error"]; ok {
			return nil, fmt.Errorf("CDP error: %v", rawError)
		}
		return response, nil
	}
}
