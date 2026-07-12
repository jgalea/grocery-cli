package main

import (
	"context"
	"encoding/json"
	"sort"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jgalea/grocery-cli/internal/registry"
)

func TestMCPToolNames(t *testing.T) {
	ctx := context.Background()
	srv := newMCPServer()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)

	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, t1, nil); err != nil {
		t.Fatal(err)
	}
	session, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	want := []string{
		"stores", "search", "batch", "compare", "product", "categories",
		"cart_get", "cart_add", "cart_set", "cart_clear",
	}
	res, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tool := range res.Tools {
		got = append(got, tool.Name)
	}
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("tool count: got %d %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tools: got %v, want %v", got, want)
		}
	}
}

func TestMCPStoresTool(t *testing.T) {
	ctx := context.Background()
	srv := newMCPServer()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)

	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, t1, nil); err != nil {
		t.Fatal(err)
	}
	session, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "stores"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("stores tool error: %v", res.GetError())
	}
	if len(res.Content) != 1 {
		t.Fatalf("expected one content block, got %d", len(res.Content))
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected text content, got %T", res.Content[0])
	}

	var got []registry.Meta
	if err := json.Unmarshal([]byte(tc.Text), &got); err != nil {
		t.Fatalf("unmarshal: %v\nbody: %s", err, tc.Text)
	}
	want := registry.List()
	if len(got) != len(want) {
		t.Fatalf("store count: got %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Key != want[i].Key {
			t.Fatalf("store[%d] key: got %q, want %q", i, got[i].Key, want[i].Key)
		}
		if got[i].Label != want[i].Label {
			t.Fatalf("store[%d] label: got %q, want %q", i, got[i].Label, want[i].Label)
		}
		if got[i].Country != want[i].Country {
			t.Fatalf("store[%d] country: got %q, want %q", i, got[i].Country, want[i].Country)
		}
	}
}
