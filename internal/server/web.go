package server

import (
	"io/fs"
	"net/http"
	"strings"
)

// mountWeb serves the built frontend: the landing page at /, the privacy notice at /privacy, the app
// (a single-page client) under /app and fingerprinted assets under /assets.
func (s *Server) mountWeb() {
	if s.Web == nil {
		return
	}
	page := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if _, err := fs.Stat(s.Web, name); err != nil {
				http.Error(w, "frontend is not built", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFileFS(w, r, s.Web, name)
		}
	}
	s.mux.HandleFunc("GET /{$}", page("landing.html"))
	s.mux.HandleFunc("GET /privacy", page("privacy.html"))
	s.mux.HandleFunc("GET /app", page("index.html"))
	s.mux.HandleFunc("GET /app/{path...}", page("index.html"))

	assets := http.FileServerFS(fileOnlyFS{s.Web})
	s.mux.HandleFunc("GET /assets/{path...}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		assets.ServeHTTP(w, r)
	})
	// Single files in the web root (favicon, robots.txt, ...).
	s.mux.HandleFunc("GET /{file}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("file")
		if strings.HasSuffix(name, ".html") || strings.HasPrefix(name, ".") {
			http.NotFound(w, r)
			return
		}
		assets.ServeHTTP(w, r)
	})
}

// fileOnlyFS hides directories so the file server never renders a directory listing.
type fileOnlyFS struct{ fs.FS }

func (f fileOnlyFS) Open(name string) (fs.File, error) {
	file, err := f.FS.Open(name)
	if err != nil {
		return nil, err
	}
	if st, err := file.Stat(); err != nil || st.IsDir() {
		file.Close()
		return nil, fs.ErrNotExist
	}
	return file, nil
}
