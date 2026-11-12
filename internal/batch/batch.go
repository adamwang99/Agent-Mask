// Package batch is Agent Mask: every video and picture of a folder scanned, its sensitive text
// hidden and checked again, the results written to another folder with a report. Jobs run one
// after another in the background; callers follow them by id, wait for them, or are told when
// one ends (a callback, a command, a desktop notice).
package batch

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/adamwang99/agent-mask/internal/redact"
	"github.com/adamwang99/agent-mask/internal/scan"
)

// Options of a job.
type Options struct {
	Kinds     []string `json:"kinds,omitempty"` // nil: every kind
	Lang      string   `json:"lang,omitempty"`  // tesseract languages, default eng
	Style     string   `json:"style,omitempty"` // auto, soft, mosaic, blur, solid...
	Overwrite bool     `json:"overwrite,omitempty"`
	// Callback and OnDone are never written to the report (a webhook URL can carry a secret,
	// and the report sits in a folder meant to be shared).
	Callback string `json:"callbackUrl,omitempty"` // POSTed the job when it ends
	OnDone   string `json:"onDone,omitempty"`      // a command run when it ends (command line only)
	Notify   bool   `json:"notify,omitempty"`      // a desktop notice when it ends
}

// Video and picture extensions read.
var (
	VideoExts = map[string]bool{".mp4": true, ".mov": true, ".mkv": true, ".webm": true, ".avi": true, ".m4v": true, ".wmv": true}
	ImageExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".bmp": true, ".tif": true, ".tiff": true}
)

// ReviewDir: where outputs that still show something go, inside the output folder.
const ReviewDir = "_review"

// FileReport is one file's result. It never holds the hidden text, only its kinds.
type FileReport struct {
	Input    string          `json:"input"`
	Output   string          `json:"output,omitempty"`
	Type     string          `json:"type"`   // video, image
	Status   string          `json:"status"` // pending, running, verified, review, failed, skipped
	Found    int             `json:"found"`
	Hidden   int             `json:"hidden"`
	Kinds    map[string]int  `json:"kinds,omitempty"`
	Regions  []redact.Region `json:"regions,omitempty"` // when each hidden thing was on screen (videos)
	Verified bool            `json:"verified"`
	Reasons  []string        `json:"reasons,omitempty"`
	Error    string          `json:"error,omitempty"`
	Seconds  float64         `json:"seconds"`
	rel      string
	outRel   string
}

// Job is one folder.
type Job struct {
	ID       string        `json:"id"`
	Input    string        `json:"input"`
	Output   string        `json:"output"`
	Options  Options       `json:"options"`
	Status   string        `json:"status"` // queued, running, done, canceled, failed
	Error    string        `json:"error,omitempty"`
	Created  time.Time     `json:"created"`
	Started  *time.Time    `json:"started,omitempty"`
	Finished *time.Time    `json:"finished,omitempty"`
	Counts   Counts        `json:"counts"`
	Report   string        `json:"report,omitempty"` // report.json
	Files    []*FileReport `json:"files"`
	canceled bool
}

// Counts of a job's files.
type Counts struct {
	Total    int `json:"total"`
	Done     int `json:"done"`
	Verified int `json:"verified"`
	Review   int `json:"review"`
	Failed   int `json:"failed"`
	Skipped  int `json:"skipped"`
}

// Event is a step of a job, for whoever follows it.
type Event struct {
	Job    string      `json:"job"`
	Type   string      `json:"type"` // started, file, done
	File   *FileReport `json:"file,omitempty"`
	Counts Counts      `json:"counts"`
	Status string      `json:"status"`
}

// Manager runs jobs one after another.
type Manager struct {
	mu     sync.Mutex
	jobs   map[string]*Job
	order  []string
	queue  chan *Job
	subs   map[string][]chan Event
	once   sync.Once
	OnEnd  func(*Job) // called when a job ends (after its report is written)
	Stderr func(string)
}

// NewManager makes an idle manager; its worker starts with the first job.
func NewManager() *Manager {
	return &Manager{jobs: map[string]*Job{}, queue: make(chan *Job, 256), subs: map[string][]chan Event{}}
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "job_" + hex.EncodeToString(b)
}

