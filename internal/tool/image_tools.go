package tool

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// image_tools.go provides the Researcher's reference-image tools: fetch_image
// (download a URL), generate_image (text→image via a configured endpoint), and
// analyze_image (vision-caption via a vision model). All three write only into
// a configured references directory and are read-only w.r.t. the codebase, so
// they are policy-auto-approved like web_search. Each degrades to a clear
// "not configured" result instead of failing when its endpoint/model is unset.

const (
	defaultReferencesDir = "references"
	defaultVisionModel   = "llama3.2-vision:11b"
	maxImageBytes        = 25 << 20 // 25 MiB cap on any downloaded/generated image
	maxReferenceImages   = 5
)

// ImageToolsConfig configures the reference-image tools. Empty fields fall back
// to environment variables so deployment matches the web_search pattern.
type ImageToolsConfig struct {
	// ReferencesDir is where images are saved. Must sit within the agent's
	// write roots. Empty -> PRIZM_IMAGE_DIR, then "references".
	ReferencesDir string
	// WorkspaceRoot is the primary workspace root for safe output_dir resolution.
	// Empty disables containment checks.
	WorkspaceRoot string
	// AllowedPaths are extra write roots that output_dir may target.
	AllowedPaths []string
	// ImageSearchEndpoint is a JSON image-search endpoint. Empty -> PRIZM_IMAGE_SEARCH_URL.
	ImageSearchEndpoint string
	// ImageSearchKey is an optional bearer token. Empty -> PRIZM_IMAGE_SEARCH_KEY.
	ImageSearchKey string
	// ImageSearchQueryParam is the query parameter name for image search. Empty -> q.
	ImageSearchQueryParam string
	// CodexExecutable is the optional Codex CLI executable for collect_reference_images.
	CodexExecutable string
	// CodexModel optionally overrides the Codex model for the image probe.
	CodexModel string
	// CodexProfile optionally selects a Codex profile for the image probe.
	CodexProfile string
	// CodexWorkspace is the directory passed to codex exec/login status.
	CodexWorkspace string
	// CodexTimeout bounds the optional Codex probe. Empty -> a short safe timeout.
	CodexTimeout time.Duration
	// CodexRunner and CodexLookPath are injectable seams for tests.
	CodexRunner   ImageCommandRunner
	CodexLookPath imageLookPathFunc
	// ImageGenEndpoint is a text->image HTTP endpoint. Empty -> PRIZM_IMAGEGEN_URL.
	ImageGenEndpoint string
	// ImageGenKey is an optional bearer token. Empty -> PRIZM_IMAGEGEN_KEY.
	ImageGenKey string
	// VisionBaseURL is the Ollama base URL for analyze_image. Empty ->
	// PRIZM_VISION_URL, then OLLAMA_BASE_URL, then http://localhost:11434.
	VisionBaseURL string
	// VisionModel is the vision-capable model tag. Empty -> PRIZM_VISION_MODEL,
	// then the default VLM.
	VisionModel string
}

func (c ImageToolsConfig) referencesDir() string {
	if c.ReferencesDir != "" {
		return c.ReferencesDir
	}
	if d := os.Getenv("PRIZM_IMAGE_DIR"); d != "" {
		return d
	}
	return defaultReferencesDir
}

func (c ImageToolsConfig) imageGenEndpoint() string {
	if c.ImageGenEndpoint != "" {
		return c.ImageGenEndpoint
	}
	return os.Getenv("PRIZM_IMAGEGEN_URL")
}

func (c ImageToolsConfig) imageGenKey() string {
	if c.ImageGenKey != "" {
		return c.ImageGenKey
	}
	return os.Getenv("PRIZM_IMAGEGEN_KEY")
}

func (c ImageToolsConfig) visionBaseURL() string {
	if c.VisionBaseURL != "" {
		return c.VisionBaseURL
	}
	if u := os.Getenv("PRIZM_VISION_URL"); u != "" {
		return u
	}
	if u := os.Getenv("OLLAMA_BASE_URL"); u != "" {
		return u
	}
	return "http://localhost:11434"
}

func (c ImageToolsConfig) visionModel() string {
	if c.VisionModel != "" {
		return c.VisionModel
	}
	if m := os.Getenv("PRIZM_VISION_MODEL"); m != "" {
		return m
	}
	return defaultVisionModel
}

