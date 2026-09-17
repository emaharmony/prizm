package main

import "testing"

func TestResolveContextTokenBudget(t *testing.T) {
	tests := []struct {
		name    string
		input   int
		want    int
		wantErr bool
	}{
		{name: "default", input: 0, want: defaultContextTokenBudget},
		{name: "explicit cap", input: 123, want: 123},
		{name: "explicit no truncation", input: -1, want: 0},
		{name: "invalid negative", input: -2, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveContextTokenBudget(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("budget = %d, want %d", got, tt.want)
			}
		})
	}
}
