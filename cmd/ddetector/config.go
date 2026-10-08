package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
)

// Only the public project URL and anon key are embedded, never OAuth secrets.
//
//go:embed public_config.json
var publicConfig []byte

func loadConfig() (map[string]string, error) {
	var values map[string]string
	if err := json.Unmarshal(publicConfig, &values); err != nil {
		return nil, fmt.Errorf("invalid embedded Supabase configuration: %w", err)
	}
	return values, nil
}

func configValue(values map[string]string, keys ...string) string {
	// Environment overrides remain available for development and testing.
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	for _, key := range keys {
		if value := values[key]; value != "" {
			return value
		}
	}
	return ""
}
