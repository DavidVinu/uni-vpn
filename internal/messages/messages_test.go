package messages

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
)

// The catalog must match uni_vpn/messages.py: ids, texts and actions.
func TestSameAsThePythonCatalog(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3")
	}
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	cmd := exec.Command(python, "-c", `import json
from uni_vpn import messages
print(json.dumps({k: [str(v), v.action] for k, v in messages.all_messages().items()}))`)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Skip("python core not importable:", err)
	}
	var want map[string][2]*string
	if err := json.Unmarshal(out, &want); err != nil {
		t.Fatal(err)
	}
	got := All()
	if len(got) != len(want) {
		t.Fatalf("%d messages, Python has %d", len(got), len(want))
	}
	for id, w := range want {
		m, ok := got[id]
		action := ""
		if w[1] != nil {
			action = *w[1]
		}
		if !ok || m.Text != *w[0] || m.Action != action {
			t.Errorf("%s: %+v, Python %q %q", id, m, *w[0], action)
		}
	}
}
