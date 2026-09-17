package tool

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const defaultCodexImageTimeout = 90 * time.Second

type ImageCommandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

type ImageCommandRunner interface {
	Run(ctx context.Context, executable string, args []string, stdin, cwd string) (ImageCommandResult, error)
}

type osImageCommandRunner struct{}

type imageLookPathFunc func(string) (string, error)

type imageCandidate struct {
	URL         string
	B64JSON     string
	ContentType string
	Filename    string
}

type imageSearchSource struct {
	Endpoint   string
	APIKey     string
	QueryParam string
}

// CollectReferenceImagesTool gathers reference images for Scout. It probes a
// local Codex CLI image-output contract first, then falls back to configured
// JSON image/web search endpoints and downloads image URLs into a safe folder.
type CollectReferenceImagesTool struct {
	Config ImageToolsConfig
	Client *http.Client
}

func (t *CollectReferenceImagesTool) Name() string { return "collect_reference_images" }
func (t *CollectReferenceImagesTool) Description() string {
	return "Collects reference images for Scout. Tries local Codex image output when available, then falls back to configured image/web search and saves files under a safe output directory."
}
func (t *CollectReferenceImagesTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"prompt":          {Type: "string", Description: "Reference image query or generation prompt", Required: true},
			"count":           {Type: "number", Description: "Number of images to save (default 1, max 5)", Required: false},
			"output_dir":      {Type: "string", Description: "Optional output directory within the workspace/write roots", Required: false},
			"filename_prefix": {Type: "string", Description: "Optional safe filename prefix for saved images", Required: false},
			"prefer_codex":    {Type: "boolean", Description: "Try local Codex image output before search fallback (default true)", Required: false},
		},
		Output: ParamSpec{Type: "object", Description: "Saved paths, source, Codex attempt status, and fallback details"},
	}
}
func (t *CollectReferenceImagesTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	prompt, ok := input["prompt"].(string)
	prompt = strings.TrimSpace(prompt)
	if !ok || prompt == "" {
		return ToolResult{Success: false, Error: "required parameter 'prompt' must be a non-empty string"}, nil
	}

	count := requestedReferenceCount(input["count"])
	dir, err := resolveImageOutputDir(t.Config, input)
	if err != nil {
		return ToolResult{Success: false, Error: err.Error()}, nil
	}
	prefix, _ := input["filename_prefix"].(string)
	prefix = safeSlug(firstNonEmptyImage(prefix, prompt), 48)
	preferCodex := true
	if v, ok := input["prefer_codex"].(bool); ok {
		preferCodex = v
	}

	var paths []string
	var errs []string
	codexAttempted := false
	fallbackReason := ""
	source := "search"
	client := imageHTTPClient(t.Client)

	if preferCodex {
		codexAttempted = true
		codexPaths, reason, codexErrs := t.collectWithCodex(ctx, prompt, count, dir, prefix, client)
		errs = append(errs, codexErrs...)
		if len(codexPaths) > 0 {
			paths = codexPaths
			source = "codex"
		} else {
			fallbackReason = reason
			if fallbackReason == "" {
				fallbackReason = "codex returned no usable image data"
			}
		}
	}

	if len(paths) < count {
		searchPaths, searchErrs, searchErr := t.collectWithSearch(ctx, prompt, count-len(paths), dir, prefix, len(paths), client)
		errs = append(errs, searchErrs...)
		paths = append(paths, searchPaths...)
		if len(searchPaths) > 0 && source != "codex" {
			source = "search"
		}
		if searchErr != nil && len(paths) == 0 {
			out := referenceImageOutput(prompt, paths, source, codexAttempted, fallbackReason, errs)
			return ToolResult{Success: false, Output: out, Error: searchErr.Error()}, nil
		}
	}

	out := referenceImageOutput(prompt, paths, source, codexAttempted, fallbackReason, errs)
	if len(paths) == 0 {
		return ToolResult{Success: false, Output: out, Error: "no reference images could be collected"}, nil
	}
	return ToolResult{Success: true, Output: out}, nil
}

