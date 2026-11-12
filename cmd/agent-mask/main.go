// Agent Mask: hide the sensitive text in every video and picture of a folder, check the
// result, write it to another folder with a report. Free. Command line, MCP server for agents,
// and a local HTTP API with callbacks.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/adamwang99/agent-mask/internal/batch"
	"github.com/adamwang99/agent-mask/internal/config"
	"github.com/adamwang99/agent-mask/internal/ocr"
)

var version = "0.1.3" // set at build time: -ldflags "-X main.version=..."

const help = `Agent Mask - hide sensitive text in videos and pictures, a whole folder at a time. Free.

  agent-mask batch <input folder> <output folder> [options]
      every video (mp4 mov mkv webm avi m4v wmv) and picture (png jpg webp bmp tif) under the
      input folder, sub-folders kept. Each result is checked again: one that still shows
      something goes to <output>/_review/. report.json and report.md list every file.
      Files already done (same size, time and options) are skipped on the next run.
      Exit code: 0 all verified, 1 some file to review or failed, 2 the job could not run.
  agent-mask watch <input folder> <output folder> [options]
      the same for each file that appears in the input folder, until Ctrl+C
  agent-mask serve [--port 7788] [--allow-commands]
                                      local HTTP API for agents and tools (see API below);
                                      "onDone" in a request is refused without --allow-commands
  agent-mask mcp                      MCP server (stdio) for Claude Code, Codex, Cursor...
  agent-mask doctor                   check ffmpeg and tesseract
  agent-mask version

Options:
  --kinds email,phone,api-key,...     only these kinds (default: all): email, phone, api-key, jwt,
                                      private-key, credit-card, secret-assignment, url-credentials, high-entropy
  --lang eng | vie+eng                OCR languages (default eng)
  --style auto|soft|mosaic|blur|solid how hidden text looks (default auto)
  --overwrite                         redo files already done
  --on-done "command"                 run when finished (AGENT_MASK_REPORT, AGENT_MASK_STATUS)
  --callback https://...              POST the result there when finished
  --notify                            a desktop notice when finished

API (agent-mask serve): every request needs "Authorization: Bearer <token>" (the token is
printed by serve and kept in its config folder). Listens on 127.0.0.1 only.
  POST /jobs            {"input":"...","output":"...","style":"auto","callbackUrl":"..."}
  GET  /jobs            GET /jobs/{id}      GET /jobs/{id}/events (server-sent events)
  POST /jobs/{id}/wait?timeout=600          POST /jobs/{id}/cancel      GET /health
  Callbacks carry X-Agent-Mask-Signature: sha256=<HMAC-SHA256 of the body with the token>.
`

func main() {
	config.App = "agent-mask"
	config.ExtendPath()
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
}

func jobFlags(name string, args []string) ([]string, *batch.Options, *int, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	o := &batch.Options{}
	kinds := fs.String("kinds", "", "")
	fs.StringVar(&o.Lang, "lang", "eng", "")
	fs.StringVar(&o.Style, "style", "auto", "")
	fs.BoolVar(&o.Overwrite, "overwrite", false, "")
	fs.StringVar(&o.OnDone, "on-done", "", "")
	fs.StringVar(&o.Callback, "callback", "", "")
	fs.BoolVar(&o.Notify, "notify", false, "")
	every := fs.Int("every", 5, "seconds between looks (watch)")
	// Flags may come after the folders.
	var pos, rest []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			rest = append(rest, args[i:]...)
			break
		}
		pos = append(pos, args[i])
	}
	if err := fs.Parse(rest); err != nil {
		return nil, nil, nil, err
	}
	pos = append(pos, fs.Args()...)
	if len(pos) != 2 {
		return nil, nil, nil, fmt.Errorf("usage: agent-mask %s <input folder> <output folder> [options]", name)
	}
	if *kinds != "" {
		o.Kinds = strings.Split(*kinds, ",")
	}
	return pos, o, every, nil
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Print(help)
		return nil
	}
	switch args[0] {
	case "version", "--version":
		fmt.Println(version)
	case "doctor":
		return doctor()
	case "batch":
		dirs, o, _, err := jobFlags("batch", args[1:])
		if err != nil {
			return err
		}
		return batchCmd(dirs[0], dirs[1], *o)
	case "watch":
		dirs, o, every, err := jobFlags("watch", args[1:])
		if err != nil {
			return err
		}
		return watchCmd(dirs[0], dirs[1], *o, time.Duration(*every)*time.Second)
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ContinueOnError)
		port := fs.Int("port", 7788, "")
		allow := fs.Bool("allow-commands", false, "")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return serve(*port, *allow)
	case "mcp":
		mcpServer().ServeStdio()
	default:
		fmt.Print(help)
	}
	return nil
}

