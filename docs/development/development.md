# Development Guide

This document is the reference for developing Yukimi. For the contribution process — sign-off, pull
requests, review — see [CONTRIBUTING.md](../../CONTRIBUTING.md).

## Prerequisites

### Required Software

- **Go**: the version in [`go.mod`](../../go.mod) or later. `go.mod` is the single source of truth for
  this; at the time of writing it pins `go 1.26.0` with `toolchain go1.26.8`.
- **Docker**: for building container images and running local clusters.
- **Linux or macOS**: the Crossplane `build` submodule that drives the Makefile refuses to run on a
  Windows host (`build only supported on linux and darwin host currently`). On Windows, work inside
  WSL2 — or edit and run `go build` / `go test` directly and let CI run `make reviewable` for you.

### Optional Binaries (Automatically Installed)

The following tools are **automatically downloaded and installed** by the build pipeline when needed.
You do **NOT** need to install these manually. Versions come from
[`build/makelib/k8s_tools.mk`](../../build/makelib/k8s_tools.mk) in the submodule and from the
Makefile, so treat those as authoritative rather than the numbers below:

- **kind**: Kubernetes in Docker for local testing
- **kubectl**: Kubernetes command-line tool
- **Crossplane CLI**: for building and managing Crossplane packages
- **Helm**: Kubernetes package manager (installed when needed)
- **golangci-lint** (v2.13.2, pinned in the `Makefile`): Go linting tool
- **kustomize**: renders `deploy/base` into `deploy/install.yaml`
- **gomplate** (v3.10.0, pinned in the `Makefile`): template rendering, used by `provider.addtype`
- **istioctl**, **kcl**: pulled in by the submodule; unused by this project

These tools are downloaded to `.cache/tools/`.



### Key Makefile Targets

#### Local Development

- **`make dev`**: Start local development (creates kind cluster if needed, then runs provider with debug logging)
- **`make dev-clean`**: Clean up local development environment (deletes kind cluster)
- **`make test`**: Run unit tests (with `-short`, which skips integration tests)
- **`make test-integration`**: Run integration tests only. **Requires real AWS and Snowflake access** via `.env`
- **`make reviewable`**: Ensures PR readiness — `generate`, `lint`, `test`. This is what CI gates on

#### Code Quality & Testing

- **`make lint`**: Run linting and code analysis tools
- **`make check-diff`**: Ensure no untracked changes after `make reviewable`

#### Code Generation

- **`make generate`**: Regenerate all auto-generated code
  - Required after API changes
  - Generates deepcopy methods, managed resource interfaces, etc.
  - Also regenerates `deploy/install.yaml` from `deploy/base`, so `check-diff` fails if the committed
    copy is stale

#### Manifests

- **`make manifests`**: Render `deploy/base` into the committed `deploy/install.yaml`
- **`make manifests.check`**: Fail if `deploy/install.yaml` is stale. Runs as part of `check-diff`
- **`make manifests.overlays`**: Verify the kustomize overlays under `deploy/overlays` still build

#### Release & Publishing

- **`make build`**: Build provider binary for host platform
- **`make build.all`**: Build for all supported platforms (linux_amd64, linux_arm64)
- **`make publish`**: Build and publish releasable artifacts (currently disabled)
- **`make tag`**: Tag a release (currently disabled)
- **`make promote`**: Promote release to a channel (currently disabled)


## Adding New Managed Resource Types

### Quick Steps

Add a new managed type using the scaffolding system:

```bash
export type=SnowflakeAccount   # CamelCase kind, per specs/design.md (e.g. SnowflakeAccount, SnowflakeReplication)
make provider.addtype provider=Snowflake group=base kind=${type}
```

Per `specs/design.md`, resource kinds fall into two API groups, not a single `infra` group:
- `base.snowflake.yukimi.io` — the platform's own managed resources: `SnowflakeAccount`, `SnowflakeReplication`, `SnowflakeDeletionRequest` (use `group=base` as above)
- `base.identity.yukimi.io` — `IdentitySyncRequest` only. This one is *emitted* by this platform's controller but *fulfilled* by a company-specific controller outside this repo, so scaffold it as its own group (`group=identity`) and expect no reconciler logic for the fulfilling side.

### Post-Scaffolding Tasks

After running the scaffolding command:

1. **Update API registration**:
   - Only once for every api group. Most likely can be skipped.
   - Edit `apis/yukimi.go` to register the new API group.
   - Add imports for all api-groups, e.g. `import(...basev1alpha1 "github.com/allianz/yukimi/apis/base/v1alpha1")`
   - Add all api-groups to init() function, e.g. `append(...basev1alpha1.SchemeBuilder.AddToScheme)`
   - Today only `apis/v1alpha1/` exists, under the `snowflake.yukimi.io` group, and it currently registers no API types; a new resource's types land in a new versioned directory named for its group, e.g. `apis/base/v1alpha1/` for `base.snowflake.yukimi.io`.

2. **Update controller registration**:
   - Run `make generate`
   - Edit `internal/controller/yukimi.go` to register the new controller
   - Add import for controller, e.g. `import(..."github.com/allianz/yukimi/internal/controller/snowflakeaccount")`
   - Add controller setup to the `SetupGated` function, e.g.
      ```go
      func SetupGated(mgr ctrl.Manager, o controller.Options) error {
      for _, setup := range []func(ctrl.Manager, controller.Options) error{
         snowflakeaccount.Setup,
      } {
      ```
   - Run `go fmt ./...`

3. **Implement controller logic**:
   - Review generated files in the new API group's directory (e.g. `apis/base/v1alpha1/`)
   - Implement business logic in `internal/controller/{newtype}/`
   - Replace sample implementations with actual Snowflake API calls

4. **Regenerate and validate**:
   ```bash
   make reviewable      # Runs generation, linting, tests
   make build          # Verify binary builds successfully
   ```

### Generated Files (Do Not Edit)

The following files are auto-generated and should never be manually edited:
- `**/zz_generated.deepcopy.go` - Deep copy methods
- `**/zz_generated.managed.go` - Managed resource interfaces
- `**/zz_generated.managedlist.go` - Managed resource list types

## Project Structure

See CLAUDE.md's "Directory Structure" section for the canonical, up-to-date project layout.

## Local Development Workflow

### First Time Setup
```bash
make submodules
make dev  # Creates kind cluster and starts provider
```

### Daily Development
```bash
make dev  # Reuses existing cluster, just starts provider
# Make code changes, then Ctrl+C to stop
make dev  # Restart with changes
```

### Reset Environment (when needed)
```bash
make dev-clean  # Delete cluster and start fresh
make dev        # Create new cluster and start provider
```

> 💡 **Tip**: The kind cluster persists between `make dev` runs, preserving any test data or configurations you've applied. Only use `make dev-clean` when you need a completely fresh environment.

## Common Environment Variables

- `DEBUG=1`: Enable debug symbols in builds
- `V=1`: Enable verbose build output
- `PLATFORM`: Target platform for builds
- `VERSION`: Version information for binaries
- `CHANNEL`: Release channel (master, main, alpha, beta, stable)