func (t *CollectReferenceImagesTool) collectWithCodex(ctx context.Context, prompt string, count int, dir, prefix string, client *http.Client) ([]string, string, []string) {
	executable, err := t.Config.resolveCodexExecutable()
	if err != nil {
		return nil, fmt.Sprintf("codex unavailable: %v", err), nil
	}
	workspace := t.Config.codexWorkspace()
	runner := t.Config.codexRunner()

	loginCtx, cancelLogin := context.WithTimeout(ctx, minDuration(t.Config.codexTimeout(), 20*time.Second))
	loginRes, loginErr := runner.Run(loginCtx, executable, []string{"login", "status"}, "", workspace)
	cancelLogin()
	if loginErr != nil || loginRes.ExitCode != 0 {
		reason := strings.TrimSpace(firstNonEmptyImage(loginRes.Stderr, loginRes.Stdout, errorStringImage(loginErr)))
		if reason == "" {
			reason = "codex login status failed"
		}
		return nil, "codex unavailable or unauthenticated: " + truncateStr(reason, 300), nil
	}

	lastMsg, err := os.CreateTemp("", "prizm-codex-image-*.json")
	if err != nil {
		return nil, fmt.Sprintf("codex temp output failed: %v", err), nil
	}
	lastPath := lastMsg.Name()
	_ = lastMsg.Close()
	defer os.Remove(lastPath)

	args := []string{
		"exec",
		"--cd", workspace,
		"--sandbox", "read-only",
		"--skip-git-repo-check",
		"--ephemeral",
		"--output-last-message", lastPath,
		"--color", "never",
	}
	if model := strings.TrimSpace(t.Config.CodexModel); model != "" {
		args = append(args, "--model", model)
	}
	if profile := strings.TrimSpace(t.Config.CodexProfile); profile != "" {
		args = append(args, "--profile", profile)
	}
	args = append(args, "-")

	execCtx, cancelExec := context.WithTimeout(ctx, t.Config.codexTimeout())
	defer cancelExec()
	res, runErr := runner.Run(execCtx, executable, args, codexImagePrompt(prompt, count), workspace)
	lastText := strings.TrimSpace(readImageTextIfExists(lastPath))
	if lastText == "" {
		lastText = strings.TrimSpace(res.Stdout)
	}
	if runErr != nil && lastText == "" {
		reason := strings.TrimSpace(firstNonEmptyImage(res.Stderr, res.Stdout, errorStringImage(runErr)))
		return nil, "codex exec failed: " + truncateStr(reason, 300), nil
	}

	candidates, reason := parseImageCandidatesFromText(lastText)
	if len(candidates) == 0 {
		if reason == "" {
			reason = "codex returned no usable image data"
		}
		return nil, reason, nil
	}
	paths, errs := saveImageCandidates(ctx, candidates, count, dir, prefix, 0, client)
	if len(paths) == 0 {
		return nil, "codex image data could not be saved", errs
	}
	return paths, "", errs
}

func (t *CollectReferenceImagesTool) collectWithSearch(ctx context.Context, prompt string, count int, dir, prefix string, offset int, client *http.Client) ([]string, []string, error) {
	sources := t.Config.imageSearchSources()
	if len(sources) == 0 {
		return nil, nil, fmt.Errorf("image search is not configured - set PRIZM_IMAGE_SEARCH_URL or PRIZM_WEBSEARCH_URL")
	}
	var errs []string
	for _, source := range sources {
		candidates, err := searchImageCandidates(ctx, client, source, prompt)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		paths, saveErrs := saveImageCandidates(ctx, candidates, count, dir, prefix, offset, client)
		errs = append(errs, saveErrs...)
		if len(paths) > 0 {
			return paths, errs, nil
		}
	}
	if len(errs) > 0 {
		return nil, errs, fmt.Errorf("image search returned no downloadable images: %s", strings.Join(errs, "; "))
	}
	return nil, nil, fmt.Errorf("image search returned no downloadable images")
}

