package core

import (
	"errors"
	"net/http"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/linecatalog"
)

func (server *Server) lineAvailability(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if server.catalog == nil || server.eventStore == nil {
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{"code": "availability_unavailable"})
		return
	}
	if request.URL.RawQuery != "" {
		writeJSON(response, http.StatusBadRequest, map[string]string{"code": "invalid_availability_query"})
		return
	}
	lineID := request.PathValue("lineID")
	if _, err := server.catalog.Get(lineID); errors.Is(err, linecatalog.ErrNotFound) {
		writeJSON(response, http.StatusNotFound, map[string]string{"code": "line_not_found"})
		return
	} else if err != nil {
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{"code": "catalog_read_failed"})
		return
	}
	history, err := server.eventStore.Availability(lineID, server.now())
	if err != nil {
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{"code": "availability_read_failed"})
		return
	}
	writeJSON(response, http.StatusOK, history)
}
