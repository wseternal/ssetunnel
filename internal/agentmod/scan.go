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

// defaultManifestProbeTimeout bounds how long one manifest probe may run. A
// healthy module prints one JSON line and exits, so the budget stays short: a
// probe runs for every package whose files changed during startup and reload,
// serially. The agent can raise it with --module-probe-timeout when a module
// needs longer on a cold start.
const defaultManifestProbeTimeout = 2 * time.Second

// maxManifestBytes caps the manifest output a probed binary may write. The
// identity is a single small JSON line, so anything larger is a broken package
// not worth buffering in the agent process.
const maxManifestBytes = 64 << 10

// ScanModules registers module packages installed under dir. Each immediate
// subdirectory holds one package: an executable and an openapi.yaml file. The
// executable prints its identity for sdk.ManifestFlag.
//
// A package whose executable and spec are unchanged since it was registered
// keeps its recorded identity and is not executed again. A package found for
// the first time is enabled. A package that is already registered keeps its
// desired state, so an explicit stop survives an agent restart. A broken
// package is logged and skipped, and the other packages still load.
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
		files, err := readPackageFiles(pkgDir)
		if err != nil {
			log.Printf("registry: skip module package %s: %v", pkgDir, err)
			continue
		}

		// Reuse the registered package when the files on disk are unchanged.
		// The probe is a child process per package on every startup and
		// reload, so it is worth avoiding when the answer cannot differ.
		if previous := r.packageForPath(files.dir, files.executable); previous != nil &&
			previous.binaryUnchanged(files.size, files.modTime) &&
			bytes.Equal(previous.OpenAPIYAML, files.openapiYAML) {
			continue
		}

		pkg, err := files.probe(r.manifestProbeBudget())
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
		a.BinarySize == b.BinarySize &&
		a.BinaryModTime == b.BinaryModTime &&
		bytes.Equal(a.OpenAPIYAML, b.OpenAPIYAML)
}

// packageFiles holds the on-disk facts about one package directory. Reading
// them costs one small file read and two stats, and never runs the module.
type packageFiles struct {
	dir         string
	executable  string
	size        int64
	modTime     int64
	openapiYAML []byte
}

// readPackageFiles collects the spec and locates the executable of one
// installed module directory.
func readPackageFiles(dir string) (*packageFiles, error) {
	openapiYAML, err := readOpenAPI(dir)
	if err != nil {
		return nil, err
	}

	executable, err := findExecutable(dir)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(executable)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", executable, err)
	}

	return &packageFiles{
		dir:         dir,
		executable:  executable,
		size:        info.Size(),
		modTime:     info.ModTime().UnixNano(),
		openapiYAML: openapiYAML,
	}, nil
}

// readOpenAPI reads the package's OpenAPI document. The document must be a
// regular file: a symlinked spec would point outside the package, which is the
// same escape the executable check rejects.
func readOpenAPI(dir string) ([]byte, error) {
	openapiPath := filepath.Join(dir, openAPIFileName)
	// Check the size before reading so an oversized document never enters
	// memory. The read path re-checks the length in case the file grew.
	specInfo, err := os.Lstat(openapiPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", openapiPath, err)
	}
	if !specInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", openapiPath)
	}
	if specInfo.Size() > moduleproto.MaxOpenAPISize {
		return nil, fmt.Errorf("%s is too large: %d bytes (max %d)", openapiPath, specInfo.Size(), moduleproto.MaxOpenAPISize)
	}
	openapiYAML, err := os.ReadFile(openapiPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", openapiPath, err)
	}
	if len(openapiYAML) > moduleproto.MaxOpenAPISize {
		return nil, fmt.Errorf("%s is too large: %d bytes (max %d)", openapiPath, len(openapiYAML), moduleproto.MaxOpenAPISize)
	}
	return openapiYAML, nil
}

// probe runs the executable's manifest probe and builds the package from the
// identity it prints.
func (f *packageFiles) probe(timeout time.Duration) (*Package, error) {
	info, err := probeManifest(f.executable, timeout)
	if err != nil {
		return nil, err
	}

	pkg := &Package{
		ModuleID:       info.ModuleID,
		ServiceName:    info.ServiceName,
		ServiceVersion: info.ServiceVersion,
		Revision:       info.Revision,
		Executable:     f.executable,
		OpenAPIYAML:    f.openapiYAML,
		PackageDir:     f.dir,
	}
	// Stamp the file as it is after the probe, so the next scan compares
	// against the version that actually answered.
	pkg.stampBinary()
	return pkg, nil
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
// identity it prints. The budget bounds a package that hangs; an overflowing
// writer cancels it sooner.
func probeManifest(executable string, timeout time.Duration) (*sdk.ManifestInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, executable, sdk.ManifestFlag)
	// Minimal environment, matching the supervisor's launch shape.
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
	}

	probe := &manifestWriter{cancel: cancel}
	cmd.Stdout = probe
	stderr := &cappedBuffer{max: maxManifestBytes}
	cmd.Stderr = stderr
	// A module that leaves a child holding its stdout must not block the probe.
	// WaitDelay closes the output pipe once the context has killed the process.
	cmd.WaitDelay = time.Second

	err := cmd.Run()
	if probe.overflow {
		return nil, fmt.Errorf("probe %s: manifest output exceeds %d bytes", executable, maxManifestBytes)
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("probe %s: %w", executable, ctxErr)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("probe %s: %w: %s", executable, err, strings.TrimSpace(stderr.buf.String()))
		}
		return nil, fmt.Errorf("probe %s: %w", executable, err)
	}

	var info sdk.ManifestInfo
	if err := json.Unmarshal(probe.buf.Bytes(), &info); err != nil {
		return nil, fmt.Errorf("parse manifest from %s: %w", executable, err)
	}
	return &info, nil
}

// manifestWriter collects a probed binary's stdout and rejects output past
// maxManifestBytes. A manifest is one small JSON line, so a larger stream is a
// broken package. Overflow cancels the probe instead of waiting out the
// budget for a process that keeps writing.
type manifestWriter struct {
	buf      bytes.Buffer
	overflow bool
	cancel   context.CancelFunc
}

func (w *manifestWriter) Write(p []byte) (int, error) {
	if w.buf.Len()+len(p) > maxManifestBytes {
		w.overflow = true
		if w.cancel != nil {
			w.cancel()
		}
		return 0, fmt.Errorf("manifest output exceeds %d bytes", maxManifestBytes)
	}
	return w.buf.Write(p)
}

// cappedBuffer keeps the first max bytes of output and drops the rest without
// failing the write, so a noisy process cannot grow agent memory while its
// exit status stays reportable.
type cappedBuffer struct {
	buf bytes.Buffer
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	written := len(p)
	if room := b.max - b.buf.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		b.buf.Write(p)
	}
	return written, nil
}