// httpClient returns a shared client with a generous timeout for image I/O.
func imageHTTPClient(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return &http.Client{Timeout: 60 * time.Second}
}

// --- fetch_image -------------------------------------------------------------

// FetchImageTool downloads an image URL into the references directory.
type FetchImageTool struct {
	Config ImageToolsConfig
	Client *http.Client
}

func (t *FetchImageTool) Name() string { return "fetch_image" }
func (t *FetchImageTool) Description() string {
	return "Downloads a reference image from a URL into the references directory. Returns the saved file path."
}
func (t *FetchImageTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"url":        {Type: "string", Description: "The image URL to download", Required: true},
			"filename":   {Type: "string", Description: "Optional filename to save as (extension inferred if omitted)", Required: false},
			"output_dir": {Type: "string", Description: "Optional output directory within the workspace/write roots", Required: false},
		},
		Output: ParamSpec{Type: "object", Description: "Saved path, size, and content type"},
	}
}
func (t *FetchImageTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	rawURL, ok := input["url"].(string)
	if !ok || strings.TrimSpace(rawURL) == "" {
		return ToolResult{Success: false, Error: "required parameter 'url' must be a non-empty string"}, nil
	}
	data, contentType, err := downloadImage(ctx, imageHTTPClient(t.Client), rawURL, t.Config.imageGenKey())
	if err != nil {
		return ToolResult{Success: false, Error: err.Error()}, nil
	}
	name, _ := input["filename"].(string)
	if strings.TrimSpace(name) == "" {
		name = filenameFromURL(rawURL, contentType)
	}
	dir, err := resolveImageOutputDir(t.Config, input)
	if err != nil {
		return ToolResult{Success: false, Error: err.Error()}, nil
	}
	path, err := saveToReferences(dir, name, contentType, data)
	if err != nil {
		return ToolResult{Success: false, Error: err.Error()}, nil
	}
	return ToolResult{Success: true, Output: map[string]any{
		"path": path, "bytes": len(data), "content_type": contentType, "source_url": rawURL,
	}}, nil
}

// --- generate_image ----------------------------------------------------------

// GenerateImageTool creates an image from a text prompt via a configured
// text→image endpoint (provider-agnostic, mirroring web_search).
type GenerateImageTool struct {
	Config ImageToolsConfig
	Client *http.Client
}

