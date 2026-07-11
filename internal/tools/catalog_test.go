package tools

import (
	"testing"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/provider"
)

func TestBuiltinCatalogContainsValidTargetTools(t *testing.T) {
	catalog := BuiltinCatalog()
	want := []string{"Read", "Write", "Edit", "Delete", "List", "Glob", "Grep", "Shell", "CallMcpTool"}
	if len(catalog) != len(want) {
		t.Fatalf("catalog length = %d, want %d", len(catalog), len(want))
	}
	request := provider.Request{Model: "model", Messages: []provider.Message{{Role: provider.RoleUser, Content: "hello"}}, Tools: catalog}
	if err := request.Validate(); err != nil {
		t.Fatalf("catalog validation error = %v", err)
	}
	for index, name := range want {
		if catalog[index].Name != name {
			t.Fatalf("tool %d = %q, want %q", index, catalog[index].Name, name)
		}
	}
	if got := catalog[3].Description; got != "Delete a file" {
		t.Fatalf("Delete description = %q, want verified file-only capability", got)
	}
}

func TestSelectReturnsIndependentToolsAndRejectsUnknownNames(t *testing.T) {
	selected, err := Select("Read", "Shell")
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if len(selected) != 2 || selected[0].Name != "Read" || selected[1].Name != "Shell" {
		t.Fatalf("selected = %#v", selected)
	}
	selected[0].Parameters[0] = 'x'
	again, err := Select("Read")
	if err != nil || len(again) != 1 || again[0].Parameters[0] == 'x' {
		t.Fatalf("Select() did not return an independent copy")
	}
	if _, err := Select("Unknown"); err == nil {
		t.Fatal("Select(Unknown) accepted unsupported tool")
	}
}
