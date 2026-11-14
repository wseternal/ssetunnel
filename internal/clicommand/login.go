package clicommand

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/wseternal/ssetunnel/internal/clientapp"
)

// RunLogin authenticates a user and stores a session for agent/connect.
func RunLogin(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	serverURL := fs.String("server", "http://127.0.0.1:8081", "tunnel server URL (console port)")
	tunnelServer := fs.String("tunnel-server", "", "tunnel server URL to save in session (default: --server with port 8081→8080)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Trim trailing slash to avoid double-slash in API paths.
	*serverURL = strings.TrimRight(*serverURL, "/")

	reader := bufio.NewReader(os.Stdin)

	fmt.Print("Username: ")
	username, _ := reader.ReadString('\n')
	username = strings.TrimSpace(username)
	if username == "" {
		return errors.New("username is required")
	}

	fmt.Print("Password: ")
	password, _ := reader.ReadString('\n')
	password = strings.TrimSpace(password)
	if password == "" {
		return errors.New("password is required")
	}

	// Check if TOTP is required for this user.
	var totpCode string
	totpRequired, _ := clientapp.CheckTOTPRequired(*serverURL, username)
	if totpRequired {
		fmt.Print("TOTP or Recovery Code: ")
		totpCode, _ = reader.ReadString('\n')
		totpCode = strings.TrimSpace(totpCode)
	} else {
		// Non-200 status or unavailable endpoint: still allow login attempt.
		fmt.Print("TOTP or Recovery Code (press Enter to skip): ")
		totpCode, _ = reader.ReadString('\n')
		totpCode = strings.TrimSpace(totpCode)
	}

	result, err := clientapp.Login(clientapp.LoginParams{
		ConsoleURL: *serverURL,
		TunnelURL:  *tunnelServer,
		Username:   username,
		Password:   password,
		TOTPCode:   totpCode,
	})
	if err != nil {
		return err
	}

	// Warn when the tunnel URL could not be derived from the console URL.
	if *tunnelServer == "" {
		if _, derived := clientapp.DeriveTunnelURL(*serverURL); !derived {
			fmt.Printf("Warning: the tunnel URL could not be derived from %s, so the session stores it as %s.\n", *serverURL, result.TunnelURL)
			fmt.Printf("If the tunnel sits behind a prefix or a different port, re-run with --tunnel-server (agents will otherwise dial %s/events).\n", result.TunnelURL)
		}
	}

	fmt.Printf("Login successful! Session saved for %s (role: %s) at %s\n", result.Username, result.Role, result.TunnelURL)
	fmt.Println("You can now run 'agent' or 'connect' without --server.")
	return nil
}
