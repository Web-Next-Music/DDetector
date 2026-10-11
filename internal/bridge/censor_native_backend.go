package bridge

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"
)

type censorRequestResolver interface {
	Request(context.Context, CensorIdentity, string) (CensorRequest, error)
}
type censorCandidateResolver interface {
	Candidate(context.Context, CensorIdentity, string) (CensorCandidate, error)
}
type censorPlaybackResolver interface {
	PlaybackCandidate(context.Context, CensorIdentity, string) (CensorCandidate, error)
}

type nativeBackend struct {
	*nativeSource
	runtime         *censorRuntime
	publisher       NativePublicationBackend
	repo            string
	repositoryError string
}

func lazyCensorBackend(b CensorBackend) bool {
	v, ok := b.(interface{ LazyOriginalAudio() bool })
	return ok && v.LazyOriginalAudio()
}
func validateBackendRequests(b CensorBackend, items []CensorRequest) error {
	if !lazyCensorBackend(b) {
		return validateCensorRequests(items)
	}
	if len(items) > 1000 {
		return ErrCensorInvalid
	}
	seen := map[string]bool{}
	for _, r := range items {
		if r.Origin != "" && r.Origin != "manual" && r.Origin != "reported_tracks" {
			return ErrCensorInvalid
		}
		if r.Origin == "manual" {
			id, err := nativeTrackID("manual", r.ID)
			if err != nil || id != r.TrackID {
				return ErrCensorInvalid
			}
		} else if strings.HasPrefix(r.ID, "manual:") {
			return ErrCensorInvalid
		}
		if !censorID(r.ID) || r.TrackID <= 0 || seen[r.ID] || len(r.Title) > 1024 || len(r.Artist) > 1024 || r.OriginalURL != "" && !censorHTTPS(r.OriginalURL) {
			return ErrCensorInvalid
		}
		seen[r.ID] = true
	}
	return nil
}
func (b *nativeBackend) configure(ctx context.Context, repo string) (err error) {
	rt := b.runtime
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if repo == b.repo && b.publisher != nil {
		return rt.clearLegacyRepositoryErrorsLocked(false)
	}
	defer func() {
		if err != nil && b.publisher == nil {
			b.repositoryError = "Native repository is unavailable; verify the saved path, permissions and upload lock, then retry native_configure under workspace coordination. Saved jobs remain available"
		}
	}()
	if rt.running || rt.commandActive {
		return ErrCensorConflict
	}
	for _, j := range rt.disk.Jobs {
		if j.PublicationKey != "" && (repo != rt.disk.RepositoryPath || j.RepositoryPath != repo) {
			return ErrCensorConflict
		}
	}
	if err := rt.lockStorageLocked(); err != nil {
		return err
	}
	p, err := NewNativePublisher(repo, rt.dir)
	if err != nil {
		return err
	}
	if err = p.Recover(ctx); err != nil {
		return err
	}
	previous := rt.disk.RepositoryPath
	rt.disk.RepositoryPath = repo
	if err = rt.clearLegacyRepositoryErrorsLocked(true); err != nil {
		rt.disk.RepositoryPath = previous
		return err
	}
	b.publisher = p
	b.repo = repo
	b.repositoryError = ""
	return nil
}
func nativeReplacement(r NativePublicationReceipt) CensorReplacement {
	s := r.Selection
	return CensorReplacement{RequestID: s.RequestID, TrackID: s.TrackID, CandidateID: s.CandidateID, IdempotencyKey: s.IdempotencyKey, URL: r.PublicationURL, Durable: r.Durable, Atomic: r.Durable, LocalOnly: true, LocalPath: r.LocalPath, GitHubPublished: r.GitHubPublished, RequestCompleted: r.RequestCompleted, Scope: "local"}
}
func (b *nativeBackend) Replace(ctx context.Context, i CensorIdentity, s CensorSelection) (CensorReplacement, error) {
	if b.publisher == nil {
		return CensorReplacement{}, errors.New("configure a repository path before saving local replacements")
	}
	for _, j := range b.runtime.snapshot().Jobs {
		if j.Request.ID == s.RequestID && j.Request.TrackID == s.TrackID && j.PublicationKey == s.IdempotencyKey && j.SelectedCandidate == s.CandidateID && j.RepositoryPath == b.repo {
			for _, c := range j.Candidates {
				if c.ID == s.CandidateID {
					if strings.HasPrefix(s.RequestID, "manual:") && (!b.currentAccount(i) || j.Request.Origin != "manual" || j.Status != "publishing" || !b.runtime.suitableCandidate(c)) {
						return CensorReplacement{}, ErrCensorConflict
					}
					r, e := b.publisher.Replace(ctx, i, s, NativePublicationRequest{CensorRequest: j.Request, ExpectedMapping: j.ExpectedMapping}, c)
					return nativeReplacement(r), e
				}
			}
		}
	}
	return CensorReplacement{}, ErrCensorConflict
}
func (b *nativeBackend) ReplacementReceipt(ctx context.Context, i CensorIdentity, s CensorSelection) (CensorReplacement, error) {
	if b.publisher == nil {
		return CensorReplacement{}, ErrCensorUnavailable
	}
	r, e := b.publisher.ReplacementReceipt(ctx, i, s)
	return nativeReplacement(r), e
}
func (b *nativeBackend) Rollback(ctx context.Context, i CensorIdentity, s CensorSelection) (CensorReplacement, error) {
	if b.publisher == nil {
		return CensorReplacement{}, ErrCensorUnavailable
	}
	original := s
	for _, j := range b.runtime.snapshot().Jobs {
		if j.Request.ID == s.RequestID && j.SelectedCandidate == s.CandidateID && j.RollbackKey == s.IdempotencyKey && j.RepositoryPath == b.repo {
			original.IdempotencyKey = j.PublicationKey
			r, e := b.publisher.Rollback(ctx, i, original)
			out := nativeReplacement(r)
			out.IdempotencyKey = s.IdempotencyKey
			return out, e
		}
	}
	return CensorReplacement{}, ErrCensorConflict
}
func validNativeRepositoryPath(p string) bool {
	return filepath.IsAbs(p) && filepath.Clean(p) == p && len(p) <= 4096 && !strings.ContainsAny(p, "\x00\r\n")
}
func (rt *censorRuntime) resolveRequest(ctx context.Context, i CensorIdentity, j *CensorJob) error {
	if b, ok := rt.backend.(censorRequestResolver); ok {
		r, e := b.Request(ctx, i, j.Request.ID)
		if e != nil {
			return e
		}
		if r.ID != j.Request.ID || r.TrackID != j.Request.TrackID || validateCensorRequests([]CensorRequest{r}) != nil {
			return ErrCensorConflict
		}
		j.Request = r
	}
	return nil
}

