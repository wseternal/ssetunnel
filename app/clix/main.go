package main

import (
	"embed"
	"fmt"
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wseternal/ssetunnel/frontend"
)

//go:embed build/appicon.png
var appIcon []byte

// ClientService exposes the clientapp workflows to the Wails frontend
// through generated bindings. It wraps the shared login, agent, and
// connect operations with narrow methods suitable for desktop use.
type ClientService struct{}

func NewClientService() *ClientService {
	return &ClientService{}
}

// Login authenticates and saves the session.
func (s *ClientService) Login(server, tunnelServer, username, password, totpCode string) (map[string]string, error) {
	return nil, fmt.Errorf("not implemented") // TODO: integrate with clientapp.Login
}

// CheckTOTPRequired checks whether TOTP is required for the user.
func (s *ClientService) CheckTOTPRequired(server, username string) (bool, error) {
	return false, fmt.Errorf("not implemented") // TODO: integrate with clientapp.CheckTOTPRequired
}

// GetSession returns the current saved session metadata.
func (s *ClientService) GetSession() (map[string]string, error) {
	return nil, fmt.Errorf("not implemented") // TODO: integrate with clientapp.ResolveSession
}

// GetAgentStatus returns the current agent status.
func (s *ClientService) GetAgentStatus() (map[string]any, error) {
	return map[string]any{"running": false}, nil
}

// StartAgent starts the agent with the given configuration.
func (s *ClientService) StartAgent(cfg map[string]any) error {
	return fmt.Errorf("not implemented") // TODO: integrate with clientapp.RunAgent
}

// StopAgent stops the running agent.
func (s *ClientService) StopAgent() error {
	return fmt.Errorf("not implemented") // TODO: integrate with agent runtime
}

// GetConnectInstances returns all named connect instances.
func (s *ClientService) GetConnectInstances() ([]map[string]any, error) {
	return nil, fmt.Errorf("not implemented") // TODO: integrate with named connect management
}

// StartConnect starts a named connect instance.
func (s *ClientService) StartConnect(name string, cfg map[string]any) (map[string]any, error) {
	return nil, fmt.Errorf("not implemented") // TODO: integrate with clientapp.StartNamedConnect
}

// StopConnect stops a named connect instance.
func (s *ClientService) StopConnect(name string) error {
	return fmt.Errorf("not implemented") // TODO: integrate with named connect management
}

func main() {
	clientFS := frontend.ClientWebRootFs

	clientSvc := NewClientService()

	app := application.New(application.Options{
		Name:        "Clix",
		Description: "ssetunnel desktop client",
		Icon:        appIcon,
		Services: []application.Service{
			application.NewService(clientSvc),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(clientFS),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  "Clix",
		Width:  1024,
		Height: 768,
	})

	log.Printf("clix: starting desktop app")

	app.OnShutdown(func() {
		log.Printf("clix: shutting down")
		// TODO: close owned agent and connect runtimes.
	})

	if err := app.Run(); err != nil {
		log.Fatalf("clix: fatal: %v", err)
	}
}
