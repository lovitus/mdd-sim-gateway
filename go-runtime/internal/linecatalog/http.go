package linecatalog

import (
	"encoding/json"
	"net/http"
	"strings"
)

type Handler struct{ store *Store }

func NewHandler(store *Store) *Handler { return &Handler{store: store} }

func (handler *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	if handler == nil || handler.store == nil {
		writeCatalogJSON(response, http.StatusServiceUnavailable, map[string]string{"code": "catalog_unavailable"})
		return
	}
	if request.Method != http.MethodGet {
		writeCatalogJSON(response, http.StatusMethodNotAllowed, map[string]string{"code": "method_not_allowed"})
		return
	}
	id := strings.TrimSpace(request.PathValue("lineID"))
	if id == "" {
		snapshot, err := handler.store.Snapshot()
		if err != nil {
			writeCatalogJSON(response, http.StatusInternalServerError, map[string]string{"code": "catalog_read_failed"})
			return
		}
		writeCatalogJSON(response, http.StatusOK, snapshot)
		return
	}
	line, err := handler.store.Get(id)
	if err == ErrNotFound {
		writeCatalogJSON(response, http.StatusNotFound, map[string]string{"code": "line_not_found"})
		return
	}
	if err != nil {
		writeCatalogJSON(response, http.StatusInternalServerError, map[string]string{"code": "catalog_read_failed"})
		return
	}
	writeCatalogJSON(response, http.StatusOK, line)
}

func writeCatalogJSON(response http.ResponseWriter, status int, value any) {
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
