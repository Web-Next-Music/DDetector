package bridge

import (
	"context"
	"ddetector-go/internal/censor"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCensorSettingsDefaultsMigrationAndModeResources(t *testing.T) {
	var migrated CensorSettings
	if err := json.Unmarshal([]byte(`{"workers":3,"retired_setting":true}`), &migrated); err != nil {
		t.Fatal(err)
	}
	if migrated.Workers != 3 || migrated.Mode != "balanced" || migrated.CPUThreads != 1 || migrated.CandidateLimit != 20 || migrated.MinConfidence != .75 {
		t.Fatal(migrated)
	}
	for _, tc := range []struct {
		mode            string
		workers, points int
	}{{"eco", 1, 256}, {"balanced", 2, 1024}, {"performance", 4, 2048}} {
		settings := censorSettingsPatch{Mode: &tc.mode}.merge(defaultCensorSettings())
		config, err := buildCensorConfig(context.Background(), settings, "private-engine")
		if err != nil {
			t.Fatal(err)
		}
		if config.Workers != tc.workers || config.MaxWaveformPoints != tc.points || config.SampleRate != 16000 || config.CPUThreads != 1 {
			t.Fatal(config)
		}
	}
}

func TestCensorSettingsHTTPPersistenceAndValidation(t *testing.T) {
	s, _, _ := integrationCensor(t)
	state := controlAPI(t, s, map[string]any{"action": "settings", "settings": map[string]any{"mode": "performance", "workers": 3, "cpu_threads": 8, "candidate_limit": 7, "min_confidence": .9}}, 200)
	if state.Settings.Mode != "performance" || state.Settings.Workers != 3 || state.Settings.CandidateLimit != 7 || state.Settings.MinConfidence != .9 || state.Settings.CPUThreads != 8 {
		t.Fatal(state.Settings)
	}
	for _, settings := range []map[string]any{{"cpu_threads": 0}, {"cpu_threads": 65}, {"candidate_limit": 0}, {"candidate_limit": 101}, {"min_confidence": 0}, {"min_confidence": 1.1}, {"mode": "fast"}} {
		body, _ := json.Marshal(map[string]any{"action": "settings", "settings": settings})
		if response := api(s, "POST", "/api/censor/control", string(body), "bridge-key"); response.Code != 400 {
			t.Fatal(settings, response.Code, response.Body)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.closeCensor(ctx, true); err != nil {
		t.Fatal(err)
	}
}

func TestCensorSettingsBusy(t *testing.T) {
	for _, name := range []string{"running", "commandActive", "both", "sourceUnavailable"} {
		t.Run(name, func(t *testing.T) {
			s, _, _ := integrationCensor(t)
			state := controlAPI(t, s, map[string]any{"action": "settings", "settings": map[string]any{"workers": 3}}, http.StatusOK)
			rt := s.censorRuntime
			rt.mu.Lock()
			rt.running = name == "running" || name == "both"
			rt.commandActive = name == "commandActive" || name == "both" || name == "sourceUnavailable"
			rt.sourceUnavailable = name == "sourceUnavailable"
			rt.mu.Unlock()
			defer func() {
				rt.mu.Lock()
				rt.running = false
				rt.commandActive = false
				rt.sourceUnavailable = false
				rt.mu.Unlock()
			}()

			workers := 4
			patch := censorSettingsPatch{Workers: &workers}
			if err := rt.applySettings(context.Background(), patch); !errors.Is(err, ErrCensorSettingsBusy) || errors.Is(err, ErrCensorConflict) {
				t.Fatalf("applySettings error = %v; want distinct settings busy error", err)
			}
			wantErr := ErrCensorSettingsBusy
			wantStatus := http.StatusConflict
			wantMessage := "Censor settings are waiting for active processing to finish"
			if name == "sourceUnavailable" {
				wantErr = ErrCensorUnavailable
				wantStatus = http.StatusServiceUnavailable
				wantMessage = "censor backend is unavailable; check native source, Yandex account and repository configuration"
			}
			if err := rt.control(context.Background(), CensorIdentity{}, censorControl{Action: "settings", Settings: &patch}); !errors.Is(err, wantErr) {
				t.Fatalf("control error = %v; want %v", err, wantErr)
			}
			response := api(s, "POST", "/api/censor/control", `{"action":"settings","settings":{"workers":4}}`, "bridge-key")
			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if response.Code != wantStatus || body.Error != wantMessage {
				t.Fatalf("response = %d %s", response.Code, response.Body)
			}
			if got := rt.settings(); got != state.Settings {
				t.Fatalf("settings mutated: got %+v, want %+v", got, state.Settings)
			}
		})
	}
}

func TestCensorSettingsStaleSelectionUnchanged(t *testing.T) {
	s, _, _ := integrationCensor(t)
	state := controlAPI(t, s, map[string]any{"action": "fetch"}, http.StatusOK)
	if len(state.Jobs) == 0 {
		t.Fatal("expected fetched job")
	}
	job := state.Jobs[0]
	body, err := json.Marshal(map[string]any{"action": "retry", "id": job.ID, "request_id": job.Request.ID, "track_id": job.Request.TrackID + 1})
	if err != nil {
		t.Fatal(err)
	}
	response := api(s, "POST", "/api/censor/control", string(body), "bridge-key")
	var result struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusConflict || result.Error != "censor selection is stale" {
		t.Fatalf("response = %d %s", response.Code, response.Body)
	}
}

func TestCensorFPcalcConfiguration(t *testing.T) {
	dir := t.TempDir()
	fpcalc := filepath.Join(dir, "fpcalc")
	if err := os.WriteFile(fpcalc, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	config, err := buildCensorConfig(context.Background(), defaultCensorSettings(), "engine")
	if err != nil || config.FPcalcCommand != fpcalc {
		t.Fatal(config, err)
	}
	if _, err = censor.NewEngine(config); err != nil {
		t.Fatal(err)
	}
}
