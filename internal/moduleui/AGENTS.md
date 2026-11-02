# Module UI Store (moduleui)

Local trusted package store for consumer UI assets and asset verification.

## Components

### Store (`store.go`)
Manages UI packages on disk. Each package is identified by its SHA-256 digest and contains a manifest listing all assets with expected sizes and digests.

- `GetAsset(digest, relPath)` — retrieves a verified asset from a trusted package.
- `InstallPackage(digest, data)` — installs a package from a tar archive, verifying all asset digests.
- Path traversal rejection: `..` in relative paths is rejected.
- `MaxAssetSize` (8 MiB) bounds individual asset reads.

## Trust Model
Consumer UI packages are installed locally and verified by digest on every request. Provider UI assets are fetched via module invocation and verified against the catalog manifest before serving.

## Rules
- Never serve an asset without verifying its digest matches the manifest.
- Package directories are keyed by digest — no mutable naming.
