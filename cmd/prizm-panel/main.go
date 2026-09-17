// Command prizm-panel is a small native desktop window that connects to a running
// Prizm instance and shows a friendly ASCII "pet" whose mood tracks the agent —
// sleeping when idle, thinking while it works, happy on success, worried when a
// human is needed. It is aimed at non-developers: the creature and a plain-language
// caption lead, with the phase/token detail shown as secondary stats.
//
// It is a read-only consumer of the same SSE event stream `prizm watch` uses
// (GET /api/v1/events/stream); it never affects a run. Local-only by default.
//
// This lives in its own Go module so its Fyne + cgo dependency stays isolated from
// the pure-Go `prizm` CLI: the root module's `go build ./...` never compiles it.
//
// Build (needs a C compiler on PATH, e.g. WinLibs/MinGW-w64 gcc):
//
//	cd cmd/prizm-panel && CGO_ENABLED=1 go build -o ../../prizm-panel .
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/emaharmony/prizm/internal/petart"
	"github.com/emaharmony/prizm/internal/sse"
	"github.com/emaharmony/prizm/internal/tracker"
)

func main() {
	fs := flag.NewFlagSet("prizm-panel", flag.ExitOnError)
	url := fs.String("url", "", "Base API URL (default http://<host>:<port>)")
	host := fs.String("host", "127.0.0.1", "API host")
	port := fs.Int("port", 8322, "API port (this is `prizm serve` port + 1)")
	token := fs.String("token", "", "Bearer token (only needed for non-loopback binds)")
	subject := fs.String("subject", "prizm.workflow.>", "NATS subject filter")
	fs.Parse(os.Args[1:])

	baseURL := *url
	if baseURL == "" {
		baseURL = fmt.Sprintf("http://%s:%d", *host, *port)
	}
	baseURL = strings.TrimRight(baseURL, "/")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a := app.New()
	w := a.NewWindow("Prizm Pet")

	u := &ui{
		model:   tracker.New(),
		baseURL: baseURL,
		pet:     widget.NewTextGrid(),
		caption: widget.NewLabelWithStyle("Waking up...", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
		stats:   widget.NewTextGrid(),
		footer:  widget.NewLabel(""),
	}
	u.pet.SetText(petart.Art(petart.MoodSleeping, 0))

	w.SetContent(container.NewPadded(container.NewVBox(
		u.pet,
		container.NewCenter(u.caption),
		widget.NewSeparator(),
		u.stats,
		widget.NewSeparator(),
		u.footer,
	)))
	w.Resize(fyne.NewSize(560, 660))

	go u.feedLoop(ctx, *subject, *token)
	go u.animate(ctx)
	// Quit the window if the process is interrupted (Ctrl+C).
	go func() {
		<-ctx.Done()
		fyne.Do(a.Quit)
	}()

	w.ShowAndRun()
}

// ui holds the shared state between the feed goroutine (writes) and the animation
// goroutine (reads + renders on the UI thread).
type ui struct {
	model   *tracker.Model
	baseURL string

	mu        sync.Mutex
	connected bool
	lastEvent time.Time // zero until the first event arrives
	frame     int

	pet     *widget.TextGrid
	caption *widget.Label
	stats   *widget.TextGrid
	footer  *widget.Label
}

// animate repaints the UI on a fixed cadence so the pet visibly animates even when
// no events are arriving. All widget mutation happens on the UI thread via fyne.Do.
func (u *ui) animate(ctx context.Context) {
	t := time.NewTicker(125 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fyne.Do(u.refresh)
		}
	}
}

// refresh renders the current model + connection state into the widgets. UI thread.
func (u *ui) refresh() {
	u.mu.Lock()
	connected := u.connected
	since := time.Hour
	if !u.lastEvent.IsZero() {
		since = time.Since(u.lastEvent)
	}
	frame := u.frame
	u.frame++
	u.mu.Unlock()

	snap := u.model.Snapshot()
	_, art, caption := petart.Render(snap, connected, since, frame)
	u.pet.SetText(art)
	u.caption.SetText(caption)
	u.stats.SetText(tracker.RenderFrame(snap, frame))

	conn := "connected"
	if !connected {
		conn = "disconnected - retrying"
	}
	u.footer.SetText(fmt.Sprintf("%s  |  %s  |  %d events", conn, u.baseURL, snap.Events))
}