func (t *GenerateImageTool) Name() string { return "generate_image" }
func (t *GenerateImageTool) Description() string {
	return "Generates a reference/concept image from a text prompt and saves it to the references directory."
}
func (t *GenerateImageTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"prompt":     {Type: "string", Description: "Text description of the image to generate", Required: true},
			"filename":   {Type: "string", Description: "Optional filename to save as", Required: false},
			"output_dir": {Type: "string", Description: "Optional output directory within the workspace/write roots", Required: false},
		},
		Output: ParamSpec{Type: "object", Description: "Saved path of the generated image"},
	}
}
func (t *GenerateImageTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	prompt, ok := input["prompt"].(string)
	if !ok || strings.TrimSpace(prompt) == "" {
		return ToolResult{Success: false, Error: "required parameter 'prompt' must be a non-empty string"}, nil
	}
	endpoint := t.Config.imageGenEndpoint()
	if endpoint == "" {
		return ToolResult{Success: false, Error: "image generation is not configured — set PRIZM_IMAGEGEN_URL (and PRIZM_IMAGEGEN_KEY if required)"}, nil
	}

	reqBody, _ := json.Marshal(map[string]any{"prompt": prompt})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return ToolResult{Success: false, Error: err.Error()}, nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, image/*")
	if key := t.Config.imageGenKey(); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := imageHTTPClient(t.Client).Do(req)
	if err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("image generation request failed: %v", err)}, nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ToolResult{Success: false, Error: fmt.Sprintf("image generation returned %d: %s", resp.StatusCode, truncateStr(string(body), 500))}, nil
	}

	ct := resp.Header.Get("Content-Type")
	data, err := imageBytesFromResponse(ct, body)
	if err != nil {
		return ToolResult{Success: false, Error: err.Error()}, nil
	}
	name, _ := input["filename"].(string)
	if strings.TrimSpace(name) == "" {
		name = "generated-" + safeSlug(prompt, 40)
	}
	dir, err := resolveImageOutputDir(t.Config, input)
	if err != nil {
		return ToolResult{Success: false, Error: err.Error()}, nil
	}
	path, err := saveToReferences(dir, name, "image/png", data)
	if err != nil {
		return ToolResult{Success: false, Error: err.Error()}, nil
	}
	return ToolResult{Success: true, Output: map[string]any{"path": path, "bytes": len(data), "prompt": prompt}}, nil
}

// --- analyze_image -----------------------------------------------------------

// AnalyzeImageTool captions/describes an image using a vision model via
// Ollama's /api/chat (images field).
type AnalyzeImageTool struct {
	Config ImageToolsConfig
	Client *http.Client
}

func (t *AnalyzeImageTool) Name() string { return "analyze_image" }
func (t *AnalyzeImageTool) Description() string {
	return "Describes/captions an image (local path or URL) using a vision model, for building a reference brief."
}
func (t *AnalyzeImageTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"image":  {Type: "string", Description: "Local file path or URL of the image to analyze", Required: true},
			"prompt": {Type: "string", Description: "Optional guiding question (default: describe for a reference brief)", Required: false},
		},
		Output: ParamSpec{Type: "object", Description: "The model's description/tags of the image"},
	}
}
func (t *AnalyzeImageTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	imageRef, ok := input["image"].(string)
	if !ok || strings.TrimSpace(imageRef) == "" {
		return ToolResult{Success: false, Error: "required parameter 'image' must be a non-empty string"}, nil
	}
	data, err := loadImageBytes(ctx, imageHTTPClient(t.Client), imageRef, t.Config.imageGenKey())
	if err != nil {
		return ToolResult{Success: false, Error: err.Error()}, nil
	}
	prompt, _ := input["prompt"].(string)
	if strings.TrimSpace(prompt) == "" {
		prompt = "Describe this reference image for a game-art brief: subject, style, palette, mood, and notable details. Be concise and concrete."
	}

	chatReq := map[string]any{
		"model":  t.Config.visionModel(),
		"stream": false,
		"messages": []map[string]any{{
			"role":    "user",
			"content": prompt,
			"images":  []string{base64.StdEncoding.EncodeToString(data)},
		}},
	}
	bodyBytes, _ := json.Marshal(chatReq)
	url := strings.TrimRight(t.Config.visionBaseURL(), "/") + "/api/chat"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return ToolResult{Success: false, Error: err.Error()}, nil
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := imageHTTPClient(t.Client).Do(req)
	if err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("vision request failed (is Ollama running with %s pulled?): %v", t.Config.visionModel(), err)}, nil
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ToolResult{Success: false, Error: fmt.Sprintf("vision model returned %d: %s", resp.StatusCode, truncateStr(string(respBody), 500))}, nil
	}
	var parsed struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("could not parse vision response: %v", err)}, nil
	}
	if strings.TrimSpace(parsed.Message.Content) == "" {
		return ToolResult{Success: false, Error: "vision model returned an empty description"}, nil
	}
	return ToolResult{Success: true, Output: map[string]any{
		"description": strings.TrimSpace(parsed.Message.Content), "model": t.Config.visionModel(),
	}}, nil
}

// --- registration + helpers --------------------------------------------------

// RegisterImageTools adds fetch_image, generate_image, analyze_image, and collect_reference_images.
func RegisterImageTools(registry *Registry, cfg ImageToolsConfig) *Registry {
	registry.Register(&FetchImageTool{Config: cfg})
	registry.Register(&GenerateImageTool{Config: cfg})
	registry.Register(&AnalyzeImageTool{Config: cfg})
	registry.Register(&CollectReferenceImagesTool{Config: cfg})
	return registry
}

// downloadImage GETs an image URL and returns its bytes + content type.
func downloadImage(ctx context.Context, client *http.Client, rawURL, bearer string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("invalid url: %w", err)
	}
	// Browser-like headers so hotlink-protected image hosts (Pinterest, news
	// sites, CDNs) serve the actual image instead of an anti-bot HTML wall.
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "image/avif,image/webp,image/png,image/jpeg,*/*")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("download returned %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes))
	if err != nil {
		return nil, "", fmt.Errorf("read body: %w", err)
	}
	// Validate the payload is really an image — many hosts return an HTML
	// consent/anti-bot page with a 200. Reject those so we don't save junk.
	ct := resp.Header.Get("Content-Type")
	if ct == "" || !strings.HasPrefix(strings.ToLower(ct), "image/") {
		sniff := http.DetectContentType(data)
		if !strings.HasPrefix(sniff, "image/") {
			return nil, "", fmt.Errorf("url did not return an image (content-type %q)", firstNonEmptyImage(ct, sniff))
		}
		ct = sniff
	}
	return data, ct, nil
}

