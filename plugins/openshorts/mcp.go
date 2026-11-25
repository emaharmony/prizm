package main

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"

	"github.com/emaharmony/prizm/plugins/openshorts/internal/creatorvod"
)

//go:embed skills/prizm-workflow-adapter/SKILL.md
var guide string

type request struct {
	Version string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type reply struct {
	Version string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type definition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func inputSchema(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{"type": "object", "additionalProperties": false}
	if properties != nil {
		schema["properties"] = properties
	}
	if len(required) != 0 {
		schema["required"] = required
	}
	return schema
}

var toolDefinitions = []definition{
	{
		Name: "guide", Description: "Return the OpenShorts plugin guide and the limits of the Stage 1 mock workflow.",
		InputSchema: inputSchema(nil),
	},
	{
		Name: "run_demo", Description: "Run or resume the OpenShorts Stage 1 synthetic clipping workflow. Uses only mock media and export receipts.",
		InputSchema: inputSchema(map[string]any{
			"generation_id": map[string]any{"type": "string", "pattern": "^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$"},
		}),
	},
	{
		Name: "status", Description: "Look up an existing OpenShorts Stage 1 job by its job ID.",
		InputSchema: inputSchema(map[string]any{
			"job_id": map[string]any{"type": "string", "pattern": "^vod_[a-f0-9]{64}$"},
		}, "job_id"),
	},
}

func serveMCP(ctx context.Context, demo *creatorvod.Demo, input io.Reader, output io.Writer) error {
	reader := bufio.NewScanner(input)
	reader.Buffer(make([]byte, 4096), 1<<20)
	writer := json.NewEncoder(output)
	for reader.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var req request
		res := reply{Version: "2.0"}
		if err := json.Unmarshal(reader.Bytes(), &req); err != nil {
			res.Error = &rpcError{Code: -32700, Message: "parse error"}
		} else if req.Version != "2.0" || req.Method == "" {
			res.Error = &rpcError{Code: -32600, Message: "invalid request"}
		} else {
			if len(req.ID) == 0 {
				continue
			}
			res.ID = req.ID
			res.Result, res.Error = dispatch(ctx, demo, req)
		}
		if err := writer.Encode(res); err != nil {
			return err
		}
	}
	return reader.Err()
}

func dispatch(ctx context.Context, demo *creatorvod.Demo, req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": serverName, "version": "0.1.0"},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": toolDefinitions}, nil
	case "tools/call":
		var call struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := decodeObject(req.Params, &call); err != nil || call.Name == "" {
			return nil, &rpcError{Code: -32602, Message: "invalid tool call"}
		}
		if len(call.Arguments) == 0 {
			call.Arguments = json.RawMessage("{}")
		}
		result, err := callTool(ctx, demo, call.Name, call.Arguments)
		if err != nil {
			return toolText(err.Error(), true), nil
		}
		return toolText(result, false), nil
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found"}
	}
}

func toolText(text string, failed bool) map[string]any {
	return map[string]any{
		"content": []map[string]string{{"type": "text", "text": text}},
		"isError": failed,
	}
}

func callTool(ctx context.Context, demo *creatorvod.Demo, name string, args json.RawMessage) (string, error) {
	var job creatorvod.Job
	var err error
	switch name {
	case "guide":
		if err := decodeObject(args, &struct{}{}); err != nil {
			return "", err
		}
		return guide, nil
	case "run_demo":
		var input struct {
			Generation string `json:"generation_id"`
		}
		if err := decodeObject(args, &input); err != nil {
			return "", err
		}
		job, err = demo.Run(ctx, input.Generation)
	case "status":
		var input struct {
			ID string `json:"job_id"`
		}
		if err := decodeObject(args, &input); err != nil {
			return "", err
		}
		job, err = demo.Status(ctx, input.ID)
	default:
		return "", errors.New("unknown OpenShorts tool")
	}
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(struct {
		Mode string         `json:"mode"`
		Job  creatorvod.Job `json:"job"`
	}{Mode: "mock", Job: job})
	return string(data), err
}

func decodeObject(raw json.RawMessage, dst any) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return errors.New("expected a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(dst)
}