const legacyMissingRepositoryMessage = "Analysis is available; configure a repository path before saving a local replacement. Local saves do not publish to GitHub or complete the public request"

// Only this exact legacy readiness notice is safe to remove from historical
// failures. Configuration cannot repair genuine provider or analysis errors.
func (rt *censorRuntime) clearLegacyRepositoryErrorsLocked(persistRepository bool) error {
	previous := rt.disk.Jobs
	jobs := append([]CensorJob{}, previous...)
	changed := false
	for n := range jobs {
		if jobs[n].Error == legacyMissingRepositoryMessage {
			jobs[n].Error = ""
			changed = true
		}
	}
	if changed || persistRepository {
		rt.disk.Jobs = jobs
		if err := rt.persistLocked(); err != nil {
			rt.disk.Jobs = previous
			return err
		}
	}
	if rt.lastError == legacyMissingRepositoryMessage {
		rt.lastError = ""
	}
	return nil
}

func (rt *censorRuntime) nativeFailureMessage(err error) string {
	if _, ok := rt.backend.(*nativeBackend); !ok {
		return ""
	}
	return err.Error()
}

// Manual identities are authorized by a persisted, account-local runtime job,
// not by the public queue or by a caller-supplied audio URL.
func (b *nativeBackend) currentAccount(i CensorIdentity) bool {
	rt := b.runtime
	if rt == nil || rt.server == nil || i.UserID == "" || i.UserID != rt.uid {
		return false
	}
	rt.server.mu.Lock()
	defer rt.server.mu.Unlock()
	return rt.server.censorRuntime == rt && rt.server.session != nil && rt.server.session.User.ID == rt.uid
}
func (b *nativeBackend) Requests(ctx context.Context, i CensorIdentity) ([]CensorRequest, error) {
	items, err := b.nativeSource.Requests(ctx, i)
	for n := range items {
		items[n].Origin = "reported_tracks"
	}
	return items, err
}
func (b *nativeBackend) Request(ctx context.Context, i CensorIdentity, opaque string) (CensorRequest, error) {
	if !strings.HasPrefix(opaque, "manual:") {
		r, err := b.nativeSource.Request(ctx, i, opaque)
		if err == nil {
			r.Origin = "reported_tracks"
		}
		return r, err
	}
	if !b.currentAccount(i) {
		return CensorRequest{}, ErrCensorDenied
	}
	id, err := nativeTrackID("manual", opaque)
	if err != nil {
		return CensorRequest{}, err
	}
	rt := b.runtime
	rt.mu.Lock()
	found := false
	for _, j := range rt.disk.Jobs {
		if j.Request.ID == opaque && j.Request.TrackID == id && j.Request.Origin == "manual" {
			found = true
			break
		}
	}
	rt.mu.Unlock()
	if !found {
		return CensorRequest{}, ErrCensorNotFound
	}
	provider, err := b.yandex()
	if err != nil {
		return CensorRequest{}, err
	}
	r, err := b.requestAudio(ctx, provider, id)
	if err != nil {
		return CensorRequest{}, err
	}
	if !b.currentAccount(i) {
		return CensorRequest{}, ErrCensorDenied
	}
	r.ID = opaque
	r.Origin = "manual"
	return r, nil
}
func (b *nativeBackend) checkTrack(ctx context.Context, i CensorIdentity, trackID int64) error {
	if trackID <= 0 {
		return ErrCensorInvalid
	}
	if !b.currentAccount(i) {
		return ErrCensorDenied
	}
	opaque := nativeOpaqueID("manual", trackID)
	rt := b.runtime
	rt.mu.Lock()
	index := -1
	for n, j := range rt.disk.Jobs {
		if j.Request.ID == opaque {
			if j.Request.TrackID != trackID || j.Request.Origin != "manual" {
				rt.mu.Unlock()
				return ErrCensorConflict
			}
			// A duplicate click while this job is running is idempotent. Completed
			// reviews are deliberately replaced below so every explicit check gets
			// fresh candidate discovery and analysis instead of replaying persisted
			// evidence shown by the UI.
			if j.Status == "running" {
				rt.mu.Unlock()
				return nil
			}
			// Never discard publication state or receipts through re-analysis.
			if j.PublicationKey != "" || j.Status == "publishing" || j.Status == "rolling_back" || j.Status == "succeeded" || j.Status == "local_saved" {
				rt.mu.Unlock()
				return nil
			}
			index = n
			break
		}
	}
	if rt.running || rt.commandActive || rt.failure != "" {
		rt.mu.Unlock()
		return ErrCensorConflict
	}
	if index == -1 && len(rt.disk.Jobs) >= 1000 {
		rt.mu.Unlock()
		return ErrCensorCapacity
	}
	job := CensorJob{ID: RandomKey(), Request: CensorRequest{ID: opaque, TrackID: trackID, Origin: "manual"}, Status: "running", Stage: "analysis", Candidates: []CensorCandidate{}, UpdatedAt: time.Now().UTC()}
	previous := rt.disk.Jobs
	rt.disk.Jobs = append([]CensorJob{}, previous...)
	if index == -1 {
		rt.disk.Jobs = append(rt.disk.Jobs, job)
	} else {
		job.ID = previous[index].ID
		rt.disk.Jobs[index] = job
	}
	err := rt.persistLocked()
	if err != nil {
		rt.disk.Jobs = previous
	}
	rt.mu.Unlock()
	if err != nil {
		return err
	}
	return rt.startCommand(job.ID, func(work context.Context, identity CensorIdentity) error {
		if err := rt.pipeline(work, nil, &job); err != nil {
			return err
		}
		job.Status = "review_required"
		job.Stage = "review"
		job.Error = ""
		return rt.update(job)
	})
}
