package bridge

import (
	"errors"
	"net/http"

	"strings"
	"time"
	"unicode"
)

func (s *Server) handleCensor(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/censor/playback" {
		s.handleCensorPlayback(w, r)
		return
	}
	if r.URL.Path == "/api/censor/job" || r.URL.Path == "/api/censor/jobs" {
		s.handleCensorDetail(w, r)
		return
	}
	if r.URL.Path == "/api/censor/state" || r.URL.Path == "/api/censor/control" {
		s.handleCensorUI(w, r)
		return
	}
	method := ""
	switch r.URL.Path {
	case "/api/censor/requests":
		method = http.MethodGet
	case "/api/censor/search", "/api/censor/replacement":
		method = http.MethodPost
	default:
		failure(w, 404, "not found")
		return
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
	var query struct {
		Query string `json:"query"`
	}
	var selection CensorSelection
	switch r.URL.Path {
	case "/api/censor/search":
		if decode(w, r, &query) != nil || !validCensorQuery(query.Query) {
			failure(w, 400, "invalid search query")
			return
		}
	case "/api/censor/replacement":
		if decode(w, r, &selection) != nil || !censorID(selection.RequestID) || !censorID(selection.CandidateID) || selection.TrackID <= 0 {
			failure(w, 400, "invalid replacement selection")
			return
		}
	}
	if !s.tryOperation() {
		failure(w, 409, "another operation is running")
		return
	}
	defer s.op.Unlock()
	ctx, cancel := s.operationContext(r.Context(), 90*time.Second)
	defer cancel()
	token, err := s.authorize(ctx)
	if err != nil {
		failure(w, 403, "censor authorization required")
		return
	}
	s.mu.Lock()
	identity := CensorIdentity{UserID: s.session.User.ID, AccessToken: token}
	s.mu.Unlock()
	rt, err := s.currentCensorRuntime(ctx, identity.UserID)
	if err != nil {
		censorFailure(w, ErrCensorUnavailable)
		return
	}
	backend := rt.backend
	switch r.URL.Path {
	case "/api/censor/requests":
		items, e := backend.Requests(ctx, identity)
		if e != nil {
			censorFailure(w, e)
			return
		}
		if validateBackendRequests(backend, items) != nil {
			failure(w, 502, "invalid censor response")
			return
		}
		if items == nil {
			items = []CensorRequest{}
		}
		send(w, 200, map[string]any{"requests": items})
	case "/api/censor/search":
		items, e := backend.Search(ctx, identity, strings.TrimSpace(query.Query))
		if e != nil {
			censorFailure(w, e)
			return
		}
		if validateCensorCandidates(items) != nil {
			failure(w, 502, "invalid censor response")
			return
		}
		if items == nil {
			items = []CensorCandidate{}
		}
		send(w, 200, map[string]any{"candidates": items})
	case "/api/censor/replacement":
		// Legacy publication cannot bypass the manual runtime review gate.
		var job CensorJob
		for _, candidateJob := range rt.snapshot().Jobs {
			if candidateJob.Request.ID == selection.RequestID && candidateJob.Request.TrackID == selection.TrackID {
				job = candidateJob
				break
			}
		}
		if job.ID == "" {
			censorFailure(w, ErrCensorConflict)
			return
		}
		if e := rt.control(ctx, identity, censorControl{Action: "approve", ID: job.ID, RequestID: selection.RequestID, TrackID: selection.TrackID, CandidateID: selection.CandidateID}); e != nil {
			censorFailure(w, e)
			return
		}
		sendCensorState(w, 200, rt.snapshot())
	}
}

func validCensorQuery(q string) bool {
	if strings.TrimSpace(q) == "" || len(q) > 512 {
		return false
	}
	for _, r := range q {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func censorFailure(w http.ResponseWriter, err error) {
	var runtimeFailure *censorRuntimeFailure
	if errors.As(err, &runtimeFailure) {
		failure(w, http.StatusServiceUnavailable, runtimeFailure.Error())
		return
	}
	var resource *censorResourceError
	if errors.As(err, &resource) {
		status := http.StatusNotImplemented
		if resource.unavailable {
			status = http.StatusServiceUnavailable
		}
		failure(w, status, resource.Error())
		return
	}
	switch {
	case errors.Is(err, ErrCensorUnavailable):
		failure(w, 503, "censor backend is unavailable; check native source, Yandex account and repository configuration")
	case errors.Is(err, ErrCensorUnsupported):
		failure(w, 501, "censor operation is not supported by this backend")
	case errors.Is(err, ErrCensorInvalid):
		failure(w, 400, "invalid censor control parameters")
	case errors.Is(err, ErrCensorDenied):
		failure(w, 403, "censor access denied")
	case errors.Is(err, ErrCensorBusy):
		failure(w, http.StatusConflict, ErrCensorBusy.Error())
	case errors.Is(err, ErrCensorSettingsBusy):
		failure(w, http.StatusConflict, ErrCensorSettingsBusy.Error())
	case errors.Is(err, ErrCensorConflict):
		failure(w, 409, "censor selection is stale")
	case errors.Is(err, ErrCensorNotFound):
		failure(w, 404, "censor item not found")
	default:
		failure(w, 502, "censor backend request failed")
	}
}
