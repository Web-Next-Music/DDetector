package bridge

import (
	"context"
	"crypto/sha256"
	"ddetector-go/internal/censor"
	"ddetector-go/internal/censorsearch"

	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const censorStateLimit int64 = 64 << 20

var ErrCensorInvalid = errors.New("invalid censor control parameters")

type CensorJob struct {
	ExpectedMapping   string             `json:"expected_mapping,omitempty"`
	RepositoryPath    string             `json:"repository_path,omitempty"`
	ID                string             `json:"id"`
	Request           CensorRequest      `json:"request"`
	Status            string             `json:"status"`
	Stage             string             `json:"stage,omitempty"`
	StageProgress     map[string]float64 `json:"stage_progress,omitempty"`
	SearchSite        string             `json:"search_site,omitempty"`
	Result            *censor.Result     `json:"result,omitempty"`
	Candidates        []CensorCandidate  `json:"candidates"`
	Queries           []string           `json:"queries,omitempty"`
	EngineID          string             `json:"engine_id,omitempty"`
	Replacement       *CensorReplacement `json:"replacement,omitempty"`
	PublicationKey    string             `json:"publication_key,omitempty"`
	RollbackKey       string             `json:"rollback_key,omitempty"`
	SelectedCandidate string             `json:"selected_candidate,omitempty"`
	Error             string             `json:"error,omitempty"`
	UpdatedAt         time.Time          `json:"updated_at"`
	MetadataWarning   string             `json:"metadata_warning,omitempty"`
	SearchWarning     string             `json:"search_warning,omitempty"`
	SearchPartial     bool               `json:"search_partial,omitempty"`
}
type censorDiskState struct {
	RepositoryPath string         `json:"repository_path,omitempty"`
	UserID         string         `json:"user_id"`
	Settings       CensorSettings `json:"settings"`
	Jobs           []CensorJob    `json:"jobs"`
}
type CensorState struct {
	Native                bool           `json:"native"`
	NativeRepositoryReady bool           `json:"native_repository_ready"`
	RepositoryPath        string         `json:"repo_path,omitempty"`
	RepositoryError       string         `json:"repository_error,omitempty"`
	RequestSource         string         `json:"request_source,omitempty"`
	Ready                 bool           `json:"ready"`
	Configured            bool           `json:"configured"`
	Running               bool           `json:"running"`
	Paused                bool           `json:"paused"`
	Settings              CensorSettings `json:"settings"`
	Jobs                  []CensorJob    `json:"jobs"`
	Stats                 map[string]int `json:"stats"`
	Error                 string         `json:"error,omitempty"`
	RollbackSupported     bool           `json:"rollback_supported"`
	Busy                  bool           `json:"busy"`
	MetadataConfigured    bool           `json:"metadata_configured"`
	MetadataWarning       string         `json:"metadata_warning,omitempty"`
}
type censorRuntime struct {
	mu                    sync.Mutex
	server                *Server
	uid, dir              string
	backend               CensorBackend
	processor             censor.Processor
	disk                  censorDiskState
	running, paused       bool
	failure               string
	queue                 *censor.Queue
	cancel                context.CancelFunc
	done                  chan struct{}
	commandCancel         context.CancelFunc
	commandDone           chan struct{}
	commandActive         bool
	lastError             string
	lock                  *os.File
	sourceUnavailable     bool
	stateVersion          [32]byte
	metadataProvider      censorsearch.Provider
	metadataConfigWarning string
}

func openCensorRuntime(s *Server, uid string, b CensorBackend) (*censorRuntime, error) {
	hash := sha256.Sum256([]byte(uid))
	dir, err := censorStoragePath(s.path, hex.EncodeToString(hash[:]), s.customSession)
	if err != nil {
		return nil, err
	}
	rt := &censorRuntime{server: s, uid: uid, dir: dir, backend: b, processor: s.censorProcessor, disk: censorDiskState{UserID: uid, Settings: defaultCensorSettings(), Jobs: []CensorJob{}}}
	// Original Yandex access is independent of replacement-candidate discovery.
	rt.metadataConfigWarning = "Automatic Yandex metadata candidate discovery is disabled"
	if _, native := b.(*nativeBackend); native {
		rt.metadataConfigWarning = "Candidate discovery searches supported music sites directly. Public MP3 completeness and redistribution rights are unverified; discovered media is listening-only"
	}
	if e := checkCensorDir(filepath.Dir(dir), false); e != nil {
		return nil, e
	}
	if e := checkCensorDir(dir, false); e != nil {
		return nil, e
	}
	data, e := readPrivateFile(filepath.Join(dir, "runtime.json"), censorStateLimit, true)
	if e != nil && !os.IsNotExist(e) {
		return nil, e
	}
	rt.stateVersion = sha256.Sum256(data)
	if e == nil {
		if json.Unmarshal(data, &rt.disk) != nil || rt.disk.UserID != uid || validateCensorSettings(rt.disk.Settings) != nil || len(rt.disk.Jobs) > 1000 {
			return nil, errors.New("invalid censor runtime state")
		}
		seen := map[string]bool{}
		for n := range rt.disk.Jobs {
			j := &rt.disk.Jobs[n]
			if !censorID(j.ID) || seen[j.ID] || validateBackendRequests(b, []CensorRequest{j.Request}) != nil || validateCensorJobCandidates(j.Candidates) != nil {
				return nil, errors.New("invalid persisted censor job")
			}
			seen[j.ID] = true
			switch j.Status {
			case "running":
				j.Status = "pending"
				j.EngineID = ""
			case "publishing":
				j.Status = "review_required"
				j.Error = "Publication interrupted; retry the same candidate to reconcile its idempotent receipt"
			case "rolling_back":
				j.Status = "succeeded"
				if j.Replacement != nil && j.Replacement.LocalOnly {
					j.Status = "local_saved"
				}
				j.Error = "Rollback interrupted; retry rollback to reconcile its idempotent receipt"
			case "pending", "review_required", "local_saved", "succeeded", "failed", "rejected", "deferred", "stopped", "rolled_back":
			default:
				return nil, errors.New("invalid persisted censor status")
			}
		}
	}
	// No directories or workers are created by construction or state polling.
	return rt, nil
}
func (rt *censorRuntime) persistLocked() error {
	if e := rt.lockStorageLocked(); e != nil {
		rt.failure = "Censor storage is unavailable or owned by another process"
		if rt.cancel != nil {
			rt.cancel()
		}
		return e
	}
	if e := censorStateAdmission(rt.disk); e != nil {
		return e
	}
	hash := sha256.New()
	e := writePrivateFile(filepath.Join(rt.dir, "runtime.json"), ".censor-*", censorStateLimit, func(w io.Writer) error { return encodeCensorState(io.MultiWriter(w, hash), rt.disk) })
	if e == nil {
		copy(rt.stateVersion[:], hash.Sum(nil))
	}
	if e != nil {
		rt.failure = "Unable to persist censor state; processing is blocked"
		if rt.cancel != nil {
			rt.cancel()
		}
	}
	return e
}
func (rt *censorRuntime) snapshot() CensorState {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	// Results are immutable once installed by update; copy containers, not the
	// entire serialized result history, on every poll.
	jobs := append([]CensorJob{}, rt.disk.Jobs...)
	for n := range jobs {
		jobs[n].Candidates = append([]CensorCandidate{}, jobs[n].Candidates...)
		jobs[n].StageProgress = cloneStageProgress(jobs[n].StageProgress)
	}
	stats := map[string]int{"total": len(jobs), "censorship_found": 0, "suitable_mp3": 0}
	for _, j := range jobs {
		stats[j.Status]++
		found := censorEvidence(j.Result, rt.disk.Settings.MinConfidence)
		suitable := false
		for _, c := range j.Candidates {
			if censorAudioCandidate(c) {
				found = found || censorEvidence(c.Result, rt.disk.Settings.MinConfidence)
			}
			if censorAudioCandidate(c) && strongCensorComparison(c.Result) && c.Result.Comparison.Confidence >= rt.disk.Settings.MinConfidence {
				suitable = true
			}
		}
		if found {
			stats["censorship_found"]++
		}
		if suitable {
			stats["suitable_mp3"]++
		}
	}
	_, rollback := rt.backend.(CensorRollbackBackend)
	if b, ok := rt.backend.(*CensorRemoteBackend); ok {
		b.mu.Lock()
		rollback = b.capabilities[rt.uid].Rollback
		b.mu.Unlock()
	}
	msg := rt.failure
	if msg == "" {
		msg = rt.lastError
	}
	native, isNative := rt.backend.(*nativeBackend)
	repo, source := "", "remote"
	repositoryError := ""
	repositoryReady := false
	if isNative {
		repo, source = native.repo, "reported_tracks"
		repositoryReady = native.publisher != nil
		repositoryError = native.repositoryError
		if !repositoryReady && repositoryError == "" {
			repositoryError = "Configure a repository before saving local replacements. Analysis does not require a repository"
		}
	}
	metadataConfigured, metadataWarning := rt.metadataStatus()
	return CensorState{Native: isNative, NativeRepositoryReady: repositoryReady, RepositoryPath: repo, RepositoryError: repositoryError, RequestSource: source, Ready: !rt.sourceUnavailable, Configured: !rt.sourceUnavailable, Running: rt.running || rt.commandActive, Busy: rt.commandActive, Paused: rt.paused, Settings: rt.disk.Settings, Jobs: jobs, Stats: stats, Error: msg, RollbackSupported: rollback, MetadataConfigured: metadataConfigured, MetadataWarning: metadataWarning}
}
func (rt *censorRuntime) jobLocked(id string) (*CensorJob, error) {
	for n := range rt.disk.Jobs {
		if rt.disk.Jobs[n].ID == id {
			return &rt.disk.Jobs[n], nil
		}
	}
	return nil, ErrCensorNotFound
}
func (rt *censorRuntime) job(id string) (CensorJob, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	j, e := rt.jobLocked(id)
	if e != nil {
		return CensorJob{}, e
	}
	data, _ := json.Marshal(j)
	var copy CensorJob
	_ = json.Unmarshal(data, &copy)
	return copy, nil
}
func (rt *censorRuntime) update(j CensorJob) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	current, e := rt.jobLocked(j.ID)
	if e != nil {
		return e
	}
	j.UpdatedAt = time.Now().UTC()
	data, e := json.Marshal(j)
	if e != nil {
		return e
	}
	var copy CensorJob
	if e = json.Unmarshal(data, &copy); e != nil {
		return e
	}
	jobLimit := censorJobLimit
	if j.PublicationKey == "" && (j.Status == "running" || j.Status == "review_required") {
		jobLimit -= 16 << 10
	}
	if len(data) > jobLimit {
		previous, err := json.Marshal(current)
		if err != nil {
			return err
		}
		// Older, oversized jobs must still accept small status/receipt updates.
		if len(previous) <= censorJobLimit || len(data) > len(previous)+(16<<10) {
			return ErrCensorCapacity
		}
	}
	// Admission happens before mutation, leaving the last durable state intact.
	proposed := rt.disk
	proposed.Jobs = append([]CensorJob(nil), rt.disk.Jobs...)
	for n := range proposed.Jobs {
		if proposed.Jobs[n].ID == j.ID {
			proposed.Jobs[n] = copy
			break
		}
	}
	limit := censorStateLimit
	if j.PublicationKey == "" && (j.Status == "running" || j.Status == "review_required") {
		limit -= 1 << 20 // headroom for failures and mutation receipts
	}
	if e = censorAdmissionLimit(proposed, limit); e != nil {
		return e
	}
	old := *current
	*current = copy
	if e = rt.persistLocked(); e != nil {
		*current = old
		return e
	}
	return nil
}
func (rt *censorRuntime) fetch(ctx context.Context, i CensorIdentity) error {
	items, e := rt.backend.Requests(ctx, i)
	if e != nil {
		if lazyCensorBackend(rt.backend) || errors.Is(e, ErrCensorUnavailable) || errors.Is(e, ErrCensorNotFound) {
			rt.mu.Lock()
			rt.sourceUnavailable = true
			rt.lastError = "The configured API has no available request source"
			if lazyCensorBackend(rt.backend) {
				rt.lastError = "Cannot read the public reported_tracks queue; verify public anon configuration and SELECT permission. The replacement catalog is not a request source"
			}
			rt.mu.Unlock()
		}
		return e
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if e = validateBackendRequests(rt.backend, items); e != nil {
		return e
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.running {
		return ErrCensorConflict
	}
	// Validate the entire refresh before mutating an existing request association.
	for _, item := range items {
		for _, j := range rt.disk.Jobs {
			if j.Request.ID == item.ID && (j.Request.TrackID != item.TrackID || j.PublicationKey != "" && !lazyCensorBackend(rt.backend) && j.Request.OriginalURL != item.OriginalURL) {
				return ErrCensorConflict
			}
		}
	}
	newCount := 0
	for _, item := range items {
		found := false
		for _, j := range rt.disk.Jobs {
			if j.Request.ID == item.ID {
				found = true
				break
			}
		}
		if !found {
			newCount++
		}
	}
	if len(rt.disk.Jobs)+newCount > 1000 {
		return errors.New("censor runtime is full")
	}
	previousJobs := rt.disk.Jobs
	rt.disk.Jobs = append([]CensorJob(nil), previousJobs...)
	for _, item := range items {
		found := false
		for n := range rt.disk.Jobs {
			j := &rt.disk.Jobs[n]
			if j.Request.ID == item.ID {
				found = true
				if !lazyCensorBackend(rt.backend) && j.Request.OriginalURL != item.OriginalURL && j.PublicationKey == "" {
					j.Result = nil
					j.Candidates = []CensorCandidate{}
					j.Queries = nil
					j.Stage = ""
					j.StageProgress = newStageProgress()
					j.Status = "pending"
				}
				if !lazyCensorBackend(rt.backend) || j.Request.OriginalURL == "" {
					j.Request = item
				}
				j.UpdatedAt = time.Now().UTC()
				break
			}
		}
		if !found {
			rt.disk.Jobs = append(rt.disk.Jobs, CensorJob{ID: RandomKey(), Request: item, Status: "pending", StageProgress: newStageProgress(), Candidates: []CensorCandidate{}, UpdatedAt: time.Now().UTC()})
		}
	}
	sort.SliceStable(rt.disk.Jobs, func(i, j int) bool { return rt.disk.Jobs[i].UpdatedAt.Before(rt.disk.Jobs[j].UpdatedAt) })
	if e = rt.persistLocked(); e != nil {
		rt.disk.Jobs = previousJobs
		return e
	}
	rt.sourceUnavailable = false
	rt.lastError = ""
	return nil
}
func (rt *censorRuntime) identity(ctx context.Context) (CensorIdentity, error) {
	// Worker authorization is independent of foreground operations. authorize
	// serializes refresh and rejects stale sessions without holding server.op.
	rt.server.mu.Lock()
	sess := rt.server.session
	valid := sess != nil && sess.User.ID == rt.uid && rt.server.censorRuntime == rt && !rt.server.closing && rt.server.logoutPending == 0
	rt.server.mu.Unlock()
	if !valid {
		return CensorIdentity{}, ErrCensorDenied
	}
	token, e := rt.server.authorize(ctx)
	if e != nil {
		return CensorIdentity{}, ErrCensorDenied
	}
	rt.server.mu.Lock()
	defer rt.server.mu.Unlock()
	sess = rt.server.session
	if sess == nil || sess.User.ID != rt.uid || sess.Access != token || rt.server.censorRuntime != rt || rt.server.closing || rt.server.logoutPending > 0 || ctx.Err() != nil {
		return CensorIdentity{}, ErrCensorDenied
	}
	return CensorIdentity{UserID: rt.uid, AccessToken: token}, nil
}
func (rt *censorRuntime) config() (censor.Config, error) {
	return buildCensorConfig(rt.server.lifecycle, rt.settings(), filepath.Join(rt.dir, "engine"))
}
func (rt *censorRuntime) start(parent context.Context) error {
	cfg, configErr := rt.config()
	if configErr != nil {
		return configErr
	}
	rt.mu.Lock()
	if rt.running {
		rt.mu.Unlock()
		return nil
	}
	if rt.failure != "" {
		rt.mu.Unlock()
		return errors.New(rt.failure)
	}
	ctx, cancel := context.WithCancel(parent)
	rt.cancel = cancel
	rt.done = make(chan struct{})
	rt.running = true
	rt.paused = false
	rt.lastError = ""
	workers := rt.disk.Settings.Workers
	rt.mu.Unlock()
	// Commit settings and ownership before creating the persistent engine queue.
	rt.mu.Lock()
	e := rt.persistLocked()
	rt.mu.Unlock()
	var q *censor.Queue
	if e == nil {
		e = checkCensorDir(filepath.Join(rt.dir, "engine"), true)
	}
	if e == nil {
		var processor censor.Processor
		processor, e = rt.queueProcessor(ctx, cfg)
		if e == nil {
			q, e = censor.OpenQueue(cfg, processor)
		}
	}
	if e != nil {
		cancel()
		rt.mu.Lock()
		rt.running = false
		close(rt.done)
		rt.mu.Unlock()
		return e
	}
	for _, old := range q.List() {
		_ = q.Cancel(old.ID)
		if old.Status != censor.Pending && old.Status != censor.Running {
			_ = q.Delete(old.ID)
		}
	}
	rt.mu.Lock()
	rt.queue = q
	rt.mu.Unlock()
	go func() {
		var wg sync.WaitGroup
		for n := 0; n < workers; n++ {
			wg.Add(1)
			go func() { defer wg.Done(); rt.worker(ctx, q) }()
		}
		wg.Wait()
		closeErr := q.Close()
		rt.mu.Lock()
		if closeErr != nil {
			rt.failure = "Persistent engine queue failed; processing is blocked"
		}
		rt.queue = nil
		rt.running = false
		rt.cancel = nil
		close(rt.done)
		rt.mu.Unlock()
	}()
	return nil
}
func (rt *censorRuntime) stop(ctx context.Context, paused bool) error {
	rt.mu.Lock()
	rt.paused = paused
	if rt.cancel != nil {
		rt.cancel()
	}
	done := rt.done
	commandDone := rt.commandDone
	if rt.commandCancel != nil {
		rt.commandCancel()
	}
	rt.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if commandDone != nil {
		select {
		case <-commandDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	// Stop retains pending work but marks it explicitly; resume/start re-enqueues it.
	if !paused {
		for n := range rt.disk.Jobs {
			if rt.disk.Jobs[n].Status == "pending" {
				rt.disk.Jobs[n].Status = "stopped"
			}
		}
	}
	if len(rt.disk.Jobs) == 0 {
		return nil
	}
	return rt.persistLocked()
}
func (rt *censorRuntime) worker(ctx context.Context, q *censor.Queue) {
	for {
		if ctx.Err() != nil {
			return
		}
		rt.mu.Lock()
		var chosen CensorJob
		for n := range rt.disk.Jobs {
			j := &rt.disk.Jobs[n]
			if j.Status == "pending" {
				j.Status = "running"
				j.Stage = "analysis"
				j.StageProgress = newStageProgress()
				j.Error = ""
				data, _ := json.Marshal(j)
				_ = json.Unmarshal(data, &chosen)
				break
			}
		}
		if chosen.ID == "" {
			rt.mu.Unlock()
			return
		}
		e := rt.persistLocked()
		rt.mu.Unlock()
		if e != nil {
			return
		}
		e = rt.pipeline(ctx, q, &chosen)
		if ctx.Err() != nil {
			chosen.Status = "pending"
			chosen.Error = ""
		} else if errors.Is(e, ErrCensorCapacity) {
			retained, loadErr := rt.job(chosen.ID)
			if loadErr != nil {
				return
			}
			chosen = retained
			chosen.Status = "failed"
			chosen.Error = "Censor result exceeds storage capacity; reduce candidate/transcript limits before retrying. Previously saved evidence is retained"
			rt.mu.Lock()
			rt.paused = true
			rt.lastError = "Censor storage capacity reached; processing paused. Review and reconciliation remain available"
			if rt.cancel != nil {
				rt.cancel()
			}
			rt.mu.Unlock()
		} else if e != nil {
			chosen.Status = "failed"
			chosen.Error = "Censor pipeline failed; verify backend and audio availability, then retry"
			if message := rt.nativeFailureMessage(e); message != "" {
				chosen.Error = message
			}
		} else {
			chosen.Status = "review_required"
			chosen.Stage = "review"
		}
		chosen.EngineID = ""
		if saveErr := rt.update(chosen); saveErr != nil {
			if errors.Is(saveErr, ErrCensorCapacity) || errors.Is(e, ErrCensorCapacity) {
				// Retain previously admitted evidence and all mutation receipts.
				retained, loadErr := rt.job(chosen.ID)
				if loadErr == nil {
					retained.Status = "failed"
					retained.EngineID = ""
					retained.Error = "Censor result exceeds storage capacity; reduce candidate/transcript limits before retrying. Previously saved evidence is retained"
					_ = rt.update(retained)
				}
				rt.mu.Lock()
				rt.paused = true
				rt.lastError = "Censor storage capacity reached; processing paused. Review and reconciliation remain available"
				if rt.cancel != nil {
					rt.cancel()
				}
				rt.mu.Unlock()
			}
			return
		}
		if e == nil && ctx.Err() == nil {
			if b, ok := rt.backend.(CensorResultsBackend); ok {
				identity, authErr := rt.identity(ctx)
				if authErr == nil {
					if saveErr := b.SaveResults(ctx, identity, chosen); saveErr != nil {
						rt.mu.Lock()
						if current, err := rt.jobLocked(chosen.ID); err == nil && current.Status == "review_required" {
							current.Error = "Local results saved; remote result persistence failed"
							_ = rt.persistLocked()
						}
						rt.mu.Unlock()
					}
				}
			}
		}
	}
}
func (rt *censorRuntime) analyze(ctx context.Context, q *censor.Queue, j *CensorJob, reference string) (*censor.Result, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	req := rt.configuredRequest(j.Request.OriginalURL, reference)
	if q == nil {
		processor, err := rt.newProcessor()
		if err != nil {
			return nil, err
		}
		result, err := processor.Analyze(ctx, req)
		if err != nil {
			return nil, err
		}
		return &result, nil
	}
	// Each pipeline owns its engine job. Never prune another worker's result
	// between completion and Wait, even if it is already terminal.
	engineJob, e := q.Submit(req)
	if e != nil {
		return nil, e
	}
	j.EngineID = engineJob.ID
	if e = rt.update(*j); e != nil {
		_ = q.Cancel(engineJob.ID)
		return nil, e
	}
	engineJob, e = q.Wait(ctx, engineJob.ID)
	if e != nil {
		_ = q.Cancel(j.EngineID)
		return nil, e
	}
	if engineJob.Status != censor.Succeeded || engineJob.Result == nil {
		return nil, errors.New("audio analysis failed")
	}
	_ = q.Delete(engineJob.ID)
	return engineJob.Result, nil
}
func censorQueries(r CensorRequest) []string {
	base := strings.TrimSpace(r.Artist + " " + r.Title)
	if !validCensorQuery(base) {
		return nil
	}
	return []string{base}
}
func (rt *censorRuntime) search(ctx context.Context, i CensorIdentity, j *CensorJob, queries []string) error {
	metadata, _ := rt.searchMetadata(ctx, j)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	baseCount := len(queries)
	j.SearchPartial = false
	j.SearchWarning = ""
	j.SearchSite = ""
	var baseError error
	baseSuccess := 0

	if j.Status == "running" && j.ID != "" {
		j.Queries = append([]string(nil), queries...)
		if err := rt.stageProgress(j, "search", 0, len(queries)); err != nil {
			return err
		}
	}
	found := []CensorCandidate{}
	seen := map[string]bool{}
	limit := rt.settings().CandidateLimit
	for index, query := range queries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		searchCtx := ctx
		if _, native := rt.backend.(*nativeBackend); native && j.Status == "running" && j.ID != "" {
			searchCtx = withSiteProgress(ctx, func(site string, completed, total int) error {
				j.SearchSite = site
				return rt.searchProgress(j, index*total+completed, len(queries)*total)
			})
		}
		candidates, e := rt.backend.Search(searchCtx, i, query)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e == nil {
			e = validateCensorCandidates(candidates)
		}
		if e != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(e, ErrCensorDenied) || errors.Is(e, context.Canceled) {
				return e
			}
			if index < baseCount {
				baseError = e
				j.SearchPartial = true
				j.SearchWarning = "Some base audio queries failed; successful query results are retained. Search is incomplete"
				if err := rt.searchProgress(j, index+1, len(queries)); err != nil {
					return err
				}
				continue
			}
			j.MetadataWarning = "Some metadata-enriched audio queries failed; base audio search results are retained"
			if err := rt.searchProgress(j, index+1, len(queries)); err != nil {
				return err
			}
			continue
		}
		if index < baseCount {
			baseSuccess++
		}
		for _, c := range candidates {
			// Reserve this namespace for inert, locally mapped metadata pages.
			if strings.HasPrefix(c.ID, "musicbrainz:") || strings.HasPrefix(c.ID, "yandex-metadata:") {
				continue
			}
			if !seen[c.ID] && len(found) < limit {
				c.Result = nil
				c.Error = ""
				if !censorAudioCandidate(c) {
					c.Error = "Manual review only: candidate is not downloadable full audio; comparison and approval are disabled"
				}
				found = append(found, c)
				seen[c.ID] = true
			}
		}
		if _, native := rt.backend.(*nativeBackend); !native {
			if err := rt.searchProgress(j, index+1, len(queries)); err != nil {
				return err
			}
		}
	}
	j.Candidates = append(found, metadata...)
	j.Queries = append([]string(nil), queries...)
	// Approval revalidation must remain strict; only running analysis/search
	// commands may use incomplete searches as reviewable partial results.
	if baseError != nil && (baseSuccess == 0 || j.Status == "review_required" || j.Status == "succeeded") {
		return baseError
	}
	return nil
}
func (rt *censorRuntime) pipeline(ctx context.Context, q *censor.Queue, j *CensorJob) error {
	i, err := rt.identity(ctx)
	if err != nil {
		return err
	}
	if err = rt.resolveRequest(ctx, i, j); err != nil {
		return err
	}
	result, e := rt.analyze(ctx, q, j, "")
	if e != nil {
		return e
	}
	j.Result = result
	if e = rt.stageProgress(j, "analysis", 1, 1); e != nil {
		return e
	}
	j.Stage = "search"
	if e = rt.update(*j); e != nil {
		return e
	}
	identity, e := rt.identity(ctx)
	if e != nil {
		return e
	}
	if e = rt.search(ctx, identity, j, censorQueries(j.Request)); e != nil {
		return e
	}
	j.Stage = "comparison"
	if e = rt.update(*j); e != nil {
		return e
	}
	return rt.compareCandidates(ctx, q, j)
}

func (rt *censorRuntime) compareCandidates(ctx context.Context, q *censor.Queue, j *CensorJob) error {
	j.Stage = "comparison"
	total, completed := 0, 0
	for _, candidate := range j.Candidates {
		if censorAudioCandidate(candidate) {
			total++
		}
	}
	if err := rt.stageProgress(j, "comparison", 0, total); err != nil {
		return err
	}
	for n := range j.Candidates {
		if !censorAudioCandidate(j.Candidates[n]) {
			continue
		}
		result, e := rt.analyze(ctx, q, j, j.Candidates[n].URL)
		if errors.Is(e, ErrCensorCapacity) || errors.Is(e, context.Canceled) {
			return e
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e != nil {
			j.Candidates[n].Error = "Candidate audio comparison failed"
		} else {
			j.Candidates[n].Result = result
			if j.Result == nil || j.Result.Comparison == nil || comparisonScore(result) > comparisonScore(j.Result) {
				j.Result = result
			}
		}
		completed++
		if e = rt.stageProgress(j, "comparison", completed, total); e != nil {
			return e
		}
	}
	return nil
}
func comparisonScore(r *censor.Result) float64 {
	if r == nil || r.Comparison == nil {
		return -1
	}
	return r.Comparison.Confidence
}

// A global recording match is insufficient: require a localized restoration
// candidate too. This remains heuristic evidence; publication is always manual.
func strongCensorComparison(r *censor.Result) bool {
	if r == nil || r.Comparison == nil || r.IntervalsTruncated {
		return false
	}
	c := r.Comparison
	finite := func(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
	if !finite(r.DurationSeconds) || r.DurationSeconds <= 0 || c.OverlapSeconds < .9*r.DurationSeconds {
		return false
	}
	if !c.Comparable || c.ConfidenceClass != "strong_match" || !finite(c.Confidence) || c.Confidence < .75 || !finite(c.SpectralSimilarity) || c.SpectralSimilarity < .93 || !finite(c.SpectralMatchFraction) || c.SpectralMatchFraction < .8 || !finite(c.Correlation) || c.Correlation < .5 || !finite(c.OffsetSeconds) || !finite(c.OverlapSeconds) || c.OverlapSeconds < 6 {
		return false
	}
	if c.FingerprintSimilarity != nil && (!finite(*c.FingerprintSimilarity) || *c.FingerprintSimilarity < .85) {
		return false
	}
	for _, interval := range r.SuspiciousIntervals {
		restoration := interval.Restoration
		if interval.Status == "reference_restoration_candidate" && restoration != nil && finite(restoration.Confidence) && restoration.Confidence >= .65 && finite(restoration.ReferenceStartSeconds) && finite(restoration.ReferenceEndSeconds) && restoration.ReferenceStartSeconds >= 0 && restoration.ReferenceEndSeconds > restoration.ReferenceStartSeconds {
			return true
		}
	}
	return false
}
