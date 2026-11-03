// Package mcp serves tools to an agent over stdio (newline-delimited JSON-RPC 2.0, the
// Model Context Protocol). No SDK, no dependencies.
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
)

// Tool is one tool an agent can call.
type Tool struct {
	Name        string                                 `json:"name"`
	Description string                                 `json:"description"`
	InputSchema map[string]any                         `json:"inputSchema"`
	Call        func(args map[string]any) (any, error) `json:"-"`
}

// Server answers MCP requests with the given tools.
type Server struct {
	Name, Version string
	Tools         []Tool
	out           sync.Mutex
	w             io.Writer
}

type request struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

func text(v any, isError bool) map[string]any {
	s, ok := v.(string)
	if !ok {
		b, _ := json.Marshal(v)
		s = string(b)
	}
	return map[string]any{"content": []map[string]any{{"type": "text", "text": s}}, "isError": isError}
}

func (s *Server) call(name string, args map[string]any) map[string]any {
	for _, t := range s.Tools {
		if t.Name != name {
			continue
		}
		r, err := t.Call(args)
		if err != nil {
			return text(err.Error(), true)
		}
		return text(r, false)
	}
	return text("Unknown tool "+name, true)
}

func (s *Server) handle(req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if p.ProtocolVersion == "" {
			p.ProtocolVersion = "2025-06-18"
		}
		return map[string]any{"protocolVersion": p.ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": s.Name, "version": s.Version}}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": s.Tools}, nil
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
			Meta      struct {
				ProgressToken any `json:"progressToken"`
			} `json:"_meta"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{-32602, "invalid params"}
		}
		if p.Arguments == nil {
			p.Arguments = map[string]any{}
		}
		// A long tool reports how far it is when the client asked for progress.
		if tok := p.Meta.ProgressToken; tok != nil {
			p.Arguments[ProgressKey] = Progress(func(done, total float64, msg string) {
				s.Notify("notifications/progress", map[string]any{"progressToken": tok, "progress": done, "total": total, "message": msg})
			})
		}
		return s.call(p.Name, p.Arguments), nil
	}
	return nil, &rpcError{-32601, "Method not found: " + req.Method}
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve reads requests from in until it closes, answering on out. Tool calls run one after
// another in arrival order.
func (s *Server) Serve(in io.Reader, out io.Writer) {
	s.out.Lock()
	s.w = out
	s.out.Unlock()
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var req request
		if json.Unmarshal(sc.Bytes(), &req) != nil || req.Method == "" {
			continue
		}
		res, rerr := s.handle(req)
		if len(req.ID) == 0 {
			continue // a notification: no answer
		}
		msg := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if rerr != nil {
			msg["error"] = rerr
		} else {
			msg["result"] = res
		}
		b, _ := json.Marshal(msg)
		s.out.Lock()
		fmt.Fprintf(out, "%s\n", b)
		s.out.Unlock()
	}
}

// ProgressKey: the argument under which a tool finds its Progress reporter, when the client
// sent a progress token.
const ProgressKey = "__progress"

// Progress reports a long tool's progress to the client.
type Progress func(done, total float64, msg string)

// Notify sends a notification to the client (between or during answers).
func (s *Server) Notify(method string, params any) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	s.out.Lock()
	defer s.out.Unlock()
	if s.w != nil {
		fmt.Fprintf(s.w, "%s\n", b)
	}
}

// ServeStdio serves on the process's stdin and stdout.
func (s *Server) ServeStdio() { s.Serve(os.Stdin, os.Stdout) }
