// Package autopatch diagnoses bug reports and produces reviewable patch artifacts.
package autopatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/gitx"
	"github.com/emaharmony/prizm/internal/task"
	"github.com/emaharmony/prizm/internal/validation"
	"github.com/rs/xid"
)

const (
	StatusProposed         = "proposed"
	StatusPROpened         = "pr_opened"
	StatusValidationFailed = "validation_failed"
	StatusFailed           = "failed"
)

var (
	ErrDisabled = errors.New("autopatch is disabled")
	// ErrDirtyWorktree aliases gitx.ErrDirtyWorktree (V56 lifted the worktree
	// helpers into internal/gitx) so errors.Is works across both packages.
	ErrDirtyWorktree = gitx.ErrDirtyWorktree
	ErrNoWorker      = errors.New("no autopatch worker is available")
)

// Request describes a bug or validation failure to diagnose and patch.
type Request struct {
	Description        string   `json:"description"`
	Source             string   `json:"source,omitempty"`
	ValidationProfiles []string `json:"validation_profiles,omitempty"`
	RunID              string   `json:"run_id,omitempty"`
	ImprovementID      string   `json:"improvement_id,omitempty"`
	SubmittedBy        string   `json:"submitted_by,omitempty"`
}

// Config controls autopatch execution.
type Config struct {
	Enabled              bool
	Mode                 string
	RequireCleanWorktree bool
	MaxAttempts          int
	ValidationProfiles   []string
	WorkerOrder          []string
	LocalAgent           string
	Root                 string
	WorktreeRoot         string
	ArtifactRoot         string
	BaseBranch           string // PR base branch (empty → repo default)
	Store                *task.Store
	Registry             *validation.Registry
	Workers              map[string]PatchWorker
	PROpener             PROpener // opens a PR in "pr" mode; nil → gh-based default
	Emit                 func(eventType, source string, payload map[string]any)
}

// Result is persisted into the task result and result.json artifact.
type Result struct {
	TaskID            string              `json:"task_id"`
	Status            string              `json:"status"`
	Diagnosis         string              `json:"diagnosis,omitempty"`
	WorkerUsed        string              `json:"worker_used,omitempty"`
	Attempts          []AttemptResult     `json:"attempts"`
	PatchPath         string              `json:"patch_path,omitempty"`
	DiffStatPath      string              `json:"diff_stat_path,omitempty"`
	DiffStat          string              `json:"diff_stat,omitempty"`
	ValidationResults []validation.Result `json:"validation_results,omitempty"`
	ReviewPath        string              `json:"review_path,omitempty"`
	Branch            string              `json:"branch,omitempty"`
	PRURL             string              `json:"pr_url,omitempty"`
	Artifacts         []string            `json:"artifacts,omitempty"`
	Error             string              `json:"error,omitempty"`
	DurationMS        int64               `json:"duration_ms"`
}

// AttemptResult records one worker attempt.
type AttemptResult struct {
	Attempt           int                 `json:"attempt"`
	Worker            string              `json:"worker"`
	Status            string              `json:"status"`
	PatchPath         string              `json:"patch_path,omitempty"`
	DiffStatPath      string              `json:"diff_stat_path,omitempty"`
	DiffStat          string              `json:"diff_stat,omitempty"`
	ValidationResults []validation.Result `json:"validation_results,omitempty"`
	Error             string              `json:"error,omitempty"`
}

// WorkerRequest is passed to a patch author.
type WorkerRequest struct {
	TaskID      string
	Attempt     int
	Worktree    string
	ArtifactDir string
	Description string
	Feedback    string
}

// WorkerResult is returned by a patch author.
type WorkerResult struct {
	Worker    string
	Diagnosis string
	Artifacts []string
}

// PatchWorker generates or applies a patch inside the provided worktree.
type PatchWorker interface {
	Name() string
	Run(ctx context.Context, req WorkerRequest) (WorkerResult, error)
}

// Service creates tasks and runs autopatch jobs asynchronously.
type Service struct {
	cfg Config
}

// NewService creates an autopatch service with normalized defaults.
func NewService(cfg Config) *Service {
	cfg = NormalizeConfig(cfg)
	return &Service{cfg: cfg}
}

// Enabled reports whether the service accepts new requests.
func (s *Service) Enabled() bool {
	return s != nil && s.cfg.Enabled
}

