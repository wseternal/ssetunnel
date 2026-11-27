package clicommand

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/wseternal/ssetunnel/internal/clientapp"
)

// maxPrettyJSON is the largest body indented on a terminal. A larger body
// streams raw so a long response is never buffered whole.
const maxPrettyJSON = 8 << 20

// consoleUsage returns the help text for the console subcommand group.
func consoleUsage() string {
	return fmt.Sprintf(`usage: %s console <subcommand> [flags]

subcommands:
  list-agents                       list agents connected to the console
  list-modules [<agent-id>]         list modules exposed by an agent
  schema [<agent-id>] <module-id>   print operation schemas for a module
  openapi [<agent-id>] <module-id>  print the module OpenAPI document
  call [<agent-id>] <module-id>     invoke a module operation

The agent ID is optional when exactly one agent is connected.
All commands use the saved login session. Run '%s login' first.
`, BinaryName, BinaryName)
}

// RunConsole reads the live console module surface with the saved session.
func RunConsole(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, consoleUsage())
		return errors.New("no subcommand specified")
	}
	if args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(os.Stdout, consoleUsage())
		return nil
	}

	switch args[0] {
	case "list-agents":
		return consoleListAgents(ctx, args[1:])
	case "list-modules":
		return consoleListModules(ctx, args[1:])
	case "schema":
		return consoleSchema(ctx, args[1:])
	case "openapi":
		return consoleOpenAPI(ctx, args[1:])
	case "call":
		return consoleCall(ctx, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown console subcommand %q\n%s", args[0], consoleUsage())
		return fmt.Errorf("unknown subcommand: %s", args[0])
	}
}

