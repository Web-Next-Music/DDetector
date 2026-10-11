package bridge

import (
	"net/http"
	"net/url"
	"strconv"
	"time"
)

func (s *Server) handleCensorDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		failure(w, 405, "method not allowed")
		return
	}
	// ParseQuery reports malformed escaping instead of silently ignoring it.
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		failure(w, 400, "invalid censor detail parameters")
		return
	}
	id, offset := "", 0
	if r.URL.Path == "/api/censor/job" {
		if len(query) != 1 || len(query["id"]) != 1 || !censorID(query.Get("id")) {
			failure(w, 400, "invalid censor job id")
			return
		}
		id = query.Get("id")
	} else {
		if len(query) != 1 || len(query["offset"]) != 1 {
			failure(w, 400, "invalid censor page")
			return
		}
		offset, err = strconv.Atoi(query.Get("offset"))
		if err != nil || offset < 0 || offset > 1000 {
			failure(w, 400, "invalid censor page")
			return
		}
	}
	if !s.tryOperation() {
		failure(w, 409, "another operation is running")
		return
	}
	defer s.op.Unlock()
	ctx, cancel := s.operationContext(r.Context(), 30*time.Second)
	defer cancel()
	if _, err = s.authorize(ctx); err != nil {
		failure(w, 403, "censor authorization required")
		return
	}
	s.mu.Lock()
	uid := s.session.User.ID
	s.mu.Unlock()
	rt, err := s.currentCensorRuntime(ctx, uid)
	if err != nil {
		censorFailure(w, err)
		return
	}
	if id != "" {
		rt.mu.Lock()
		job, err := rt.jobLocked(id)
		var view censorUIJob
		if err == nil {
			view = censorJobView(*job, false)
		}
		rt.mu.Unlock()
		if err != nil {
			censorFailure(w, err)
			return
		}
		sendCensorJSON(w, 200, map[string]any{"job": view})
	} else {
		sendCensorPage(w, 200, rt.snapshot(), offset)
	}
}
