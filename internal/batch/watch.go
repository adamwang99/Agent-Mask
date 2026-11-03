package batch

import (
	"context"
	"os"
	"time"
)

// Watch queues every video or picture that appears (or changes) in the input folder, once it
// has stopped growing, until ctx ends. Files already done with the same options are skipped
// by the job itself, so a restart does not redo the folder.
func (m *Manager) Watch(ctx context.Context, in, out string, o Options, every time.Duration, started func(*Job)) error {
	in, out, err := Check(in, out)
	if err != nil {
		return err
	}
	type seen struct {
		size int64
		mod  time.Time
	}
	last := map[string]seen{}
	queued := map[string]seen{}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		files, _ := List(in)
		var ready []string
		for _, f := range files {
			st, err := os.Stat(f)
			if err != nil {
				continue
			}
			now := seen{st.Size(), st.ModTime()}
			prev, had := last[f]
			last[f] = now
			// Stable for one whole interval and not just written: a copy in progress waits.
			if !had || prev != now || time.Since(now.mod) < every || queued[f] == now {
				continue
			}
			queued[f] = now
			ready = append(ready, f)
		}
		if len(ready) > 0 {
			if j, err := m.StartFiles(in, out, ready, o); err == nil && started != nil {
				started(j)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}