func (osImageCommandRunner) Run(ctx context.Context, executable string, args []string, stdin, cwd string) (ImageCommandResult, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		exitCode = 1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}
	return ImageCommandResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitCode}, err
}

func resolveImageOutputDir(cfg ImageToolsConfig, input map[string]any) (string, error) {
	dir := strings.TrimSpace(cfg.referencesDir())
	if input != nil {
		if override, ok := input["output_dir"].(string); ok && strings.TrimSpace(override) != "" {
			dir = strings.TrimSpace(override)
		}
	}
	if dir == "" {
		dir = defaultReferencesDir
	}
	if ContainsPathTraversal(dir) {
		return "", fmt.Errorf("output_dir path traversal blocked: %q", dir)
	}
	if strings.TrimSpace(cfg.WorkspaceRoot) != "" {
		return ResolveToolPath(ToolPaths{WorkspaceRoot: cfg.WorkspaceRoot, AllowedPaths: cfg.AllowedPaths}, dir)
	}
	abs, err := filepath.Abs(filepath.Clean(dir))
	if err != nil {
		return "", fmt.Errorf("resolve output_dir: %w", err)
	}
	return abs, nil
}

func (c ImageToolsConfig) imageSearchSources() []imageSearchSource {
	var sources []imageSearchSource
	if endpoint := strings.TrimSpace(firstNonEmptyImage(c.ImageSearchEndpoint, os.Getenv("PRIZM_IMAGE_SEARCH_URL"))); endpoint != "" {
		sources = append(sources, imageSearchSource{
			Endpoint:   endpoint,
			APIKey:     firstNonEmptyImage(c.ImageSearchKey, os.Getenv("PRIZM_IMAGE_SEARCH_KEY")),
			QueryParam: firstNonEmptyImage(c.ImageSearchQueryParam, os.Getenv("PRIZM_IMAGE_SEARCH_QUERY_PARAM"), "q"),
		})
	}
	if endpoint := strings.TrimSpace(os.Getenv("PRIZM_WEBSEARCH_URL")); endpoint != "" && !sameEndpoint(endpoint, sources) {
		sources = append(sources, imageSearchSource{
			Endpoint:   endpoint,
			APIKey:     os.Getenv("PRIZM_WEBSEARCH_KEY"),
			QueryParam: firstNonEmptyImage(os.Getenv("PRIZM_WEBSEARCH_QUERY_PARAM"), "q"),
		})
	}
	return sources
}

func (c ImageToolsConfig) codexExecutable() string {
	if strings.TrimSpace(c.CodexExecutable) != "" {
		return strings.TrimSpace(c.CodexExecutable)
	}
	if runtime.GOOS == "windows" {
		return "codex.cmd"
	}
	return "codex"
}

