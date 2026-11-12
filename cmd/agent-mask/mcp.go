package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/adamwang99/agent-mask/internal/batch"
	"github.com/adamwang99/agent-mask/internal/mcp"
)

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func obj(props map[string]any, required ...string) map[string]any {
	o := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		o["required"] = required
	}
	return o
}
func sArg(a map[string]any, k string) string { s, _ := a[k].(string); return s }
func nArg(a map[string]any, k string, def float64) float64 {
	if v, ok := a[k].(float64); ok && v > 0 {
		return v
	}
	return def
}

func optionsFrom(a map[string]any) batch.Options {
	// No command and no callback from an agent: it is told by job_wait, and a tool argument
	// must never become a shell command or an outbound request.
	o := batch.Options{Lang: sArg(a, "lang"), Style: sArg(a, "style")}
	if k := sArg(a, "kinds"); k != "" {
		o.Kinds = strings.Split(k, ",")
	}
	o.Overwrite, _ = a["overwrite"].(bool)
	o.Notify, _ = a["notify"].(bool)
	return o
}

// view: a job for an agent, files only when asked (a big folder makes a long answer).
func view(j *batch.Job, files bool) map[string]any {
	out := map[string]any{"id": j.ID, "status": j.Status, "input": j.Input, "output": j.Output, "counts": j.Counts, "summary": batch.Summary(j)}
	if j.Report != "" && j.Ended() {
		out["report"], out["reportMd"] = j.Report, strings.TrimSuffix(j.Report, ".json")+".md"
	}
	if j.Error != "" {
		out["error"] = j.Error
	}
	if files {
		out["files"] = j.Files
	} else {
		var attention []*batch.FileReport
		for _, f := range j.Files {
			if f.Status == "review" || f.Status == "failed" {
				attention = append(attention, f)
			}
		}
		if len(attention) > 0 {
			out["needsAttention"] = attention
		}
	}
	return out
}

// waitWithProgress waits for a job, reporting each file as MCP progress when the client asked.
func waitWithProgress(m *batch.Manager, id string, timeout time.Duration, p mcp.Progress) (*batch.Job, error) {
	if p != nil {
		if ch, stop, err := m.Subscribe(id); err == nil {
			go func() {
				for e := range ch {
					if e.Type == "file" && e.File != nil {
						p(float64(e.Counts.Done), float64(e.Counts.Total), fmt.Sprintf("%s %s", e.File.Status, e.File.Input))
					}
				}
			}()
			defer stop()
		}
	}
	return m.Wait(id, timeout)
}

func mcpServer() *mcp.Server {
	m := batch.NewManager()
	m.OnEnd = func(j *batch.Job) { batch.Notify(j, apiToken()) }
	common := map[string]any{
		"kinds":     str("Comma-separated kinds to hide (default all): email, phone, api-key, jwt, private-key, credit-card, secret-assignment, url-credentials, high-entropy."),
		"lang":      str("OCR languages: eng (default) or vie+eng."),
		"style":     str("auto (default), soft, mosaic, blur, solid."),
		"overwrite": map[string]any{"type": "boolean", "description": "Redo files already done."},
		"notify":    map[string]any{"type": "boolean", "description": "Show a desktop notice when it ends."},
	}
	with := func(extra map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range common {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	waitDesc := map[string]any{"type": "number", "description": "Seconds to wait at most (default 600, max 3600)."}
	tools := []mcp.Tool{
		{Name: "mask_folder",
			Description: "Hide sensitive text (emails, phones, keys, passwords, cards, IDs...) in every video and picture of a folder, sub-folders kept, and write the results to another folder. Each result is checked again by OCR: files that still show something go to <output>/_review/. Writes report.json and report.md. Returns a job id at once; then call job_wait (blocks until done, the simplest way to be told) or job_status. wait=true does both in one call.",
			InputSchema: obj(with(map[string]any{"input": str("Folder to read."), "output": str("Folder to write (not inside the input)."),
				"wait": map[string]any{"type": "boolean", "description": "Block until the job ends (up to timeoutSec)."}, "timeoutSec": waitDesc}), "input", "output"),
			Call: func(a map[string]any) (any, error) {
				j, err := m.Start(sArg(a, "input"), sArg(a, "output"), optionsFrom(a))
				if err != nil {
					return nil, err
				}
				if w, _ := a["wait"].(bool); w {
					p, _ := a[mcp.ProgressKey].(mcp.Progress)
					if j, err = waitWithProgress(m, j.ID, time.Duration(min(3600, nArg(a, "timeoutSec", 600)))*time.Second, p); err != nil {
						return nil, err
					}
				}
				return view(j, false), nil
			}},
		{Name: "mask_files",
			Description: "Like mask_folder for some files of a folder only (paths inside input). Returns a job id.",
			InputSchema: obj(with(map[string]any{"input": str("The folder the files are in (their sub-folders are kept)."), "output": str(""),
				"files": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}), "input", "output", "files"),
			Call: func(a map[string]any) (any, error) {
				var files []string
				if list, ok := a["files"].([]any); ok {
					for _, f := range list {
						if s, ok := f.(string); ok {
							files = append(files, s)
						}
					}
				}
				if len(files) == 0 {
					return nil, fmt.Errorf("files is empty")
				}
				j, err := m.StartFiles(sArg(a, "input"), sArg(a, "output"), files, optionsFrom(a))
				if err != nil {
					return nil, err
				}
				return view(j, false), nil
			}},
		{Name: "job_wait", Description: "Wait until a job ends (or timeoutSec passes) and return its result: counts, report paths, the files that need attention.",
			InputSchema: obj(map[string]any{"id": str(""), "timeoutSec": waitDesc}, "id"),
			Call: func(a map[string]any) (any, error) {
				p, _ := a[mcp.ProgressKey].(mcp.Progress)
				j, err := waitWithProgress(m, sArg(a, "id"), time.Duration(min(3600, nArg(a, "timeoutSec", 600)))*time.Second, p)
				if err != nil {
					return nil, err
				}
				return view(j, false), nil
			}},
		{Name: "job_status", Description: "A job's state now. files=true lists every file.",
			InputSchema: obj(map[string]any{"id": str(""), "files": map[string]any{"type": "boolean"}}, "id"),
			Call: func(a map[string]any) (any, error) {
				j, ok := m.Get(sArg(a, "id"))
				if !ok {
					return nil, fmt.Errorf("no job %s", sArg(a, "id"))
				}
				files, _ := a["files"].(bool)
				return view(j, files), nil
			}},
		{Name: "jobs_list", Description: "The jobs of this session, newest first.", InputSchema: obj(map[string]any{}),
			Call: func(map[string]any) (any, error) {
				var out []map[string]any
				for _, j := range m.Jobs() {
					out = append(out, view(j, false))
				}
				return map[string]any{"jobs": out}, nil
			}},
		{Name: "job_cancel", Description: "Stop a job after the file it is on.", InputSchema: obj(map[string]any{"id": str("")}, "id"),
			Call: func(a map[string]any) (any, error) {
				return map[string]any{"canceling": sArg(a, "id")}, m.Cancel(sArg(a, "id"))
			}},
	}
	return &mcp.Server{Name: "agent-mask", Version: version, Tools: tools}
}
