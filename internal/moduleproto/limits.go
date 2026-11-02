package moduleproto

import "github.com/wseternal/ssetunnel/sdk"

// Protocol limits for the module registry wire protocol.
// These bounds prevent runaway memory allocation from misbehaving peers
// while supporting realistic module catalogs.
//
// Limits shared with the public SDK are re-exported from sdk to maintain
// a single source of truth.

// Shared limits (canonical definitions in sdk/manifest.go).
const (
	MaxModulesPerAgent   = sdk.MaxModulesPerAgent
	MaxModuleIDLen       = sdk.MaxModuleIDLen
	MaxServiceNameLen    = sdk.MaxServiceNameLen
	MaxServiceVersionLen = sdk.MaxServiceVersionLen
	MaxRevisionLen       = sdk.MaxRevisionLen
	MaxOpenAPISize       = sdk.MaxOpenAPISize
)

// Internal-only protocol limits.

// MaxCatalogSize caps the total aggregate catalog data at 4 MiB.
const MaxCatalogSize = 4 << 20

// SnapshotChunkSize is the maximum raw chunk size for catalog snapshots.
const SnapshotChunkSize = 32 << 10 // 32 KiB

// MaxPendingOpens caps the number of pending module_open requests per agent.
const MaxPendingOpens = 32

// MaxActiveInvocations caps the total active invocations per agent.
const MaxActiveInvocations = 32

// MaxActiveInvocationsPerModule caps active invocations per module.
const MaxActiveInvocationsPerModule = 8

// EstablishmentDeadline is the maximum seconds to wait for an agent to
// open the reverse stream after receiving module_open.
const EstablishmentDeadline = 3
