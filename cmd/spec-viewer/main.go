package main

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	specviewer "github.com/bzon/spec-viewer"
	"github.com/bzon/spec-viewer/internal/config"
	"github.com/bzon/spec-viewer/internal/instance"
	"github.com/bzon/spec-viewer/internal/server"
	"github.com/bzon/spec-viewer/internal/watcher"
)

var version = "dev"

func main() {
	themeFlag := flag.String("theme", "", "syntax highlight theme")
	portFlag := flag.Int("port", 0, "port to listen on (0 = random)")
	hostFlag := flag.String("host", "", "host to listen on")
	noOpenFlag := flag.Bool("no-open", false, "do not open browser")
	versionFlag := flag.Bool("version", false, "print version")
	printThemeFlag := flag.Bool("print-theme-template", false, "print theme template CSS")
	flag.Parse()

	if *versionFlag {
		fmt.Printf("spec-viewer %s\n", version)
		os.Exit(0)
	}

	if *printThemeFlag {
		data, err := specviewer.FrontendAssets.ReadFile("frontend/css/theme-template.css")
		if err != nil {
			log.Fatalf("error reading theme template: %v", err)
		}
		fmt.Print(string(data))
		os.Exit(0)
	}

	cfg, err := config.LoadFromFile(config.DefaultConfigPath())
	if err != nil {
		log.Fatalf("error loading config: %v", err)
	}

	flags := config.Flags{
		Theme:  *themeFlag,
		Port:   *portFlag,
		Host:   *hostFlag,
		NoOpen: *noOpenFlag,
	}
	cfg = cfg.MergeFlags(flags)

	targetPath := "."
	if flag.NArg() > 0 {
		targetPath = flag.Arg(0)
	}

	absTarget, err := filepath.Abs(targetPath)
	if err != nil {
		log.Fatalf("error resolving path: %v", err)
	}

	lockPath := instance.LockPath()
	if info, alive := instance.CheckExisting(lockPath); alive {
		_, openErr := instance.OpenInExisting(info, absTarget)
		if openErr != nil {
			log.Printf("warning: could not send file to existing instance: %v", openErr)
		} else {
			fmt.Printf("spec-viewer: sent %s to existing instance on port %d\n", absTarget, info.Port)
			os.Exit(0)
		}
	}

	rootDir := absTarget
	targetFile := ""
	fi, err := os.Stat(absTarget)
	if err != nil {
		log.Fatalf("error accessing path: %v", err)
	}
	if !fi.IsDir() {
		rootDir = filepath.Dir(absTarget)
		targetFile = fi.Name()
	}

	frontendFS, err := fs.Sub(specviewer.FrontendAssets, "frontend")
	if err != nil {
		log.Fatalf("error creating frontend sub-FS: %v", err)
	}

	srv, err := server.New(rootDir, frontendFS, cfg.Host, cfg.Port, cfg.Theme, targetFile)
	if err != nil {
		log.Fatalf("error creating server: %v", err)
	}

	info := instance.Info{
		Port: srv.Port(),
		PID:  os.Getpid(),
	}
	if err := instance.WriteLock(lockPath, info); err != nil {
		log.Printf("warning: could not write lockfile: %v", err)
	}
	defer instance.Cleanup(lockPath)

	w, err := watcher.New(absTarget, func(changedPath string) {
		msg := fmt.Sprintf(`{"type":"reload","path":"%s"}`, changedPath)
		srv.Hub().Broadcast([]byte(msg))
	})
	if err != nil {
		log.Printf("warning: could not start file watcher: %v", err)
	} else {
		defer w.Close()
	}

	if !cfg.NoOpen {
		go openBrowser(srv.URL())
	}

	fmt.Printf("spec-viewer: serving %s at %s\n", rootDir, srv.URL())

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if shutdownErr := srv.Shutdown(ctx); shutdownErr != nil {
			log.Printf("shutdown error: %v", shutdownErr)
		}
	}()

	if startErr := srv.Start(); startErr != nil {
		log.Printf("server stopped: %v", startErr)
	}
}

func openBrowser(url string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
		args = []string{url}
	case "linux":
		cmd = "xdg-open"
		args = []string{url}
	default:
		return
	}
	if err := exec.Command(cmd, args...).Start(); err != nil {
		log.Printf("warning: could not open browser: %v", err)
	}
}
