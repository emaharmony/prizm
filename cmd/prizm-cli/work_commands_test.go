package main

import (
	"testing"

	"github.com/emaharmony/prizm/internal/orchestrator"
)

func TestParseWorkStartRequestExplicitArgs(t *testing.T) {
	req := parseWorkStartRequest("work", "project:GudEats path:D:/_projects_/GudEats task:start a Vite React app")
	if req.Project != "GudEats" {
		t.Fatalf("project = %q", req.Project)
	}
	if req.RepoPath != "D:/_projects_/GudEats" {
		t.Fatalf("repo path = %q", req.RepoPath)
	}
	if !req.Bootstrap {
		t.Fatal("expected path requests to set bootstrap")
	}
	if req.Prompt != "start a Vite React app" {
		t.Fatalf("prompt = %q", req.Prompt)
	}
}

func TestParseWorkStartRequestPromptWithoutTaskMarker(t *testing.T) {
	req := parseWorkStartRequest("work", "start a web app")
	if req.Prompt != "a web app" {
		t.Fatalf("prompt = %q", req.Prompt)
	}
}

func TestLooksLikeWorkStartRequestOnlyBuildRoomAllTools(t *testing.T) {
	role := &orchestrator.ChannelRole{Role: "build-room", Tools: "all"}
	if !looksLikeWorkStartRequest("create a web app for recipes", role) {
		t.Fatal("expected build-room work request")
	}
	if looksLikeWorkStartRequest("how do I create a web app?", role) {
		t.Fatal("questions should not auto-start work")
	}
	if looksLikeWorkStartRequest("create a web app", &orchestrator.ChannelRole{Role: "manager-room", Tools: "all"}) {
		t.Fatal("manager-room should not auto-start work")
	}
	if looksLikeWorkStartRequest("create a web app", &orchestrator.ChannelRole{Role: "build-room", Tools: "read-only"}) {
		t.Fatal("read-only channel should not auto-start work")
	}
}
