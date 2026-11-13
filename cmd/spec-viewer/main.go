package main

import (
	"context"
	"errors"
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

	// Hand-off only exists between loopback servers. A server bound to a
	// network-reachable host neither takes the lock nor receives folders from
	// other invocations, and this invocation does not hand its folder to one.
	handoffEnabled := server.IsLoopbackHost(cfg.Host)
	lockPath := instance.LockPath()
	if handoffEnabled {
		handoff := instance.TryHandoff(lockPath, absTarget)
		if outcome := describeHandoff(handoff, absTarget, cfg.NoOpen, openBrowser); outcome.handled {
			if outcome.failed {
				fmt.Fprint(os.Stderr, outcome.message)
				os.Exit(1)
			}
			fmt.Print(outcome.message)
			os.Exit(0)
		} else if outcome.message != "" {
			log.Print(outcome.message)
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

	var token string
	if handoffEnabled {
		token, err = instance.NewToken()
		if err != nil {
			log.Fatalf("error: %v", err)
		}
	}
	srv.SetHandoff(token, version)

	if !cfg.NoOpen {
		go openBrowser(srv.DefaultWorkspaceURL())
	}

	// Claim the lock last, right before serving, so a concurrent invocation
	// that probes this instance finds a listening server. A healthy holder keeps
	// the lock and this server runs alongside it, lock-less.
	self := instance.Info{Port: srv.Port(), PID: os.Getpid(), Token: token}
	var (
		owned   bool
		holder  instance.Info
		lockErr error
	)
	if handoffEnabled {
		owned, holder, lockErr = instance.AcquireLock(lockPath, self, instance.Healthy)
		switch {
		case lockErr != nil:
			log.Printf("warning: could not write lockfile: %v", lockErr)
		case owned:
			defer instance.Release(lockPath, self.PID)
		}
	}

	fmt.Printf("spec-viewer: serving %s at %s\n", absTarget, srv.DefaultWorkspaceURL())
	if !handoffEnabled {
		fmt.Printf("spec-viewer: hand-off is disabled because this server is network-exposed (host %q)\n", cfg.Host)
	}
	if handoffEnabled && lockErr == nil && !owned {
		fmt.Printf("spec-viewer: another viewer on port %d keeps the shared lock; this server runs alongside it\n", holder.Port)
	}

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

// handoffOutcome is what main does after trying to hand the target to a running instance.
type handoffOutcome struct {
	handled bool   // the hand-off is settled and no server should start; print message and exit
	failed  bool   // with handled: the instance refused the path, so print message to stderr and exit 1
	message string // stdout text when delivered, otherwise a log line (may be empty)
}

// describeHandoff decides what to do with a TryHandoff result. When the
// instance accepted the path it opens a browser tab through openURL only if no
// tab is connected to that workspace and noOpen is unset. A refusal of the path
// itself (missing, not markdown) ends the command; any other failure of a live
// instance falls back to a separate server that leaves its lock alone.
func describeHandoff(h instance.Handoff, target string, noOpen bool, openURL func(string)) handoffOutcome {
	var rejected *instance.RejectedError
	switch {
	case h.Done:
		url := fmt.Sprintf("http://localhost:%d%s", h.Info.Port, h.Result.URL)
		if h.Result.Clients == 0 && !noOpen {
			openURL(url)
		}
		return handoffOutcome{handled: true, message: fmt.Sprintf(
			"spec-viewer: sent %s to existing instance on port %d\nspec-viewer: url %s\n", target, h.Info.Port, url)}
	case errors.As(h.Err, &rejected):
		return handoffOutcome{handled: true, failed: true, message: fmt.Sprintf(
			"spec-viewer: the viewer on port %d refused %s: %s\n", h.Info.Port, target, rejected.Message)}
	case h.Holder:
		return handoffOutcome{message: fmt.Sprintf(
			"warning: could not send %s to the viewer on port %d: %v; starting a separate server without taking over its lock", target, h.Info.Port, h.Err)}
	}
	return handoffOutcome{}
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