// consoleListAgents prints the agents the session can reach.
func consoleListAgents(ctx context.Context, args []string) error {
	fs := consoleFlagSet("console list-agents")
	server := fs.String("server", "", consoleServerHelp)
	jsonOut := fs.Bool("json", false, "print JSON instead of a table")

	flags, pos := splitConsoleArgs(args, map[string]bool{"server": true})
	if help, err := parseConsoleFlags(fs, flags); err != nil {
		return err
	} else if help {
		return nil
	}
	if len(pos) != 0 {
		return fmt.Errorf("usage: %s console list-agents", BinaryName)
	}

	client, err := clientapp.NewConsoleClient(*server)
	if err != nil {
		return err
	}
	agents, err := client.ListAgents(ctx)
	if err != nil {
		return err
	}
	if *jsonOut {
		if agents == nil {
			agents = []clientapp.ConsoleAgent{}
		}
		return printJSON(agents)
	}
	if len(agents) == 0 {
		fmt.Println("No connected agents.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "AGENT ID\tSESSION ID")
	for _, agent := range agents {
		fmt.Fprintf(w, "%s\t%s\n", agent.AgentID, agent.SessionID)
	}
	return w.Flush()
}

// consoleListModules prints the modules one agent exposes.
func consoleListModules(ctx context.Context, args []string) error {
	fs := consoleFlagSet("console list-modules")
	server := fs.String("server", "", consoleServerHelp)
	jsonOut := fs.Bool("json", false, "print JSON instead of a table")

	flags, pos := splitConsoleArgs(args, map[string]bool{"server": true})
	if help, err := parseConsoleFlags(fs, flags); err != nil {
		return err
	} else if help {
		return nil
	}
	if len(pos) > 1 {
		return fmt.Errorf("usage: %s console list-modules [<agent-id>]", BinaryName)
	}

	var explicitAgent string
	if len(pos) == 1 {
		explicitAgent = pos[0]
	}
	client, err := clientapp.NewConsoleClient(*server)
	if err != nil {
		return err
	}
	agentID, err := resolveConsoleAgent(ctx, client, explicitAgent)
	if err != nil {
		return err
	}
	modules, err := client.ListModules(ctx, agentID)
	if err != nil {
		return err
	}
	if *jsonOut {
		if modules == nil {
			modules = []clientapp.ConsoleModule{}
		}
		return printJSON(modules)
	}
	if len(modules) == 0 {
		fmt.Println("This agent exposes no modules.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "MODULE ID\tSERVICE\tVERSION\tREVISION\tHEALTH\tUI")
	for _, module := range modules {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			module.ModuleID,
			dashIfEmpty(module.ServiceName),
			dashIfEmpty(module.ServiceVersion),
			dashIfEmpty(module.Revision),
			dashIfEmpty(module.Health),
			yesIf(module.HasUI),
		)
	}
	return w.Flush()
}

// consoleSchema prints the operation document for one module.
func consoleSchema(ctx context.Context, args []string) error {
	fs := consoleFlagSet("console schema")
	server := fs.String("server", "", consoleServerHelp)

	flags, pos := splitConsoleArgs(args, map[string]bool{"server": true})
	if help, err := parseConsoleFlags(fs, flags); err != nil {
		return err
	} else if help {
		return nil
	}
	explicitAgent, moduleID, err := consoleModuleTarget(pos)
	if err != nil {
		return fmt.Errorf("usage: %s console schema [<agent-id>] <module-id>", BinaryName)
	}

	client, err := clientapp.NewConsoleClient(*server)
	if err != nil {
		return err
	}
	agentID, err := resolveConsoleAgent(ctx, client, explicitAgent)
	if err != nil {
		return err
	}
	raw, _, err := client.Operations(ctx, agentID, moduleID)
	if err != nil {
		return err
	}
	return writeIndentedJSON(raw)
}

// consoleOpenAPI prints the raw OpenAPI document for one module.
func consoleOpenAPI(ctx context.Context, args []string) error {
	fs := consoleFlagSet("console openapi")
	server := fs.String("server", "", consoleServerHelp)

	flags, pos := splitConsoleArgs(args, map[string]bool{"server": true})
	if help, err := parseConsoleFlags(fs, flags); err != nil {
		return err
	} else if help {
		return nil
	}
	explicitAgent, moduleID, err := consoleModuleTarget(pos)
	if err != nil {
		return fmt.Errorf("usage: %s console openapi [<agent-id>] <module-id>", BinaryName)
	}

	client, err := clientapp.NewConsoleClient(*server)
	if err != nil {
		return err
	}
	agentID, err := resolveConsoleAgent(ctx, client, explicitAgent)
	if err != nil {
		return err
	}
	data, err := client.OpenAPI(ctx, agentID, moduleID)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(data)
	return err
}

// consoleCall invokes one module operation.
func consoleCall(ctx context.Context, args []string) error {
	fs := consoleFlagSet("console call")
	server := fs.String("server", "", consoleServerHelp)
	method := fs.String("method", "", "operation method (required without --operation)")
	path := fs.String("path", "", "operation path, query included (required without --operation)")
	operation := fs.String("operation", "", "operation ID from the schema view")
	data := fs.String("d", "", "JSON body, @file, or @- for stdin")
	fs.StringVar(data, "data", "", "JSON body, @file, or @- for stdin")
	include := fs.Bool("i", false, "print the status line and response headers")
	fs.BoolVar(include, "include", false, "print the status line and response headers")
	var headers consoleHeaderFlags
	fs.Var(&headers, "H", "request header in \"Name: value\" form (repeatable)")
	fs.Var(&headers, "header", "request header in \"Name: value\" form (repeatable)")

	flags, pos := splitConsoleArgs(args, map[string]bool{
		"method": true, "path": true, "operation": true, "server": true,
		"H": true, "header": true, "d": true, "data": true,
	})
	if help, err := parseConsoleFlags(fs, flags); err != nil {
		return err
	} else if help {
		return nil
	}
	explicitAgent, moduleID, err := consoleModuleTarget(pos)
	if err != nil {
		return fmt.Errorf("usage: %s console call [<agent-id>] <module-id> --method <method> --path <path>", BinaryName)
	}

	requestMethod := strings.ToUpper(strings.TrimSpace(*method))
	requestPath := strings.TrimSpace(*path)
	operationID := strings.TrimSpace(*operation)
	if operationID != "" && (requestMethod != "" || requestPath != "") {
		return errors.New("use either --operation or --method with --path, not both")
	}
	if operationID == "" && (requestMethod == "" || requestPath == "") {
		return errors.New("--method and --path are required unless --operation is given")
	}

	client, err := clientapp.NewConsoleClient(*server)
	if err != nil {
		return err
	}
	agentID, err := resolveConsoleAgent(ctx, client, explicitAgent)
	if err != nil {
		return err
	}

	if operationID != "" {
		_, operations, err := client.Operations(ctx, agentID, moduleID)
		if err != nil {
			return err
		}
		op, err := matchConsoleOperation(operations, operationID)
		if err != nil {
			return err
		}
		requestMethod = strings.ToUpper(op.Method)
		requestPath = op.Path
		if strings.Contains(requestPath, "{") {
			return fmt.Errorf("operation %q has path parameters; pass --method %s and --path with concrete values", operationID, requestMethod)
		}
	}
	if requestMethod == "" || requestPath == "" {
		return errors.New("--method and --path are required unless --operation is given")
	}

	requestHeaders, err := buildConsoleHeaders(headers)
	if err != nil {
		return err
	}
	body, err := readConsoleBody(*data)
	if err != nil {
		return err
	}

	resp, err := client.Invoke(ctx, agentID, moduleID, clientapp.InvokeRequest{
		Method: requestMethod,
		Path:   requestPath,
		Header: requestHeaders,
		Body:   body,
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if *include {
		printConsoleResponseHead(resp)
	}
	if err := writeConsoleCallBody(resp); err != nil {
		return fmt.Errorf("read response body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("HTTP %s: session rejected; run login", resp.Status)
		}
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	return nil
}

// consoleModuleTarget splits the optional agent ID and the module ID.
func consoleModuleTarget(pos []string) (agentID, moduleID string, err error) {
	switch len(pos) {
	case 1:
		return "", pos[0], nil
	case 2:
		return pos[0], pos[1], nil
	default:
		return "", "", errors.New("expected [<agent-id>] <module-id>")
	}
}

// resolveConsoleAgent returns the explicit agent ID, or the only connected
// agent. The inferred ID is printed to stderr so a script never guesses which
// agent it reached.
func resolveConsoleAgent(ctx context.Context, client *clientapp.ConsoleClient, explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	agents, err := client.ListAgents(ctx)
	if err != nil {
		return "", err
	}
	switch len(agents) {
	case 0:
		return "", errors.New("no connected agents; connect an agent to the console first")
	case 1:
		log.Printf("using agent %q (only connected agent)", agents[0].AgentID)
		return agents[0].AgentID, nil
	default:
		ids := make([]string, 0, len(agents))
		for _, agent := range agents {
			ids = append(ids, agent.AgentID)
		}
		return "", fmt.Errorf("%d agents are connected; pass <agent-id> explicitly: %s", len(agents), strings.Join(ids, ", "))
	}
}

// matchConsoleOperation resolves one operation ID to its method and path.
func matchConsoleOperation(operations []clientapp.OperationRef, operationID string) (clientapp.OperationRef, error) {
	var matches []clientapp.OperationRef
	var available []string
	for _, op := range operations {
		if op.OperationID != "" {
			available = append(available, op.OperationID)
		}
		if op.OperationID == operationID {
			matches = append(matches, op)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		if len(available) == 0 {
			return clientapp.OperationRef{}, fmt.Errorf("no operation has ID %q; the module declares no operation IDs", operationID)
		}
		return clientapp.OperationRef{}, fmt.Errorf("no operation has ID %q; available: %s", operationID, strings.Join(available, ", "))
	default:
		return clientapp.OperationRef{}, fmt.Errorf("operation ID %q is ambiguous; use --method and --path", operationID)
	}
}

// consoleHeaderFlags collects repeated header flags.
type consoleHeaderFlags []string

// String implements flag.Value.
func (h *consoleHeaderFlags) String() string {
	return strings.Join(*h, ", ")
}

// Set implements flag.Value.
func (h *consoleHeaderFlags) Set(value string) error {
	*h = append(*h, value)
	return nil
}

// buildConsoleHeaders parses "Name: value" lines into a header map.
func buildConsoleHeaders(lines []string) (http.Header, error) {
	header := make(http.Header, len(lines))
	for _, line := range lines {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, errors.New(`a header must be in "Name: value" form`)
		}
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, errors.New("a header name is empty")
		}
		header.Add(name, strings.TrimSpace(value))
	}
	return header, nil
}

// readConsoleBody returns an inline body, a file body, or a stdin body.
func readConsoleBody(value string) ([]byte, error) {
	if value == "" {
		return nil, nil
	}
	if !strings.HasPrefix(value, "@") {
		return []byte(value), nil
	}
	source := strings.TrimPrefix(value, "@")
	if source == "-" {
		return io.ReadAll(os.Stdin)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return nil, fmt.Errorf("read body file: %w", err)
	}
	return data, nil
}

// printConsoleResponseHead prints the status line and headers for -i.
func printConsoleResponseHead(resp *http.Response) {
	fmt.Fprintf(os.Stdout, "%s %s\n", resp.Proto, resp.Status)
	keys := make([]string, 0, len(resp.Header))
	for key := range resp.Header {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		for _, value := range resp.Header[key] {
			fmt.Fprintf(os.Stdout, "%s: %s\n", key, value)
		}
	}
	fmt.Println()
}

// writeConsoleCallBody streams a response body. A JSON body under the cap is
// indented on a terminal; every other body streams raw so pipes stay exact.
func writeConsoleCallBody(resp *http.Response) error {
	if stdoutIsTerminal() && strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "json") {
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxPrettyJSON+1))
		if readErr != nil {
			if len(data) > 0 {
				_, _ = os.Stdout.Write(data)
			}
			return readErr
		}
		if len(data) <= maxPrettyJSON && json.Valid(data) {
			return writeIndentedJSON(data)
		}
		if len(data) > 0 {
			if _, err := os.Stdout.Write(data); err != nil {
				return err
			}
		}
		_, err := io.Copy(os.Stdout, resp.Body)
		return err
	}
	_, err := io.Copy(os.Stdout, resp.Body)
	return err
}

// writeIndentedJSON prints JSON with two-space indentation.
func writeIndentedJSON(raw []byte) error {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		if _, err := os.Stdout.Write(raw); err != nil {
			return err
		}
		if len(raw) > 0 && raw[len(raw)-1] != '\n' {
			_, err = os.Stdout.Write([]byte("\n"))
		}
		return err
	}
	buf.WriteByte('\n')
	_, err := os.Stdout.Write(buf.Bytes())
	return err
}

// printJSON prints any value as indented JSON.
func printJSON(value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = os.Stdout.Write(data)
	return err
}

// stdoutIsTerminal reports whether stdout is a character device.
func stdoutIsTerminal() bool {
	info, err := os.Stdout.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// consoleFlagSet builds a flag set that reports errors without exiting.
func consoleFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

// parseConsoleFlags parses flags and reports a help request as a clean stop.
func parseConsoleFlags(fs *flag.FlagSet, flags []string) (help bool, err error) {
	if err := fs.Parse(flags); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

// splitConsoleArgs separates flags from positionals so flags may follow the
// positional arguments. valueFlags names the flags that consume the next
// argument; every other flag is treated as a boolean flag.
func splitConsoleArgs(args []string, valueFlags map[string]bool) (flags, positionals []string) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if len(arg) > 1 && arg[0] == '-' {
			flags = append(flags, arg)
			name := strings.TrimLeft(arg, "-")
			if strings.Contains(name, "=") {
				continue
			}
			if valueFlags[name] && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		positionals = append(positionals, arg)
	}
	return flags, positionals
}

// consoleServerHelp documents the shared server flag.
const consoleServerHelp = "console base URL (default: saved login session)"

// dashIfEmpty prints a dash for an empty column.
func dashIfEmpty(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

// yesIf prints "yes" or a dash for a boolean column.
func yesIf(value bool) string {
	if value {
		return "yes"
	}
	return "-"
}
