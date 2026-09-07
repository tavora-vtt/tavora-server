package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestHealthIsIndependentOfStorage(t *testing.T) {
	router := NewRouter(Deps{
		Log:     discardLogger(),
		Ready:   func(context.Context) error { return errors.New("storage down") },
		Backend: "sqlite",
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if recorder.Code != http.StatusOK {
		t.Errorf("healthz returned %d while storage was down, want %d", recorder.Code, http.StatusOK)
	}
}

func TestReadinessFollowsStorage(t *testing.T) {
	tests := []struct {
		name  string
		ready ReadinessCheck
		want  int
	}{
		{"available", func(context.Context) error { return nil }, http.StatusOK},
		{"unavailable", func(context.Context) error { return errors.New("no connection") }, http.StatusServiceUnavailable},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := NewRouter(Deps{Log: discardLogger(), Ready: test.ready, Backend: "sqlite"})

			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))

			if recorder.Code != test.want {
				t.Errorf("readyz returned %d, want %d", recorder.Code, test.want)
			}
			if got := recorder.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
				t.Errorf("content type is %q", got)
			}
		})
	}
}

func TestUnknownPathIsNotFound(t *testing.T) {
	router := NewRouter(Deps{Log: discardLogger()})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/nope", nil))

	if recorder.Code != http.StatusNotFound {
		t.Errorf("unknown path returned %d, want %d", recorder.Code, http.StatusNotFound)
	}
}
