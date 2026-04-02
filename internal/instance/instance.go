package instance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
)

// Info holds the running instance metadata persisted in the lockfile.
type Info struct {
	Port int `json:"port"`
	PID  int `json:"pid"`
}

// WriteLock creates parent directories as needed and writes info as JSON to path.
func WriteLock(path string, info Info) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("instance: mkdir: %w", err)
	}
	data, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("instance: marshal: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("instance: write: %w", err)
	}
	return nil
}

// ReadLock reads the JSON lockfile at path and returns the Info.
func ReadLock(path string) (Info, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Info{}, fmt.Errorf("instance: read: %w", err)
	}
	var info Info
	if err := json.Unmarshal(data, &info); err != nil {
		return Info{}, fmt.Errorf("instance: unmarshal: %w", err)
	}
	return info, nil
}

// Cleanup removes the lockfile at path, ignoring errors.
func Cleanup(path string) {
	_ = os.Remove(path)
}

// IsAlive returns true if the process with the given PID is running.
func IsAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil
}

// CheckExisting reads the lockfile at lockPath. If the recorded PID is alive,
// it returns (Info, true). If the PID is dead (stale), it removes the lockfile
// and returns (Info{}, false). If the file doesn't exist it also returns false.
func CheckExisting(lockPath string) (Info, bool) {
	info, err := ReadLock(lockPath)
	if err != nil {
		return Info{}, false
	}
	if IsAlive(info.PID) {
		return info, true
	}
	Cleanup(lockPath)
	return Info{}, false
}

// LockPath returns the canonical path for the lockfile.
func LockPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".config", "spec-viewer", "instance.json")
}

// OpenInExisting POSTs a request to the existing instance to open filePath.
// It returns the URL the existing instance is serving on success.
func OpenInExisting(info Info, filePath string) (string, error) {
	body, err := json.Marshal(map[string]string{"path": filePath})
	if err != nil {
		return "", fmt.Errorf("instance: marshal open request: %w", err)
	}
	url := fmt.Sprintf("http://localhost:%d/api/open", info.Port)
	resp, err := http.Post(url, "application/json", bytes.NewReader(body)) //nolint:noctx
	if err != nil {
		return "", fmt.Errorf("instance: POST /api/open: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("instance: /api/open returned %d", resp.StatusCode)
	}
	return url, nil
}
