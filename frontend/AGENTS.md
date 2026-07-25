# Frontend

React admin console SPA built with Vite + TypeScript. Embedded into the Go binary via `go:embed` for the `litespaserver`.

## Structure

```
frontend/
├── console/
│   ├── dist/           # Built assets (committed for go:embed)
│   ├── src/
│   │   ├── App.tsx     # Main application component
│   │   └── main.tsx    # Entry point
│   ├── index.html
│   ├── package.json
│   └── vite.config.ts
└── frontend.go         # go:embed ConsoleWebRootFs
```

## Build

```bash
cd frontend/console && npm install && npm run build
```

Output goes to `frontend/console/dist/`, which is embedded by `frontend.go`.

## Integration

The SPA is served by `consoleserver.NewConsoleHandler` via `litespaserver.ServeRoot` at the console listen address (`:8081`). API routes under `/api/v1/` are handled by `consoleapi`.

## Rules
* Always rebuild `dist/` before committing Go changes that touch the frontend.
* Static asset paths (`/assets/**`) are allow-listed by `litespaserver.Config.StaticPaths`.
