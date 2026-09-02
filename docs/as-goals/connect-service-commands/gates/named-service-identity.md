# Gate: Named Service Identity

## Condition
Each named connect instance gets a unique service name (`ssetunnel-connect-<name>`), PID file, and args file. Multiple named services can coexist without conflict. `buildRunFn` correctly routes to `runConnect`. `buildServiceArgs` produces correct arguments for connect services.

## Evidence Required
- [ ] Service name includes `--name` value: `ssetunnel-connect-<name>`
- [ ] PID file: `~/.ssetunnel/ssetunnel-connect-<name>.pid`
- [ ] Args file: `~/.ssetunnel/ssetunnel-connect-<name>.args`
- [ ] `buildRunFn` handles `"connect"` subcommand

## Verification Method
Code review + unit tests for service name generation and args persistence

## Owner
Engineer