func (c ImageToolsConfig) resolveCodexExecutable() (string, error) {
	executable := c.codexExecutable()
	if filepath.IsAbs(executable) || strings.ContainsAny(executable, `/\`) {
		if _, err := os.Stat(executable); err != nil {
			return "", err
		}
		return executable, nil
	}
	return c.lookPath()(executable)
}

func (c ImageToolsConfig) codexWorkspace() string {
	if strings.TrimSpace(c.CodexWorkspace) != "" {
		return strings.TrimSpace(c.CodexWorkspace)
	}
	if strings.TrimSpace(c.WorkspaceRoot) != "" {
		return strings.TrimSpace(c.WorkspaceRoot)
	}
	return "."
}

func (c ImageToolsConfig) codexTimeout() time.Duration {
	if c.CodexTimeout > 0 {
		return c.CodexTimeout
	}
	return defaultCodexImageTimeout
}

func (c ImageToolsConfig) codexRunner() ImageCommandRunner {
	if c.CodexRunner != nil {
		return c.CodexRunner
	}
	return osImageCommandRunner{}
}

func (c ImageToolsConfig) lookPath() imageLookPathFunc {
	if c.CodexLookPath != nil {
		return c.CodexLookPath
	}
	return exec.LookPath
}

func requestedReferenceCount(value any) int {
	count := 1
	switch v := value.(type) {
	case float64:
		count = int(v)
	case int:
		count = v
	case int64:
		count = int(v)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			count = int(n)
		}
	}
	if count < 1 {
		count = 1
	}
	if count > maxReferenceImages {
		count = maxReferenceImages
	}
	return count
}

func referenceImageOutput(prompt string, paths []string, source string, codexAttempted bool, fallbackReason string, errs []string) map[string]any {
	out := map[string]any{
		"prompt":          prompt,
		"paths":           paths,
		"count":           len(paths),
		"source":          source,
		"codex_attempted": codexAttempted,
	}
	if fallbackReason != "" {
		out["fallback_reason"] = fallbackReason
	}
	if len(errs) > 0 {
		out["errors"] = errs
	}
	return out
}

func codexImagePrompt(prompt string, count int) string {
	payload := map[string]any{
		"task":   "collect_reference_images",
		"prompt": prompt,
		"count":  count,
		"contract": map[string]any{
			"available": true,
			"images": []map[string]string{{
				"b64_json":     "base64 image bytes, optional when url is set",
				"url":          "https image url, optional when b64_json is set",
				"content_type": "image/png",
				"filename":     "reference.png",
			}},
		},
	}
	b, _ := json.Marshal(payload)
	return "Prizm Scout needs reference images. If this Codex runtime has an image generation service/tool available, use it and return ONLY JSON matching the contract below. If no image service/tool is available, return ONLY {\"available\":false,\"images\":[],\"reason\":\"image output unavailable\"}. Do not include markdown or commentary.\n" + string(b)
}

func parseImageCandidatesFromText(text string) ([]imageCandidate, string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, "codex returned an empty response"
	}
	for _, candidateJSON := range possibleJSONPayloads(text) {
		var payload any
		if err := json.Unmarshal([]byte(candidateJSON), &payload); err != nil {
			continue
		}
		reason := jsonReason(payload)
		candidates := extractImageCandidates(payload)
		if len(candidates) > 0 {
			return candidates, ""
		}
		if reason != "" {
			return nil, "codex image output unavailable: " + reason
		}
	}
	return nil, "codex response did not match the expected image JSON contract"
}

func possibleJSONPayloads(text string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	add(text)
	if strings.Contains(text, "```") {
		parts := strings.Split(text, "```")
		for i := 1; i < len(parts); i += 2 {
			block := strings.TrimSpace(parts[i])
			block = strings.TrimPrefix(block, "json")
			add(block)
		}
	}
	if start, end := strings.Index(text, "{"), strings.LastIndex(text, "}"); start >= 0 && end > start {
		add(text[start : end+1])
	}
	if start, end := strings.Index(text, "["), strings.LastIndex(text, "]"); start >= 0 && end > start {
		add(text[start : end+1])
	}
	return out
}

func jsonReason(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range []string{"reason", "message", "error"} {
		if s, ok := m[key].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	if available, ok := m["available"].(bool); ok && !available {
		return "available=false"
	}
	return ""
}

func isFirecrawlEndpoint(endpoint string) bool {
	return strings.Contains(strings.ToLower(endpoint), "firecrawl.dev")
}

func searchImageCandidates(ctx context.Context, client *http.Client, source imageSearchSource, prompt string) ([]imageCandidate, error) {
	var req *http.Request
	var err error
	if isFirecrawlEndpoint(source.Endpoint) {
		// Firecrawl search API is a POST with a JSON body; request the "images"
		// source so results carry imageUrl fields the JSON walker recognizes.
		body, _ := json.Marshal(map[string]any{
			"query":   prompt,
			"sources": []string{"images"},
			"limit":   10,
		})
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, source.Endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
	} else {
		u, perr := url.Parse(source.Endpoint)
		if perr != nil {
			return nil, fmt.Errorf("invalid image search url: %w", perr)
		}
		q := u.Query()
		q.Set(firstNonEmptyImage(source.QueryParam, "q"), prompt)
		u.RawQuery = q.Encode()
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
	}
	req.Header.Set("Accept", "application/json")
	if strings.TrimSpace(source.APIKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(source.APIKey))
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("image search request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("image search returned %d: %s", resp.StatusCode, truncateStr(string(body), 500))
	}
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("image search response was not JSON: %w", err)
	}
	candidates := extractImageCandidates(parsed)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("image search JSON had no likely image URLs")
	}
	return candidates, nil
}

