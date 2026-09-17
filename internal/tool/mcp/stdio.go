package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
)

// stdio.go implements the MCP stdio transport: newline-free, sequential JSON-RPC
// 2.0 messages over a pair of streams (a server's stdout→our reader, our
// writer→the server's stdin). The Conn type is stream-based (not process-based)
// so the framing + request/response correlation is unit-testable against an
// in-memory fake server; spawning a real subprocess is a thin wrapper on top.

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int   `json:"id,omitempty"` // nil → notification
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

type rpcMessage struct {
	ID     *int            `json:"id,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

// Conn is a JSON-RPC 2.0 connection over a reader/writer pair. It runs a read
// loop that correlates responses to in-flight calls by id; server-originated
// notifications (no id) are ignored.
type Conn struct {
	mu      sync.Mutex
	w       io.Writer
	nextID  int
	pending map[int]chan rpcMessage
	closed  chan struct{}
	closeFn func() error
	readErr error
}

// NewConn starts a JSON-RPC connection reading from r and writing to w. closeFn
// (may be nil) is invoked by Close to release the underlying resource.
func NewConn(r io.Reader, w io.Writer, closeFn func() error) *Conn {
	c := &Conn{w: w, pending: map[int]chan rpcMessage{}, closed: make(chan struct{}), closeFn: closeFn}
	go c.readLoop(r)
	return c
}

func (c *Conn) readLoop(r io.Reader) {
	dec := json.NewDecoder(r)
	for {
		var msg rpcMessage
		if err := dec.Decode(&msg); err != nil {
			c.fail(err)
			return
		}
		if msg.ID == nil {
			continue // server notification/request — ignored by this client
		}
		c.mu.Lock()
		ch, ok := c.pending[*msg.ID]
		if ok {
			delete(c.pending, *msg.ID)
		}
		c.mu.Unlock()
		if ok {
			ch <- msg
		}
	}
}

func (c *Conn) fail(err error) {
	c.mu.Lock()
	if c.readErr == nil {
		c.readErr = err
	}
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
}

// Call sends a request and waits for its response (or ctx cancellation / conn close).
func (c *Conn) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	if c.readErr != nil {
		c.mu.Unlock()
		return nil, c.readErr
	}
	c.nextID++
	id := c.nextID
	ch := make(chan rpcMessage, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.write(rpcRequest{JSONRPC: "2.0", ID: &id, Method: method, Params: params}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.closed:
		return nil, fmt.Errorf("mcp: connection closed: %w", c.readErr)
	case msg, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("mcp: connection closed during call")
		}
		if msg.Error != nil {
			return nil, msg.Error
		}
		return msg.Result, nil
	}
}

// Notify sends a notification (no id, no response expected).
func (c *Conn) Notify(method string, params any) error {
	return c.write(rpcRequest{JSONRPC: "2.0", Method: method, Params: params})
}

func (c *Conn) write(req rpcRequest) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.w.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

// Close releases the underlying resource.
func (c *Conn) Close() error {
	if c.closeFn != nil {
		return c.closeFn()
	}
	return nil
}

// StdioClient is an MCP Client over a JSON-RPC Conn.
type StdioClient struct {
	conn *Conn
}

// NewStdioClient wraps a Conn as an MCP Client.
func NewStdioClient(conn *Conn) *StdioClient { return &StdioClient{conn: conn} }

// NewProcessClient spawns an MCP server subprocess (e.g. command="npx",
// args=["-y","@modelcontextprotocol/server-filesystem","/repo"]) and speaks the
// stdio transport to it. extraEnv entries (KEY=VALUE) are appended to the parent
// environment. The caller must call Initialize, then Close when done. This is the
// production constructor; the Conn/StdioClient logic it builds on is unit-tested
// against an in-memory fake server.
func NewProcessClient(ctx context.Context, command string, args, extraEnv []string) (*StdioClient, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Stderr = os.Stderr // surface server diagnostics
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp: start %q: %w", command, err)
	}
	closeFn := func() error {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return cmd.Wait()
	}
	conn := NewConn(stdout, stdin, closeFn)
	return &StdioClient{conn: conn}, nil
}

// Close releases the connection (and, for a process client, the subprocess).
func (c *StdioClient) Close() error { return c.conn.Close() }

// Initialize performs the MCP handshake: an `initialize` request followed by the
// `notifications/initialized` notification.
func (c *StdioClient) Initialize(ctx context.Context) error {
	_, err := c.conn.Call(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "prizm", "version": "1"},
	})
	if err != nil {
		return fmt.Errorf("mcp initialize: %w", err)
	}
	return c.conn.Notify("notifications/initialized", map[string]any{})
}

// ListTools implements Client via the MCP `tools/list` method.
func (c *StdioClient) ListTools(ctx context.Context) ([]ToolDef, error) {
	raw, err := c.conn.Call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var res struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("mcp tools/list decode: %w", err)
	}
	defs := make([]ToolDef, 0, len(res.Tools))
	for _, t := range res.Tools {
		defs = append(defs, ToolDef{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return defs, nil
}

// CallTool implements Client via the MCP `tools/call` method, flattening the
// returned content blocks into text.
func (c *StdioClient) CallTool(ctx context.Context, name string, args map[string]any) (CallResult, error) {
	if args == nil {
		args = map[string]any{}
	}
	raw, err := c.conn.Call(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return CallResult{}, err
	}
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return CallResult{}, fmt.Errorf("mcp tools/call decode: %w", err)
	}
	var sb []string
	for _, b := range res.Content {
		if b.Text != "" {
			sb = append(sb, b.Text)
		}
	}
	rawMap := map[string]any{}
	_ = json.Unmarshal(raw, &rawMap)
	return CallResult{Content: joinNonEmpty(sb), IsError: res.IsError, Raw: rawMap}, nil
}

func joinNonEmpty(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "\n"
		}
		out += p
	}
	return out
}