// NormalizeConfig applies safe defaults.
func NormalizeConfig(cfg Config) Config {
	if cfg.Mode == "" {
		cfg.Mode = "propose"
	}
	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = 2
	}
	if len(cfg.ValidationProfiles) == 0 {
		cfg.ValidationProfiles = []string{"go_test_all"}
	}
	if len(cfg.WorkerOrder) == 0 {
		cfg.WorkerOrder = []string{"codex", "local_agent"}
	}
	if cfg.Root == "" {
		cfg.Root = "."
	}
	if cfg.WorktreeRoot == "" {
		cfg.WorktreeRoot = filepath.Join(".prizm", "worktrees")
	}
	if cfg.ArtifactRoot == "" {
		cfg.ArtifactRoot = filepath.Join(".prizm", "data", "autopatch")
	}
	if cfg.Registry == nil {
		cfg.Registry = validation.NewRegistry()
	}
	if cfg.Workers == nil {
		cfg.Workers = map[string]PatchWorker{}
	}
	if cfg.Mode == "pr" && cfg.PROpener == nil {
		cfg.PROpener = ghPROpener{}
	}
	return cfg
}

// Start creates a task and runs it in the background.
func (s *Service) Start(ctx context.Context, req Request) (*task.Task, error) {
	if s == nil || !s.cfg.Enabled {
		return nil, ErrDisabled
	}
	if s.cfg.Store == nil {
		return nil, fmt.Errorf("autopatch task store is unavailable")
	}
	if strings.TrimSpace(req.Description) == "" {
		return nil, fmt.Errorf("description is required")
	}
	if s.cfg.RequireCleanWorktree {
		if err := ensureCleanWorktree(ctx, s.cfg.Root); err != nil {
			return nil, err
		}
	}

	now := time.Now()
	taskID := "autopatch-" + xid.New().String()
	if req.Source == "" {
		req.Source = "manual"
	}
	t := &task.Task{
		ID:          taskID,
		Type:        "auto_patch",
		Status:      task.StatusCreated,
		DelegatedBy: firstNonEmpty(req.SubmittedBy, "autopatch"),
		DelegatedTo: "autopatch",
		Description: req.Description,
		Priority:    "normal",
		Context: map[string]any{
			"source":              req.Source,
			"run_id":              req.RunID,
			"improvement_id":      req.ImprovementID,
			"validation_profiles": req.ValidationProfiles,
			"mode":                s.cfg.Mode,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.cfg.Store.Create(t); err != nil {
		return nil, err
	}
	s.emit("prizm.autopatch.created", map[string]any{"task_id": taskID, "source": req.Source})
	go s.runTask(taskID, req)
	return t, nil
}

func (s *Service) runTask(taskID string, req Request) {
	_ = s.cfg.Store.UpdateStatus(taskID, task.StatusInProgress, nil)
	result, err := s.Run(context.Background(), taskID, req)
	if err != nil {
		if result.TaskID == "" {
			result.TaskID = taskID
		}
		result.Status = StatusFailed
		result.Error = err.Error()
		_ = s.cfg.Store.UpdateStatus(taskID, task.StatusFailed, resultToMap(result))
		s.emit("prizm.autopatch.failed", map[string]any{"task_id": taskID, "error": err.Error()})
		return
	}
	if result.Status == StatusPROpened {
		_ = s.cfg.Store.UpdateStatus(taskID, task.StatusCompleted, resultToMap(result))
		s.emit("prizm.autopatch.pr_opened", map[string]any{"task_id": taskID, "pr_url": result.PRURL, "branch": result.Branch})
		return
	}
	if result.Status == StatusProposed {
		_ = s.cfg.Store.UpdateStatus(taskID, task.StatusCompleted, resultToMap(result))
		s.emit("prizm.autopatch.proposed", map[string]any{"task_id": taskID, "patch_path": result.PatchPath})
		return
	}
	_ = s.cfg.Store.UpdateStatus(taskID, task.StatusFailed, resultToMap(result))
	s.emit("prizm.autopatch.validation_failed", map[string]any{"task_id": taskID, "patch_path": result.PatchPath})
}

// Run executes an autopatch task synchronously. Tests can call this directly.
func (s *Service) Run(ctx context.Context, taskID string, req Request) (Result, error) {
	start := time.Now()
	result := Result{TaskID: taskID}
	cfg := s.cfg
	profiles := req.ValidationProfiles
	if len(profiles) == 0 {
		profiles = append([]string(nil), cfg.ValidationProfiles...)
	}

	root, err := filepath.Abs(cfg.Root)
	if err != nil {
		return result, err
	}
	if cfg.RequireCleanWorktree {
		if err := ensureCleanWorktree(ctx, root); err != nil {
			return result, err
		}
	}

	artifactDir := resolveUnder(root, cfg.ArtifactRoot)
	artifactDir = filepath.Join(artifactDir, safeID(taskID))
	if err := os.MkdirAll(artifactDir, 0755); err != nil {
		return result, fmt.Errorf("create artifact dir: %w", err)
	}
	requestPath := filepath.Join(artifactDir, "request.json")
	writeJSONFile(requestPath, req)
	result.Artifacts = append(result.Artifacts, requestPath)

	worktreeRoot := resolveUnder(root, cfg.WorktreeRoot)
	worktreePath := filepath.Join(worktreeRoot, safeID(taskID))
	if err := createWorktree(ctx, root, worktreePath); err != nil {
		return result, err
	}
	defer removeWorktree(context.Background(), root, worktreePath)

	feedback := ""
	var lastErr error
	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		attemptDir := filepath.Join(artifactDir, fmt.Sprintf("attempt-%d", attempt))
		_ = os.MkdirAll(attemptDir, 0755)

		worker, workerErr := s.runFirstAvailableWorker(ctx, WorkerRequest{
			TaskID:      taskID,
			Attempt:     attempt,
			Worktree:    worktreePath,
			ArtifactDir: attemptDir,
			Description: req.Description,
			Feedback:    feedback,
		})
		attemptResult := AttemptResult{Attempt: attempt, Worker: worker.Worker, Status: "worker_completed"}
		result.WorkerUsed = firstNonEmpty(result.WorkerUsed, worker.Worker)
		result.Diagnosis = firstNonEmpty(result.Diagnosis, worker.Diagnosis)
		result.Artifacts = append(result.Artifacts, worker.Artifacts...)
		if workerErr != nil {
			attemptResult.Status = StatusFailed
			attemptResult.Error = workerErr.Error()
			result.Attempts = append(result.Attempts, attemptResult)
			lastErr = workerErr
			continue
		}

		diff, stat, diffErr := captureDiff(ctx, worktreePath)
		if diffErr != nil {
			attemptResult.Status = StatusFailed
			attemptResult.Error = diffErr.Error()
			result.Attempts = append(result.Attempts, attemptResult)
			lastErr = diffErr
			continue
		}
		if strings.TrimSpace(diff) == "" {
			err := fmt.Errorf("worker produced no diff")
			attemptResult.Status = StatusFailed
			attemptResult.Error = err.Error()
			result.Attempts = append(result.Attempts, attemptResult)
			lastErr = err
			continue
		}

		patchPath := filepath.Join(attemptDir, "git-diff.patch")
		statPath := filepath.Join(attemptDir, "git-diff-stat.txt")
		_ = os.WriteFile(patchPath, []byte(diff), 0644)
		_ = os.WriteFile(statPath, []byte(stat), 0644)
		attemptResult.PatchPath = patchPath
		attemptResult.DiffStatPath = statPath
		attemptResult.DiffStat = strings.TrimSpace(stat)
		result.PatchPath = patchPath
		result.DiffStatPath = statPath
		result.DiffStat = strings.TrimSpace(stat)
		result.Artifacts = append(result.Artifacts, patchPath, statPath)

		validations := runValidations(ctx, cfg.Registry, worktreePath, attemptDir, profiles, taskID)
		attemptResult.ValidationResults = validations
		result.ValidationResults = validations
		if validationsPassed(validations) {
			result.Status = StatusProposed
			result.PatchPath, result.DiffStatPath = copyFinalDiffs(artifactDir, diff, stat)
			result.Artifacts = append(result.Artifacts, result.PatchPath, result.DiffStatPath)
			// "pr" mode: turn the validated patch into a real pull request. A PR
			// failure keeps the proposed patch (records the error) rather than
			// discarding good work.
			s.maybeOpenPR(ctx, cfg, req, taskID, worktreePath, &result)
			result.ReviewPath = writeReview(artifactDir, result)
			result.Artifacts = append(result.Artifacts, result.ReviewPath)
			attemptResult.Status = StatusProposed
			result.Attempts = append(result.Attempts, attemptResult)
			result.DurationMS = time.Since(start).Milliseconds()
			writeJSONFile(filepath.Join(artifactDir, "result.json"), result)
			return result, nil
		}

		attemptResult.Status = StatusValidationFailed
		result.Attempts = append(result.Attempts, attemptResult)
		feedback = formatValidationFeedback(validations)
		lastErr = fmt.Errorf("validation failed")
	}

	result.Status = StatusValidationFailed
	if result.PatchPath == "" {
		result.Status = StatusFailed
		if lastErr != nil {
			result.Error = lastErr.Error()
		}
	}
	result.ReviewPath = writeReview(artifactDir, result)
	result.Artifacts = append(result.Artifacts, result.ReviewPath)
	result.DurationMS = time.Since(start).Milliseconds()
	writeJSONFile(filepath.Join(artifactDir, "result.json"), result)
	if result.Status == StatusFailed {
		return result, lastErr
	}
	return result, nil
}

func (s *Service) runFirstAvailableWorker(ctx context.Context, req WorkerRequest) (WorkerResult, error) {
	var errs []string
	for _, name := range s.cfg.WorkerOrder {
		worker := s.cfg.Workers[name]
		if worker == nil {
			errs = append(errs, name+": not configured")
			continue
		}
		res, err := worker.Run(ctx, req)
		if err == nil {
			if res.Worker == "" {
				res.Worker = worker.Name()
			}
			return res, nil
		}
		errs = append(errs, worker.Name()+": "+err.Error())
	}
	if len(errs) == 0 {
		return WorkerResult{}, ErrNoWorker
	}
	return WorkerResult{}, fmt.Errorf("%w: %s", ErrNoWorker, strings.Join(errs, "; "))
}

func (s *Service) emit(eventType string, payload map[string]any) {
	if s.cfg.Emit != nil {
		s.cfg.Emit(eventType, "prizm-autopatch", payload)
	}
}

func runValidations(ctx context.Context, reg *validation.Registry, worktree, artifactDir string, profiles []string, correlationID string) []validation.Result {
	if len(profiles) == 0 {
		return nil
	}
	out := make([]validation.Result, 0, len(profiles))
	for _, profile := range profiles {
		exec := validation.NewExecutor(reg, worktree, artifactDir)
		res, err := exec.Run(ctx, profile, correlationID)
		if res != nil {
			out = append(out, *res)
			continue
		}
		out = append(out, validation.Result{Profile: profile, Status: "error", Error: errorString(err)})
	}
	return out
}

func validationsPassed(results []validation.Result) bool {
	if len(results) == 0 {
		return true
	}
	for _, r := range results {
		if r.Status != "passed" {
			return false
		}
	}
	return true
}

// Worktree/git helpers live in internal/gitx (V56); these thin wrappers keep
// autopatch call sites unchanged.

func createWorktree(ctx context.Context, root, worktree string) error {
	return gitx.CreateDetachedWorktree(ctx, root, worktree)
}

func removeWorktree(ctx context.Context, root, worktree string) {
	gitx.RemoveWorktree(ctx, root, worktree)
}

func ensureCleanWorktree(ctx context.Context, root string) error {
	return gitx.EnsureClean(ctx, root)
}

func captureDiff(ctx context.Context, worktree string) (string, string, error) {
	diff, err := runCommand(ctx, worktree, "", "git", "diff", "--binary")
	if err != nil {
		return "", "", err
	}
	stat, err := runCommand(ctx, worktree, "", "git", "diff", "--stat")
	if err != nil {
		return "", "", err
	}
	return diff, stat, nil
}

func copyFinalDiffs(artifactDir, diff, stat string) (string, string) {
	patchPath := filepath.Join(artifactDir, "git-diff.patch")
	statPath := filepath.Join(artifactDir, "git-diff-stat.txt")
	_ = os.WriteFile(patchPath, []byte(diff), 0644)
	_ = os.WriteFile(statPath, []byte(stat), 0644)
	return patchPath, statPath
}

func writeReview(artifactDir string, result Result) string {
	path := filepath.Join(artifactDir, "review.md")
	var b strings.Builder
	b.WriteString("# Autopatch Review\n\n")
	b.WriteString("- Status: " + result.Status + "\n")
	if result.WorkerUsed != "" {
		b.WriteString("- Worker: " + result.WorkerUsed + "\n")
	}
	if result.PatchPath != "" {
		b.WriteString("- Patch: `" + result.PatchPath + "`\n")
	}
	if result.DiffStat != "" {
		b.WriteString("\n## Diff Stat\n\n```text\n" + result.DiffStat + "\n```\n")
	}
	if len(result.ValidationResults) > 0 {
		b.WriteString("\n## Validation\n\n")
		for _, vr := range result.ValidationResults {
			b.WriteString(fmt.Sprintf("- %s: %s (exit %d)\n", vr.Profile, vr.Status, vr.ExitCode))
		}
	}
	if result.Error != "" {
		b.WriteString("\n## Error\n\n" + result.Error + "\n")
	}
	_ = os.WriteFile(path, []byte(b.String()), 0644)
	return path
}

func formatValidationFeedback(results []validation.Result) string {
	if len(results) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Previous validation failed. Fix the patch and preserve the intended behavior.\n\n")
	for _, vr := range results {
		b.WriteString(fmt.Sprintf("- %s: %s exit=%d error=%s stdout=%s stderr=%s\n", vr.Profile, vr.Status, vr.ExitCode, vr.Error, vr.StdoutPath, vr.StderrPath))
	}
	return b.String()
}

func writeJSONFile(path string, value any) {
	data, _ := json.MarshalIndent(value, "", "  ")
	_ = os.WriteFile(path, append(data, '\n'), 0644)
}

func resultToMap(result Result) map[string]any {
	data, _ := json.Marshal(result)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return out
}

func runCommand(ctx context.Context, dir, stdin, name string, args ...string) (string, error) {
	return gitx.RunCommand(ctx, dir, stdin, name, args...)
}

func resolveUnder(root, path string) string {
	return gitx.ResolveUnder(root, path)
}

func safeID(value string) string {
	return gitx.SafeID(value, "autopatch")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
