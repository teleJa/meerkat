package agent

import (
	"encoding/base64"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"meerkat/edge/internal/protocol"
)

type captureArtifactSender struct {
	messages []any
}

func (s *captureArtifactSender) Send(v any) error {
	s.messages = append(s.messages, v)
	return nil
}

func (s *captureArtifactSender) SendRaw([]byte) error { return nil }

func TestCollectArtifactsSendsRegisterMessage(t *testing.T) {
	dir := t.TempDir()
	content := []byte("<html><body>artifact test</body></html>")
	if err := os.WriteFile(filepath.Join(dir, "preview.html"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	sender := &captureArtifactSender{}
	manager := &Manager{sender: sender, log: log.Default()}
	cmd := &protocol.Command{
		TaskID: "tsk_test", SessionID: "sess_test", TurnID: "turn_test",
		Payload: protocol.CommandPayload{Cwd: dir},
	}
	manager.collectArtifacts(cmd, time.Now().Add(-time.Minute))
	if len(sender.messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(sender.messages))
	}
	register, ok := sender.messages[0].(protocol.ArtifactRegister)
	if !ok {
		t.Fatalf("message type = %T", sender.messages[0])
	}
	if register.Type != protocol.TypeArtifactRegister || register.Artifact.TaskID != "tsk_test" || register.Artifact.Name != "preview.html" {
		t.Fatalf("unexpected artifact: %+v", register)
	}
	decoded, err := base64.StdEncoding.DecodeString(register.Artifact.ContentBase64)
	if err != nil || string(decoded) != string(content) {
		t.Fatalf("content mismatch: %v", err)
	}
}
