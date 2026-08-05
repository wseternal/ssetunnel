package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kardianos/service"
	"github.com/sourcegraph/conc"
	"github.com/sourcegraph/conc/panics"
)

// serviceActions is the set of verbs recognised by dispatchServiceAction.
var serviceActions = map[string]bool{
	"run": true, "start": true, "stop": true,
	"restart": true, "status": true, "reload": true,
	"uninstall": true,
}

// serviceProgram implements service.Interface, wrapping either runServer
// or runAgent as a managed OS service.  The run function is launched via
// a conc.WaitGroup for panic-safe structured concurrency.
type serviceProgram struct {
	name     string
	runFn    func(context.Context) error
	cancel   context.CancelFunc
	wg       *conc.WaitGroup
	stopOnce sync.Once
	stopErr  error
}

func (p *serviceProgram) Start(_ service.Service) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	// Write PID file so the reload action can find the daemon.
	writePIDFile(p.name)
	p.wg = conc.NewWaitGroup()
	p.wg.Go(func() {
		if err := p.runFn(ctx); err != nil {
			log.Printf("%s: run: %v", p.name, err)
		}
	})
	return nil
}

func (p *serviceProgram) Stop(_ service.Service) error {
	p.stopOnce.Do(func() {
		p.stopErr = p.doStop()
	})
	return p.stopErr
}

func (p *serviceProgram) doStop() error {
	p.cancel()
	removePIDFile(p.name)
	// WaitAndRecover in a helper goroutine so we can enforce a deadline.
	// On timeout the goroutine is intentionally abandoned; it will resolve
	// when the worker eventually exits.
	ch := make(chan *panics.Recovered, 1)
	go func() {
		ch <- p.wg.WaitAndRecover()
	}()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case rec := <-ch:
		if rec != nil {
			return fmt.Errorf("%s: worker panic: %v", p.name, rec)
		}
		return nil
	case <-timer.C:
		log.Printf("%s: service stop timed out after 30s", p.name)
		return nil
	}
}

