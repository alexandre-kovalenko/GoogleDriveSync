package syncer

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Config contains local sync settings and the OAuth credentials used silently
// to obtain Google Drive access tokens.
type Config struct {
	LocalDir        string      `json:"local_dir"`
	DriveFolderName string      `json:"drive_folder_name"`
	DriveFolderID   string      `json:"drive_folder_id,omitempty"`
	OAuth           OAuthConfig `json:"oauth"`
}

type OAuthConfig struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RefreshToken string `json:"refresh_token"`
}

func LoadConfig(path string) (Config, error) {
	cfg, err := readConfig(path)
	if err != nil {
		return Config{}, err
	}

	if cfg.LocalDir == "" {
		return Config{}, fmt.Errorf("config must set local_dir")
	}
	if (cfg.DriveFolderName == "") == (cfg.DriveFolderID == "") {
		return Config{}, fmt.Errorf("config must set exactly one of drive_folder_name or drive_folder_id")
	}
	if err := validateOAuthConfig(cfg.OAuth, true); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func LoadOAuthConfig(path string) (OAuthConfig, error) {
	cfg, err := readConfig(path)
	if err != nil {
		return OAuthConfig{}, err
	}
	if err := validateOAuthConfig(cfg.OAuth, false); err != nil {
		return OAuthConfig{}, err
	}
	return cfg.OAuth, nil
}

func readConfig(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config: %w", err)
	}
	defer file.Close()

	var cfg Config
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return Config{}, fmt.Errorf("decode config: multiple JSON values")
		}
		return Config{}, fmt.Errorf("decode config trailing data: %w", err)
	}

	cfg.LocalDir = strings.TrimSpace(cfg.LocalDir)
	cfg.DriveFolderName = strings.TrimSpace(cfg.DriveFolderName)
	cfg.DriveFolderID = strings.TrimSpace(cfg.DriveFolderID)
	cfg.OAuth.ClientID = strings.TrimSpace(cfg.OAuth.ClientID)
	cfg.OAuth.ClientSecret = strings.TrimSpace(cfg.OAuth.ClientSecret)
	cfg.OAuth.RefreshToken = strings.TrimSpace(cfg.OAuth.RefreshToken)
	return cfg, nil
}

func validateOAuthConfig(cfg OAuthConfig, requireRefreshToken bool) error {
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return fmt.Errorf("config oauth must set client_id and client_secret")
	}
	if requireRefreshToken && cfg.RefreshToken == "" {
		return fmt.Errorf("config oauth must set refresh_token; run with -authorize to obtain one")
	}
	return nil
}

func SaveRefreshToken(path, refreshToken string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config to save refresh token: %w", err)
	}

	var config map[string]json.RawMessage
	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("decode config to save refresh token: %w", err)
	}
	var oauth map[string]json.RawMessage
	if raw, ok := config["oauth"]; ok {
		if err := json.Unmarshal(raw, &oauth); err != nil {
			return fmt.Errorf("decode oauth config to save refresh token: %w", err)
		}
	} else {
		return fmt.Errorf("config is missing oauth settings")
	}
	if oauth == nil {
		return fmt.Errorf("config oauth settings must be a JSON object")
	}
	tokenJSON, err := json.Marshal(refreshToken)
	if err != nil {
		return fmt.Errorf("encode refresh token: %w", err)
	}
	oauth["refresh_token"] = tokenJSON
	oauthJSON, err := json.Marshal(oauth)
	if err != nil {
		return fmt.Errorf("encode oauth config: %w", err)
	}
	config["oauth"] = oauthJSON
	updated, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	updated = append(updated, '\n')

	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary config file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0600); err != nil {
		return errors.Join(fmt.Errorf("secure temporary config file: %w", err), temp.Close())
	}
	if _, err := temp.Write(updated); err != nil {
		return errors.Join(fmt.Errorf("write updated config: %w", err), temp.Close())
	}
	if err := temp.Sync(); err != nil {
		return errors.Join(fmt.Errorf("sync updated config: %w", err), temp.Close())
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close updated config: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace config with updated refresh token: %w", err)
	}
	return nil
}
