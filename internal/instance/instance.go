// Package instance coordinates a single shared spec-viewer through a lock file
// that records the port, pid and hand-off token of the running server.
package instance

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const (
	healthTimeout = time.Second
	openTimeout   = 5 * time.Second
	tokenHeader   = "X-Spec-Viewer-Token"
	// acquireAttempts bounds the create/replace loop in AcquireLock.
	acquireAttempts = 5
)

// RejectedError means the running instance refused the path itself (missing,
// not markdown, or not servable), so starting another server would not help.
type RejectedError struct {
	Status  int
	Message string
}

func (e *RejectedError) Error() string {
	return fmt.Sprintf("instance: /api/open refused the path (%d): %s", e.Status, e.Message)
}

// ErrNoToken means the lock file has no hand-off token (written by an older binary).
var ErrNoToken = errors.New("instance: lock file has no token; cannot hand off")

// Info holds the running instance metadata persisted in the lockfile.
type Info struct {
	Port  int    `json:"port"`
	PID   int    `json:"pid"`
	Token string `json:"token,omitempty"`
}

// NewToken returns 32 random bytes, hex encoded.
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("instance: generate token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// writeTemp writes info to a new 0600 temp file next to path and returns its name.
func writeTemp(path string, info Info) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("instance: mkdir: %w", err)
	}
	data, err := json.Marshal(info)
	if err != nil {
		return "", fmt.Errorf("instance: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".instance-*.tmp") // created 0600
	if err != nil {
		return "", fmt.Errorf("instance: create temp: %w", err)
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("instance: write temp: %w", errors.Join(werr, cerr))
	}
	return tmp.Name(), nil
}

// WriteLock writes info to path atomically (temp file + rename), replacing any
// existing lock. Use AcquireLock to claim the lock without clobbering a live owner.
func WriteLock(path string, info Info) error {
	tmp, err := writeTemp(path, info)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("instance: rename: %w", err)
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

// AcquireLock claims the lock for info unless a healthy instance already holds
// it. It returns (true, info) when the lock now names info, or (false, holder)
// when a healthy holder was found; the caller then runs without the lock.
// healthy decides whether a recorded instance is alive (normally Healthy).
//
// The lock is created by writing a temp file and os.Link-ing it to path: link
// fails if path exists, so creation is exclusive, and the file appears with its
// full content at once (O_EXCL create would briefly expose an empty file to
// readers). A stale or unreadable lock is moved aside with rename, and only
// deleted if it still holds the content we judged stale; if another process
// replaced it in between, it is put back and the loop retries.
func AcquireLock(path string, info Info, healthy func(Info) bool) (bool, Info, error) {
	tmp, err := writeTemp(path, info)
	if err != nil {
		return false, Info{}, err
	}
	defer os.Remove(tmp)

	for range acquireAttempts {
		linkErr := os.Link(tmp, path)
		if linkErr == nil {
			return true, info, nil
		}
		if !os.IsExist(linkErr) {
			return false, Info{}, fmt.Errorf("instance: link lock: %w", linkErr)
		}

		raw, readErr := os.ReadFile(path)
		if os.IsNotExist(readErr) {
			continue
		}
		var holder Info
		if readErr == nil && json.Unmarshal(raw, &holder) == nil && holder.PID != 0 && healthy(holder) {
			return false, holder, nil
		}

		// Stale: move it aside, then delete it only if it is the file we read.
		aside := fmt.Sprintf("%s.stale.%d", path, os.Getpid())
		if err := os.Rename(path, aside); err != nil {
			continue
		}
		moved, _ := os.ReadFile(aside)
		if bytes.Equal(moved, raw) {
			_ = os.Remove(aside)
		} else if err := os.Link(aside, path); err == nil || os.IsExist(err) {
			_ = os.Remove(aside)
		}
	}
	return false, Info{}, errors.New("instance: could not acquire lock: contended")
}

// Release removes the lockfile at path only if it still names pid.
// The read-then-remove is not atomic; a lock replaced inside that window is
// the same hazard the stale-replacement path already tolerates.
func Release(path string, pid int) {
	info, err := ReadLock(path)
	if err != nil || info.PID != pid {
		return
	}
	_ = os.Remove(path)
}

// LockPath returns the canonical path for the lockfile.
func LockPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".config", "spec-viewer", "instance.json")
}