// dispatchServiceAction checks whether args[0] is a service action verb.
// If so, it builds a service.Service, executes the action, and returns
// handled=true.  If args is empty or args[0] is not a verb, it returns
// handled=false and the caller falls through to the legacy direct-run path.
func dispatchServiceAction(subcommand string, args []string) (handled bool, err error) {
	if len(args) == 0 || !serviceActions[args[0]] {
		return false, nil
	}
	action := args[0]

	// Extract --service-user before passing args to buildServiceArgs.
	// This flag is consumed by the service installer, not the runtime.
	serviceUser, filteredArgs := extractFlag(args, "--service-user")

	// Default service user: use the current OS user when not root.
	// Running the daemon as root causes embedded postgres to fail
	// (initdb refuses to run as root), so we encourage non-root service
	// users. When running as root, --service-user must be specified.
	// Only auto-detect for actions that create or run the service;
	// stop/restart/status/uninstall identify the service by name alone.
	if serviceUser == "" && (action == "start" || action == "run") {
		if os.Getuid() == 0 {
			return true, fmt.Errorf("running as root is not supported (embedded postgres initdb restriction); "+
				"specify --service-user <name> to run the daemon as a non-root user")
		}
		if u, err := user.Current(); err == nil && u.Username != "root" {
			serviceUser = u.Username
		}
	}

	// Determine service mode:
	// - Non-root user → user-level service (systemd --user / LaunchAgents).
	//   UserName is intentionally omitted: user services already run as
	//   the owning user, and setting it is ignored on systemd or rejected
	//   by macOS launchd.
	// - Root + --service-user X → system-level service with User=X.
	// - Root without --service-user → error (handled above).
	userService := os.Getuid() != 0

	// Runtime flags are everything after the action verb, excluding
	// --service-user which was already extracted above.
	runtimeFlags := filteredArgs[1:]

	// --service-user affects the service definition (UserName), not the
	// runtime. Include it in the persisted flags so a subsequent bare
	// "start" can reconstruct the full configuration.
	if serviceUser != "" {
		runtimeFlags = append([]string{"--service-user", serviceUser}, runtimeFlags...)
	}

	// When "start" is invoked without runtime flags, load persisted
	// flags from the previous "start" (if any) so the user doesn't
	// have to re-specify --base, --db-url, etc. every time.
	if action == "start" && len(runtimeFlags) == 0 {
		if saved, loadErr := loadServiceArgs("ssetunnel-" + subcommand); loadErr == nil && len(saved) > 0 {
			runtimeFlags = saved
			fmt.Println("Using saved service args: " + strings.Join(saved, " "))
		}
	}

	svcConfig := &service.Config{
		Name:        "ssetunnel-" + subcommand,
		DisplayName: "ssetunnel " + subcommand,
		Description: "ssetunnel " + subcommand + " daemon",
		Arguments:   buildServiceArgs(subcommand, append([]string{action}, runtimeFlags...)),
		Option:      service.KeyValue{},
	}
	if userService {
		svcConfig.Option["UserService"] = true
	} else if serviceUser != "" {
		svcConfig.UserName = serviceUser
	}

	prg := &serviceProgram{
		name:  svcConfig.Name,
		runFn: buildRunFn(subcommand, runtimeFlags),
	}

	svc, err := service.New(prg, svcConfig)
	if err != nil {
		return true, fmt.Errorf("create service: %w", err)
	}

	switch action {
	case "run":
		return true, svc.Run()

	case "start":
		st, serr := svc.Status()
		if serr == nil && st == service.StatusRunning {
			fmt.Println("Service is already running.")
			if len(runtimeFlags) > 0 {
				fmt.Println("NOTE: new flags were not applied. Stop the service first, then re-start to change configuration.")
			}
			return true, nil
		}
		// Uninstall any existing service definition first so that
		// Install() writes a fresh unit file with the current flags.
		// Errors are expected when no service is installed yet.
		_ = svc.Uninstall()
		if ierr := svc.Install(); ierr != nil {
			return true, fmt.Errorf("install service: %w (may need root/sudo)", ierr)
		}
		// Persist runtime flags so a subsequent bare "start" reuses them.
		// New flags fully replace any previously saved flags.
		if len(runtimeFlags) > 0 {
			if prev, _ := loadServiceArgs(svcConfig.Name); len(prev) > 0 {
				fmt.Println("NOTE: new flags replace previously saved flags.")
			}
			if err := saveServiceArgs(svcConfig.Name, runtimeFlags); err != nil {
				log.Printf("warning: failed to save service args: %v", err)
			}
		}
		if serr := svc.Start(); serr != nil {
			return true, fmt.Errorf("start: %w", serr)
		}
		fmt.Println("Service started.")
		if userService && runtime.GOOS == "linux" {
			fmt.Println("NOTE: on Linux, user services stop when you log out.")
			fmt.Println("      Run 'loginctl enable-linger' to keep the service running after logout.")
		}
		return true, nil

	case "stop":
		return true, svc.Stop()

	case "restart":
		return true, svc.Restart()

	case "status":
		st, serr := svc.Status()
		if serr != nil {
			return true, fmt.Errorf("status: %w", serr)
		}
		switch st {
		case service.StatusRunning:
			fmt.Println("Status: Running")
		case service.StatusStopped:
			fmt.Println("Status: Stopped")
		default:
			fmt.Println("Status: Unknown")
		}
		return true, nil

	case "reload":
		return true, sendReload(svcConfig)

	case "uninstall":
		// Stop the service first if it is running.
		// Errors are ignored — the service may already be stopped or absent.
		_ = svc.Stop()
		if uerr := svc.Uninstall(); uerr != nil {
			return true, fmt.Errorf("uninstall service: %w (may need root/sudo)", uerr)
		}
		// Clean up persisted state.
		removePIDFile(svcConfig.Name)
		argsPath := filepath.Join(pidDir(), svcConfig.Name+".args")
		if rerr := os.Remove(argsPath); rerr != nil && !os.IsNotExist(rerr) {
			log.Printf("warning: failed to remove saved args: %v", rerr)
		}
		fmt.Println("Service uninstalled.")
		return true, nil
	}
	return false, nil
}

