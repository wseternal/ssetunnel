# Frontend

React admin console SPA built with Vite + TypeScript + MUI v9 + orca-ui. Mercury Console light theme design system. Embedded into the Go binary via `go:embed` for the `litespaserver`.

## Structure

```
frontend/
├── console/
│   ├── dist/           # Built assets (committed for go:embed)
│   ├── src/
│   │   ├── App.tsx          # Main application component
│   │   ├── main.tsx         # Entry point
│   │   └── theme/
│   │       └── theme.ts     # Mercury Console design tokens
│   ├── index.html
│   ├── package.json
│   └── vite.config.ts
└── frontend.go         # go:embed ConsoleWebRootFs
```

## Dependencies

- **MUI v9** (`@mui/material`, `@mui/icons-material`): Material UI components
- **orca-ui** (`@doublefin/orca-ui`): Mercury Console UI kit (AdminTable, PageHeader, StatusPill, etc.)
- **react-router**: Routing (peer dependency)

## Build

```bash
cd frontend/console && npm install && npx vite build
```

Output goes to `frontend/console/dist/`, which is embedded by `frontend.go`.

## Tabs

The console has four tabs:
- **Sessions**: Live tunnel sessions with stats
- **Tokens**: Bearer token management
- **Agents**: Agent config management (allowed targets)
- **Users**: User management with permissions

## Design System

Follows the Mercury Console light theme:
- Canvas background: `#f8fafc`
- Paper surfaces: `#ffffff`
- Ink text: `#0f172a`
- Indigo accent: `#4f46e5`
- Hairline borders: `#e2e8f0`

Uses `AdminTable` for data tables, `StatusPill` for status badges, `PageHeader` for section headers.

## Session Persistence

Login state is persisted to `localStorage` and validated via `/console/api/v1/me` on page refresh.

## Integration

The SPA is served by `consoleserver.NewConsoleHandler` via `litespaserver.ServeRoot` at `/console/` on the console listen address (`:8081`). API routes under `/console/api/v1/` are handled by `consoleapi`.

## Rules
* Always rebuild `dist/` before committing Go changes that touch the frontend.
* Static asset paths (`/assets/**`) are allow-listed by `litespaserver.Config.StaticPaths`.
* Use theme tokens instead of raw hex colors.
* Use orca-ui kit components (AdminTable, PageHeader, StatusPill) for consistency.
