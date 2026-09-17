package sse_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/sse"
)

func TestDecoder_SimpleEvent(t *testing.T) {
	input := "event: message\ndata: hello\n\n"
	dec := sse.NewDecoder(strings.NewReader(input))

	ev, err := dec.Next()
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if ev.Event != "message" {
		t.Errorf("Event = %q, want message", ev.Event)
	}
	if string(ev.Data) != "hello" {
		t.Errorf("Data = %q, want hello", string(ev.Data))
	}
}

func TestDecoder_MultipleEvents(t *testing.T) {
	input := "event: start\ndata: {\"type\":\"start\"}\n\nevent: delta\ndata: {\"text\":\"hi\"}\n\n"
	dec := sse.NewDecoder(strings.NewReader(input))

	ev1, err := dec.Next()
	if err != nil {
		t.Fatalf("First Next() error = %v", err)
	}
	if ev1.Event != "start" {
		t.Errorf("First Event = %q, want start", ev1.Event)
	}

	ev2, err := dec.Next()
	if err != nil {
		t.Fatalf("Second Next() error = %v", err)
	}
	if ev2.Event != "delta" {
		t.Errorf("Second Event = %q, want delta", ev2.Event)
	}

	_, err = dec.Next()
	if err == nil {
		t.Error("Expected EOF after all events")
	}
}

func TestDecoder_DataOnly(t *testing.T) {
	input := "data: {\"content\":\"hello\"}\n\ndata: {\"content\":\"world\"}\n\n"
	dec := sse.NewDecoder(strings.NewReader(input))

	ev1, err := dec.Next()
	if err != nil {
		t.Fatalf("First Next() error = %v", err)
	}
	if ev1.Event != "" {
		t.Errorf("Event = %q, want empty (default)", ev1.Event)
	}
	if string(ev1.Data) != "{\"content\":\"hello\"}" {
		t.Errorf("Data = %q, want {\"content\":\"hello\"}", string(ev1.Data))
	}
}

func TestDecoder_Comments(t *testing.T) {
	input := ": this is a comment\ndata: payload\n\n"
	dec := sse.NewDecoder(strings.NewReader(input))

	ev, err := dec.Next()
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if string(ev.Data) != "payload" {
		t.Errorf("Data = %q, want payload", string(ev.Data))
	}
}

func TestDecoder_NextData(t *testing.T) {
	input := "data: {\"text\":\"hello\"}\n\ndata: {\"text\":\"world\"}\n\n"
	dec := sse.NewDecoder(strings.NewReader(input))

	d1, err := dec.NextData()
	if err != nil {
		t.Fatalf("First NextData() error = %v", err)
	}
	var v1 map[string]string
	if err := json.Unmarshal(d1, &v1); err != nil {
		t.Fatalf("Unmarshal first: %v", err)
	}
	if v1["text"] != "hello" {
		t.Errorf("First text = %q, want hello", v1["text"])
	}

	d2, err := dec.NextData()
	if err != nil {
		t.Fatalf("Second NextData() error = %v", err)
	}
	var v2 map[string]string
	if err := json.Unmarshal(d2, &v2); err != nil {
		t.Fatalf("Unmarshal second: %v", err)
	}
	if v2["text"] != "world" {
		t.Errorf("Second text = %q, want world", v2["text"])
	}

	_, err = dec.NextData()
	if err == nil {
		t.Error("Expected EOF after all data")
	}
}

func TestDecoder_EmptyStream(t *testing.T) {
	dec := sse.NewDecoder(strings.NewReader(""))

	_, err := dec.Next()
	if err == nil {
		t.Error("Expected EOF from empty stream")
	}
}

func TestDecoder_FieldWithSpace(t *testing.T) {
	input := "data: {\"key\": \"value\"}\n\n"
	dec := sse.NewDecoder(strings.NewReader(input))

	ev, err := dec.Next()
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if string(ev.Data) != "{\"key\": \"value\"}" {
		t.Errorf("Data = %q, want {\"key\": \"value\"}", string(ev.Data))
	}
}

func TestDecoder_MultilineData(t *testing.T) {
	// Per SSE spec (HTML Living Standard §9.2.6), multiple data: fields
	// are concatenated with LF between them.
	input := "data: line1\ndata: line2\n\n"
	dec := sse.NewDecoder(strings.NewReader(input))

	ev, err := dec.Next()
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if string(ev.Data) != "line1\nline2" {
		t.Errorf("Data = %q, want line1\nline2 (SSE spec: multiline data joined with LF)", string(ev.Data))
	}
}

func TestDecoder_MixedNextAndNextData(t *testing.T) {
	// Verify that using NextData on a stream with event fields still works
	// (NextData only looks for data: lines)
	input := "event: delta\ndata: {\"text\":\"hi\"}\n\nevent: stop\ndata: [DONE]\n\n"
	dec := sse.NewDecoder(strings.NewReader(input))

	d, err := dec.NextData()
	if err != nil {
		t.Fatalf("NextData() error = %v", err)
	}
	if string(d) != "{\"text\":\"hi\"}" {
		t.Errorf("Data = %q, want {\"text\":\"hi\"}", string(d))
	}
}
