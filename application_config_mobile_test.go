package songya

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMobileConfigurationPathsStayBesideRelativeAdministratorConfiguration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "admin.json")
	if err := os.WriteFile(path, []byte(`{"stores":{"person":"memories"},"password":"fixture-password","public_url":"https://songya.example/","android_apk":"Songya.apk"}`), 0600); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, path)
	if err != nil {
		t.Fatal(err)
	}
	config, err := LoadApplicationConfiguration(relative)
	if err != nil {
		t.Fatal(err)
	}
	if config.MobileState != filepath.Join(dir, "admin.mobile.json") || config.AndroidAPK != filepath.Join(dir, "Songya.apk") || config.Stores["person"] != filepath.Join(dir, "memories") {
		t.Fatalf("paths resolved against a duplicated directory: %#v", config)
	}
	if config.PublicURL != "https://songya.example" {
		t.Fatal("origin was not normalized")
	}
}
