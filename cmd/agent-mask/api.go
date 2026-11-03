package main

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/adamwang99/agent-mask/internal/batch"
)

// serve: the local HTTP API. 127.0.0.1 only, a bearer token on every request (a web page in
// the user's browser cannot read the token, so it cannot drive the API either).
func serve(port int) error {
	token := apiToken()
	m := batch.NewManager()
	m.OnEnd = func(j *batch.Job) {
		for _, p := range batch.Notify(j, token) {
			fmt.Fprintln(os.Stderr, "warning:", p)
		}
		fmt.Fprintf(os.Stderr, "%s %s: %s\n", time.Now().Format("15:04:05"), j.ID, batch.Summary(j))
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Agent Mask API on http://127.0.0.1:%d\nToken: %s\n(also in %s; AGENT_MASK_TOKEN overrides it)\n", port, token, "api-token in "+configDirHint())
	return http.Serve(ln, apiHandler(m, token))
}

func configDirHint() string { return strings.TrimSpace(fmtConfig()) }

func apiHandler(m *batch.Manager, token string) http.Handler {
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	fail := func(w http.ResponseWriter, code int, err error) {
		reply(w, code, map[string]string{"error": err.Error()})
	}
	job := func(w http.ResponseWriter, r *http.Request) (*batch.Job, bool) {
		j, ok := m.Get(r.PathValue("id"))
		if !ok {
			fail(w, 404, fmt.Errorf("no job %s", r.PathValue("id")))
		}
		return j, ok
	}
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, map[string]any{"ok": true, "version": version})
	})
	mux.HandleFunc("POST /jobs", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			batch.Options
			Input  string   `json:"input"`
			Output string   `json:"output"`
			Files  []string `json:"files"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			fail(w, 400, err)
			return
		}
		if req.Callback != "" && !strings.HasPrefix(req.Callback, "http://") && !strings.HasPrefix(req.Callback, "https://") {
			fail(w, 400, fmt.Errorf("callbackUrl must be http(s)"))
			return
		}
		var j *batch.Job
		var err error
		if len(req.Files) > 0 {
			j, err = m.StartFiles(req.Input, req.Output, req.Files, req.Options)
		} else {
			j, err = m.Start(req.Input, req.Output, req.Options)
		}
		if err != nil {
			fail(w, 400, err)
			return
		}
		w.Header().Set("Location", "/jobs/"+j.ID)
		reply(w, 202, j)
	})
	mux.HandleFunc("GET /jobs", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, map[string]any{"jobs": m.Jobs()}) })
	mux.HandleFunc("GET /jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		if j, ok := job(w, r); ok {
			reply(w, 200, j)
		}
	})
	mux.HandleFunc("POST /jobs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		if err := m.Cancel(r.PathValue("id")); err != nil {
			fail(w, 404, err)
			return
		}
		reply(w, 202, map[string]string{"canceling": r.PathValue("id")})
	})
	mux.HandleFunc("POST /jobs/{id}/wait", func(w http.ResponseWriter, r *http.Request) {
		sec, _ := strconv.Atoi(r.URL.Query().Get("timeout"))
		if sec <= 0 || sec > 3600 {
			sec = 600
		}
		j, err := m.Wait(r.PathValue("id"), time.Duration(sec)*time.Second)
		if err != nil {
			fail(w, 404, err)
			return
		}
		reply(w, 200, j)
	})
	mux.HandleFunc("GET /jobs/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		ch, stop, err := m.Subscribe(r.PathValue("id"))
		if err != nil {
			fail(w, 404, err)
			return
		}
		defer stop()
		fl, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		for {
			select {
			case e, ok := <-ch:
				if !ok {
					return
				}
				b, _ := json.Marshal(e)
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, b)
				if fl != nil {
					fl.Flush()
				}
				if e.Type == "done" {
					return
				}
			case <-r.Context().Done():
				return
			}
		}
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only local callers that know the token; a browser page from another site is refused
		// even before the token (it would carry an Origin header).
		if o := r.Header.Get("Origin"); o != "" {
			fail(w, 403, fmt.Errorf("browser requests are not accepted"))
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			fail(w, 401, fmt.Errorf("missing or wrong token (Authorization: Bearer <token>)"))
			return
		}
		mux.ServeHTTP(w, r)
	})
}