// Check the two folders: both given, the input exists, neither inside the other (an output
// inside the input would be read again as new input).
func Check(in, out string) (string, string, error) {
	if in == "" || out == "" {
		return "", "", fmt.Errorf("an input folder and an output folder are both needed")
	}
	in, _ = filepath.Abs(in)
	out, _ = filepath.Abs(out)
	if st, err := os.Stat(in); err != nil || !st.IsDir() {
		return "", "", fmt.Errorf("input folder %s does not exist", in)
	}
	inside := func(a, b string) bool {
		r, err := filepath.Rel(b, a)
		return err == nil && (r == "." || !strings.HasPrefix(r, ".."))
	}
	if inside(out, in) || inside(in, out) {
		return "", "", fmt.Errorf("the output folder must not be the input folder or inside it (and the other way round)")
	}
	return in, out, nil
}

// List the videos and pictures under in, sorted.
func List(in string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(in, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped, not fatal
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && p != in {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		if (VideoExts[ext] || ImageExts[ext]) && !strings.HasPrefix(d.Name(), ".") {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// Start queues a job for a folder.
func (m *Manager) Start(in, out string, o Options) (*Job, error) {
	in, out, err := Check(in, out)
	if err != nil {
		return nil, err
	}
	files, err := List(in)
	if err != nil {
		return nil, err
	}
	return m.queueFiles(in, out, files, o)
}

// StartFiles queues a job for some files of a folder (watch mode: the new ones).
func (m *Manager) StartFiles(in, out string, files []string, o Options) (*Job, error) {
	return m.startFiles(in, out, files, o, false)
}

func (m *Manager) startFiles(in, out string, files []string, o Options, skipMissing bool) (*Job, error) {
	in, out, err := Check(in, out)
	if err != nil {
		return nil, err
	}
	var list []string
	for _, f := range files {
		if !filepath.IsAbs(f) {
			f = filepath.Join(in, f)
		}
		f = filepath.Clean(f)
		if r, err := filepath.Rel(in, f); err != nil || strings.HasPrefix(r, "..") {
			return nil, fmt.Errorf("%s is not inside %s", f, in)
		}
		ext := strings.ToLower(filepath.Ext(f))
		if !VideoExts[ext] && !ImageExts[ext] {
			return nil, fmt.Errorf("%s is not a video or picture", f)
		}
		if _, err := os.Stat(f); err != nil {
			if skipMissing {
				continue
			}
			return nil, err
		}
		list = append(list, f)
	}
	return m.queueFiles(in, out, list, o)
}

func (m *Manager) queueFiles(in, out string, files []string, o Options) (*Job, error) {
	if o.Style == "" {
		o.Style = "auto"
	}
	if o.Lang == "" {
		o.Lang = "eng"
	}
	j := &Job{ID: newID(), Input: in, Output: out, Options: o, Status: "queued", Created: time.Now().UTC()}
	taken := map[string]bool{}
	for _, f := range files {
		rel, _ := filepath.Rel(in, f)
		t := "video"
		if ImageExts[strings.ToLower(filepath.Ext(f))] {
			t = "image"
		}
		j.Files = append(j.Files, &FileReport{Input: f, Type: t, Status: "pending", rel: rel, outRel: outName(rel, t, taken)})
	}
	j.Counts.Total = len(j.Files)
	m.mu.Lock()
	m.jobs[j.ID] = j
	m.order = append(m.order, j.ID)
	m.mu.Unlock()
	first := j.copy() // the caller's own copy: the worker writes to j from now on
	m.once.Do(func() { go m.worker() })
	m.queue <- j
	return first, nil
}

// Get a copy of a job (safe to read while it runs).
func (m *Manager) Get(id string) (*Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return nil, false
	}
	return j.copy(), true
}

// List the jobs, newest first, without their files.
func (m *Manager) Jobs() []*Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Job
	for i := len(m.order) - 1; i >= 0; i-- {
		c := m.jobs[m.order[i]].copy()
		c.Files = nil
		out = append(out, c)
	}
	return out
}

func (j *Job) copy() *Job {
	c := *j
	c.Files = make([]*FileReport, len(j.Files))
	for i, f := range j.Files {
		fc := *f
		c.Files[i] = &fc
	}
	return &c
}

// Ended: done, canceled or failed.
func (j *Job) Ended() bool {
	return j.Status == "done" || j.Status == "canceled" || j.Status == "failed"
}

// Cancel stops a job after the file it is on.
func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return fmt.Errorf("no job %s", id)
	}
	if j.Ended() {
		return nil
	}
	j.canceled = true
	// Not started yet (another job is running): it ends now, it will never start.
	if j.Status == "queued" {
		now := time.Now().UTC()
		j.Status, j.Finished = "canceled", &now
		m.emit(j, Event{Type: "done"})
	}
	return nil
}

