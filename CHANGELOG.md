# Changelog

## [1.9.0](https://github.com/wseternal/ssetunnel/compare/v1.8.0...v1.9.0) (2026-09-02)


### Features

* add service commands (start/stop/restart/status/uninstall) for connect sub-command ([#59](https://github.com/wseternal/ssetunnel/issues/59)) ([46e1c59](https://github.com/wseternal/ssetunnel/commit/46e1c596ddabc240dac78eadb4429d97fcd3c351))
* **frontend:** add duration selector and manual refresh to metrics page ([#60](https://github.com/wseternal/ssetunnel/issues/60)) ([97f29f0](https://github.com/wseternal/ssetunnel/commit/97f29f002183d9da7e16d56dff83f093000de529))


### Bug Fixes

* add solarized light theme support for cloud shell terminal ([#56](https://github.com/wseternal/ssetunnel/issues/56)) ([ed360b4](https://github.com/wseternal/ssetunnel/commit/ed360b484df43c9c888236a8a46ea0df99b5f43c))
* remove black border in solarized light theme, default to solarized light ([#58](https://github.com/wseternal/ssetunnel/issues/58)) ([e124e1d](https://github.com/wseternal/ssetunnel/commit/e124e1d3d27ecbb6f14f052b6f659f69699fdc24))

## [1.8.0](https://github.com/wseternal/ssetunnel/compare/v1.7.1...v1.8.0) (2026-08-31)


### Features

* **auth:** add transparent session token refresh mechanism ([#55](https://github.com/wseternal/ssetunnel/issues/55)) ([a64433f](https://github.com/wseternal/ssetunnel/commit/a64433fecb175619ba85c3cfa0eff3b6770f9c0e))


### Bug Fixes

* stable sort order by agent_id for console API responses ([#53](https://github.com/wseternal/ssetunnel/issues/53)) ([eeb675e](https://github.com/wseternal/ssetunnel/commit/eeb675eb1e832856100d28edcd953f6a553e790d))

## [1.7.1](https://github.com/wseternal/ssetunnel/compare/v1.7.0...v1.7.1) (2026-08-13)


### Bug Fixes

* **console:** improve cloud shell UX - non-focusable buttons, fullscreen, auto-disconnect on exit ([#50](https://github.com/wseternal/ssetunnel/issues/50)) ([5ab2d0e](https://github.com/wseternal/ssetunnel/commit/5ab2d0e1929d7a3f6448af38b9c89f4afbc9491f))
* task to sign the app bundle ([#52](https://github.com/wseternal/ssetunnel/issues/52)) ([b6af907](https://github.com/wseternal/ssetunnel/commit/b6af9072502c56d9a7b1e5a4901795f78b6636ca))

## [1.7.0](https://github.com/wseternal/ssetunnel/compare/v1.6.3...v1.7.0) (2026-08-12)


### Features

* **server:** add persistent cloud shell sessions with reattach support ([#47](https://github.com/wseternal/ssetunnel/issues/47)) ([d21acd5](https://github.com/wseternal/ssetunnel/commit/d21acd572c7630502b23efd68e4e8702cca5cf7f))


### Bug Fixes

* **remoteapp:** improve screenshot text clarity at default zoom ([#49](https://github.com/wseternal/ssetunnel/issues/49)) ([584f352](https://github.com/wseternal/ssetunnel/commit/584f3526c005f71670573d557693d32a17a6ead4))
* **server,console:** fix shell tab switch blank page and scrollback restore ([8d2a1bf](https://github.com/wseternal/ssetunnel/commit/8d2a1bfa08e6199373c29bc0603b07be11fafa97))
* **server:** move lastActivity update inside mutex to prevent data race ([dca9c1c](https://github.com/wseternal/ssetunnel/commit/dca9c1c4f8a9956b825cf839c20fe148b74398dc))

## [1.6.3](https://github.com/wseternal/ssetunnel/compare/v1.6.2...v1.6.3) (2026-08-10)


### Bug Fixes

* **console:** minor fixes to session id and remote desktop screenshot cache ([#45](https://github.com/wseternal/ssetunnel/issues/45)) ([683c985](https://github.com/wseternal/ssetunnel/commit/683c985aea397f2a8abece17e49a748bfae0cd45))

## [1.6.2](https://github.com/wseternal/ssetunnel/compare/v1.6.1...v1.6.2) (2026-08-08)


### Bug Fixes

* **agent:** invoke cloud shell as login shell to source .zprofile/.profile ([e16b792](https://github.com/wseternal/ssetunnel/commit/e16b7924f09e7c7b3bc94f0cbb735b2e9c1e36f0))

## [1.6.1](https://github.com/wseternal/ssetunnel/compare/v1.6.0...v1.6.1) (2026-08-07)


### Bug Fixes

* add code signing tasks to Taskfile.yml ([f1fdb23](https://github.com/wseternal/ssetunnel/commit/f1fdb23665129ee649c61315415e3d5e4e6183e8))
* address multi-agent-review findings for remoteapp UX ([dbc1098](https://github.com/wseternal/ssetunnel/commit/dbc109881c0629dd77e0e1ce3e8ca03a1e94d639))
* magnifier and handle display-off gracefully ([#42](https://github.com/wseternal/ssetunnel/issues/42)) ([3800793](https://github.com/wseternal/ssetunnel/commit/38007939fd884c1b5a5aa28e664c2a65572b314f))
* remoteapp fullscreen button event propagation + tooltip overlap ([3565f07](https://github.com/wseternal/ssetunnel/commit/3565f07cef326aa3e0c44e20b71919a7943b5d41))
* remoteapp UX — fullscreen toggle + mouse coord accuracy ([3565f07](https://github.com/wseternal/ssetunnel/commit/3565f07cef326aa3e0c44e20b71919a7943b5d41))
* **remoteapp:** exclude robotgo-dependent files from purego builds ([#43](https://github.com/wseternal/ssetunnel/issues/43)) ([f0c0c25](https://github.com/wseternal/ssetunnel/commit/f0c0c251d1054cec6c7ce186aef089442024c0a1))

## [1.6.0](https://github.com/wseternal/ssetunnel/compare/v1.5.0...v1.6.0) (2026-08-06)


### Features

* **remoteapp:** input ack + deferred capture for live UX feedback ([#39](https://github.com/wseternal/ssetunnel/issues/39)) ([61947ff](https://github.com/wseternal/ssetunnel/commit/61947ff53e8253b1cddcab4c29a710a62248e907))

## [1.5.0](https://github.com/wseternal/ssetunnel/compare/v1.4.0...v1.5.0) (2026-08-06)


### Features

* **remoteapp:** smart screenshot capture with signal-driven model ([#37](https://github.com/wseternal/ssetunnel/issues/37)) ([a9ea99b](https://github.com/wseternal/ssetunnel/commit/a9ea99ba3ff6cc94d94a627a9d7a8ed2af99ee23))

## [1.4.0](https://github.com/wseternal/ssetunnel/compare/v1.3.1...v1.4.0) (2026-08-06)


### Features

* **remoteapp:** add real-time activity log for remote desktop sessions ([#35](https://github.com/wseternal/ssetunnel/issues/35)) ([fbe00c1](https://github.com/wseternal/ssetunnel/commit/fbe00c17c0e6dcec2a5ac01999c0fba3855536ce))

## [1.3.1](https://github.com/wseternal/ssetunnel/compare/v1.3.0...v1.3.1) (2026-08-05)


### Bug Fixes

* **metrics:** pre-create metrics directory before opening BadgerDB ([#33](https://github.com/wseternal/ssetunnel/issues/33)) ([5433c85](https://github.com/wseternal/ssetunnel/commit/5433c854efc83cdc87e2905e8562303dde22cf38))

## [1.3.0](https://github.com/wseternal/ssetunnel/compare/v1.2.0...v1.3.0) (2026-08-05)


### Features

* add uninstall service action for server and agent ([#31](https://github.com/wseternal/ssetunnel/issues/31)) ([07bfc86](https://github.com/wseternal/ssetunnel/commit/07bfc86576e61afff9a35fa2a8018fd0a6eaac0b))

## [1.2.0](https://github.com/wseternal/ssetunnel/compare/v1.1.1...v1.2.0) (2026-08-05)


### Features

* **console:** display network diagnostics on remote desktop panel ([#30](https://github.com/wseternal/ssetunnel/issues/30)) ([6b82e87](https://github.com/wseternal/ssetunnel/commit/6b82e8787472c8b9376cbad62bb2ddfd44b70cd3))


### Bug Fixes

* **service:** persist runtime flags so bare start reuses previous args ([#28](https://github.com/wseternal/ssetunnel/issues/28)) ([3dcc3e0](https://github.com/wseternal/ssetunnel/commit/3dcc3e041ae6865befc30e0b64c4b549027f01df))

## [1.1.1](https://github.com/wseternal/ssetunnel/compare/v1.1.0...v1.1.1) (2026-08-04)


### Bug Fixes

* exclude release-please branches from CI ([e9bf0a2](https://github.com/wseternal/ssetunnel/commit/e9bf0a22b55a57d34237e4b55112433731f1aebb))
* repair shell/desktop reconnection after tab switch ([04ba2dd](https://github.com/wseternal/ssetunnel/commit/04ba2dda67b6749a565bf6ba576fa7d246ba78b1))
* simplify release tasks to single go:release ([1e91b80](https://github.com/wseternal/ssetunnel/commit/1e91b805760251596726d6699bbdf88216da0d9e))

## [1.1.0](https://github.com/wseternal/ssetunnel/compare/v1.0.5...v1.1.0) (2026-08-04)


### Features

* add remote desktop control via robotgo ([#25](https://github.com/wseternal/ssetunnel/issues/25)) ([806d2fc](https://github.com/wseternal/ssetunnel/commit/806d2fcbe8310979339b1674f614af38ed092376))


### Bug Fixes

* switch release builds to native CGo builds ([4394e86](https://github.com/wseternal/ssetunnel/commit/4394e86c926670dddda2a5fe7906c57ffd8ef6d6))

## [1.0.5](https://github.com/wseternal/ssetunnel/compare/v1.0.4...v1.0.5) (2026-08-02)


### Bug Fixes

* **ci:** use shell bash and version.txt for Windows release builds ([2e5b7fd](https://github.com/wseternal/ssetunnel/commit/2e5b7fdd89ee02da1c1cf2736fb338de3385c139))

## [1.0.4](https://github.com/wseternal/ssetunnel/compare/v1.0.3...v1.0.4) (2026-08-02)


### Bug Fixes

* **ci:** align workflows with kimi-code PR [#12](https://github.com/wseternal/ssetunnel/issues/12) fixes ([2d1c650](https://github.com/wseternal/ssetunnel/commit/2d1c650917208dbd0ac246f2ec4ad6093747a1cf))
* cloud shell keyboard input (Delete, Ctrl+A/E/U) and PTY resize ([#23](https://github.com/wseternal/ssetunnel/issues/23)) ([fce5487](https://github.com/wseternal/ssetunnel/commit/fce54873a4aeb9e0c98800ac7f07657c8f4bc893))

## [1.0.3](https://github.com/wseternal/ssetunnel/compare/v1.0.2...v1.0.3) (2026-07-30)


### Bug Fixes

* **ci:** authenticate setup-task to resolve API rate limit ([c52fc69](https://github.com/wseternal/ssetunnel/commit/c52fc69aa12a2054c3633b8b2a1d0f71dd2d162e))

## [1.0.2](https://github.com/wseternal/ssetunnel/compare/v1.0.1...v1.0.2) (2026-07-30)


### Bug Fixes

* use release_pat for release-please ([938ad4e](https://github.com/wseternal/ssetunnel/commit/938ad4ee7e37628c6666b4d082884357f90ea4c2))

## [1.0.1](https://github.com/wseternal/ssetunnel/compare/v1.0.0...v1.0.1) (2026-07-30)


### Bug Fixes

* **console:** register shell connect routes before PathPrefix catch-all ([e2071cf](https://github.com/wseternal/ssetunnel/commit/e2071cfd0578177129cadf7467d18bc6fa631410))

## 1.0.0 (2026-07-30)


### Features

* add --base path prefix and console CSP config ([#4](https://github.com/wseternal/ssetunnel/issues/4)) ([9593d15](https://github.com/wseternal/ssetunnel/commit/9593d1544aa29b07996c5e5b8ed07cad5f45a623))
* add auto-tuning metrics and statistics system ([#15](https://github.com/wseternal/ssetunnel/issues/15)) ([5c432fe](https://github.com/wseternal/ssetunnel/commit/5c432fe7a260ab6927651a1a1ad2d461e2d4c5df))
* add OS service manager for server and agent subcommands ([#11](https://github.com/wseternal/ssetunnel/issues/11)) ([cecd87b](https://github.com/wseternal/ssetunnel/commit/cecd87b5e3c1a0e700798569eb3bed88dac704d3))
* agent routing and dynamic target support ([5de39bf](https://github.com/wseternal/ssetunnel/commit/5de39bf9902e8f078ce89181bd39feb051305328))
* allow non-admin users to view their own sessions and agents ([bc41bbf](https://github.com/wseternal/ssetunnel/commit/bc41bbf18c4ba99ead00f784930e79e4c9e7ea28))
* **auth:** add user-centric authentication (Phases 1-4) ([448a53f](https://github.com/wseternal/ssetunnel/commit/448a53f86aacd70f3429c63c8c1defe81ef3ee8b))
* **auth:** auto-seed admin user on first startup ([19bd1fd](https://github.com/wseternal/ssetunnel/commit/19bd1fda15fdd41eb60df3c8769b59bf53e8c1f9))
* **auth:** implement per-user TOTP with recovery codes ([ba4df1c](https://github.com/wseternal/ssetunnel/commit/ba4df1cb744352753ad9e6abfdfed8d98639e138))
* **auth:** per-user permission flags, remove agent role, fix agent auth ([712ad2a](https://github.com/wseternal/ssetunnel/commit/712ad2ad6a2363c5c6d49f535658b9612deb2f64))
* **build:** embed git short SHA in binary via ldflags ([3f170ae](https://github.com/wseternal/ssetunnel/commit/3f170aed88ced028bf6a21dc5d2a800718cab8d0))
* **cloud-shell:** add browser-based PTY shell via console ([#16](https://github.com/wseternal/ssetunnel/issues/16)) ([41fa5e5](https://github.com/wseternal/ssetunnel/commit/41fa5e5903326b594926e572c118803a3d917db7))
* **console:** add Users tab and username/password login (Phase 2.5) ([ccfa605](https://github.com/wseternal/ssetunnel/commit/ccfa60572224db276eeb612574cb1cb6cb432f40))
* optimize upstream throughput for high-bandwidth workloads ([#10](https://github.com/wseternal/ssetunnel/issues/10)) ([1654866](https://github.com/wseternal/ssetunnel/commit/16548665bde330343f9c7f768225e787559a5aba))
* replace TCP entry listener with HTTP connect endpoints ([#1](https://github.com/wseternal/ssetunnel/issues/1)) ([6556f4e](https://github.com/wseternal/ssetunnel/commit/6556f4ea6881b14631d4eb583cccb5039e73dbdd))
* **server:** PIN redemption flow + per-session yamux ([c7e115a](https://github.com/wseternal/ssetunnel/commit/c7e115a04fb18c5dde4097f5e3e10f393beeea52))
* ssetunnel Cycle 3 ([a7a20bf](https://github.com/wseternal/ssetunnel/commit/a7a20bff920b3d9bcea167124ee81730a2ac19fd))
* transport core for SSE reverse TCP tunnel (cycle 1) ([13a2130](https://github.com/wseternal/ssetunnel/commit/13a2130ef34c3926332f5720f51c3fa16910f9d3))
* **transport:** concurrent POSTs + reorder window + probe (cycle 2) ([0047d64](https://github.com/wseternal/ssetunnel/commit/0047d64499cd338a250efb0ce58fd0e91993d726))
* user level service ([#14](https://github.com/wseternal/ssetunnel/issues/14)) ([77dbcc3](https://github.com/wseternal/ssetunnel/commit/77dbcc35f07b38d0b6a1f333315813500b5fed7f))


### Bug Fixes

* add deprecated aliases for --entry and --server-entry flags ([2cad1ee](https://github.com/wseternal/ssetunnel/commit/2cad1ee65a121341bbe38ac05a3770e74629fb1c))
* add release workflow with native cross-compilation ([#17](https://github.com/wseternal/ssetunnel/issues/17)) ([452aeff](https://github.com/wseternal/ssetunnel/commit/452aeff990835da43033291b6b107d3ab72676d9))
* address pr-review findings for non-admin console view ([ea04ff0](https://github.com/wseternal/ssetunnel/commit/ea04ff09bf3cfbd9aab232171ea756e56c6947b1))
* address review findings from full codebase audit ([e47a8dd](https://github.com/wseternal/ssetunnel/commit/e47a8ddd36cf6bf63a7be9f560395b80242e9482))
* **agent,connect:** use exponential backoff for reconnection retries ([d0d90d6](https://github.com/wseternal/ssetunnel/commit/d0d90d690676a99b12468285b48e83583dd9ade2))
* **agent:** default --server to http://127.0.0.1:8080 ([5e97a11](https://github.com/wseternal/ssetunnel/commit/5e97a1102f6ff90e8deefb4ba53602f879a7fb22))
* **agent:** exit immediately on 401 Unauthorized instead of retrying ([9275554](https://github.com/wseternal/ssetunnel/commit/92755547501fb1b1057e7653dc73326bbd642033))
* **auth,cli:** address second-round review findings ([73213b2](https://github.com/wseternal/ssetunnel/commit/73213b2f9022da3ed182b9a719a89b7d9256985a))
* **auth,consoleapi:** address PR review findings for per-user TOTP ([cb15db0](https://github.com/wseternal/ssetunnel/commit/cb15db0046f399fb0da4b0f53a9fdfa523f0ccac))
* **auth:** Phase 0 security hardening ([72b906a](https://github.com/wseternal/ssetunnel/commit/72b906a42c485048aa1902c6d5a1c87851a58f38))
* **auth:** use native pgx array scanning instead of pq.Array for agent configs ([3b0ae52](https://github.com/wseternal/ssetunnel/commit/3b0ae523ae010d72880c1ff48d49fe0c17e28bff))
* bundle console into single index.html with vite-plugin-singlefile ([#5](https://github.com/wseternal/ssetunnel/issues/5)) ([82a0058](https://github.com/wseternal/ssetunnel/commit/82a00580b4770e1416220aad8df7ded6a88f8d39))
* compute version from version.txt, git tags, or fallback 0.1.0 ([948e3fc](https://github.com/wseternal/ssetunnel/commit/948e3fce21c4e0a53ea9a239b5ab42c1af2c71cf))
* **connect:** prevent ServeRW deadlock when server closes before stdin ([99be399](https://github.com/wseternal/ssetunnel/commit/99be399c7898eaa0e93cc0be4930a849fbd64afd))
* **connect:** validate token at startup and log handshake failures ([17a54f8](https://github.com/wseternal/ssetunnel/commit/17a54f813e200a8ac20bde4ca130b422212fc536))
* **connect:** wait for both copy directions in ServeStdio and add half-close ([10c82f8](https://github.com/wseternal/ssetunnel/commit/10c82f8c113568a6bcd7277597e5610cca58c28f))
* **console:** persist session token to localStorage across page refreshes ([4fd8ee7](https://github.com/wseternal/ssetunnel/commit/4fd8ee7280320e3b8b02d5fe334e7e5811f42d58))
* **console:** use session registry for Shell tab connected agents ([#18](https://github.com/wseternal/ssetunnel/issues/18)) ([6233958](https://github.com/wseternal/ssetunnel/commit/62339583b9e75b7ebe45efe4989373451e240ce7))
* disable CSP header for embedded console frontend ([5c35c97](https://github.com/wseternal/ssetunnel/commit/5c35c9778851cd112061417843ef88db5c07cee1))
* **docs:** correct README quick start, add version cmd, fix Go prerequisite ([4191eb0](https://github.com/wseternal/ssetunnel/commit/4191eb066a0dba9b178cd12cc90b2cbfee10fdc1))
* **frontend:** gate admin APIs behind role check for non-admin users ([aaa12cb](https://github.com/wseternal/ssetunnel/commit/aaa12cbebf9318c18312be4dee3cf44827eac53d))
* **frontend:** replace TOTP banner with Shield icon tooltip indicator ([fa4daa3](https://github.com/wseternal/ssetunnel/commit/fa4daa365e2cdba5503379602f3d990f4761e241))
* harden session lifecycle and agent config guards ([b334520](https://github.com/wseternal/ssetunnel/commit/b33452059c0402045720ef181dc938ced80d945a))
* improve SSH ProxyCommand error messages with friendly output ([9738149](https://github.com/wseternal/ssetunnel/commit/97381491531edd194c50ed95ab916eee2b41c704))
* include /console path in login --console flag default ([5e83543](https://github.com/wseternal/ssetunnel/commit/5e83543d1b6f6b44bde71b062e7d5e1978b0065e))
* **local:** remove debug echo in local.sh ([10e9be3](https://github.com/wseternal/ssetunnel/commit/10e9be3231c47dc9edf1000ba37ff1b0491093e5))
* prefix all console endpoints with /console/ ([#6](https://github.com/wseternal/ssetunnel/issues/6)) ([2de2e82](https://github.com/wseternal/ssetunnel/commit/2de2e8281c8ed88e975af9c0a17f804e79e6abaa))
* prevent embedded postgres from running as root ([#13](https://github.com/wseternal/ssetunnel/issues/13)) ([de12164](https://github.com/wseternal/ssetunnel/commit/de121648cec2328d7fe03971b2d3afe6fa4deab9))
* resolve empty server URL from deprecated flag zero-default overwrite ([#3](https://github.com/wseternal/ssetunnel/issues/3)) ([f157784](https://github.com/wseternal/ssetunnel/commit/f157784a3e89c3b6d45fe993a85635a8182dfbaa))
* **server:** fail eagerly when listen, entry, or console-listen address is already bound ([d1ed2e4](https://github.com/wseternal/ssetunnel/commit/d1ed2e46dbbca1ec16dd6bd83c95f60a24cf661c))
* **server:** set HTTP IdleTimeout to prevent 60s agent disconnections ([e169d49](https://github.com/wseternal/ssetunnel/commit/e169d49d1c4ed1d470da5fe9a3ca16bed5206d60))
* **server:** track bytes_sent/bytes_received in session stats ([dd63f2d](https://github.com/wseternal/ssetunnel/commit/dd63f2d862fadb6294f2eaeb7409c58f50950e80))
* **test:** cancel agent context before server teardown in e2e TestMain ([b35b518](https://github.com/wseternal/ssetunnel/commit/b35b518e0047fd8cc4619def63752a82cd05b21a))
* unify CLI --server flag and multi-session storage ([#12](https://github.com/wseternal/ssetunnel/issues/12)) ([be7e16a](https://github.com/wseternal/ssetunnel/commit/be7e16a323dd85388db6cc1c5a21fa5dfe25f17c))
* update login command and server log for /console/ prefix ([#7](https://github.com/wseternal/ssetunnel/issues/7)) ([61530bc](https://github.com/wseternal/ssetunnel/commit/61530bca3c10a7b3d98dd641f69afbf9fbd74e6c))
* upgrade orcacommon to v0.3.3 for embedded postgres datapath support ([41f3757](https://github.com/wseternal/ssetunnel/commit/41f37575fac71e414711ff1b5f8ff22d27c0d01d))
