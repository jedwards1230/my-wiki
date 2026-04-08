package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/jedwards1230/home-wiki/internal/service"
)

func (h *Handler) handlePageRead(w http.ResponseWriter, r *http.Request) {
	path := r.PathValue("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}

	content, err := h.pages.Read(path)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"path":    path,
		"content": content,
	})
}

func (h *Handler) handlePageWrite(w http.ResponseWriter, r *http.Request) {
	path := r.PathValue("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read body")
		return
	}

	if err := h.pages.Write(path, string(body)); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
		"path":   path,
	})
}

func (h *Handler) handlePageDelete(w http.ResponseWriter, r *http.Request) {
	path := r.PathValue("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}

	if err := h.pages.Delete(path); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) handlePagePatch(w http.ResponseWriter, r *http.Request) {
	path := r.PathValue("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}

	var body struct {
		Operations []service.PatchOp `json:"operations"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if len(body.Operations) == 0 {
		writeError(w, http.StatusBadRequest, "operations is required and must not be empty")
		return
	}

	content, err := h.pages.Patch(path, body.Operations)
	if err != nil {
		msg := err.Error()
		switch {
		case strings.Contains(msg, "page not found"):
			writeError(w, http.StatusNotFound, msg)
		case strings.Contains(msg, "path traversal"),
			strings.Contains(msg, "must not be empty"),
			strings.Contains(msg, "must be non-empty"):
			writeError(w, http.StatusBadRequest, msg)
		case strings.Contains(msg, "find string not found"):
			writeError(w, http.StatusUnprocessableEntity, msg)
		default:
			writeError(w, http.StatusInternalServerError, msg)
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"path":    path,
		"content": content,
	})
}

func (h *Handler) handlePageList(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")

	pages, err := h.pages.List(prefix)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, pages)
}
