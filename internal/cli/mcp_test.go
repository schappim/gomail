package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// mcpExchange feeds lines to a server whose runner records the gomail
// command line, and returns the decoded responses.
func mcpExchange(t *testing.T, lines ...string) ([]map[string]any, [][]string) {
	t.Helper()
	var calls [][]string
	s := &mcpServer{version: "test", run: func(ctx context.Context, args []string) (string, bool) {
		calls = append(calls, args)
		return `{"ok":true}`, false
	}}
	var out strings.Builder
	if err := s.serve(context.Background(), strings.NewReader(strings.Join(lines, "\n")), &out); err != nil {
		t.Fatal(err)
	}
	var resps []map[string]any
	sc := bufio.NewScanner(strings.NewReader(out.String()))
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("bad response %q: %v", sc.Text(), err)
		}
		resps = append(resps, m)
	}
	return resps, calls
}

func TestMCPInitializeAndList(t *testing.T) {
	resps, _ := mcpExchange(t,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"nope"}`,
	)
	if len(resps) != 3 {
		t.Fatalf("got %d responses, want 3 (notifications get none)", len(resps))
	}
	init := resps[0]["result"].(map[string]any)
	if init["protocolVersion"] != "2025-03-26" {
		t.Errorf("protocolVersion = %v", init["protocolVersion"])
	}
	tools := resps[1]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != len(mcpTools) {
		t.Errorf("listed %d tools, want %d", len(tools), len(mcpTools))
	}
	for _, x := range tools {
		name := x.(map[string]any)["name"].(string)
		if strings.Contains(name, "send") {
			t.Errorf("tool %q looks like it sends mail", name)
		}
	}
	if resps[2]["error"].(map[string]any)["code"].(float64) != -32601 {
		t.Errorf("unknown method: %v", resps[2])
	}
}

func TestMCPToolArgs(t *testing.T) {
	cases := []struct {
		name string
		args string
		want []string
	}{
		{"profiles", `{}`, []string{"profile", "list"}},
		{"search", `{"profile":"work","query":"from:a@b.com in:sent","limit":5,"snippets":true}`,
			[]string{"--profile", "work", "search", "--limit", "5", "--snippets", "--", "from:a@b.com in:sent"}},
		{"read", `{"id":"18c3f2a1b2c3d4e5"}`, []string{"read", "--", "18c3f2a1b2c3d4e5"}},
		{"reply_draft", `{"id":"18c3f2a1b2c3d4e5","all":true,"cc":["x@y.com"],"markdown":"**hi**"}`,
			[]string{"reply", "--draft", "--all", "--cc", "x@y.com", "--markdown", "**hi**", "--", "18c3f2a1b2c3d4e5"}},
		{"forward_draft", `{"id":"18c3f2a1b2c3d4e5","to":"a@b.com"}`,
			[]string{"forward", "--draft", "--to", "a@b.com", "--", "18c3f2a1b2c3d4e5"}},
	}
	for _, c := range cases {
		req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + c.name + `","arguments":` + c.args + `}}`
		resps, calls := mcpExchange(t, req)
		if len(calls) != 1 || !reflect.DeepEqual(calls[0], c.want) {
			t.Errorf("%s: ran %q, want %q", c.name, calls, c.want)
		}
		if resps[0]["result"].(map[string]any)["isError"] != false {
			t.Errorf("%s: isError set", c.name)
		}
	}
}

func TestMCPMissingRequired(t *testing.T) {
	resps, calls := mcpExchange(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":{}}}`)
	if len(calls) != 0 {
		t.Errorf("ran gomail without a query: %q", calls)
	}
	if resps[0]["result"].(map[string]any)["isError"] != true {
		t.Errorf("want isError for missing query: %v", resps[0])
	}
}
