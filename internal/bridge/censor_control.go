package bridge

import (
	"context"
	"ddetector-go/internal/censor"

	"errors"

	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type censorControl struct {
	RepositoryPath string               `json:"repository_path,omitempty"`
	Action         string               `json:"action"`
	ID             string               `json:"id,omitempty"`
	RequestID      string               `json:"request_id,omitempty"`
	TrackID        int64                `json:"track_id,omitempty"`
	CandidateID    string               `json:"candidate_id,omitempty"`
	Query          string               `json:"query,omitempty"`
	Settings       *censorSettingsPatch `json:"settings,omitempty"`
}

// Called with the server operation lock. Switching identity never reuses a
// runtime or its queue; logout stops it before discarding the session.
func (s *Server) currentCensorRuntime(ctx context.Context, uid string) (*censorRuntime, error) {
	s.mu.Lock()
	old := s.censorRuntime
	b := s.censor
	s.mu.Unlock()
	if old != nil && old.uid == uid {
		return old, nil
	}
	if old != nil {
		if e := old.stop(ctx, true); e != nil {
			return nil, e
		}
		if e := old.releaseStorage(); e != nil {
			return nil, e
		}
		s.mu.Lock()
		s.censorRuntime = nil
		s.mu.Unlock()
	}
	if b == nil {
		base := os.Getenv("FCKCENSOR_API_URL")
		if base == "" {
			b = &nativeBackend{nativeSource: NativeSources(s)}
		} else {
			remote, e := NewCensorRemoteBackend(base)
			if e != nil {
				return nil, e
			}
			b = remote
		}
	}
	rt, e := openCensorRuntime(s, uid, b)
	if e != nil {
		return nil, e
	}
	if native, ok := b.(*nativeBackend); ok {
		native.runtime = rt
		// Polling restores intent only. Repository recovery requires the explicit
		// native_configure command after Qt has quiesced workspace operations.
		native.repo = rt.disk.RepositoryPath
		native.publisher = nil
		if native.repo != "" {
			native.repositoryError = "Saved repository is not ready; coordinate workspace operations and run native_configure to validate and recover it"
		}
	}
	s.mu.Lock()
	s.censorRuntime = rt
	s.mu.Unlock()
	return rt, nil
}
func unconfiguredCensorState(msg string) CensorState {
	return CensorState{Settings: defaultCensorSettings(), Jobs: []CensorJob{}, Stats: map[string]int{"total": 0, "suitable_mp3": 0}, Error: msg}
}
func (s *Server) handleCensorUI(w http.ResponseWriter, r *http.Request) {
	method := http.MethodGet
	if r.URL.Path == "/api/censor/control" {
		method = http.MethodPost
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		failure(w, 405, "method not allowed")
		return
	}
	if r.URL.RawQuery != "" {
		failure(w, 400, "query parameters are not supported")
		return
	}
	var in censorControl
	if method == http.MethodPost {
		if decode(w, r, &in) != nil {
			failure(w, 400, "invalid censor control parameters")
			return
		}

		if !validCensorControl(in) {
			failure(w, 400, "invalid censor control parameters")
			return
		}
	}
	if !s.tryOperation() {
		failure(w, 409, "another operation is running")
		return
	}
	defer s.op.Unlock()
	ctx, cancel := s.operationContext(r.Context(), 15*time.Minute)
	defer cancel()
	s.mu.Lock()
	sess := s.session
	s.mu.Unlock()
	if sess == nil && method == http.MethodGet {
		sendCensorState(w, 200, unconfiguredCensorState("Login is required to access Censor Detector"))
		return
	}
	token, e := s.authorize(ctx)
	if e != nil {
		failure(w, 403, "censor authorization required")
		return
	}
	rt, e := s.currentCensorRuntime(ctx, sess.User.ID)
	if e != nil {
		if method == http.MethodGet {
			sendCensorState(w, 200, unconfiguredCensorState("Censor backend is unavailable. Check native source and local storage configuration"))
			return
		}
		censorFailure(w, ErrCensorUnavailable)
		return
	}
	if method == http.MethodGet {
		rt.mu.Lock()
		unavailable := rt.sourceUnavailable
		rt.mu.Unlock()
		if unavailable {
			health, cancelHealth := context.WithTimeout(ctx, 5*time.Second)
			items, healthErr := rt.backend.Requests(health, CensorIdentity{UserID: rt.uid, AccessToken: token})
			cancelHealth()
			if healthErr == nil && validateBackendRequests(rt.backend, items) == nil {
				rt.mu.Lock()
				rt.sourceUnavailable = false
				rt.lastError = ""
				rt.mu.Unlock()
			}
		}
		sendCensorState(w, 200, rt.snapshot())
		return
	}
	identity := CensorIdentity{UserID: rt.uid, AccessToken: token}
	if e = rt.control(ctx, identity, in); e != nil {
		censorFailure(w, e)
		return
	}
	sendCensorState(w, 200, rt.snapshot())
}
func validCensorControl(in censorControl) bool {

	if in.RepositoryPath != "" && in.Action != "native_configure" {
		return false
	}
	if in.Action == "native_configure" {
		return validNativeRepositoryPath(in.RepositoryPath) && in.ID == "" && in.RequestID == "" && in.CandidateID == "" && in.TrackID == 0 && in.Query == "" && in.Settings == nil
	}
	if in.Action == "check_track" {
		return in.TrackID > 0 && in.ID == "" && in.RequestID == "" && in.CandidateID == "" && in.Query == "" && in.Settings == nil
	}
	switch in.Action {
	case "fetch", "start", "pause", "resume", "stop", "retry", "analyze", "search", "approve", "reject", "defer", "rollback", "settings":
	default:
		return false
	}
	if in.ID != "" && !censorID(in.ID) || in.RequestID != "" && !censorID(in.RequestID) || in.CandidateID != "" && !censorID(in.CandidateID) || in.TrackID < 0 {
		return false
	}
	if in.Query != "" && (in.Action != "search" || !validCensorQuery(in.Query)) {
		return false
	}
	if in.Action == "search" && !validCensorQuery(in.Query) {
		return false
	}
	if in.Settings != nil && in.Action != "settings" {
		return false
	}
	if in.Action == "settings" {
		if in.Settings == nil || in.Settings.empty() {
			return false
		}

		if validateCensorSettings(in.Settings.merge(defaultCensorSettings())) != nil {
			return false
		}
	}
	switch in.Action {
	case "retry", "analyze", "search", "approve", "reject", "defer", "rollback":
		if !censorID(in.ID) {
			return false
		}
	}
	if in.Action == "approve" && !censorID(in.CandidateID) {
		return false
	}
	return true
}
func (rt *censorRuntime) control(ctx context.Context, i CensorIdentity, in censorControl) error {

	rt.mu.Lock()
	busy := rt.commandActive
	unavailable := rt.sourceUnavailable
	rt.mu.Unlock()
	if in.Action == "check_track" {
		b, ok := rt.backend.(*nativeBackend)
		if !ok {
			return ErrCensorUnsupported
		}
		return b.checkTrack(ctx, i, in.TrackID)
	}
	if unavailable && in.Action != "native_configure" && in.Action != "approve" && in.Action != "fetch" && in.Action != "pause" && in.Action != "stop" {
		return ErrCensorUnavailable
	}
	if busy && in.Action != "pause" && in.Action != "stop" && in.Action != "settings" {
		return ErrCensorConflict
	}
	switch in.Action {
	case "native_configure":
		b, ok := rt.backend.(*nativeBackend)
		if !ok {
			return ErrCensorUnsupported
		}
		return b.configure(ctx, in.RepositoryPath)
	case "fetch":
		short, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return rt.fetch(short, i)
	case "pause":
		return rt.stop(ctx, true)
	case "stop":
		return rt.stop(ctx, false)
	case "start", "resume":
		rt.mu.Lock()
		for n := range rt.disk.Jobs {
			if rt.disk.Jobs[n].Status == "stopped" {
				rt.disk.Jobs[n].Status = "pending"
			}
		}
		e := rt.persistLocked()
		rt.mu.Unlock()
		if e != nil {
			return e
		}
		return rt.start(rt.server.lifecycle)
	case "settings":
		return rt.applySettings(ctx, *in.Settings)
	}
	j, e := rt.job(in.ID)
	if e != nil {
		return e
	}
	if in.RequestID != "" && in.RequestID != j.Request.ID || in.TrackID != 0 && in.TrackID != j.Request.TrackID {
		return ErrCensorConflict
	}
	if j.Status == "running" || j.Status == "publishing" || j.Status == "rolling_back" {
		return ErrCensorConflict
	}
	switch in.Action {
	case "retry", "analyze":
		if j.PublicationKey != "" || (j.Status == "succeeded" || j.Status == "local_saved") {
			return ErrCensorConflict
		}
		j.Status = "pending"
		j.Stage = ""
		j.StageProgress = newStageProgress()
		j.Error = ""
		j.Result = nil
		j.Candidates = []CensorCandidate{}
		j.Queries = nil
		if e = rt.update(j); e != nil {
			return e
		}
		// Restart a finite worker batch so a newly queued job cannot race its exit.
		if e = rt.stop(ctx, true); e != nil {
			return e
		}
		return rt.start(rt.server.lifecycle)
	case "search":
		if j.PublicationKey != "" || (j.Status == "succeeded" || j.Status == "local_saved") {
			return ErrCensorConflict
		}
		// Manual search still compares every returned full-audio candidate before review.
		if e = rt.stop(ctx, true); e != nil {
			return e
		}
		j.Status = "running"
		j.Stage = "search"
		j.StageProgress = map[string]float64{"search": 0, "comparison": 0}
		if j.Result != nil {
			j.StageProgress["analysis"] = 1
		}
		j.Error = ""
		if e = rt.update(j); e != nil {
			return e
		}
		return rt.startCommand(j.ID, func(work context.Context, identity CensorIdentity) error {
			return rt.manualSearch(work, identity, j, strings.TrimSpace(in.Query))
		})
	case "approve":
		if j.PublicationKey != "" && j.Status == "review_required" {
			if !boundCensorPublication(j, in.CandidateID) {
				return ErrCensorConflict
			}
		} else {
			if unavailable && j.Request.Origin != "manual" {
				return ErrCensorUnavailable
			}
			if e := rt.validateApproval(j, in.CandidateID); e != nil {
				return e
			}
		}
		if j.Status == "succeeded" || j.Status == "local_saved" {
			return nil
		}
		return rt.startCommand(j.ID, func(work context.Context, identity CensorIdentity) error {
			return rt.approve(work, identity, j, in.CandidateID)
		})
	case "rollback":
		short, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return rt.rollback(short, i, j)
	case "reject", "defer":
		if j.PublicationKey != "" || (j.Status == "succeeded" || j.Status == "local_saved") {
			return ErrCensorConflict
		}
		j.Status = "rejected"
		if in.Action == "defer" {
			j.Status = "deferred"
		}
		j.Error = ""
		return rt.update(j)
	}
	return ErrCensorInvalid
}
func (rt *censorRuntime) newProcessor() (censor.Processor, error) {
	if rt.processor != nil {
		return rt.processor, nil
	}
	cfg, err := rt.config()
	if err != nil {
		return nil, err
	}
	cfg.StateDir = filepath.Join(rt.dir, "review")
	if e := checkCensorDir(cfg.StateDir, true); e != nil {
		return nil, e
	}
	return censor.NewEngine(cfg)
}
func boundCensorPublication(j CensorJob, candidateID string) bool {
	return j.SelectedCandidate == candidateID && validCensorReceiptSelection(CensorSelection{RequestID: j.Request.ID, TrackID: j.Request.TrackID, CandidateID: candidateID, IdempotencyKey: j.PublicationKey})
}

func (rt *censorRuntime) approve(ctx context.Context, i CensorIdentity, j CensorJob, candidateID string) (err error) {
	if j.Status == "succeeded" || j.Status == "local_saved" {
		if j.SelectedCandidate == candidateID && j.Replacement != nil {
			return nil
		}
		return ErrCensorConflict
	}
	if j.Status != "review_required" || j.PublicationKey != "" && j.SelectedCandidate != candidateID {
		return ErrCensorConflict
	}
	mutationAttempted := false
	if j.PublicationKey != "" {
		if !boundCensorPublication(j, candidateID) {
			return ErrCensorConflict
		}
		uncertainty := "Publication may already be committed; authoritative receipt lookup is unsupported and strict revalidation failed"
		defer func() {
			if err != nil && !mutationAttempted && j.Status == "review_required" {
				j.Error = uncertainty
				if saveErr := rt.update(j); saveErr != nil {
					err = saveErr
				}
			}
		}()
		if b, ok := rt.backend.(CensorReceiptBackend); ok {
			selection := CensorSelection{RequestID: j.Request.ID, TrackID: j.Request.TrackID, CandidateID: candidateID, IdempotencyKey: j.PublicationKey}
			receipt, lookupErr := b.ReplacementReceipt(ctx, i, selection)
			if lookupErr == nil {
				if !validCensorReceipt(receipt, selection) {
					uncertainty = "Publication may already be committed; authoritative receipt failed selection validation"
					return ErrCensorConflict
				}
				j.Replacement = &receipt
				j.Status, j.Stage, j.Error = "succeeded", "published", ""
				if receipt.LocalOnly {
					j.Status, j.Stage = "local_saved", "local_saved"
				}
				return rt.update(j)
			}
			if errors.Is(lookupErr, ErrCensorNotFound) {
				uncertainty = "Publication may already be committed; no authoritative receipt is available and strict revalidation failed"
			} else if !errors.Is(lookupErr, ErrCensorUnsupported) {
				uncertainty = "Publication may already be committed; authoritative receipt lookup failed. No publication was retried"
				return lookupErr
			}
		}
	}
	var candidate *CensorCandidate
	for n := range j.Candidates {
		if j.Candidates[n].ID == candidateID {
			candidate = &j.Candidates[n]
			break
		}
	}
	if candidate == nil || !rt.suitableCandidate(*candidate) {
		return ErrCensorConflict
	}
	var e error
	if b, ok := rt.backend.(*nativeBackend); ok {
		if b.publisher == nil {
			return ErrCensorUnavailable
		}
		if j.PublicationKey == "" {
			j.ExpectedMapping, e = b.publisher.Mapping(ctx, j.Request.TrackID)
			if e != nil {
				return e
			}
			j.RepositoryPath = b.repo
		} else if j.RepositoryPath != b.repo {
			return ErrCensorConflict
		}
		// A retry retains its original CAS precondition, even when no receipt
		// exists. Recapturing the mapping would authorize intervening user edits.
	}
	if _, ok := rt.backend.(censorRequestResolver); ok {
		if e = rt.resolveRequest(ctx, i, &j); e != nil {
			return e
		}
	} else {
		// Revalidate server provenance, not just a cached UI selection or audio URL.
		requests, e := rt.backend.Requests(ctx, i)
		if e != nil {
			return e
		}
		if e = validateCensorRequests(requests); e != nil {
			return e
		}
		current := false
		for _, r := range requests {
			if r.ID == j.Request.ID && r.TrackID == j.Request.TrackID && r.OriginalURL == j.Request.OriginalURL {
				current = true
				break
			}
		}
		if !current {
			return ErrCensorConflict
		}
	}
	if resolver, ok := rt.backend.(censorCandidateResolver); ok {
		c, err := resolver.Candidate(ctx, i, candidateID)
		if err != nil {
			return err
		}
		if c.ID != candidateID || !censorAudioCandidate(c) {
			return ErrCensorConflict
		}
		*candidate = c
	} else {
		fresh := j
		if e = rt.search(ctx, i, &fresh, j.Queries); e != nil {
			return e
		}
		current := false
		for _, c := range fresh.Candidates {
			if c.ID == candidateID && c.URL == candidate.URL && censorAudioCandidate(c) {
				current = true
				break
			}
		}
		if !current {
			return ErrCensorConflict
		}
	}
	p, e := rt.newProcessor()
	if e != nil {
		return e
	}
	result, e := p.Analyze(ctx, rt.configuredRequest(j.Request.OriginalURL, candidate.URL))
	checked := *candidate
	checked.Result = &result
	if e != nil || !rt.suitableCandidate(checked) {
		return ErrCensorConflict
	}
	candidate.Result = &result
	j.Result = &result
	if j.PublicationKey == "" {
		j.PublicationKey = RandomKey()
		j.SelectedCandidate = candidateID
	}
	j.Status = "publishing"
	j.Error = ""
	// Persist the key before sending a mutation: a timeout/restart must replay the
	// same operation, never silently publish a second replacement.
	if e = rt.update(j); e != nil {
		return e
	}
	selection := CensorSelection{RequestID: j.Request.ID, TrackID: j.Request.TrackID, CandidateID: candidateID, IdempotencyKey: j.PublicationKey}
	// The audio recheck can outlive a JWT. Revalidate account access immediately
	// before publication, and never carry an old account's identity across it.
	i, e = rt.identity(ctx)
	var replacement CensorReplacement
	if e == nil {
		mutationAttempted = true
		replacement, e = rt.backend.Replace(ctx, i, selection)
	}
	if e != nil || !validCensorReplacement(replacement, selection) || !replacement.Durable || !replacement.Atomic || replacement.IdempotencyKey != selection.IdempotencyKey {
		j.Status = "review_required"
		j.Error = "Publication was not durably acknowledged; retry the same candidate to reconcile"

		if saveErr := rt.update(j); saveErr != nil {
			return saveErr
		}
		if e != nil {
			return e
		}
		return ErrCensorConflict
	}
	j.Replacement = &replacement
	j.Status = "succeeded"
	j.Stage = "published"
	if replacement.LocalOnly {
		j.Status, j.Stage = "local_saved", "local_saved"
	}
	j.Error = ""
	return rt.update(j)
}
func (rt *censorRuntime) rollback(ctx context.Context, i CensorIdentity, j CensorJob) error {
	b, ok := rt.backend.(CensorRollbackBackend)
	if !ok {
		return ErrCensorUnsupported
	}
	if j.Status == "rolled_back" {
		return nil
	}
	if (j.Status != "succeeded" && j.Status != "local_saved") || j.Replacement == nil {
		return ErrCensorConflict
	}
	if j.RollbackKey == "" {
		j.RollbackKey = RandomKey()
	}
	j.Status = "rolling_back"
	if e := rt.update(j); e != nil {
		return e
	}
	selection := CensorSelection{RequestID: j.Request.ID, TrackID: j.Request.TrackID, CandidateID: j.SelectedCandidate, IdempotencyKey: j.RollbackKey}
	replacement, e := b.Rollback(ctx, i, selection)
	if e != nil || !validCensorReplacement(replacement, selection) || !replacement.Durable || !replacement.Atomic || replacement.IdempotencyKey != j.RollbackKey {
		j.Status = "succeeded"
		if j.Replacement.LocalOnly {
			j.Status = "local_saved"
		}
		j.Error = "Rollback was not durably acknowledged; retry to reconcile"
		if saveErr := rt.update(j); saveErr != nil {
			return saveErr
		}
		if e != nil {
			return e
		}
		return ErrCensorConflict
	}
	j.Status = "rolled_back"
	j.Stage = "rollback"
	j.Replacement = &replacement
	j.Error = ""
	return rt.update(j)
}

func (s *Server) cancelCensor() *censorRuntime {
	s.mu.Lock()
	rt := s.censorRuntime
	s.mu.Unlock()
	if rt != nil {
		rt.mu.Lock()
		if rt.cancel != nil {
			rt.cancel()
		}
		if rt.commandCancel != nil {
			rt.commandCancel()
		}
		rt.mu.Unlock()
	}
	return rt
}
func (s *Server) closeCensor(ctx context.Context, detach bool) error {
	rt := s.cancelCensor()
	if rt == nil {
		return nil
	}
	if e := rt.stop(ctx, true); e != nil {
		return e
	}
	if detach {
		if e := rt.releaseStorage(); e != nil {
			return e
		}
		s.mu.Lock()
		if s.censorRuntime == rt {
			s.censorRuntime = nil
		}
		s.mu.Unlock()
	}
	return nil
}
