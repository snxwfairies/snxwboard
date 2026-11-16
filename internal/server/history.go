package server

import (
	"net/http"
	"strconv"

	"github.com/snxwfairies/snxwboard/internal/store"
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
	// No session means the whole feed, newest first; see the session-less
	// branch below (2026-11-16, the MCP search_feed tool).
	session := q.Get("session")
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
	var (
		evs []store.Event
		err error
	)
	if session == "" {
		// No session: the newest events across every session, which is what a
		// feed search wants (MCP search_feed, 2026-11-16).
		evs, err = s.st.QueryRecent(r.Context(), limit)
	} else {
		evs, err = s.st.QuerySession(r.Context(), session, after, before, int(limit))
	}
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
