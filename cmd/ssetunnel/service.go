package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/kardianos/service"
	"github.com/sourcegraph/conc"
	"github.com/sourcegraph/conc/panics"
)

// serviceActions is the set of verbs recognised by dispatchServiceAction.
var serviceActions = map[string]bool{
	"run": true, "start": true, "stop": true,
	"restart": true, "status": true, "reload": true,
}

// serviceProgram implements service.Interface, wrapping either runServer
// or runAgent as a managed OS service.  The run function is launched via
// a conc.WaitGroup for panic-safe structured concurrency.
type serviceProgram struct {
	name   string
	runFn  func(context.Context) error
	cancel context.CancelFunc
	wg     *conc.WaitGroup
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
	p.cancel()
	removePIDFile(p.name)
	// WaitAndRecover in a helper goroutine so we can enforce a deadline.
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

	svcConfig := &service.Config{
		Name:        "ssetunnel-" + subcommand,
		DisplayName: "ssetunnel " + subcommand,
		Description: "ssetunnel " + subcommand + " daemon",
		Arguments:   buildServiceArgs(subcommand, args),
	}

	prg := &serviceProgram{
		name:  svcConfig.Name,
		runFn: buildRunFn(subcommand, args[1:]), // strip the action verb
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
			return true, nil
		}
		// Install (or update) the service definition so that the
		// current flags are persisted for future restart/recovery.
		if ierr := svc.Install(); ierr != nil {
			log.Printf("service install: %v (may need root/sudo)", ierr)
		}
		if serr := svc.Start(); serr != nil {
			return true, fmt.Errorf("start: %w", serr)
		}
		fmt.Println("Service started.")
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
		return true, sendReload(svc)
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
func sendReload(_ service.Service) error {
	if runtime.GOOS == "windows" {
		return fmt.Errorf("reload is not supported on %s", runtime.GOOS)
	}
	pid, err := readPIDFile("ssetunnel-server")
	if err != nil {
		// Fall back to the agent PID file.
		pid, err = readPIDFile("ssetunnel-agent")
	}
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

// pidDir returns the directory for PID files (~/.ssetunnel/).
func pidDir() string {
	home, _ := os.UserHomeDir()
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
	os.Remove(pidFilePath(name))
}

// readPIDFile reads the PID from a service's PID file.
func readPIDFile(name string) (int, error) {
	data, err := os.ReadFile(pidFilePath(name))
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(string(data))
}

// installSIGHUPHandler registers a SIGHUP listener that logs the given
// message.  The actual reload logic is a TODO per subcommand.
func installSIGHUPHandler(role, message string) {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscallSIGHUP())
	go func() {
		for range hup {
			log.Printf("%s: %s", role, message)
		}
	}()
}
