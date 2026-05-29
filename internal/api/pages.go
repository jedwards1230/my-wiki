package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/jedwards1230/my-wiki/internal/notify"
	"github.com/jedwards1230/my-wiki/internal/service"
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

	r.Body = http.MaxBytesReader(w, r.Body, 10<<20) // 10 MB
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large (max 10MB)")
		} else {
			writeError(w, http.StatusBadRequest, "failed to read body")
		}
		return
	}

	if err := h.pages.Write(path, string(body)); err != nil {
		var ve *service.ValidationError
		switch {
		case errors.Is(err, service.ErrPathDenied):
			writeError(w, http.StatusNotFound, err.Error())
		case errors.As(err, &ve):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	h.markDirty(path, notify.ChangeModified)
	warnings := h.lint.LintPage(path)
	writeJSONWithWarnings(w, http.StatusOK, map[string]string{
		"status": "ok",
		"path":   path,
	}, warnings)
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

	h.markDirty(path, notify.ChangeDeleted)
	warnings := h.lint.LintDelete(path)
	writeJSONWithWarnings(w, http.StatusOK, map[string]string{"status": "deleted"}, warnings)
}

func (h *Handler) handlePagePatch(w http.ResponseWriter, r *http.Request) {
	path := r.PathValue("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 10<<20) // 10 MB
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
		case errors.Is(err, service.ErrPathDenied):
			writeError(w, http.StatusNotFound, msg)
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

	h.markDirty(path, notify.ChangeModified)
	warnings := h.lint.LintPage(path)
	writeJSONWithWarnings(w, http.StatusOK, map[string]string{
		"path":    path,
		"content": content,
	}, warnings)
}

func (h *Handler) handlePageList(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	sortBy := r.URL.Query().Get("sort_by")
	limit := 0
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			limit = n
		}
	}

	pages, err := h.pages.List(service.ListOptions{
		Prefix: prefix,
		SortBy: sortBy,
		Limit:  limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, pages)
}