func progress(m *batch.Manager, j *batch.Job) {
	ch, _, err := m.Subscribe(j.ID)
	if err != nil {
		return
	}
	for e := range ch {
		if e.Type == "file" && e.File != nil {
			rel, _ := filepath.Rel(j.Input, e.File.Input)
			note := ""
			if e.File.Error != "" {
				note = ": " + e.File.Error
			} else if e.File.Status == "review" {
				note = " -> " + batch.ReviewDir + "/ (" + strings.Join(e.File.Reasons, "; ") + ")"
			}
			fmt.Fprintf(os.Stderr, "[%d/%d] %-8s %s (%d hidden, %.1fs)%s\n", e.Counts.Done, e.Counts.Total, e.File.Status, rel, e.File.Hidden, e.File.Seconds, note)
		}
	}
}

func batchCmd(in, out string, o batch.Options) error {
	m := batch.NewManager()
	done := make(chan *batch.Job, 1)
	m.OnEnd = func(j *batch.Job) { done <- j }
	j, err := m.Start(in, out, o)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Agent Mask: %d file(s) from %s to %s\n", j.Counts.Total, j.Input, j.Output)
	go progress(m, j)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	go func() { <-sig; fmt.Fprintln(os.Stderr, "Stopping after the current file..."); _ = m.Cancel(j.ID) }()
	final := <-done
	time.Sleep(50 * time.Millisecond) // let the last progress line print
	for _, p := range batch.Notify(final, apiToken()) {
		fmt.Fprintln(os.Stderr, "warning:", p)
	}
	fmt.Fprintf(os.Stderr, "%s\nReport: %s\n", batch.Summary(final), final.Report)
	if final.Counts.Review > 0 || final.Counts.Failed > 0 {
		os.Exit(1)
	}
	return nil
}

func watchCmd(in, out string, o batch.Options, every time.Duration) error {
	m := batch.NewManager()
	m.OnEnd = func(j *batch.Job) {
		for _, p := range batch.Notify(j, apiToken()) {
			fmt.Fprintln(os.Stderr, "warning:", p)
		}
		fmt.Fprintf(os.Stderr, "%s - %s\n", time.Now().Format("15:04:05"), batch.Summary(j))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	fmt.Fprintf(os.Stderr, "Watching %s -> %s (every %s). Ctrl+C to stop.\n", in, out, every)
	return m.Watch(ctx, in, out, o, every, func(j *batch.Job) { go progress(m, j) })
}

// apiToken: the local API's token, made once and kept in the config folder (owner-only).
func apiToken() string {
	if t := os.Getenv("AGENT_MASK_TOKEN"); t != "" {
		return t
	}
	f := filepath.Join(config.Dir(), "api-token")
	if b, err := os.ReadFile(f); err == nil && len(strings.TrimSpace(string(b))) >= 32 {
		return strings.TrimSpace(string(b))
	}
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	t := hex.EncodeToString(b)
	_ = os.MkdirAll(config.Dir(), 0o700)
	_ = os.WriteFile(f, []byte(t+"\n"), 0o600)
	return t
}

func doctor() error {
	ok := true
	for _, c := range [][2]string{{"ffmpeg", "-version"}, {"ffprobe", "-version"}, {"tesseract", "--version"}} {
		out, err := exec.Command(c[0], c[1]).CombinedOutput()
		if err != nil && len(out) == 0 {
			fmt.Printf("MISSING %s\n", c[0])
			ok = false
			continue
		}
		fmt.Printf("OK      %s  %s\n", c[0], strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0])
	}
	if err := ocr.AssertWorks("eng"); err != nil {
		fmt.Printf("BROKEN  OCR self-test: %v\n", err)
		ok = false
	} else {
		fmt.Println("OK      OCR self-test")
	}
	fmt.Println("config folder:", config.Dir())
	if !ok {
		os.Exit(1)
	}
	return nil
}

func printJSON(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}

func fmtConfig() string { return config.Dir() }
