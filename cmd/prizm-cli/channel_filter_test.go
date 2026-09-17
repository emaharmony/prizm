package main

import (
	"testing"

	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/tool"
)

func TestFilterChatToolsByChannelRole(t *testing.T) {
	allTools := []provider.ChatTool{
		{Function: provider.FunctionDef{Name: "read_project"}},
		{Function: provider.FunctionDef{Name: "search_files"}},
		{Function: provider.FunctionDef{Name: "git_status"}},
		{Function: provider.FunctionDef{Name: "git_commit"}},
		{Function: provider.FunctionDef{Name: "git_push"}},
		{Function: provider.FunctionDef{Name: "plan_create"}},
		{Function: provider.FunctionDef{Name: "state_get"}},
		{Function: provider.FunctionDef{Name: "web_search"}},
		{Function: provider.FunctionDef{Name: "memory_search"}},
		{Function: provider.FunctionDef{Name: "fetch_image"}},
		{Function: provider.FunctionDef{Name: "generate_image"}},
		{Function: provider.FunctionDef{Name: "analyze_image"}},
		{Function: provider.FunctionDef{Name: "collect_reference_images"}},
		{Function: provider.FunctionDef{Name: "plan_list"}},
		{Function: provider.FunctionDef{Name: "plan_update"}},
	}

	tests := []struct {
		name        string
		role        *orchestrator.ChannelRole
		expectCount int
		expectNames []string
	}{
		{
			name:        "nil role returns all tools",
			role:        nil,
			expectCount: 15,
		},
		{
			name:        "all tools returns all",
			role:        &orchestrator.ChannelRole{Role: "manager-room", Tools: "all"},
			expectCount: 15,
		},
		{
			name:        "empty tools returns all",
			role:        &orchestrator.ChannelRole{Role: "manager-room", Tools: ""},
			expectCount: 15,
		},
		{
			name:        "none returns no tools",
			role:        &orchestrator.ChannelRole{Role: "fun", Tools: "none"},
			expectCount: 0,
		},
		{
			name:        "read-only returns only read tools",
			role:        &orchestrator.ChannelRole{Role: "casual", Tools: "read-only"},
			expectCount: 13,
			expectNames: []string{"read_project", "search_files", "git_status", "state_get", "web_search", "memory_search", "fetch_image", "generate_image", "analyze_image", "collect_reference_images", "plan_create", "plan_list", "plan_update"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterChatToolsByChannelRole(allTools, tt.role)
			if len(result) != tt.expectCount {
				t.Errorf("got %d tools, want %d", len(result), tt.expectCount)
			}
			if tt.expectNames != nil {
				resultNames := make(map[string]bool)
				for _, ct := range result {
					resultNames[ct.Function.Name] = true
				}
				for _, name := range tt.expectNames {
					if !resultNames[name] {
						t.Errorf("missing expected tool %q", name)
					}
				}
			}
		})
	}
}
func TestFilterToolInfosByChannelRole(t *testing.T) {
	allInfos := []tool.ToolInfo{
		{Name: "read_project", Description: "Read project files"},
		{Name: "search_files", Description: "Search files"},
		{Name: "git_status", Description: "Check git status"},
		{Name: "git_commit", Description: "Git commit"},
		{Name: "git_push", Description: "Git push"},
		{Name: "web_search", Description: "Search web"},
		{Name: "collect_reference_images", Description: "Collect images"},
	}

	tests := []struct {
		name        string
		role        *orchestrator.ChannelRole
		expectCount int
	}{
		{
			name:        "nil role returns all",
			role:        nil,
			expectCount: 7,
		},
		{
			name:        "all returns all",
			role:        &orchestrator.ChannelRole{Role: "manager-room", Tools: "all"},
			expectCount: 7,
		},
		{
			name:        "none returns nil",
			role:        &orchestrator.ChannelRole{Role: "fun", Tools: "none"},
			expectCount: 0,
		},
		{
			name:        "read-only returns only read tools",
			role:        &orchestrator.ChannelRole{Role: "casual", Tools: "read-only"},
			expectCount: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterToolInfosByChannelRole(allInfos, tt.role)
			if len(result) != tt.expectCount {
				t.Errorf("got %d tool infos, want %d", len(result), tt.expectCount)
			}
		})
	}
}
func TestResolveToolMode(t *testing.T) {
	tests := []struct {
		name   string
		role   *orchestrator.ChannelRole
		expect ToolMode
	}{
		{name: "nil returns all", role: nil, expect: ToolModeAll},
		{name: "all returns all", role: &orchestrator.ChannelRole{Tools: "all"}, expect: ToolModeAll},
		{name: "empty returns all", role: &orchestrator.ChannelRole{Tools: ""}, expect: ToolModeAll},
		{name: "none returns none", role: &orchestrator.ChannelRole{Tools: "none"}, expect: ToolModeNone},
		{name: "read-only returns read-only", role: &orchestrator.ChannelRole{Tools: "read-only"}, expect: ToolModeReadOnly},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveToolMode(tt.role)
			if got != tt.expect {
				t.Errorf("resolveToolMode() = %v, want %v", got, tt.expect)
			}
		})
	}
}
