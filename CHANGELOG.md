# Changelog

## 1.0.0 (2026-07-28)


### Features

* add --base path prefix and console CSP config ([#4](https://github.com/wseternal/ssetunnel/issues/4)) ([6b1a2f8](https://github.com/wseternal/ssetunnel/commit/6b1a2f8d6292c63170628708800a5aa9574fd46d))
* agent routing and dynamic target support ([7673375](https://github.com/wseternal/ssetunnel/commit/7673375f07970c3f40194d1a503dad575ebb66c6))
* allow non-admin users to view their own sessions and agents ([96ff026](https://github.com/wseternal/ssetunnel/commit/96ff026561a0116fe85cc946b665ae02a74db908))
* **auth:** add user-centric authentication (Phases 1-4) ([b6666c9](https://github.com/wseternal/ssetunnel/commit/b6666c9e181e6248322b78ed4ad4638572d59143))
* **auth:** auto-seed admin user on first startup ([5a48ac3](https://github.com/wseternal/ssetunnel/commit/5a48ac343b4e63a49a7f48e062843e0ad89e1975))
* **auth:** implement per-user TOTP with recovery codes ([0f1133b](https://github.com/wseternal/ssetunnel/commit/0f1133b17949f60ad0b92a53051c150f1bd45ff7))
* **auth:** per-user permission flags, remove agent role, fix agent auth ([03eed97](https://github.com/wseternal/ssetunnel/commit/03eed9790068713775b15f83458bb489535dde7f))
* **build:** embed git short SHA in binary via ldflags ([3a97269](https://github.com/wseternal/ssetunnel/commit/3a972690c833fb03f42dcd352c68dcd0b8d7b3f6))
* **console:** add Users tab and username/password login (Phase 2.5) ([7574634](https://github.com/wseternal/ssetunnel/commit/7574634516400f5a8cec6cb7428a5c1ae521e214))
* replace TCP entry listener with HTTP connect endpoints ([#1](https://github.com/wseternal/ssetunnel/issues/1)) ([78b0375](https://github.com/wseternal/ssetunnel/commit/78b0375474eb97e6509aa4d2e28c1975c04a0aa8))
* **server:** PIN redemption flow + per-session yamux ([f22c273](https://github.com/wseternal/ssetunnel/commit/f22c27311694d9fb54324587a6ce799ee62b544f))
* ssetunnel Cycle 3 ([d563831](https://github.com/wseternal/ssetunnel/commit/d563831bd260ea00c1b75a3ef5fc228da6f59c79))
* transport core for SSE reverse TCP tunnel (cycle 1) ([8b004a0](https://github.com/wseternal/ssetunnel/commit/8b004a03f81003cfe2ae3c366123dd04502c1f96))
* **transport:** concurrent POSTs + reorder window + probe (cycle 2) ([0b09809](https://github.com/wseternal/ssetunnel/commit/0b0980952c6fda5d1770a5340e29962208d56d96))


### Bug Fixes

* add deprecated aliases for --entry and --server-entry flags ([5c96465](https://github.com/wseternal/ssetunnel/commit/5c96465e54ac59fc927bf83da2f8de68891cdfba))
* address pr-review findings for non-admin console view ([bc0cffe](https://github.com/wseternal/ssetunnel/commit/bc0cffe6e272ab0cf272f097d516468064ec72a2))
* address review findings from full codebase audit ([df120ad](https://github.com/wseternal/ssetunnel/commit/df120adec54d43aa45b0dcd4ea016536139d5518))
* **agent,connect:** use exponential backoff for reconnection retries ([634aa05](https://github.com/wseternal/ssetunnel/commit/634aa0556539fd975e4a50fca421e44096571879))
* **agent:** default --server to http://127.0.0.1:8080 ([9ae51d9](https://github.com/wseternal/ssetunnel/commit/9ae51d93ba3691044edbfb6f6e5fd47fbe354d94))
* **agent:** exit immediately on 401 Unauthorized instead of retrying ([c165f0e](https://github.com/wseternal/ssetunnel/commit/c165f0e770aa32b023bd3d747c4d369e00667c3c))
* **auth,cli:** address second-round review findings ([ac5ce38](https://github.com/wseternal/ssetunnel/commit/ac5ce38309425e6f55b9f98432afc815923c5c71))
* **auth,consoleapi:** address PR review findings for per-user TOTP ([bbf7ff4](https://github.com/wseternal/ssetunnel/commit/bbf7ff4d9b1784c47bc31733720a009535247701))
* **auth:** Phase 0 security hardening ([e22c768](https://github.com/wseternal/ssetunnel/commit/e22c7681467c9bf1fdf7224b4a41092e63efdc87))
* **auth:** use native pgx array scanning instead of pq.Array for agent configs ([b8970fd](https://github.com/wseternal/ssetunnel/commit/b8970fdf3b8cd2021a1b921bd41cdad63c8aa3be))
* bundle console into single index.html with vite-plugin-singlefile ([#5](https://github.com/wseternal/ssetunnel/issues/5)) ([efc3736](https://github.com/wseternal/ssetunnel/commit/efc37367cb0e780d459b4e28b2a82ee56fa23dba))
* **connect:** prevent ServeRW deadlock when server closes before stdin ([15c8cd8](https://github.com/wseternal/ssetunnel/commit/15c8cd80ddd825ddcaede337faaecab276e355e8))
* **connect:** validate token at startup and log handshake failures ([e069091](https://github.com/wseternal/ssetunnel/commit/e069091fac56a6e7ec8737cafe352e90d9914005))
* **connect:** wait for both copy directions in ServeStdio and add half-close ([d01228b](https://github.com/wseternal/ssetunnel/commit/d01228bcf2e4058e286722800e3653fea25c9fb8))
* **console:** persist session token to localStorage across page refreshes ([c1b6601](https://github.com/wseternal/ssetunnel/commit/c1b66016870f9351cf4de1982ac204788d8165f4))
* disable CSP header for embedded console frontend ([6dfa7b0](https://github.com/wseternal/ssetunnel/commit/6dfa7b0c0c942a57543eabe1b366451d719fe92c))
* **docs:** correct README quick start, add version cmd, fix Go prerequisite ([2d26e03](https://github.com/wseternal/ssetunnel/commit/2d26e0395e1a259daec2693166d63da0c265b512))
* **frontend:** gate admin APIs behind role check for non-admin users ([c82736e](https://github.com/wseternal/ssetunnel/commit/c82736e1be021a2ccdd940f88c2d10bfd9c87db7))
* **frontend:** replace TOTP banner with Shield icon tooltip indicator ([c5a7674](https://github.com/wseternal/ssetunnel/commit/c5a76741916b46c7390d587adfe794c3c473b660))
* harden session lifecycle and agent config guards ([011689c](https://github.com/wseternal/ssetunnel/commit/011689cda053e5126d3de5e0fdfe16e83f0d4bea))
* improve SSH ProxyCommand error messages with friendly output ([d9013cc](https://github.com/wseternal/ssetunnel/commit/d9013ccee28779044d41dd0419cb1947f607a255))
* **local:** remove debug echo in local.sh ([88e10c6](https://github.com/wseternal/ssetunnel/commit/88e10c636fe5ea12c8fa0eba510aabe34332b017))
* prefix all console endpoints with /console/ ([#6](https://github.com/wseternal/ssetunnel/issues/6)) ([eafa256](https://github.com/wseternal/ssetunnel/commit/eafa256716bfb49385089f41fdb4e222984b51ff))
* resolve empty server URL from deprecated flag zero-default overwrite ([#3](https://github.com/wseternal/ssetunnel/issues/3)) ([33c41d4](https://github.com/wseternal/ssetunnel/commit/33c41d49bbe3c4abe04bb1879c7d98be6b425243))
* **server:** fail eagerly when listen, entry, or console-listen address is already bound ([3362312](https://github.com/wseternal/ssetunnel/commit/3362312da82d0594e29ef71c8dbe7bb34496569d))
* **server:** set HTTP IdleTimeout to prevent 60s agent disconnections ([001c66f](https://github.com/wseternal/ssetunnel/commit/001c66f3713fd2443a3c6a66503f22db1a1c72ae))
* **server:** track bytes_sent/bytes_received in session stats ([ccd09fc](https://github.com/wseternal/ssetunnel/commit/ccd09fceff6e73f324bab9354d3ace8259938e8f))
* **test:** cancel agent context before server teardown in e2e TestMain ([51d922d](https://github.com/wseternal/ssetunnel/commit/51d922d19322a0669c6cd812350c3514543c7f6e))
* update login command and server log for /console/ prefix ([#7](https://github.com/wseternal/ssetunnel/issues/7)) ([65aea7d](https://github.com/wseternal/ssetunnel/commit/65aea7dbe68f0b6e51916107a646df95c11e67bb))