// buildRunFn returns a closure that calls runServer or runAgent with the
// action verb already stripped from args.
func buildRunFn(subcommand string, args []string) func(context.Context) error {
	switch subcommand {
	case "server":
		return func(ctx context.Context) error { return runServer(ctx, args) }
	case "agent":
		return func(ctx context.Context) error { return runAgent(ctx, args) }
	default:
		return func(context.Context) error {
			return fmt.Errorf("unsupported subcommand for service: %s", subcommand)
		}
	}
}

// buildServiceArgs constructs the Arguments slice for service.Config.
// The OS service manager invokes: binary <Arguments...>.
// We replace the action verb with "run" and preserve all remaining flags.
func buildServiceArgs(subcommand string, args []string) []string {
	out := []string{subcommand, "run"}
	if len(args) > 1 {
		out = append(out, args[1:]...)
	}
	return out
}

// sendReload sends SIGHUP to the running service process.  It reads the
// PID from the file written by Start (pidFilePath).  On platforms without
// SIGHUP (Windows) it returns an error.
func sendReload(svcConfig *service.Config) error {
	if runtime.GOOS == "windows" {
		return fmt.Errorf("reload is not supported on %s", runtime.GOOS)
	}
	pid, err := readPIDFile(svcConfig.Name)
	if err != nil {
		return fmt.Errorf("no running daemon found (start it first): %w", err)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}
	if err := proc.Signal(syscallSIGHUP()); err != nil {
		return fmt.Errorf("send SIGHUP to pid %d: %w", pid, err)
	}
	fmt.Printf("Reload signal sent to pid %d.\n", pid)
	return nil
}

// pidDir returns the directory for PID and args files (~/.ssetunnel/).
// Tests may override this variable to redirect to a temp directory.
var pidDir = func() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".ssetunnel")
	}
	return filepath.Join(home, ".ssetunnel")
}

// pidFilePath returns the PID file path for a named service.
func pidFilePath(name string) string {
	return filepath.Join(pidDir(), name+".pid")
}

// writePIDFile writes the current process PID so the reload action can
// find the running daemon.
func writePIDFile(name string) {
	dir := pidDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("%s: write pid dir: %v", name, err)
		return
	}
	if err := os.WriteFile(pidFilePath(name), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		log.Printf("%s: write pid file: %v", name, err)
	}
}

// removePIDFile cleans up the PID file on stop.
func removePIDFile(name string) {
	if err := os.Remove(pidFilePath(name)); err != nil && !os.IsNotExist(err) {
		log.Printf("%s: remove pid file: %v", name, err)
	}
}

// readPIDFile reads the PID from a service's PID file.
func readPIDFile(name string) (int, error) {
	data, err := os.ReadFile(pidFilePath(name))
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

// saveServiceArgs persists runtime flags so a subsequent bare "start"
// can reuse them without the user having to re-specify every flag.
func saveServiceArgs(name string, args []string) error {
	return saveServiceArgsToDir(pidDir(), name, args)
}

// saveServiceArgsToDir is the testable core of saveServiceArgs.
func saveServiceArgsToDir(dir, name string, args []string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name+".args"), []byte(strings.Join(args, "\n")), 0o600)
}

// loadServiceArgs loads previously saved runtime flags.
// Returns nil if no saved args exist.
func loadServiceArgs(name string) ([]string, error) {
	return loadServiceArgsFromDir(pidDir(), name)
}

// loadServiceArgsFromDir is the testable core of loadServiceArgs.
func loadServiceArgsFromDir(dir, name string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(dir, name+".args"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return nil, nil
	}
	return strings.Split(text, "\n"), nil
}

// extractFlag removes a --key value pair from args and returns the value
// and the remaining args. It supports both --key value and --key=value
// forms. If the flag is not present, it returns ("", originalArgs).
func extractFlag(args []string, key string) (value string, remaining []string) {
	remaining = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]

		// --key=value form
		if strings.HasPrefix(arg, key+"=") {
			value = strings.TrimPrefix(arg, key+"=")
			continue
		}

		// --key value form
		if arg == key && i+1 < len(args) {
			value = args[i+1]
			i++ // skip the next arg (value)
			continue
		}

		remaining = append(remaining, arg)
	}
	return value, remaining
}
