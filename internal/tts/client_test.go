package tts

import (
	"testing"
)

func TestShouldVoice(t *testing.T) {
	tests := []struct {
		name          string
		cfg           Config
		channelTTS    bool
		messageLength int
		want          bool
	}{
		{
			name:          "enabled, channel on, short message",
			cfg:           Config{Enabled: true, MaxChars: 500},
			channelTTS:    true,
			messageLength: 100,
			want:          true,
		},
		{
			name:          "disabled globally",
			cfg:           Config{Enabled: false},
			channelTTS:    true,
			messageLength: 100,
			want:          false,
		},
		{
			name:          "enabled, channel off",
			cfg:           Config{Enabled: true, MaxChars: 500},
			channelTTS:    false,
			messageLength: 100,
			want:          false,
		},
		{
			name:          "enabled, channel on, message too long",
			cfg:           Config{Enabled: true, MaxChars: 500},
			channelTTS:    true,
			messageLength: 600,
			want:          false,
		},
		{
			name:          "enabled, channel on, no max_chars limit",
			cfg:           Config{Enabled: true, MaxChars: 0},
			channelTTS:    true,
			messageLength: 5000,
			want:          true,
		},
		{
			name:          "enabled, channel on, exact max_chars",
			cfg:           Config{Enabled: true, MaxChars: 500},
			channelTTS:    true,
			messageLength: 500,
			want:          true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShouldVoice(tt.cfg, tt.channelTTS, tt.messageLength); got != tt.want {
				t.Errorf("ShouldVoice() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Enabled {
		t.Error("default should be disabled")
	}
	if cfg.VoiceboxURL == "" {
		t.Error("default should have VoiceboxURL")
	}
	if cfg.MaxChars <= 0 {
		t.Error("default should have MaxChars > 0")
	}
}

func TestNewClient(t *testing.T) {
	c := NewClient("")
	if c.URL != "http://localhost:17493" {
		t.Errorf("expected default URL, got %s", c.URL)
	}

	c = NewClient("http://custom:8080")
	if c.URL != "http://custom:8080" {
		t.Errorf("expected custom URL, got %s", c.URL)
	}
}