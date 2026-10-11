package bridge

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

// Playback URLs are transient access, not replacements for persisted audio evidence.
func (s *Server) handleCensorPlayback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		failure(w, 405, "method not allowed")
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query) != 2 || len(query["id"]) != 1 || len(query["candidate_id"]) != 1 || !censorID(query.Get("id")) || !censorID(query.Get("candidate_id")) {
		failure(w, 400, "invalid censor playback parameters")
		return
	}
	// Playback only resolves transient access to existing evidence. It must not
	// queue behind independent analysis or polling, nor replace operationCancel.
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	stop := context.AfterFunc(s.lifecycle, cancel)
	key := make(chan struct{})
	s.mu.Lock()
	if s.closing || s.logoutPending > 0 || s.session == nil {
		s.mu.Unlock()
		cancel()
		stop()
		censorFailure(w, ErrCensorDenied)
		return
	}
	sess := s.session
	uid := sess.User.ID
	if s.playbackCancels == nil {
		s.playbackCancels = make(map[chan struct{}]context.CancelFunc)
	}
	s.playbackCancels[key] = cancel
	s.mu.Unlock()
	defer func() {
		cancel()
		stop()
		s.mu.Lock()
		delete(s.playbackCancels, key)
		s.mu.Unlock()
	}()
	token, err := s.authorize(ctx)
	if err != nil {
		failure(w, 403, "censor authorization required")
		return
	}
	s.mu.Lock()
	identity := CensorIdentity{UserID: uid, AccessToken: token}
	rt := s.censorRuntime
	current := s.session != nil && s.session.User.ID == uid && s.session.Access == token && !s.closing && s.logoutPending == 0
	sess = s.session
	s.mu.Unlock()
	// Do not create/replace runtimes outside operation serialization. Stored
	// membership necessarily belongs to an already-open runtime.
	if !current || rt == nil || rt.uid != uid || ctx.Err() != nil {
		censorFailure(w, ErrCensorDenied)
		return
	}
	id, candidateID := query.Get("id"), query.Get("candidate_id")
	if err = rt.playbackMembership(id, candidateID); err != nil {
		censorFailure(w, err)
		return
	}
	// No runtime mutex is held across provider I/O. Prefer playback-specific
	// resolution so listening does not inherit approval-only metadata checks.
	var candidate CensorCandidate
	switch resolver := rt.backend.(type) {
	case censorPlaybackResolver:
		candidate, err = resolver.PlaybackCandidate(ctx, identity, candidateID)
	case censorCandidateResolver:
		candidate, err = resolver.Candidate(ctx, identity, candidateID)
	default:
		censorFailure(w, ErrCensorUnsupported)
		return
	}
	if err != nil {
		censorFailure(w, err)
		return
	}
	if candidate.ID != candidateID || !censorPlaybackAudio(candidate) || validateCensorCandidates([]CensorCandidate{candidate}) != nil {
		censorFailure(w, ErrCensorConflict)
		return
	}
	s.mu.Lock()
	current = s.censorRuntime == rt && s.session == sess && s.session != nil && s.session.User.ID == identity.UserID && s.session.Access == token && !s.closing && s.logoutPending == 0
	s.mu.Unlock()
	if !current || ctx.Err() != nil {
		censorFailure(w, ErrCensorDenied)
		return
	}
	// A background command may have removed the candidate during resolution.
	if err = rt.playbackMembership(id, candidateID); err != nil {
		censorFailure(w, err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session != sess || s.censorRuntime != rt || s.closing || s.logoutPending > 0 || ctx.Err() != nil {
		censorFailure(w, ErrCensorDenied)
		return
	}
	send(w, 200, map[string]string{"url": candidate.URL})
}

// Listening to discovered public media does not establish approval evidence.
func censorPlaybackAudio(c CensorCandidate) bool {
	if censorAudioCandidate(c) {
		return true
	}
	source, err := webDecode(c.ID)
	u, parseErr := url.Parse(c.URL)
	return err == nil && parseErr == nil && webURL(u) && c.ID == webID(source, c.URL) && c.Downloadable && c.Provider == "direct_web" && c.Metadata["kind"] == "audio" && c.Metadata["content_type"] == "audio/mpeg" && c.Metadata["source_url"] == source && c.Metadata["audio_url"] == c.URL
}

func (rt *censorRuntime) playbackMembership(id, candidateID string) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	job, err := rt.jobLocked(id)
	if err != nil {
		return err
	}
	for _, candidate := range job.Candidates {
		if candidate.ID == candidateID {
			if !censorPlaybackAudio(candidate) {
				return ErrCensorConflict
			}
			return nil
		}
	}
	return ErrCensorNotFound
}
