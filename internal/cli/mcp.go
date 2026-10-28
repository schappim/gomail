package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/spf13/cobra"
)

func (a *app) mcpCmd(version string) *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run an MCP server on stdin/stdout (search, read and draft; never sends)",
		Long: `Serve gomail as a Model Context Protocol server over stdio, so AI apps such
as Claude Desktop can search, read and draft mail through it. The server runs
on the host, so it uses this machine's config and network.

Tools: profiles, labels, list, search, read, thread, attachments, draft_list,
draft_create, reply_draft, forward_draft. Nothing it exposes sends mail:
replies and forwards are saved as drafts for you to review and send.

Each tool call runs this gomail binary with --json, so --config (if given)
applies to every call. Example Claude Desktop entry (claude_desktop_config.json):

  "mcpServers": {
    "gomail": { "command": "/opt/homebrew/bin/gomail", "args": ["mcp"] }
  }`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			s := &mcpServer{version: version, run: execRunner(exe, a.cfgPath)}
			return s.serve(cmd.Context(), a.in, a.out)
		},
	}
}

// mcpRunner runs gomail with args and returns its combined output and
// whether it failed.
type mcpRunner func(ctx context.Context, args []string) (string, bool)

func execRunner(exe, cfgPath string) mcpRunner {
	return func(ctx context.Context, args []string) (string, bool) {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		full := []string{"--json"}
		if cfgPath != "" {
			full = append(full, "--config", cfgPath)
		}
		full = append(full, args...)
		var stdout, stderr bytes.Buffer
		c := exec.CommandContext(ctx, exe, full...)
		c.Stdout, c.Stderr = &stdout, &stderr
		err := c.Run()
		out := bytes.TrimSpace(append(stdout.Bytes(), stderr.Bytes()...))
		if len(out) == 0 && err != nil {
			return err.Error(), true
		}
		return string(out), err != nil
	}
}

type mcpServer struct {
	version string
	run     mcpRunner
}

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpError       `json:"error,omitempty"`
}

// serve reads newline-delimited JSON-RPC requests from r and answers on w
// until r is closed.
func (s *mcpServer) serve(ctx context.Context, r io.Reader, w io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var req mcpRequest
		if err := json.Unmarshal(line, &req); err != nil {
			if err := enc.Encode(mcpResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &mcpError{-32700, "parse error"}}); err != nil {
				return err
			}
			continue
		}
		if len(req.ID) == 0 {
			continue // notification
		}
		resp := mcpResponse{JSONRPC: "2.0", ID: req.ID}
		resp.Result, resp.Error = s.handle(ctx, req)
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return sc.Err()
}

func (s *mcpServer) handle(ctx context.Context, req mcpRequest) (any, *mcpError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if p.ProtocolVersion == "" {
			p.ProtocolVersion = "2025-06-18"
		}
		return map[string]any{
			"protocolVersion": p.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "gomail", "version": s.version},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		list := make([]map[string]any, 0, len(mcpTools))
		for _, t := range mcpTools {
			list = append(list, t.schema())
		}
		return map[string]any{"tools": list}, nil
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &mcpError{-32602, "invalid params: " + err.Error()}
		}
		t := findMCPTool(p.Name)
		if t == nil {
			return nil, &mcpError{-32602, "unknown tool: " + p.Name}
		}
		args, err := t.cmdArgs(mcpArgs(p.Arguments))
		if err != nil {
			return toolResult(err.Error(), true), nil
		}
		out, failed := s.run(ctx, args)
		if out == "" {
			out = "(no output)"
		}
		return toolResult(out, failed), nil
	}
	return nil, &mcpError{-32601, "method not found: " + req.Method}
}

func toolResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
}

// mcpArgs wraps a tool call's arguments with typed accessors that turn them
// into gomail flags.
type mcpArgs map[string]any

