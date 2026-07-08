package middleware

import "net/http"

// statusWriter wraps an http.ResponseWriter to capture the response status code
// for the logging and metrics middlewares. It records the first WriteHeader (or
// defaults to 200 on the first Write) and exposes the underlying writer via
// Unwrap so http.ResponseController can reach Flush/Hijack on the real writer.
type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.status = code
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.status = http.StatusOK
		w.wroteHeader = true
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