// loadImageBytes returns image bytes from a local path or a URL.
func loadImageBytes(ctx context.Context, client *http.Client, ref, bearer string) ([]byte, error) {
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		data, _, err := downloadImage(ctx, client, ref, bearer)
		return data, err
	}
	data, err := os.ReadFile(ref)
	if err != nil {
		return nil, fmt.Errorf("could not read image %q: %w", ref, err)
	}
	if len(data) > maxImageBytes {
		return nil, fmt.Errorf("image %q exceeds %d bytes", ref, maxImageBytes)
	}
	return data, nil
}

// imageBytesFromResponse extracts raw image bytes from a generation response
// that may be either raw image/* or JSON carrying base64 (b64_json/image/data).
func imageBytesFromResponse(contentType string, body []byte) ([]byte, error) {
	if strings.HasPrefix(contentType, "image/") {
		return body, nil
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		// Not JSON and not image/* — assume raw bytes.
		return body, nil
	}
	// Common shapes: {b64_json}, {image}, {data:[{b64_json|url}]}.
	if b64 := firstStringField(payload, "b64_json", "image", "b64"); b64 != "" {
		return base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	}
	if arr, ok := payload["data"].([]any); ok && len(arr) > 0 {
		if first, ok := arr[0].(map[string]any); ok {
			if b64 := firstStringField(first, "b64_json", "image", "b64"); b64 != "" {
				return base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
			}
		}
	}
	return nil, fmt.Errorf("image generation response had no recognizable image data")
}

func firstStringField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// saveToReferences writes data to the references dir under a safe filename with
// an extension inferred from contentType when missing.
func saveToReferences(dir, name, contentType string, data []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create references dir: %w", err)
	}
	rawExt := filepath.Ext(name)
	base := safeSlug(strings.TrimSuffix(filepath.Base(name), rawExt), 60)
	ext := safeImageExt(rawExt, contentType)
	path := filepath.Join(dir, base+ext)
	for i := 2; fileExists(path); i++ {
		path = filepath.Join(dir, fmt.Sprintf("%s-%d%s", base, i, ext))
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("write image: %w", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path, nil
	}
	return abs, nil
}
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func filenameFromURL(rawURL, contentType string) string {
	base := rawURL
	if i := strings.IndexAny(base, "?#"); i >= 0 {
		base = base[:i]
	}
	base = strings.TrimRight(base, "/")
	if idx := strings.LastIndex(base, "/"); idx >= 0 {
		base = base[idx+1:]
	}
	if base == "" {
		base = "reference"
	}
	if filepath.Ext(base) == "" {
		base += extFromContentType(contentType)
	}
	return base
}

func safeImageExt(ext, contentType string) string {
	ext = strings.ToLower(ext)
	switch ext {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif":
		return ext
	default:
		return extFromContentType(contentType)
	}
}

func extFromContentType(ct string) string {
	switch {
	case strings.Contains(ct, "jpeg"), strings.Contains(ct, "jpg"):
		return ".jpg"
	case strings.Contains(ct, "webp"):
		return ".webp"
	case strings.Contains(ct, "gif"):
		return ".gif"
	default:
		return ".png"
	}
}

// safeSlug keeps [a-zA-Z0-9._-], collapses the rest to '-', and caps length.
func safeSlug(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		default:
			return '-'
		}
	}, s)
	s = strings.Trim(s, "-.")
	if s == "" {
		s = "reference"
	}
	if len(s) > max {
		s = s[:max]
	}
	return s
}
