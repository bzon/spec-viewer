package instance_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bzon/spec-viewer/internal/instance"
)

func TestWriteAndReadLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "instance.json")
	info := instance.Info{Port: 8080, PID: 12345}

	if err := instance.WriteLock(path, info); err != nil {
		t.Fatalf("WriteLock error: %v", err)
	}

	got, err := instance.ReadLock(path)
	if err != nil {
		t.Fatalf("ReadLock error: %v", err)
	}
	if got.Port != info.Port || got.PID != info.PID {
		t.Errorf("got %+v, want %+v", got, info)
	}
}

func TestReadLockMissing(t *testing.T) {
	_, err := instance.ReadLock("/nonexistent/path/instance.json")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestCleanup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "instance.json")
	info := instance.Info{Port: 9090, PID: 99}

	if err := instance.WriteLock(path, info); err != nil {
		t.Fatalf("WriteLock error: %v", err)
	}

	instance.Cleanup(path)

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected lockfile to be removed after Cleanup")
	}
}

func TestIsAliveCurrentProcess(t *testing.T) {
	pid := os.Getpid()
	if !instance.IsAlive(pid) {
		t.Errorf("expected current PID %d to be alive", pid)
	}
}

func TestIsAliveDeadProcess(t *testing.T) {
	if instance.IsAlive(99999999) {
		t.Error("expected PID 99999999 to not be alive")
	}
}

func TestCheckExistingStaleRemoved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "instance.json")
	// Use a dead PID
	info := instance.Info{Port: 7070, PID: 99999999}

	if err := instance.WriteLock(path, info); err != nil {
		t.Fatalf("WriteLock error: %v", err)
	}

	_, alive := instance.CheckExisting(path)
	if alive {
		t.Error("expected stale lockfile to return false")
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected stale lockfile to be removed")
	}
}
