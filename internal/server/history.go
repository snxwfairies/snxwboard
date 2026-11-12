package server

import (
	"net/http"
	"strconv"

	"github.com/snxwfairies-ai/snxwboard-oss/internal/store"
)

const (
	historyDefault = 200
	historyMax     = 1000
)

// handleHistory serves one session's events for the detail view:
// ?session=ID&after=SEQ (newer, oldest first) or &before=SEQ (older, newest
// first), at most limit.
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	session := q.Get("session")
	if session == "" {
		writeError(w, http.StatusBadRequest, "session required")
		return
	}
	after, err1 := optInt(q.Get("after"))
	before, err2 := optInt(q.Get("before"))
	limit, err3 := optInt(q.Get("limit"))
	if err1 != nil || err2 != nil || err3 != nil || after < 0 || before < 0 || limit < 0 {
		writeError(w, http.StatusBadRequest, "after, before and limit must be non-negative integers")
		return
	}
	if limit == 0 {
		limit = historyDefault
	}
	if limit > historyMax {
		limit = historyMax
	}
	evs, err := s.st.QuerySession(r.Context(), session, after, before, int(limit))
	if err != nil {
		s.log.Error("read history", "session", session, "err", err)
		writeError(w, http.StatusInternalServerError, "read history")
		return
	}
	if evs == nil {
		evs = []store.Event{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": evs})
}

func optInt(v string) (int64, error) {
	if v == "" {
		return 0, nil
	}
	return strconv.ParseInt(v, 10, 64)
}
