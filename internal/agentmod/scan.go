package agentmod

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/wseternal/ssetunnel/internal/moduleproto"
	"github.com/wseternal/ssetunnel/sdk"
)

// openAPIFileName is the OpenAPI document inside an installed module
// package directory.
const openAPIFileName = "openapi.yaml"

// manifestProbeTimeout bounds how long one manifest probe may run.
const manifestProbeTimeout = 10 * time.Second

// ScanModules registers module packages installed under dir. Each immediate
// subdirectory holds one package: an executable and an openapi.yaml file. The
// executable prints its identity for sdk.ManifestFlag.
//
// A package found for the first time is enabled. A package that is already
// registered keeps its desired state, so an explicit stop survives an agent
// restart. A broken package is logged and skipped, and the other packages
// still load.
//
// The returned IDs list the packages that were installed or updated.
func (r *Registry) ScanModules(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read module directory: %w", err)
	}

	var installed []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		pkgDir := filepath.Join(dir, entry.Name())
		pkg, err := readPackage(pkgDir)
		if err != nil {
			log.Printf("registry: skip module package %s: %v", pkgDir, err)
			continue
		}

		previous := r.Package(pkg.ModuleID)
		if previous != nil && samePackage(previous, pkg) {
			continue
		}

		// Same check as the CLI install path: reject symlinks that escape
		// the package directory before the supervisor can start the binary.
		if err := pkg.ValidateExecutable(); err != nil {
			log.Printf("registry: skip module %s from %s: %v", pkg.ModuleID, pkgDir, err)
			continue
		}

		if err := r.Install(pkg); err != nil {
			log.Printf("registry: install module %s from %s: %v", pkg.ModuleID, pkgDir, err)
			continue
		}
		if previous == nil {
			if err := r.Enable(pkg.ModuleID); err != nil {
				log.Printf("registry: enable module %s: %v", pkg.ModuleID, err)
			}
		}
		installed = append(installed, pkg.ModuleID)
	}
	return installed, nil
}

// samePackage reports whether a registered package matches a scanned one.
func samePackage(a, b *Package) bool {
	return a.ServiceName == b.ServiceName &&
		a.ServiceVersion == b.ServiceVersion &&
		a.Revision == b.Revision &&
		a.Executable == b.Executable &&
		a.PackageDir == b.PackageDir &&
		bytes.Equal(a.OpenAPIYAML, b.OpenAPIYAML)
}

// readPackage builds a package from one installed module directory.
func readPackage(dir string) (*Package, error) {
	openapiPath := filepath.Join(dir, openAPIFileName)
	openapiYAML, err := os.ReadFile(openapiPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", openapiPath, err)
	}
	if len(openapiYAML) > moduleproto.MaxOpenAPISize {
		return nil, fmt.Errorf("%s is too large: %d bytes (max %d)", openapiPath, len(openapiYAML), moduleproto.MaxOpenAPISize)
	}

	executable, err := findExecutable(dir)
	if err != nil {
		return nil, err
	}

	info, err := probeManifest(executable)
	if err != nil {
		return nil, err
	}

	return &Package{
		ModuleID:       info.ModuleID,
		ServiceName:    info.ServiceName,
		ServiceVersion: info.ServiceVersion,
		Revision:       info.Revision,
		Executable:     executable,
		OpenAPIYAML:    openapiYAML,
		PackageDir:     dir,
	}, nil
}

// findExecutable locates the module binary in a package directory. It prefers
// the file named after the directory, then falls back to the only visible
// regular file that is not the OpenAPI document.
func findExecutable(dir string) (string, error) {
	name := filepath.Base(dir)
	for _, candidate := range []string{name, name + ".exe"} {
		path := filepath.Join(dir, candidate)
		if isRegularFile(path) {
			return path, nil
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", dir, err)
	}

	var candidates []string
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == openAPIFileName || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if isRegularFile(path) {
			candidates = append(candidates, path)
		}
	}

	switch len(candidates) {
	case 0:
		return "", fmt.Errorf("no executable found in %s", dir)
	case 1:
		return candidates[0], nil
	default:
		return "", fmt.Errorf("ambiguous executables in %s: %s", dir, strings.Join(candidates, ", "))
	}
}

// isRegularFile reports whether path is a regular file.
func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// probeManifest runs the module binary with sdk.ManifestFlag and reads the
// identity it prints.
func probeManifest(executable string) (*sdk.ManifestInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), manifestProbeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, executable, sdk.ManifestFlag)
	// Minimal environment, matching the supervisor's launch shape.
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
	}

	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("probe %s: %w: %s", executable, err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("probe %s: %w", executable, err)
	}

	var info sdk.ManifestInfo
	if err := json.Unmarshal(out, &info); err != nil {
		return nil, fmt.Errorf("parse manifest from %s: %w", executable, err)
	}
	return &info, nil
}
