// Package server exposes the JSON API and serves the web UI.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"time"

	"cleanarr/internal/cleaner"
	"cleanarr/internal/config"
	"cleanarr/internal/scanner"
)

type Server struct {
	cfg     *config.Store
	svc     *cleaner.Service
	static  fs.FS
	version string
}

func New(cfg *config.Store, svc *cleaner.Service, static fs.FS, version string) *Server {
	return &Server{cfg: cfg, svc: svc, static: static, version: version}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("POST /api/scan", s.scan)
	mux.HandleFunc("GET /api/items", s.items)
	mux.HandleFunc("POST /api/plan", s.plan)
	mux.HandleFunc("GET /api/jobs", s.jobs)
	mux.HandleFunc("POST /api/jobs", s.createJob)
	mux.HandleFunc("GET /api/jobs/{id}", s.job)
	mux.HandleFunc("GET /api/config", s.getConfig)
	mux.HandleFunc("PUT /api/config", s.putConfig)
	mux.HandleFunc("POST /api/test/arr", s.testArr)
	mux.HandleFunc("POST /api/test/qbit", s.testQbit)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, errors.New("not found"))
	})
	files := http.FileServerFS(s.static)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Embedded files carry no modification time; make browsers revalidate
		// so an upgrade is picked up immediately.
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
	return logRequests(mux)
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			log.Printf("%s %s", r.Method, r.URL.Path)
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func readJSON(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 8<<20)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		return errors.New("invalid request: " + err.Error())
	}
	return nil
}

type scanInfo struct {
	StartedAt    time.Time      `json:"startedAt"`
	FinishedAt   time.Time      `json:"finishedAt"`
	FilesScanned int            `json:"filesScanned"`
	TrackedFiles int            `json:"trackedFiles"`
	Torrents     int            `json:"torrents"`
	Warnings     []string       `json:"warnings"`
	Roots        []scanner.Root `json:"roots"`
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{
		"version": s.version,
		"scan":    s.svc.ScanState(),
		"summary": s.svc.Summary(),
	}
	if res := s.svc.Result(); res != nil {
		resp["lastScan"] = scanInfo{
			StartedAt: res.StartedAt, FinishedAt: res.FinishedAt, FilesScanned: res.FilesScanned,
			TrackedFiles: res.TrackedFiles, Torrents: res.Torrents, Warnings: res.Warnings, Roots: res.Roots,
		}
	}
	active := 0
	for _, j := range s.svc.Jobs() {
		if j.Status == cleaner.Queued || j.Status == cleaner.Running {
			active++
		}
	}
	resp["activeJobs"] = active
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) scan(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.StartScan(); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, s.svc.ScanState())
}

func (s *Server) items(w http.ResponseWriter, r *http.Request) {
	res := s.svc.Result()
	if res == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	writeJSON(w, http.StatusOK, res.Items)
}

type deleteRequest struct {
	IDs     []string        `json:"ids"`
	Options cleaner.Options `json:"options"`
}

func (s *Server) plan(w http.ResponseWriter, r *http.Request) {
	var req deleteRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	p, err := s.svc.Plan(req.IDs, req.Options)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) createJob(w http.ResponseWriter, r *http.Request) {
	var req deleteRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	j, err := s.svc.CreateJob(req.IDs, req.Options)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusAccepted, j)
}

func (s *Server) jobs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.Jobs())
}

func (s *Server) job(w http.ResponseWriter, r *http.Request) {
	j := s.svc.Job(r.PathValue("id"))
	if j == nil {
		writeError(w, http.StatusNotFound, errors.New("job not found"))
		return
	}
	writeJSON(w, http.StatusOK, j)
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.cfg.Get())
}

func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	c := config.Default()
	if err := readJSON(r, &c); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.cfg.Set(c); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, s.cfg.Get())
}

func (s *Server) testArr(w http.ResponseWriter, r *http.Request) {
	var inst config.ArrInstance
	if err := readJSON(r, &inst); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	msg, err := cleaner.TestArr(ctx, inst)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": msg})
}

func (s *Server) testQbit(w http.ResponseWriter, r *http.Request) {
	var inst config.QbitInstance
	if err := readJSON(r, &inst); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	msg, err := cleaner.TestQbit(ctx, inst)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": msg})
}
