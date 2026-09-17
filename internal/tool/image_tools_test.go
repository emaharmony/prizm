package tool

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 1x1 PNG.
var onePixelPNG = mustB64("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+M8AAAMBAQDJ/pLvAAAAAElFTkSuQmCC")

func mustB64(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

func TestFetchImage_SavesToReferences(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(onePixelPNG)
	}))
	defer srv.Close()

	dir := t.TempDir()
	tool := &FetchImageTool{Config: ImageToolsConfig{ReferencesDir: dir}}
	res, err := tool.Execute(context.Background(), map[string]any{"url": srv.URL + "/art/goblin.png"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got: %s", res.Error)
	}
	path, _ := res.Output["path"].(string)
	if path == "" {
		t.Fatal("no path returned")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("saved file missing: %v", err)
	}
	if filepath.Ext(path) != ".png" {
		t.Errorf("expected .png, got %s", path)
	}
}

func TestFetchImage_MissingURL(t *testing.T) {
	tool := &FetchImageTool{Config: ImageToolsConfig{ReferencesDir: t.TempDir()}}
	res, _ := tool.Execute(context.Background(), map[string]any{})
	if res.Success {
		t.Fatal("expected failure for missing url")
	}
}

func TestGenerateImage_NotConfigured(t *testing.T) {
	// No endpoint set and no env → graceful "not configured".
	t.Setenv("PRIZM_IMAGEGEN_URL", "")
	tool := &GenerateImageTool{Config: ImageToolsConfig{ReferencesDir: t.TempDir()}}
	res, _ := tool.Execute(context.Background(), map[string]any{"prompt": "a goblin"})
	if res.Success {
		t.Fatal("expected not-configured failure")
	}
	if !strings.Contains(res.Error, "not configured") {
		t.Errorf("expected 'not configured', got: %s", res.Error)
	}
}

func TestGenerateImage_RawImageResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(onePixelPNG)
	}))
	defer srv.Close()

	dir := t.TempDir()
	tool := &GenerateImageTool{Config: ImageToolsConfig{ReferencesDir: dir, ImageGenEndpoint: srv.URL}}
	res, err := tool.Execute(context.Background(), map[string]any{"prompt": "a stylized goblin warrior"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got: %s", res.Error)
	}
	if p, _ := res.Output["path"].(string); p == "" {
		t.Fatal("no path returned")
	}
}

func TestGenerateImage_JSONBase64Response(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"b64_json": base64.StdEncoding.EncodeToString(onePixelPNG)}},
		})
	}))
	defer srv.Close()

	dir := t.TempDir()
	tool := &GenerateImageTool{Config: ImageToolsConfig{ReferencesDir: dir, ImageGenEndpoint: srv.URL}}
	res, err := tool.Execute(context.Background(), map[string]any{"prompt": "concept art"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got: %s", res.Error)
	}
	path, _ := res.Output["path"].(string)
	got, _ := os.ReadFile(path)
	if len(got) != len(onePixelPNG) {
		t.Errorf("decoded image size mismatch: got %d want %d", len(got), len(onePixelPNG))
	}
}

