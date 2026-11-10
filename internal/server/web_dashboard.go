package server

import (
	"embed"
	"net/http"
	"strings"
)

//go:embed web/*
var dashboardFiles embed.FS

func registerWebDashboard(mux *http.ServeMux) {
	mux.HandleFunc("/dashboard", func(w http.ResponseWriter, r *http.Request) {
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		serveDashboardFile(w, "web/index.html", "text/html; charset=utf-8")
	})
	mux.HandleFunc("/dashboard/", func(w http.ResponseWriter, r *http.Request) {
		if !requireMethod(w, r, http.MethodGet) {
			return
		}
		switch strings.TrimPrefix(r.URL.Path, "/dashboard/") {
		case "", "index.html":
			serveDashboardFile(w, "web/index.html", "text/html; charset=utf-8")
		case "style.css":
			serveDashboardFile(w, "web/style.css", "text/css; charset=utf-8")
		case "app.js":
			serveDashboardFile(w, "web/app.js", "text/javascript; charset=utf-8")
		default:
			http.NotFound(w, r)
		}
	})
}

func serveDashboardFile(w http.ResponseWriter, name, contentType string) {
	data, err := dashboardFiles.ReadFile(name)
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; script-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'")
	_, _ = w.Write(data)
}