func (u *ui) setConnected(b bool) {
	u.mu.Lock()
	u.connected = b
	u.mu.Unlock()
}

func (u *ui) noteEvent() {
	u.mu.Lock()
	u.lastEvent = time.Now()
	u.mu.Unlock()
}

// feedLoop keeps a live SSE connection, reconnecting with exponential backoff so the
// panel survives `prizm serve` restarting. This is the "resilient" behaviour.
func (u *ui) feedLoop(ctx context.Context, subject, token string) {
	const base = 500 * time.Millisecond
	backoff := base
	for ctx.Err() == nil {
		connected, err := u.streamOnce(ctx, subject, token)
		u.setConnected(false)
		if ctx.Err() != nil {
			return
		}
		if connected {
			backoff = base // a good connection resets the backoff
		}
		_ = err // errors are expected while the daemon is down; just retry
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 5*time.Second {
			backoff = min(backoff*2, 5*time.Second)
		}
	}
}

// streamOnce opens one SSE connection and applies events until it drops. It reports
// whether the connection was successfully established (for backoff reset).
func (u *ui) streamOnce(ctx context.Context, subject, token string) (connected bool, err error) {
	streamURL := u.baseURL + "/api/v1/events/stream?subject=" + subject
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	if err != nil {
		return false, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("stream returned %s", resp.Status)
	}

	u.setConnected(true)
	u.hydrate(ctx, token) // best-effort catch-up on the current run

	dec := sse.NewDecoder(resp.Body)
	for {
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		ev, err := dec.Next()
		if err != nil {
			return true, err
		}
		if ev.Event == "" || ev.Event == "connected" || ev.Event == "heartbeat" {
			continue
		}
		var envelope struct {
			Type    string         `json:"type"`
			Payload map[string]any `json:"payload"`
		}
		if json.Unmarshal(ev.Data, &envelope) != nil || envelope.Type == "" {
			continue
		}
		u.model.Apply(envelope.Type, envelope.Payload)
		u.noteEvent()
	}
}

// hydrate seeds the model from the current run so the panel isn't blank between
// events / after a reconnect. Best-effort: any failure is ignored and live events
// will fill things in. It stays decoupled from the persisted-state schema by reading
// a few well-known fields permissively.
func (u *ui) hydrate(ctx context.Context, token string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.baseURL+"/api/v1/workflows/runs/current", nil)
	if err != nil {
		return
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	var m map[string]any
	if json.NewDecoder(resp.Body).Decode(&m) != nil || m == nil {
		return
	}

	if wf := firstString(m, "workflow_name", "workflow", "WorkflowName"); wf != "" {
		u.model.Apply("workflow.started", map[string]any{"workflow": wf})
	}
	phase := firstString(m, "current_phase", "CurrentPhase", "phase")
	if phase != "" {
		u.model.Apply("phase.entered", map[string]any{"phase": phase})
	}
	total := firstNum(m, "total_prompt_tokens", "TotalPromptTokens") + firstNum(m, "total_completion_tokens", "TotalCompletionTokens")
	maxTok := firstNum(m, "max_total_tokens", "MaxTotalTokens")
	if total > 0 {
		u.model.Apply("phase.tokens", map[string]any{"phase": phase, "total": total, "max": maxTok})
	}
	switch status := strings.ToLower(firstString(m, "status", "Status")); {
	case strings.Contains(status, "complete"):
		u.model.Apply("workflow.completed", map[string]any{})
	case strings.Contains(status, "paused"):
		u.model.Apply("workflow.paused", map[string]any{"phase": phase})
	case strings.Contains(status, "block"):
		u.model.Apply("workflow.blocked", map[string]any{})
	}
	u.noteEvent()
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func firstNum(m map[string]any, keys ...string) float64 {
	for _, k := range keys {
		if v, ok := m[k].(float64); ok {
			return v
		}
	}
	return 0
}