func TestAnalyzeImage_VisionResponse(t *testing.T) {
	var gotImages int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("expected /api/chat, got %s", r.URL.Path)
		}
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Images []string `json:"images"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if len(body.Messages) > 0 {
			gotImages = len(body.Messages[0].Images)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]any{"content": "A green goblin warrior, painterly style, earthy palette."},
		})
	}))
	defer srv.Close()

	// Local image file to analyze.
	dir := t.TempDir()
	imgPath := filepath.Join(dir, "ref.png")
	os.WriteFile(imgPath, onePixelPNG, 0o644)

	tool := &AnalyzeImageTool{Config: ImageToolsConfig{VisionBaseURL: srv.URL, VisionModel: "llama3.2-vision:11b"}}
	res, err := tool.Execute(context.Background(), map[string]any{"image": imgPath})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got: %s", res.Error)
	}
	if gotImages != 1 {
		t.Errorf("expected 1 base64 image sent, got %d", gotImages)
	}
	desc, _ := res.Output["description"].(string)
	if !strings.Contains(desc, "goblin") {
		t.Errorf("unexpected description: %q", desc)
	}
}

func TestAnalyzeImage_MissingImage(t *testing.T) {
	tool := &AnalyzeImageTool{Config: ImageToolsConfig{}}
	res, _ := tool.Execute(context.Background(), map[string]any{})
	if res.Success {
		t.Fatal("expected failure for missing image")
	}
}

func TestRegisterImageTools(t *testing.T) {
	reg := NewRegistry()
	RegisterImageTools(reg, ImageToolsConfig{})
	for _, name := range []string{"fetch_image", "generate_image", "analyze_image", "collect_reference_images"} {
		if _, err := reg.Resolve(name); err != nil {
			t.Errorf("tool %q not registered: %v", name, err)
		}
	}
}

func TestImageToolsConfig_Defaults(t *testing.T) {
	t.Setenv("PRIZM_IMAGE_DIR", "")
	t.Setenv("PRIZM_VISION_MODEL", "")
	t.Setenv("PRIZM_VISION_URL", "")
	t.Setenv("OLLAMA_BASE_URL", "")
	c := ImageToolsConfig{}
	if c.referencesDir() != defaultReferencesDir {
		t.Errorf("referencesDir default = %q", c.referencesDir())
	}
	if c.visionModel() != defaultVisionModel {
		t.Errorf("visionModel default = %q", c.visionModel())
	}
	if c.visionBaseURL() != "http://localhost:11434" {
		t.Errorf("visionBaseURL default = %q", c.visionBaseURL())
	}
}

type fakeImageRunner struct {
	results     []ImageCommandResult
	errs        []error
	lastMessage string
	calls       [][]string
}

func (f *fakeImageRunner) Run(_ context.Context, _ string, args []string, _ string, _ string) (ImageCommandResult, error) {
	copied := append([]string(nil), args...)
	f.calls = append(f.calls, copied)
	if f.lastMessage != "" {
		for i, arg := range args {
			if arg == "--output-last-message" && i+1 < len(args) {
				_ = os.WriteFile(args[i+1], []byte(f.lastMessage), 0o644)
			}
		}
	}
	idx := len(f.calls) - 1
	var res ImageCommandResult
	if idx < len(f.results) {
		res = f.results[idx]
	}
	var err error
	if idx < len(f.errs) {
		err = f.errs[idx]
	}
	if err != nil && res.ExitCode == 0 {
		res.ExitCode = 1
	}
	return res, err
}

func TestFetchImage_OutputDirWithinWorkspace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(onePixelPNG)
	}))
	defer srv.Close()

	workspace := t.TempDir()
	tool := &FetchImageTool{Config: ImageToolsConfig{ReferencesDir: filepath.Join(workspace, "references"), WorkspaceRoot: workspace}}
	res, err := tool.Execute(context.Background(), map[string]any{
		"url":        srv.URL + "/ref.png",
		"output_dir": "delegated_refs",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got: %s", res.Error)
	}
	path, _ := res.Output["path"].(string)
	wantPrefix := filepath.Join(workspace, "delegated_refs")
	if !strings.HasPrefix(path, wantPrefix) {
		t.Fatalf("path %q not under %q", path, wantPrefix)
	}
}

func TestFetchImage_RejectsUnsafeOutputDir(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(onePixelPNG)
	}))
	defer srv.Close()

	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	tool := &FetchImageTool{Config: ImageToolsConfig{ReferencesDir: filepath.Join(workspace, "references"), WorkspaceRoot: workspace}}
	res, _ := tool.Execute(context.Background(), map[string]any{"url": srv.URL + "/ref.png", "output_dir": outside})
	if res.Success {
		t.Fatalf("expected unsafe output_dir to fail")
	}
	if !strings.Contains(res.Error, "not within") {
		t.Fatalf("expected containment error, got %q", res.Error)
	}
}

func TestSaveToReferences_UniqueNames(t *testing.T) {
	dir := t.TempDir()
	first, err := saveToReferences(dir, "same.png", "image/png", onePixelPNG)
	if err != nil {
		t.Fatalf("first save: %v", err)
	}
	second, err := saveToReferences(dir, "same.png", "image/png", onePixelPNG)
	if err != nil {
		t.Fatalf("second save: %v", err)
	}
	if first == second {
		t.Fatalf("expected unique paths, got %q", first)
	}
	if _, err := os.Stat(second); err != nil {
		t.Fatalf("second file missing: %v", err)
	}
}

func TestCollectReferenceImages_CodexBase64Success(t *testing.T) {
	dir := t.TempDir()
	runner := &fakeImageRunner{results: []ImageCommandResult{
		{},
		{Stdout: `{"available":true,"images":[{"b64_json":"` + base64.StdEncoding.EncodeToString(onePixelPNG) + `","content_type":"image/png","filename":"codex.png"}]}`},
	}}
	tool := &CollectReferenceImagesTool{Config: ImageToolsConfig{
		ReferencesDir:  dir,
		WorkspaceRoot:  dir,
		CodexRunner:    runner,
		CodexLookPath:  func(string) (string, error) { return "codex.cmd", nil },
		CodexWorkspace: dir,
	}}
	res, err := tool.Execute(context.Background(), map[string]any{"prompt": "blue crystal", "count": float64(1)})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got %s", res.Error)
	}
	if got, _ := res.Output["source"].(string); got != "codex" {
		t.Fatalf("source = %q, want codex", got)
	}
	paths, _ := res.Output["paths"].([]string)
	if len(paths) != 1 {
		t.Fatalf("paths = %#v", res.Output["paths"])
	}
	if _, err := os.Stat(paths[0]); err != nil {
		t.Fatalf("saved file missing: %v", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("expected login and exec calls, got %d", len(runner.calls))
	}
}

func TestCollectReferenceImages_CodexURLSuccess(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(onePixelPNG)
	}))
	defer srv.Close()

	runner := &fakeImageRunner{results: []ImageCommandResult{
		{},
		{Stdout: `{"images":[{"url":"` + srv.URL + `/codex-url.png","content_type":"image/png"}]}`},
	}}
	tool := &CollectReferenceImagesTool{Config: ImageToolsConfig{
		ReferencesDir:  dir,
		WorkspaceRoot:  dir,
		CodexRunner:    runner,
		CodexLookPath:  func(string) (string, error) { return "codex.cmd", nil },
		CodexWorkspace: dir,
	}}
	res, _ := tool.Execute(context.Background(), map[string]any{"prompt": "red ship"})
	if !res.Success {
		t.Fatalf("expected success, got %s", res.Error)
	}
	if got, _ := res.Output["source"].(string); got != "codex" {
		t.Fatalf("source = %q, want codex", got)
	}
}

func TestCollectReferenceImages_CodexUnavailableFallsBackToSearch(t *testing.T) {
	dir := t.TempDir()
	srv := imageSearchTestServer(t)
	defer srv.Close()
	t.Setenv("PRIZM_IMAGE_SEARCH_URL", srv.URL+"/search")
	t.Setenv("PRIZM_WEBSEARCH_URL", "")

	tool := &CollectReferenceImagesTool{Config: ImageToolsConfig{
		ReferencesDir: dir,
		WorkspaceRoot: dir,
		CodexLookPath: func(string) (string, error) { return "", errors.New("codex missing") },
	}}
	res, _ := tool.Execute(context.Background(), map[string]any{"prompt": "tree", "count": float64(1)})
	if !res.Success {
		t.Fatalf("expected fallback success, got %s", res.Error)
	}
	if got, _ := res.Output["source"].(string); got != "search" {
		t.Fatalf("source = %q, want search", got)
	}
	if attempted, _ := res.Output["codex_attempted"].(bool); !attempted {
		t.Fatalf("codex_attempted should be true")
	}
	if reason, _ := res.Output["fallback_reason"].(string); !strings.Contains(reason, "codex unavailable") {
		t.Fatalf("fallback_reason = %q", reason)
	}
}

func TestCollectReferenceImages_CodexNoImagesFallsBackToSearch(t *testing.T) {
	dir := t.TempDir()
	srv := imageSearchTestServer(t)
	defer srv.Close()
	t.Setenv("PRIZM_IMAGE_SEARCH_URL", srv.URL+"/search")
	t.Setenv("PRIZM_WEBSEARCH_URL", "")

	runner := &fakeImageRunner{results: []ImageCommandResult{
		{},
		{Stdout: `{"available":false,"images":[],"reason":"no image service"}`},
	}}
	tool := &CollectReferenceImagesTool{Config: ImageToolsConfig{
		ReferencesDir:  dir,
		WorkspaceRoot:  dir,
		CodexRunner:    runner,
		CodexLookPath:  func(string) (string, error) { return "codex.cmd", nil },
		CodexWorkspace: dir,
	}}
	res, _ := tool.Execute(context.Background(), map[string]any{"prompt": "tree", "count": float64(1)})
	if !res.Success {
		t.Fatalf("expected fallback success, got %s", res.Error)
	}
	if got, _ := res.Output["source"].(string); got != "search" {
		t.Fatalf("source = %q, want search", got)
	}
	if reason, _ := res.Output["fallback_reason"].(string); !strings.Contains(reason, "no image service") {
		t.Fatalf("fallback_reason = %q", reason)
	}
}

func TestCollectReferenceImages_ConfiguredSearchDownload(t *testing.T) {
	dir := t.TempDir()
	srv := imageSearchTestServer(t)
	defer srv.Close()

	tool := &CollectReferenceImagesTool{Config: ImageToolsConfig{
		ReferencesDir:         dir,
		WorkspaceRoot:         dir,
		ImageSearchEndpoint:   srv.URL + "/search",
		ImageSearchQueryParam: "query",
	}}
	res, _ := tool.Execute(context.Background(), map[string]any{"prompt": "lamp", "prefer_codex": false})
	if !res.Success {
		t.Fatalf("expected success, got %s", res.Error)
	}
	paths, _ := res.Output["paths"].([]string)
	if len(paths) != 1 {
		t.Fatalf("paths = %#v", res.Output["paths"])
	}
}

func TestCollectReferenceImages_SearchNotConfigured(t *testing.T) {
	t.Setenv("PRIZM_IMAGE_SEARCH_URL", "")
	t.Setenv("PRIZM_WEBSEARCH_URL", "")
	dir := t.TempDir()
	tool := &CollectReferenceImagesTool{Config: ImageToolsConfig{ReferencesDir: dir, WorkspaceRoot: dir}}
	res, _ := tool.Execute(context.Background(), map[string]any{"prompt": "lamp", "prefer_codex": false})
	if res.Success {
		t.Fatalf("expected not-configured failure")
	}
	if !strings.Contains(res.Error, "not configured") {
		t.Fatalf("error = %q", res.Error)
	}
}

func imageSearchTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"results": []map[string]any{{"image_url": "http://" + r.Host + "/images/ref.png", "content_type": "image/png"}},
			})
		case "/images/ref.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(onePixelPNG)
		default:
			http.NotFound(w, r)
		}
	}))
}
