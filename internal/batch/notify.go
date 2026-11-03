package batch

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Notify tells whoever asked that a job ended: its callback URL (POST, signed with secret
// when there is one: X-Agent-Mask-Signature: sha256=<hex HMAC of the body>), its command
// (AGENT_MASK_JOB, AGENT_MASK_REPORT, AGENT_MASK_STATUS in its environment), a desktop notice.
// It returns what failed, in words; nothing fails the job.
func Notify(j *Job, secret string) []string {
	var problems []string
	if u := j.Options.Callback; u != "" {
		summary := *j
		summary.Files = nil
		body, _ := json.Marshal(map[string]any{"event": "job.ended", "job": summary, "summary": Summary(j)})
		req, err := http.NewRequest("POST", u, bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("User-Agent", "agent-mask")
			if secret != "" {
				mac := hmac.New(sha256.New, []byte(secret))
				mac.Write(body)
				req.Header.Set("X-Agent-Mask-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
			}
			c := &http.Client{Timeout: 15 * time.Second}
			var resp *http.Response
			for try := 0; try < 3; try++ {
				if resp, err = c.Do(req); err == nil && resp.StatusCode < 500 {
					break
				}
				if resp != nil {
					resp.Body.Close()
				}
				time.Sleep(time.Duration(1<<try) * time.Second)
				req.Body, _ = req.GetBody()
			}
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode >= 300 {
					err = fmt.Errorf("HTTP %d", resp.StatusCode)
				}
			}
		}
		if err != nil {
			problems = append(problems, "callback: "+err.Error())
		}
	}
	if c := j.Options.OnDone; c != "" {
		cmd := shell(c)
		cmd.Env = append(os.Environ(), "AGENT_MASK_JOB="+j.ID, "AGENT_MASK_REPORT="+j.Report, "AGENT_MASK_STATUS="+j.Status, "AGENT_MASK_OUTPUT="+j.Output)
		if out, err := cmd.CombinedOutput(); err != nil {
			problems = append(problems, fmt.Sprintf("onDone: %v %s", err, strings.TrimSpace(string(out))))
		}
	}
	if j.Options.Notify {
		Desktop("Agent Mask", Summary(j))
	}
	return problems
}

func shell(c string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("cmd", "/C", c)
	}
	return exec.Command("sh", "-c", c)
}

// Desktop shows a notice the OS's own way; best effort.
func Desktop(title, text string) {
	switch runtime.GOOS {
	case "darwin":
		_ = exec.Command("osascript", "-e", fmt.Sprintf("display notification %q with title %q", text, title)).Run()
	case "windows":
		ps := `Add-Type -AssemblyName System.Windows.Forms; $n = New-Object System.Windows.Forms.NotifyIcon; $n.Icon = [System.Drawing.SystemIcons]::Information; $n.Visible = $true; $n.ShowBalloonTip(8000, $env:AM_TITLE, $env:AM_TEXT, 'Info'); Start-Sleep -Seconds 9; $n.Dispose()`
		cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command", ps)
		cmd.Env = append(os.Environ(), "AM_TITLE="+title, "AM_TEXT="+text)
		_ = cmd.Start()
	default:
		_ = exec.Command("notify-send", title, text).Run()
	}
}
