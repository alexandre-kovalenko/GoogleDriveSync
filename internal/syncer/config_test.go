package syncer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveRefreshTokenPreservesOtherConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	initial := `{"local_dir":"/tmp/source","drive_folder_id":"folder-id","custom":"kept","oauth":{"client_id":"client-id","client_secret":"client-secret","refresh_token":""}}`
	if err := os.WriteFile(path, []byte(initial), 0644); err != nil {
		t.Fatal(err)
	}

	if err := SaveRefreshToken(path, "new-refresh-token"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["custom"] != "kept" || got["local_dir"] != "/tmp/source" {
		t.Fatalf("unrelated config fields changed: %#v", got)
	}
	oauth := got["oauth"].(map[string]any)
	if oauth["refresh_token"] != "new-refresh-token" || oauth["client_id"] != "client-id" {
		t.Fatalf("unexpected oauth config: %#v", oauth)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("config permissions = %04o, want 0600", info.Mode().Perm())
	}
}

func TestLoadOAuthConfigAllowsMissingRefreshToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	config := `{"oauth":{"client_id":"client-id","client_secret":"client-secret"}}`
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}

	got, err := LoadOAuthConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.RefreshToken != "" {
		t.Errorf("refresh token = %q, want empty", got.RefreshToken)
	}
}
