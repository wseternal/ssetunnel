package clicommand

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/wseternal/ssetunnel/internal/agentmod"
)

// BinaryName is the CLI binary name used in help text and usage messages.
// It defaults to "ssetunnel" and is overridden by cmd/cli to "cli".
var BinaryName = "ssetunnel"

// moduleUsage returns the help text for the module subcommand,
// using the current BinaryName.
func moduleUsage() string {
	return fmt.Sprintf(`usage: %s module <subcommand> [flags]

subcommands:
  install    install a module package
  start      enable a module (agent will start it)
  stop       disable a module (agent will stop it)
  restart    restart a module (disable then enable)
  status     show module status
  remove     remove an installed module
  list       list all installed modules
`, BinaryName)
}

// getRegistry creates or loads the module registry from the default data directory.
func getRegistry() (*agentmod.Registry, error) {
	dataDir, err := AgentDataDir()
	if err != nil {
		return nil, err
	}
	return agentmod.NewRegistry(dataDir)
}

// RunModule manages agent module lifecycle (install, start, stop, etc.).
func RunModule(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, moduleUsage())
		return fmt.Errorf("no subcommand specified")
	}

	subcmd := args[0]
	subargs := args[1:]

	switch subcmd {
	case "install":
		return moduleInstall(subargs)
	case "start":
		return moduleStart(subargs)
	case "stop":
		return moduleStop(subargs)
	case "restart":
		return moduleRestart(subargs)
	case "status":
		return moduleStatus(subargs)
	case "remove":
		return moduleRemove(subargs)
	case "list":
		return moduleList(subargs)
	default:
		fmt.Fprintf(os.Stderr, "unknown module subcommand %q\n%s", subcmd, moduleUsage())
		return fmt.Errorf("unknown subcommand: %s", subcmd)
	}
}

func moduleInstall(args []string) error {
	fs := flag.NewFlagSet("module install", flag.ExitOnError)
	executable := fs.String("executable", "", "Path to the module executable (required)")
	openapiFile := fs.String("openapi", "", "Path to the OpenAPI YAML file (required)")
	revision := fs.String("revision", "", "Module revision string")
	fs.Parse(args)

	if *executable == "" {
		return fmt.Errorf("--executable is required")
	}
	if *openapiFile == "" {
		return fmt.Errorf("--openapi is required")
	}
	if fs.NArg() < 3 {
		return fmt.Errorf("usage: %s module install <module-id> <service-name> <version> --executable <path> --openapi <path>", BinaryName)
	}

	moduleID := fs.Arg(0)
	serviceName := fs.Arg(1)
	version := fs.Arg(2)

	// Resolve paths.
	exePath, err := filepath.Abs(*executable)
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	openapiPath, err := filepath.Abs(*openapiFile)
	if err != nil {
		return fmt.Errorf("resolve openapi path: %w", err)
	}

	// Read OpenAPI YAML.
	openapiYAML, err := os.ReadFile(openapiPath)
	if err != nil {
		return fmt.Errorf("read openapi file: %w", err)
	}

	// Determine package directory (parent of executable).
	packageDir := filepath.Dir(exePath)

	pkg := &agentmod.Package{
		ModuleID:       moduleID,
		ServiceName:    serviceName,
		ServiceVersion: version,
		Revision:       *revision,
		Executable:     exePath,
		OpenAPIYAML:    openapiYAML,
		PackageDir:     packageDir,
	}

	reg, err := getRegistry()
	if err != nil {
		return fmt.Errorf("get registry: %w", err)
	}

	if err := reg.Install(pkg); err != nil {
		return fmt.Errorf("install: %w", err)
	}

	// W8 fix: Validate executable path (symlink rejection check).
	if err := pkg.ValidateExecutable(); err != nil {
		return fmt.Errorf("validate executable: %w", err)
	}

	fmt.Printf("Installed module %s v%s\n", moduleID, version)
	fmt.Printf("Note: Use '%s module start' to enable the module.\n", BinaryName)
	return nil
}

func moduleStart(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: %s module start <module-id>", BinaryName)
	}
	moduleID := args[0]

	reg, err := getRegistry()
	if err != nil {
		return fmt.Errorf("get registry: %w", err)
	}

	if err := reg.Enable(moduleID); err != nil {
		return fmt.Errorf("enable: %w", err)
	}

	fmt.Printf("Module %s enabled (will start on next agent reconcile)\n", moduleID)
	fmt.Println("Note: The agent must be running for the module to start.")
	return nil
}

func moduleStop(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: %s module stop <module-id>", BinaryName)
	}
	moduleID := args[0]

	reg, err := getRegistry()
	if err != nil {
		return fmt.Errorf("get registry: %w", err)
	}

	if err := reg.Disable(moduleID); err != nil {
		return fmt.Errorf("disable: %w", err)
	}

	fmt.Printf("Module %s disabled (will stop on next agent reconcile)\n", moduleID)
	return nil
}

func moduleRestart(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: %s module restart <module-id>", BinaryName)
	}
	moduleID := args[0]

	reg, err := getRegistry()
	if err != nil {
		return fmt.Errorf("get registry: %w", err)
	}

	// Disable first.
	if err := reg.Disable(moduleID); err != nil {
		return fmt.Errorf("disable: %w", err)
	}

	// Brief pause to allow stop to propagate.
	time.Sleep(100 * time.Millisecond)

	// Re-enable.
	if err := reg.Enable(moduleID); err != nil {
		return fmt.Errorf("enable: %w", err)
	}

	fmt.Printf("Module %s restarted (will take effect on next agent reconcile)\n", moduleID)
	return nil
}

func moduleStatus(args []string) error {
	reg, err := getRegistry()
	if err != nil {
		return fmt.Errorf("get registry: %w", err)
	}

	if len(args) < 1 {
		return moduleList(nil)
	}

	moduleID := args[0]
	pkg := reg.Package(moduleID)
	if pkg == nil {
		return fmt.Errorf("module %q not installed", moduleID)
	}

	enabled := reg.IsEnabled(moduleID)
	status := "disabled"
	if enabled {
		status = "enabled"
	}

	fmt.Printf("Module:     %s\n", pkg.ModuleID)
	fmt.Printf("Service:    %s v%s\n", pkg.ServiceName, pkg.ServiceVersion)
	if pkg.Revision != "" {
		fmt.Printf("Revision:   %s\n", pkg.Revision)
	}
	fmt.Printf("Executable: %s\n", pkg.Executable)
	fmt.Printf("Status:     %s\n", status)
	return nil
}

func moduleRemove(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: %s module remove <module-id>", BinaryName)
	}
	moduleID := args[0]

	reg, err := getRegistry()
	if err != nil {
		return fmt.Errorf("get registry: %w", err)
	}

	if err := reg.Remove(moduleID); err != nil {
		return fmt.Errorf("remove: %w", err)
	}

	fmt.Printf("Module %s removed\n", moduleID)
	return nil
}

func moduleList(args []string) error {
	reg, err := getRegistry()
	if err != nil {
		return fmt.Errorf("get registry: %w", err)
	}

	modules := reg.AllModules()
	if len(modules) == 0 {
		fmt.Println("No modules installed.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "MODULE ID\tSERVICE\tVERSION\tSTATUS")
	for _, id := range modules {
		pkg := reg.Package(id)
		if pkg == nil {
			continue
		}
		status := "disabled"
		if reg.IsEnabled(id) {
			status = "enabled"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", pkg.ModuleID, pkg.ServiceName, pkg.ServiceVersion, status)
	}
	return w.Flush()
}
