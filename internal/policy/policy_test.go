package policy

import "testing"

// TestSessionGrantIsExact 验证一次授权不会扩大到其他参数或工作目录。
func TestSessionGrantIsExact(t *testing.T) {
	engine, err := NewEngine(ModeWorkspaceWrite)
	if err != nil {
		t.Fatal(err)
	}
	approved := Request{Tool: "exec", Mutating: true, Command: []string{"/bin/go", "version"}, Workdir: "/workspace"}
	if engine.Decide(approved) != Ask {
		t.Fatal("command should ask first")
	}
	engine.GrantSession(GrantKey(approved))
	if engine.Decide(approved) != Allow {
		t.Fatal("exact grant was not honored")
	}
	changed := approved
	changed.Command = []string{"/bin/go", "run", "."}
	if engine.Decide(changed) != Ask {
		t.Fatal("grant escaped to other arguments")
	}
	changed = approved
	changed.Workdir = "/other"
	if engine.Decide(changed) != Ask {
		t.Fatal("grant escaped to another directory")
	}
}
