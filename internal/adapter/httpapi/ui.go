package httpapi

import (
	_ "embed"
	"net/http"
)

//go:embed web/index.html
var studentUIHTML []byte

func handleStudentUI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(studentUIHTML)
}
