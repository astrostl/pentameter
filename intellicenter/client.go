package intellicenter

import (
	"context"
	"fmt"
	"math"
	"net"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Client owns a single WebSocket connection to IntelliCenter. It is synchronous:
// every request writes then reads until the matching messageID arrives, skipping
// unsolicited push notifications. A mutex serializes round-trips so callers may
// share one client across goroutines safely.
type Client struct {
	url string

	// Retry tuning for ConnectWithRetry (defaulted in New; overridable, e.g. for
	// fast tests).
	RetryMax       int
	RetryBaseDelay time.Duration
	RetryMaxDelay  time.Duration

	mu   sync.Mutex
	conn *websocket.Conn
	seq  int

	lastHealthCheck time.Time
}

// New builds a client for ws://host:port. An empty port defaults to 6680.
func New(host, port string) *Client {
	if port == "" {
		port = defaultICPortStr
	}
	return &Client{
		url:            fmt.Sprintf("ws://%s", net.JoinHostPort(host, port)),
		RetryMax:       maxRetries,
		RetryBaseDelay: baseDelay,
		RetryMaxDelay:  maxDelay,
	}
}

// Connect dials once. Use ConnectWithRetry for backoff.
func (c *Client) Connect(ctx context.Context) error {
	u, err := url.Parse(c.url)
	if err != nil {
		return fmt.Errorf("parse url %q: %w", c.url, err)
	}
	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = handshakeTimeout

	conn, resp, err := dialer.DialContext(ctx, u.String(), nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("dial %s: %w", c.url, err)
	}

	c.mu.Lock()
	c.conn = conn
	c.lastHealthCheck = time.Now()
	c.mu.Unlock()
	return nil
}

// ConnectWithRetry dials with exponential backoff (1s→30s, factor 2, max 5
// attempts), honoring ctx cancellation.
func (c *Client) ConnectWithRetry(ctx context.Context) error {
	var lastErr error
	for attempt := 0; attempt <= c.RetryMax; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("canceled during retry: %w", ctx.Err())
			case <-time.After(c.backoffDelay(attempt)):
			}
		}
		if err := c.Connect(ctx); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return fmt.Errorf("connect failed after %d attempts: %w", c.RetryMax+1, lastErr)
}

func (c *Client) backoffDelay(attempt int) time.Duration {
	d := float64(c.RetryBaseDelay) * math.Pow(backoffFactor, float64(attempt-1))
	if d > float64(c.RetryMaxDelay) {
		d = float64(c.RetryMaxDelay)
	}
	return time.Duration(d)
}

// Close tears down the connection.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
}

// Connected reports whether a connection is currently held.
func (c *Client) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn != nil
}

// Healthy pings the server (every healthCheckInterval at most) to detect a dead
// connection. Returns false if the ping fails.
func (c *Client) Healthy() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return false
	}
	if time.Since(c.lastHealthCheck) < healthCheckInterval {
		return true
	}
	deadline := time.Now().Add(pingTimeout)
	if err := c.conn.WriteControl(websocket.PingMessage, nil, deadline); err != nil {
		return false
	}
	c.lastHealthCheck = time.Now()
	return true
}

func (c *Client) nextMessageID(prefix string) string {
	c.seq++
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().Unix(), time.Now().Nanosecond()%nanosecondMod)
}

// roundTrip writes a request and reads until the response with the matching
// messageID arrives, discarding unsolicited push notifications in between. It
// validates the response code (must be empty or "200").
func (c *Client) roundTrip(prefix string, req Request) (*Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil, fmt.Errorf("not connected")
	}
	req.MessageID = c.nextMessageID(prefix)

	if err := c.conn.WriteJSON(req); err != nil {
		return nil, fmt.Errorf("write %s: %w", req.Command, err)
	}

	if err := c.conn.SetReadDeadline(time.Now().Add(responseReadTimeout)); err != nil {
		return nil, fmt.Errorf("set read deadline: %w", err)
	}
	defer func() { _ = c.conn.SetReadDeadline(time.Time{}) }()

	for range maxUnsolicitedMessages {
		var resp Response
		if err := c.conn.ReadJSON(&resp); err != nil {
			return nil, fmt.Errorf("read %s response: %w", req.Command, err)
		}
		if resp.MessageID == req.MessageID {
			if resp.Response != "" && resp.Response != "200" {
				return nil, fmt.Errorf("%s failed: response=%s", req.Command, resp.Response)
			}
			return &resp, nil
		}
		// Unsolicited push (NotifyList/WriteParamList) — skip; callers poll for state.
	}
	return nil, fmt.Errorf("no matching response for %s after %d messages", req.MessageID, maxUnsolicitedMessages)
}

// Do runs an arbitrary typed request through the shared connection and returns
// the matching response (skipping unsolicited pushes). A fresh messageID is
// assigned internally. Exposed so other consumers (e.g. the metrics monitor)
// can share this transport instead of duplicating it.
func (c *Client) Do(req Request) (*Response, error) {
	return c.roundTrip("do", req)
}

// ReadMessage reads the next message from the connection as a generic map,
// without filtering. Listen-style consumers loop on this to observe unsolicited
// push notifications. Blocks until a message arrives or the connection errors.
func (c *Client) ReadMessage() (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil, fmt.Errorf("not connected")
	}
	_ = c.conn.SetReadDeadline(time.Time{}) // block until a message arrives
	var msg map[string]any
	if err := c.conn.ReadJSON(&msg); err != nil {
		return nil, err
	}
	return msg, nil
}

// DoRaw runs a request expressed as a generic map and returns the matching
// response as a generic map. Used for GetConfiguration, whose response envelope
// ("answer") differs from the standard objectList shape. A fresh messageID is
// assigned internally.
func (c *Client) DoRaw(req map[string]any) (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil, fmt.Errorf("not connected")
	}
	mid := c.nextMessageID("raw")
	req["messageID"] = mid

	if err := c.conn.WriteJSON(req); err != nil {
		return nil, fmt.Errorf("write raw %v: %w", req["command"], err)
	}
	if err := c.conn.SetReadDeadline(time.Now().Add(responseReadTimeout)); err != nil {
		return nil, fmt.Errorf("set read deadline: %w", err)
	}
	defer func() { _ = c.conn.SetReadDeadline(time.Time{}) }()

	for range maxUnsolicitedMessages {
		var resp map[string]any
		if err := c.conn.ReadJSON(&resp); err != nil {
			return nil, fmt.Errorf("read raw response: %w", err)
		}
		if id, ok := resp["messageID"].(string); ok && id == mid {
			return resp, nil
		}
	}
	return nil, fmt.Errorf("no matching raw response for %s", mid)
}
