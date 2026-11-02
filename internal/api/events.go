package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/ansrazm/dropgate/internal/events"
)

// listEvents returns the newest events of a drop from the MongoDB log.
// GET /api/drops/{id}/events?limit=50
func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	list, err := s.events.Recent(r.Context(), id, limit)
	switch {
	case errors.Is(err, events.ErrDisabled):
		writeError(w, http.StatusServiceUnavailable, "EVENT_LOG_DISABLED", "set MONGODB_URI to enable the event log")
	case err != nil:
		s.internalError(w, r, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"events": list})
	}
}
