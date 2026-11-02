package moduleproto

// Protocol limits for the module registry wire protocol.
// These bounds prevent runaway memory allocation from misbehaving peers
// while supporting realistic module catalogs.

// MaxModulesPerAgent caps the number of modules per agent.
const MaxModulesPerAgent = 64

// MaxModuleIDLen caps a module ID at 128 characters.
const MaxModuleIDLen = 128

// MaxServiceNameLen caps a service name at 128 characters.
const MaxServiceNameLen = 128

// MaxServiceVersionLen caps a semantic version string at 32 characters.
const MaxServiceVersionLen = 32

// MaxRevisionLen caps a revision string at 64 characters.
const MaxRevisionLen = 64

// MaxOpenAPISize caps an OpenAPI YAML document at 1 MiB.
const MaxOpenAPISize = 1 << 20

// MaxCatalogSize caps the total aggregate catalog data at 4 MiB.
const MaxCatalogSize = 4 << 20

// SnapshotChunkSize is the maximum raw chunk size for catalog snapshots.
// Chunks carry OpenAPI YAML and UI asset data between start and commit.
const SnapshotChunkSize = 32 << 10 // 32 KiB

// MaxPendingOpens caps the number of pending module_open requests per agent.
const MaxPendingOpens = 32

// MaxActiveInvocations caps the total active invocations per agent.
const MaxActiveInvocations = 32

// MaxActiveInvocationsPerModule caps active invocations per module.
const MaxActiveInvocationsPerModule = 8

// EstablishmentDeadline is the maximum time to wait for an agent to
// open the reverse stream after receiving module_open.
const EstablishmentDeadline = 3 // seconds

// MaxRequestHeaders caps request header size at 64 KiB.
const MaxRequestHeaders = 64 << 10

// MaxPathLen caps a request path at 8 KiB.
const MaxPathLen = 8 << 10

// MaxQueryLen caps a raw query string at 16 KiB.
const MaxQueryLen = 16 << 10

// MaxAssetSize caps a single UI asset at 8 MiB.
const MaxAssetSize = 8 << 20

// MaxUICacheSize caps the server UI asset cache at 64 MiB.
const MaxUICacheSize = 64 << 20

// OpenAPIValidationWorkers is the number of concurrent OpenAPI validators.
const OpenAPIValidationWorkers = 2

// OpenAPIValidationQueue is the maximum queued OpenAPI documents.
const OpenAPIValidationQueue = 8

// CopyBufferSize is the buffer size for bidirectional proxy copies.
const CopyBufferSize = 32 << 10 // 32 KiB
