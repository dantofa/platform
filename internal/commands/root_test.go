package commands

import "testing"

func TestRootWiresDOGroupAndSubcommands(t *testing.T) {
	root := NewRootCmd()

	for _, path := range [][]string{
		{"do"},
		{"do", "cluster", "list"},
		{"do", "cluster", "create"},
		{"do", "cluster", "update"},
		{"do", "cluster", "connect"},
		{"do", "cluster", "delete"},
		{"do", "space", "list"},
		{"do", "space", "create"},
		{"do", "space", "delete"},
		{"do", "space", "link"},
		{"do", "space", "unlink"},
		{"flux", "source", "create"},
		{"flux", "source", "delete"},
		{"flux", "kustomization", "create"},
		{"flux", "kustomization", "delete"},
		{"local", "cluster", "list"},
		{"local", "cluster", "create"},
		{"local", "cluster", "bootstrap"},
		{"local", "cluster", "push"},
		{"local", "cluster", "delete"},
		{"local", "cluster", "connect"},
	} {
		if _, _, err := root.Find(path); err != nil {
			t.Errorf("command %v not wired: %v", path, err)
		}
	}
}

func TestDigitaloceanAliasResolvesToDo(t *testing.T) {
	root := NewRootCmd()
	cmd, _, err := root.Find([]string{"digitalocean", "space", "list"})
	if err != nil {
		t.Fatalf("digitalocean alias not resolvable: %v", err)
	}
	if cmd.Name() != "list" {
		t.Errorf("expected to resolve to `list`, got %q", cmd.Name())
	}
}