func extractImageCandidates(v any) []imageCandidate {
	var out []imageCandidate
	walkImageJSON(v, "", &out)
	return dedupeImageCandidates(out)
}

func walkImageJSON(v any, key string, out *[]imageCandidate) {
	switch x := v.(type) {
	case map[string]any:
		if c, ok := imageCandidateFromMap(x); ok {
			*out = append(*out, c)
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			walkImageJSON(x[k], k, out)
		}
	case []any:
		for _, item := range x {
			walkImageJSON(item, key, out)
		}
	case string:
		if isImageURLKey(key) && isHTTPImageLike(x) {
			*out = append(*out, imageCandidate{URL: x})
		}
	}
}

func imageCandidateFromMap(m map[string]any) (imageCandidate, bool) {
	var c imageCandidate
	c.B64JSON = firstStringField(m, "b64_json", "b64", "base64")
	c.URL = firstStringField(m, "image_url", "imageUrl", "thumbnail_url", "thumbnailUrl", "thumbnail", "contentUrl", "src", "url")
	c.ContentType = firstStringField(m, "content_type", "contentType", "mime", "mimeType", "type")
	c.Filename = firstStringField(m, "filename", "file_name", "name", "title")
	if strings.HasPrefix(strings.TrimSpace(c.B64JSON), "data:image/") {
		c.URL = c.B64JSON
		c.B64JSON = ""
	}
	if c.B64JSON != "" {
		return c, true
	}
	if c.URL != "" && (isHTTPImageLike(c.URL) || strings.HasPrefix(strings.ToLower(strings.TrimSpace(c.ContentType)), "image/") || hasSpecificImageURLField(m)) {
		return c, true
	}
	return imageCandidate{}, false
}

func hasSpecificImageURLField(m map[string]any) bool {
	for _, key := range []string{"image_url", "imageUrl", "thumbnail_url", "thumbnailUrl", "thumbnail", "contentUrl", "src"} {
		if s, ok := m[key].(string); ok && strings.TrimSpace(s) != "" {
			return true
		}
	}
	return false
}

func saveImageCandidates(ctx context.Context, candidates []imageCandidate, count int, dir, prefix string, offset int, client *http.Client) ([]string, []string) {
	var paths []string
	var errs []string
	for i, candidate := range candidates {
		if len(paths) >= count {
			break
		}
		filename := imageCandidateFilename(candidate, prefix, offset+len(paths)+1)
		data, contentType, err := bytesFromImageCandidate(ctx, candidate, client)
		if err != nil {
			errs = append(errs, fmt.Sprintf("candidate %d: %v", i+1, err))
			continue
		}
		path, err := saveToReferences(dir, filename, contentType, data)
		if err != nil {
			errs = append(errs, fmt.Sprintf("candidate %d: %v", i+1, err))
			continue
		}
		paths = append(paths, path)
	}
	return paths, errs
}

func bytesFromImageCandidate(ctx context.Context, candidate imageCandidate, client *http.Client) ([]byte, string, error) {
	contentType := firstNonEmptyImage(candidate.ContentType, "image/png")
	if candidate.B64JSON != "" {
		data, ct, err := decodeImageBase64(candidate.B64JSON, contentType)
		return data, ct, err
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(candidate.URL)), "data:image/") {
		return decodeDataImageURL(candidate.URL)
	}
	if candidate.URL == "" {
		return nil, "", fmt.Errorf("candidate has no image data")
	}
	return downloadImage(ctx, client, candidate.URL, "")
}