// Subscribe follows a job's events; the channel closes when the job ends.
func (m *Manager) Subscribe(id string) (<-chan Event, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return nil, nil, fmt.Errorf("no job %s", id)
	}
	ch := make(chan Event, 64)
	if j.Ended() {
		ch <- Event{Job: id, Type: "done", Counts: j.Counts, Status: j.Status}
		close(ch)
		return ch, func() {}, nil
	}
	m.subs[id] = append(m.subs[id], ch)
	stop := func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		list := m.subs[id]
		for i, c := range list {
			if c == ch {
				m.subs[id] = append(list[:i], list[i+1:]...)
				close(ch)
				return
			}
		}
	}
	return ch, stop, nil
}

// Wait until a job ends or the timeout passes; the job as it is then.
func (m *Manager) Wait(id string, timeout time.Duration) (*Job, error) {
	ch, stop, err := m.Subscribe(id)
	if err != nil {
		return nil, err
	}
	defer func() {
		if stop != nil {
			go stop()
		}
	}()
	deadline := time.After(timeout)
	for {
		select {
		case e, ok := <-ch:
			if !ok || e.Type == "done" {
				stop = nil
				j, _ := m.Get(id)
				return j, nil
			}
		case <-deadline:
			j, _ := m.Get(id)
			return j, nil
		}
	}
}

func (m *Manager) emit(j *Job, e Event) {
	// m.mu is held.
	e.Job, e.Counts, e.Status = j.ID, j.Counts, j.Status
	for _, ch := range m.subs[j.ID] {
		select {
		case ch <- e:
		default: // a slow follower misses a step, never blocks the job
		}
	}
	if e.Type == "done" {
		for _, ch := range m.subs[j.ID] {
			close(ch)
		}
		delete(m.subs, j.ID)
	}
}

func (m *Manager) worker() {
	for j := range m.queue {
		m.mu.Lock()
		skip := j.Status == "canceled"
		m.mu.Unlock()
		if skip {
			if m.OnEnd != nil {
				m.OnEnd(j.copy())
			}
			continue
		}
		m.run(j)
	}
}

func (m *Manager) run(j *Job) {
	m.mu.Lock()
	now := time.Now().UTC()
	j.Status, j.Started = "running", &now
	m.emit(j, Event{Type: "started"})
	m.mu.Unlock()
	idx := loadIndex(j.Output)
	for _, f := range j.Files {
		m.mu.Lock()
		if j.canceled {
			m.mu.Unlock()
			break
		}
		f.Status = "running"
		m.mu.Unlock()
		res := m.one(j, f, idx)
		m.mu.Lock()
		*f = *res
		j.Counts.Done++
		switch f.Status {
		case "verified":
			j.Counts.Verified++
		case "review":
			j.Counts.Review++
		case "failed":
			j.Counts.Failed++
		case "skipped":
			j.Counts.Skipped++
		}
		fc := *f
		m.emit(j, Event{Type: "file", File: &fc})
		m.mu.Unlock()
		idx.save(j.Output)
	}
	m.mu.Lock()
	end := time.Now().UTC()
	j.Finished = &end
	j.Status = "done"
	if j.canceled {
		j.Status = "canceled"
	}
	j.Report = filepath.Join(j.Output, "report.json")
	snapshot := j.copy()
	m.mu.Unlock()
	if err := writeReports(snapshot); err != nil {
		m.mu.Lock()
		j.Error = err.Error()
		m.mu.Unlock()
	}
	m.mu.Lock()
	m.emit(j, Event{Type: "done"})
	final := j.copy()
	m.mu.Unlock()
	if m.OnEnd != nil {
		m.OnEnd(final)
	}
}

