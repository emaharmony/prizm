package version

import "testing"

func TestString(t *testing.T) {
	original := Version
	t.Cleanup(func() { Version = original })
	Version = "1.2.3-test"
	if got, want := String(), "prizm v1.2.3-test"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
