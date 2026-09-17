package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSetYAMLPath_PreservesCommentsAndSiblings(t *testing.T) {
	src := `# top comment
prizm:
  instance_id: "astraea"   # inline comment
  port: 8321
  scheduler:
    enabled: false
    jobs: []

# remembrance section
remembrance:
  enabled: true
  url: "http://localhost:18790"
`
	// Replace prizm.scheduler with a new value.
	type job struct {
		Name     string `yaml:"name"`
		Schedule string `yaml:"schedule"`
		Event    string `yaml:"event"`
		Enabled  bool   `yaml:"enabled"`
	}
	type sched struct {
		Enabled bool  `yaml:"enabled"`
		Jobs    []job `yaml:"jobs"`
	}
	newSched := sched{Enabled: true, Jobs: []job{{Name: "status-report", Schedule: "0 */2 * * *", Event: "prizm.task.scheduled", Enabled: true}}}

	out, err := SetYAMLPath([]byte(src), []string{"prizm", "scheduler"}, newSched)
	if err != nil {
		t.Fatalf("SetYAMLPath: %v", err)
	}
	s := string(out)

	// Comments and untouched keys preserved.
	for _, want := range []string{"# top comment", "inline comment", "# remembrance section", "instance_id", "http://localhost:18790"} {
		if !strings.Contains(s, want) {
			t.Errorf("output lost %q:\n%s", want, s)
		}
	}
	// New scheduler content present.
	for _, want := range []string{"status-report", "0 */2 * * *", "enabled: true"} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing new scheduler content %q:\n%s", want, s)
		}
	}
	// Result must still parse into a valid Config.
	if _, err := LoadConfigFromBytes(out); err != nil {
		t.Fatalf("edited config no longer loads: %v", err)
	}
}

func TestSetYAMLPath_CreatesMissingPath(t *testing.T) {
	src := `prizm:
  instance_id: "x"
`
	out, err := SetYAMLPath([]byte(src), []string{"prizm", "scheduler", "enabled"}, true)
	if err != nil {
		t.Fatalf("SetYAMLPath: %v", err)
	}
	var m map[string]any
	if err := yaml.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	prizm, _ := m["prizm"].(map[string]any)
	scheduler, _ := prizm["scheduler"].(map[string]any)
	if scheduler["enabled"] != true {
		t.Fatalf("expected prizm.scheduler.enabled=true, got: %#v", m)
	}
	// instance_id preserved.
	if prizm["instance_id"] != "x" {
		t.Fatalf("instance_id not preserved: %#v", prizm)
	}
}

func TestSetYAMLPath_EmptySource(t *testing.T) {
	out, err := SetYAMLPath(nil, []string{"prizm", "port"}, 9000)
	if err != nil {
		t.Fatalf("SetYAMLPath: %v", err)
	}
	var m map[string]any
	if err := yaml.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	prizm, _ := m["prizm"].(map[string]any)
	if prizm["port"] != 9000 {
		t.Fatalf("expected prizm.port=9000, got %#v", m)
	}
}

func TestSetYAMLPath_ReplaceScalarInPlace(t *testing.T) {
	src := "prizm:\n  port: 8321\n  log_level: info\n"
	out, err := SetYAMLPath([]byte(src), []string{"prizm", "port"}, 9999)
	if err != nil {
		t.Fatalf("SetYAMLPath: %v", err)
	}
	if !strings.Contains(string(out), "9999") || !strings.Contains(string(out), "log_level: info") {
		t.Fatalf("in-place replace failed:\n%s", out)
	}
}

func TestValidateAndWrite_RejectsInvalidLeavesFileUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prizm.yaml")
	original := "prizm:\n  instance_id: good\nagents:\n  - id: a\n    role: r\n    model: m\n    provider: ollama\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	// An agent with an invalid ID (uppercase) must fail validation.
	bad := "agents:\n  - id: BadID\n    role: r\n    model: m\n    provider: ollama\n"
	if err := ValidateAndWrite(path, []byte(bad)); err == nil {
		t.Fatal("expected validation error for invalid agent id")
	}
	got, _ := os.ReadFile(path)
	if string(got) != original {
		t.Fatalf("file was modified on invalid write:\n%s", got)
	}
}

func TestValidateAndWrite_WritesValid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prizm.yaml")
	valid := "prizm:\n  instance_id: fresh\n"
	if err := ValidateAndWrite(path, []byte(valid)); err != nil {
		t.Fatalf("ValidateAndWrite: %v", err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "instance_id: fresh") {
		t.Fatalf("file not written: %s", got)
	}
	// No leftover temp file.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("temp file left behind")
	}
}