// one processes one file.
func (m *Manager) one(j *Job, f *FileReport, idx *index) *FileReport {
	r := &FileReport{Input: f.Input, Type: f.Type, rel: f.rel, outRel: f.outRel}
	t0 := time.Now()
	defer func() { r.Seconds = float64(time.Since(t0).Milliseconds()) / 1000 }()
	outRel := f.outRel
	key := idx.key(f.Input, j.Options)
	if prev, ok := idx.Done[f.rel]; ok && prev.Key == key && !j.Options.Overwrite {
		if _, err := os.Stat(filepath.Join(j.Output, prev.Output)); err == nil {
			r.Output, r.Verified = filepath.Join(j.Output, prev.Output), prev.Verified
			r.Status = "skipped"
			// Unchanged, but it was not verified last time either: it still needs a look.
			if !prev.Verified {
				r.Status = "review"
				r.Reasons = []string{"unchanged since the last run, where it was not verified: still in " + ReviewDir + "/"}
			}
			return r
		}
	}
	// Written under a temporary name, then moved into the output folder or _review.
	tmpDir := filepath.Join(j.Output, ".agent-mask", "work")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		r.Status, r.Error = "failed", err.Error()
		return r
	}
	tmp := filepath.Join(tmpDir, fmt.Sprintf("%s-%d%s", j.ID, time.Now().UnixNano(), filepath.Ext(outRel)))
	var res *FileReport
	var err error
	if f.Type == "video" {
		res, err = redactVideo(f.Input, tmp, j.Options)
	} else {
		res, err = redactImage(f.Input, tmp, j.Options)
	}
	if err != nil {
		os.Remove(tmp)
		r.Status, r.Error = "failed", err.Error()
		return r
	}
	r.Found, r.Hidden, r.Kinds, r.Verified, r.Reasons, r.Regions = res.Found, res.Hidden, res.Kinds, res.Verified, res.Reasons, res.Regions
	dest := outRel
	r.Status = "verified"
	if !r.Verified {
		dest, r.Status = filepath.Join(ReviewDir, outRel), "review"
	}
	full := filepath.Join(j.Output, dest)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err == nil {
		_ = os.Remove(filepath.Join(j.Output, outRel))
		_ = os.Remove(filepath.Join(j.Output, ReviewDir, outRel))
		err = os.Rename(tmp, full)
		if err != nil {
			r.Status, r.Error = "failed", err.Error()
			return r
		}
	}
	r.Output = full
	idx.Done[f.rel] = indexEntry{Key: key, Output: dest, Verified: r.Verified}
	return r
}

// outName: the output name of an input, unique in the job: "a.mov" and "a.mp4" both become
// videos named a.mp4, so the second keeps its own extension in the name (a.mov.mp4).
func outName(rel, typ string, taken map[string]bool) string {
	out := ImageOut(rel)
	if typ == "video" {
		out = strings.TrimSuffix(rel, filepath.Ext(rel)) + ".mp4"
	}
	key := strings.ToLower(out)
	if taken[key] {
		out = rel + filepath.Ext(out)
		key = strings.ToLower(out)
	}
	taken[key] = true
	return out
}

func redactVideo(in, out string, o Options) (*FileReport, error) {
	res, err := redact.Video(in, out, redact.Options{Kinds: o.Kinds, Lang: o.Lang, Style: o.Style})
	if err != nil {
		return nil, err
	}
	return &FileReport{Found: res.Found, Hidden: res.Hidden, Verified: res.Verified, Reasons: res.Reasons, Kinds: res.Kinds, Regions: res.Regions}, nil
}

func kindsOf(r *scan.Result) map[string]int {
	if len(r.Regions) == 0 {
		return nil
	}
	k := map[string]int{}
	for _, t := range r.Regions {
		k[t.Kind]++
	}
	return k
}

// index remembers what was done, so a second run skips unchanged files.
type index struct {
	Done map[string]indexEntry `json:"done"`
}

type indexEntry struct {
	Key      string `json:"key"`
	Output   string `json:"output"`
	Verified bool   `json:"verified"`
}

func indexFile(out string) string { return filepath.Join(out, ".agent-mask", "index.json") }

func loadIndex(out string) *index {
	idx := &index{Done: map[string]indexEntry{}}
	if b, err := os.ReadFile(indexFile(out)); err == nil {
		_ = json.Unmarshal(b, idx)
		if idx.Done == nil {
			idx.Done = map[string]indexEntry{}
		}
	}
	return idx
}

func (idx *index) save(out string) {
	_ = os.MkdirAll(filepath.Dir(indexFile(out)), 0o755)
	b, _ := json.MarshalIndent(idx, "", "  ")
	_ = os.WriteFile(indexFile(out), b, 0o644)
}

// key: the file's size and time and the options that change the result.
func (idx *index) key(file string, o Options) string {
	st, err := os.Stat(file)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d|%d|%s|%s|%s", st.Size(), st.ModTime().UnixNano(), strings.Join(o.Kinds, ","), o.Lang, o.Style)
}