func baseURL(port int) string {
	return fmt.Sprintf("http://localhost:%d", port)
}

// httpClient ignores proxy settings: instances are always on this machine.
func httpClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: &http.Transport{Proxy: nil}}
}

// Healthy reports whether info names a live spec-viewer: GET /api/health must
// answer as spec-viewer with the pid recorded in the lock. This, not
// kill(pid, 0), is the liveness test, so a recycled pid or another program on
// the port does not count.
func Healthy(info Info) bool {
	if info.Port <= 0 || info.PID <= 0 {
		return false
	}
	client := httpClient(healthTimeout)
	defer client.CloseIdleConnections()
	resp, err := client.Get(baseURL(info.Port) + "/api/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var health struct {
		App string `json:"app"`
		PID int    `json:"pid"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&health); err != nil {
		return false
	}
	return health.App == "spec-viewer" && health.PID == info.PID
}

// OpenResult is the success reply of /api/open.
type OpenResult struct {
	// URL is the path-absolute URL the browser should open (for example /w/<id>/#docs/a.md).
	URL string `json:"url"`
	// Clients is the number of browser tabs connected to the workspace.
	Clients int `json:"clients"`
	// Workspace is the id of the workspace serving the path.
	Workspace string `json:"workspace"`
	// Created is true when the request added a new workspace.
	Created bool `json:"created"`
}

// OpenInExisting asks the instance named by info to show path (absolute).
// It returns a *RejectedError when the instance refuses the path and ErrNoToken when info carries no token.
func OpenInExisting(info Info, path string) (OpenResult, error) {
	if info.Token == "" {
		return OpenResult{}, ErrNoToken
	}
	body, err := json.Marshal(map[string]string{"path": path})
	if err != nil {
		return OpenResult{}, fmt.Errorf("instance: marshal open request: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, baseURL(info.Port)+"/api/open", bytes.NewReader(body))
	if err != nil {
		return OpenResult{}, fmt.Errorf("instance: build open request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tokenHeader, info.Token)

	client := httpClient(openTimeout)
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return OpenResult{}, fmt.Errorf("instance: POST /api/open: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound:
		var refusal struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&refusal)
		return OpenResult{}, &RejectedError{Status: resp.StatusCode, Message: refusal.Error}
	default:
		return OpenResult{}, fmt.Errorf("instance: /api/open returned %d", resp.StatusCode)
	}
	var result OpenResult
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&result); err != nil {
		return OpenResult{}, fmt.Errorf("instance: decode /api/open reply: %w", err)
	}
	return result, nil
}

// Handoff is the outcome of TryHandoff.
type Handoff struct {
	// Done is true when a healthy instance accepted the path.
	Done   bool
	Info   Info       // the instance that was contacted (valid when Holder is set)
	Holder bool       // a healthy instance exists, whether or not it accepted
	Result OpenResult // valid when Done
	// Err explains why Done is false for a healthy holder (*RejectedError, ErrNoToken, ...).
	Err error
}

// TryHandoff sends path to the instance named in the lock at lockPath, after
// confirming with a health probe that the instance is really alive. Without a
// healthy instance it returns the zero Handoff.
func TryHandoff(lockPath, path string) Handoff {
	return tryHandoff(lockPath, path, Healthy)
}

func tryHandoff(lockPath, path string, healthy func(Info) bool) Handoff {
	info, err := ReadLock(lockPath)
	if err != nil || !healthy(info) {
		return Handoff{}
	}
	result, err := OpenInExisting(info, path)
	if err != nil {
		return Handoff{Info: info, Holder: true, Err: err}
	}
	return Handoff{Done: true, Info: info, Holder: true, Result: result}
}