func imageCandidateFilename(candidate imageCandidate, prefix string, index int) string {
	if prefix != "" {
		return fmt.Sprintf("%s-%d%s", safeSlug(prefix, 48), index, imageExtFromCandidate(candidate))
	}
	if strings.TrimSpace(candidate.Filename) != "" {
		return candidate.Filename
	}
	if candidate.URL != "" && !strings.HasPrefix(strings.ToLower(strings.TrimSpace(candidate.URL)), "data:") {
		return filenameFromURL(candidate.URL, candidate.ContentType)
	}
	return fmt.Sprintf("reference-%d%s", index, imageExtFromCandidate(candidate))
}

func imageExtFromCandidate(candidate imageCandidate) string {
	if ext := filepath.Ext(candidate.Filename); ext != "" && isSafeImageExt(ext) {
		return strings.ToLower(ext)
	}
	if candidate.URL != "" {
		if ext := filepath.Ext(strings.Split(candidate.URL, "?")[0]); ext != "" && isSafeImageExt(ext) {
			return strings.ToLower(ext)
		}
	}
	return extFromContentType(candidate.ContentType)
}

func decodeImageBase64(value, fallbackContentType string) ([]byte, string, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "data:image/") {
		return decodeDataImageURL(value)
	}
	compact := strings.NewReplacer("\n", "", "\r", "", "\t", "", " ", "").Replace(value)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if data, err := enc.DecodeString(compact); err == nil {
			return data, firstNonEmptyImage(fallbackContentType, "image/png"), nil
		}
	}
	return nil, "", fmt.Errorf("decode base64 image: invalid image data")
}

func decodeDataImageURL(value string) ([]byte, string, error) {
	parts := strings.SplitN(value, ",", 2)
	if len(parts) != 2 {
		return nil, "", fmt.Errorf("invalid data image url")
	}
	meta := strings.ToLower(strings.TrimSpace(parts[0]))
	if !strings.HasPrefix(meta, "data:image/") || !strings.Contains(meta, ";base64") {
		return nil, "", fmt.Errorf("unsupported data image url")
	}
	contentType := strings.TrimPrefix(strings.Split(meta, ";")[0], "data:")
	data, _, err := decodeImageBase64(parts[1], contentType)
	return data, contentType, err
}

func dedupeImageCandidates(candidates []imageCandidate) []imageCandidate {
	seen := map[string]bool{}
	out := make([]imageCandidate, 0, len(candidates))
	for _, c := range candidates {
		key := firstNonEmptyImage(c.URL, c.B64JSON)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	return out
}

func isImageURLKey(key string) bool {
	key = strings.ToLower(key)
	return key == "image" || key == "image_url" || key == "imageurl" || key == "thumbnail" || key == "thumbnail_url" || key == "thumbnailurl" || key == "contenturl" || key == "src"
}

func isHTTPImageLike(rawURL string) bool {
	rawURL = strings.TrimSpace(rawURL)
	lower := strings.ToLower(rawURL)
	if strings.HasPrefix(lower, "data:image/") {
		return true
	}
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	ext := strings.ToLower(filepath.Ext(parsed.Path))
	return isSafeImageExt(ext) || strings.Contains(lower, "image") || strings.Contains(lower, "thumbnail")
}

func isSafeImageExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif":
		return true
	default:
		return false
	}
}

func sameEndpoint(endpoint string, sources []imageSearchSource) bool {
	for _, source := range sources {
		if strings.TrimRight(source.Endpoint, "/") == strings.TrimRight(endpoint, "/") {
			return true
		}
	}
	return false
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func readImageTextIfExists(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func firstNonEmptyImage(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func errorStringImage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
