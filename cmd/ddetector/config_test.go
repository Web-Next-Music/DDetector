package main

import (
	"ddetector-go/internal/bridge"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestEmbeddedConfig(t *testing.T) {
	values, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 2 {
		t.Fatal("embedded configuration must contain only two public settings")
	}
	key := values["DDETECTOR_SUPABASE_ANON_KEY"]
	if _, err := bridge.NewClient(values["DDETECTOR_SUPABASE_URL"], key); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(key, "sb_publishable_") {
		return
	}
	parts := strings.Split(key, ".")
	if len(parts) != 3 {
		t.Fatal("expected a public Supabase key")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal("invalid public key payload")
	}
	var claims struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Role != "anon" {
		t.Fatal("only anon keys may be embedded")
	}
}

func TestConfigEnvironmentOverride(t *testing.T) {
	values := map[string]string{"DDETECTOR_SUPABASE_URL": "https://default.supabase.co"}
	t.Setenv("DDETECTOR_SUPABASE_URL", "")
	t.Setenv("NEXT_PUBLIC_SUPABASE_URL", "")
	if got := configValue(values, "DDETECTOR_SUPABASE_URL", "NEXT_PUBLIC_SUPABASE_URL"); got != values["DDETECTOR_SUPABASE_URL"] {
		t.Fatal("embedded default not used")
	}
	t.Setenv("NEXT_PUBLIC_SUPABASE_URL", "https://override.supabase.co")
	if got := configValue(values, "DDETECTOR_SUPABASE_URL", "NEXT_PUBLIC_SUPABASE_URL"); got != "https://override.supabase.co" {
		t.Fatal("environment did not take precedence")
	}
}
