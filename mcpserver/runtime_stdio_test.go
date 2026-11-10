package mcpserver_test

import (
	"context"
	"testing"
	"time"
)

func TestStatusReportsConnectedRuntime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session := connect(t, ctx, binary(t), t.TempDir(), "")
	envelope, result := invoke(t, ctx, session, "memory_maintenance", map[string]any{"action": "status"})
	status := payload(t, envelope)
	runtime := status["runtime"].(map[string]any)
	if result.IsError || runtime["pid"].(float64) <= 0 || runtime["version"] == "" || runtime["version"] == "unknown" || runtime["started_at"] == "" {
		t.Fatal(envelope)
	}
	if runtime["version"] != session.InitializeResult().ServerInfo.Version {
		t.Fatal("initialization and status disagree", envelope)
	}
	caps := runtime["capabilities"].(map[string]any)
	for _, capability := range []string{"image_content", "text_pages", "attachment_resources", "attachment_resource_pages"} {
		if caps[capability] != true {
			t.Fatal("missing capability", capability, envelope)
		}
	}
}