func (m mcpArgs) str(k string) string {
	switch v := m[k].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

func (m mcpArgs) on(k string) bool {
	v, _ := m[k].(bool)
	return v
}

func (m mcpArgs) list(k string) []string {
	switch v := m[k].(type) {
	case string:
		if v != "" {
			return []string{v}
		}
	case []any:
		var out []string
		for _, x := range v {
			if s, ok := x.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// flag appends --name value when the argument is set.
func (m mcpArgs) flag(dst []string, k, name string) []string {
	if v := m.str(k); v != "" {
		return append(dst, name, v)
	}
	return dst
}

func (m mcpArgs) boolFlag(dst []string, k, name string) []string {
	if m.on(k) {
		return append(dst, name)
	}
	return dst
}

func (m mcpArgs) repeat(dst []string, k, name string) []string {
	for _, v := range m.list(k) {
		dst = append(dst, name, v)
	}
	return dst
}

func (m mcpArgs) paging(dst []string) []string {
	dst = m.flag(dst, "limit", "--limit")
	dst = m.flag(dst, "page", "--page")
	dst = m.boolFlag(dst, "snippets", "--snippets")
	return m.boolFlag(dst, "unread", "--unread")
}

func (m mcpArgs) compose(dst []string) []string {
	dst = m.repeat(dst, "to", "--to")
	dst = m.repeat(dst, "cc", "--cc")
	dst = m.repeat(dst, "bcc", "--bcc")
	dst = m.flag(dst, "subject", "--subject")
	dst = m.flag(dst, "body", "--body")
	dst = m.flag(dst, "markdown", "--markdown")
	dst = m.flag(dst, "from", "--from")
	dst = m.flag(dst, "signature", "--signature")
	dst = m.boolFlag(dst, "no_signature", "--no-signature")
	return m.repeat(dst, "attach", "--attach")
}

type mcpTool struct {
	name, desc string
	props      map[string]any
	required   []string
	noProfile  bool
	args       func(m mcpArgs) []string
}

func (t *mcpTool) schema() map[string]any {
	props := map[string]any{}
	if !t.noProfile {
		props["profile"] = mcpProfileProp
	}
	for k, v := range t.props {
		props[k] = v
	}
	required := t.required
	if required == nil {
		required = []string{}
	}
	return map[string]any{
		"name":        t.name,
		"description": t.desc,
		"inputSchema": map[string]any{"type": "object", "properties": props, "required": required},
	}
}

// cmdArgs builds the gomail command line for a call, after checking
// required arguments.
func (t *mcpTool) cmdArgs(m mcpArgs) ([]string, error) {
	for _, k := range t.required {
		if m.str(k) == "" && len(m.list(k)) == 0 {
			return nil, fmt.Errorf("missing required argument %q", k)
		}
	}
	var out []string
	if !t.noProfile {
		out = m.flag(out, "profile", "--profile")
	}
	return append(out, t.args(m)...), nil
}

func findMCPTool(name string) *mcpTool {
	for i := range mcpTools {
		if mcpTools[i].name == name {
			return &mcpTools[i]
		}
	}
	return nil
}

func prop(typ, desc string) map[string]any {
	p := map[string]any{"type": typ}
	if desc != "" {
		p["description"] = desc
	}
	return p
}

func listProp(desc string) map[string]any {
	p := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	if desc != "" {
		p["description"] = desc
	}
	return p
}

func withProps(sets ...map[string]any) map[string]any {
	out := map[string]any{}
	for _, s := range sets {
		for k, v := range s {
			out[k] = v
		}
	}
	return out
}

var (
	mcpProfileProp = prop("string", "Account profile (name or email); see the profiles tool. Defaults to the config's default_profile.")
	mcpIDProp      = prop("string", "Gmail message id (hex, as shown in list/search results)")
	mcpPagingProps = map[string]any{
		"limit":    prop("integer", "Maximum number of messages (default 25)"),
		"page":     prop("integer", "Page number, 1-based"),
		"snippets": prop("boolean", "Include a short body preview for each message"),
		"unread":   prop("boolean", "Only unread messages"),
	}
	mcpComposeProps = map[string]any{
		"to":           listProp(`Recipients ("Name <a@b.com>")`),
		"cc":           listProp(""),
		"bcc":          listProp(""),
		"subject":      prop("string", ""),
		"body":         prop("string", "Plain-text body"),
		"markdown":     prop("string", "Markdown body, sent as HTML plus plain text; use instead of body"),
		"from":         prop("string", "From address (must be a Gmail send-as alias)"),
		"signature":    prop("string", "Signature name (default: the profile's default signature)"),
		"no_signature": prop("boolean", "Don't add a signature"),
		"attach":       listProp("Paths of files on this machine to attach"),
	}
)

var mcpTools = []mcpTool{
	{
		name: "profiles", desc: "List the configured account profiles.", noProfile: true,
		props: map[string]any{},
		args:  func(m mcpArgs) []string { return []string{"profile", "list"} },
	},
	{
		name: "labels", desc: "List labels/folders (Inbox, Sent, Drafts, user labels...).",
		props: map[string]any{"counts": prop("boolean", "Include total and unread counts")},
		args:  func(m mcpArgs) []string { return m.boolFlag([]string{"labels"}, "counts", "--counts") },
	},
	{
		name: "list", desc: "List the newest messages in a label/folder (default inbox).",
		props: withProps(mcpPagingProps, map[string]any{
			"label": prop("string", "inbox, sent, drafts, all, trash, spam, starred, important, or a label name"),
		}),
		args: func(m mcpArgs) []string { return m.paging(m.flag([]string{"list"}, "label", "--label")) },
	},
	{
		name: "search", desc: "Search with Gmail search syntax (from:, to:, subject:, has:attachment, in:sent, after:2026/01/01, is:unread, ...). Searches All Mail unless a label is given.",
		props: withProps(mcpPagingProps, map[string]any{
			"query": prop("string", "Gmail search query"),
			"label": prop("string", "Restrict to this label/folder (default all)"),
		}),
		required: []string{"query"},
		args: func(m mcpArgs) []string {
			return append(m.paging(m.flag([]string{"search"}, "label", "--label")), "--", m.str("query"))
		},
	},
	{
		name: "read", desc: "Show one message.",
		props:    map[string]any{"id": mcpIDProp, "headers": prop("boolean", "Show all headers")},
		required: []string{"id"},
		args: func(m mcpArgs) []string {
			return append(m.boolFlag([]string{"read"}, "headers", "--headers"), "--", m.str("id"))
		},
	},
	{
		name: "thread", desc: "Show a whole conversation (thread id or any message id in it).",
		props:    map[string]any{"id": mcpIDProp, "full": prop("boolean", "Keep quoted reply text")},
		required: []string{"id"},
		args: func(m mcpArgs) []string {
			return append(m.boolFlag([]string{"thread"}, "full", "--full"), "--", m.str("id"))
		},
	},
	{
		name: "attachments", desc: "List a message's attachments, or save them into a directory on this machine.",
		props: map[string]any{
			"id":     mcpIDProp,
			"output": prop("string", "Directory to save into (omit to only list)"),
			"name":   prop("string", "Only attachments whose filename matches this glob, e.g. *.pdf"),
		},
		required: []string{"id"},
		args: func(m mcpArgs) []string {
			out := m.flag([]string{"attachments"}, "output", "--output")
			return append(m.flag(out, "name", "--name"), "--", m.str("id"))
		},
	},
	{
		name: "draft_list", desc: "List drafts.",
		props: mcpPagingProps,
		args:  func(m mcpArgs) []string { return m.paging([]string{"draft", "list"}) },
	},
	{
		name: "draft_create", desc: "Save a new message to Drafts. Does not send.",
		props: mcpComposeProps,
		args:  func(m mcpArgs) []string { return m.compose([]string{"draft", "create"}) },
	},
	{
		name: "reply_draft", desc: "Save a reply to a message as a draft in its thread. Does not send.",
		props:    withProps(mcpComposeProps, map[string]any{"id": mcpIDProp, "all": prop("boolean", "Reply to all recipients")}),
		required: []string{"id"},
		args: func(m mcpArgs) []string {
			out := m.boolFlag([]string{"reply", "--draft"}, "all", "--all")
			return append(m.compose(out), "--", m.str("id"))
		},
	},
	{
		name: "forward_draft", desc: "Save a forward of a message, with its attachments, as a draft. Does not send.",
		props:    withProps(mcpComposeProps, map[string]any{"id": mcpIDProp}),
		required: []string{"id", "to"},
		args: func(m mcpArgs) []string {
			return append(m.compose([]string{"forward", "--draft"}), "--", m.str("id"))
		},
	},
}
