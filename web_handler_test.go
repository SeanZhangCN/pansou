package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestAppHandlerGateway(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string]string{
		"index.html":          "home",
		"designs/cinema.html": "cinema",
		"assets/site.css":     "body{}",
	} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health" {
			t.Errorf("API path = %q", r.URL.Path)
		}
		w.Write([]byte("ok"))
	})
	handler := newAppHandler(api, dir, "/app/pansou")
	for _, tc := range []struct {
		path, body string
		status     int
	}{
		{"/app/pansou", "home", 200},
		{"/app/pansou/designs/cinema.html", "cinema", 200},
		{"/app/pansou/assets/site.css", "body{}", 200},
		{"/app/pansou/api/health", "ok", 200},
		{"/app/pansou/missing.js", "404 page not found\n", 404},
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest("GET", tc.path, nil))
		if recorder.Code != tc.status || recorder.Body.String() != tc.body {
			t.Errorf("GET %s: status=%d body=%q", tc.path, recorder.Code, recorder.Body.String())
		}
	}
}
