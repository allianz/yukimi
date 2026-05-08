# Development Guide

This document provides a comprehensive guide for developing the Snowflake Crossplane provider.

## Prerequisites

### Required Software

Before you begin development, ensure you have the following software installed:

- **Go**: Version 1.23.0 or later (project uses toolchain go1.23.8)
- **Docker**: For building container images and running local clusters

### Optional Binaries (Automatically Installed)

The following tools are **automatically downloaded and installed** by the build pipeline when needed. You do **NOT** need to install these manually:

- **kind** (v0.23.0): Kubernetes in Docker for local testing
- **kubectl** (v1.24.3): Kubernetes command-line tool
- **Crossplane CLI** (v1.20.0): For building and managing Crossplane packages
- **Helm** (v3.9.1): Kubernetes package manager (installed when needed)
- **golangci-lint** (v1.61.0): Go linting tool
- **kustomize** (v4.5.5): Kubernetes configuration management
- **gomplate** (v3.10.0): Template rendering tool
- **istioctl** (v1.12.9): Istio service mesh CLI
- **kcl** (v0.10.0): KCL configuration language

These tools are downloaded to `.cache/tools/`.



### Key Makefile Targets

#### Local Development

- **`make dev`**: Start local development (creates kind cluster if needed, then runs provider with debug logging)
- **`make dev-clean`**: Clean up local development environment (deletes kind cluster)
- **`make test`**: Run unit tests
- **`make reviewable`**: Ensures PR readiness

#### Code Quality & Testing

- **`make lint`**: Run linting and code analysis tools
- **`make e2e`**: Tests packaging and deployment of the provider to a real cluster. (currently disabled)
- **`make check-diff`**: Ensure no untracked changes after `make reviewable`

#### Code Generation

- **`make generate`**: Regenerate all auto-generated code
  - Required after API changes
  - Generates deepcopy methods, managed resource interfaces, etc.

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
export type=MyType            # CamelCase (e.g., Database, User, Role)
make provider.addtype provider=Snowflake group=infra kind=${type}
```

### Post-Scaffolding Tasks

After running the scaffolding command:

1. **Update API registration**:
   - Only once for every api group. Most likely can be skipped. 
   - Edit `apis/snowflake.go` to register the new API group. 
   - Add imports for all api-groups, e.g. `import(...infrav1alpha1 "github.com/crossplane/provider-snowflake/apis/infra/v1alpha1")`
   - Add all api-groups to init() function, e.g. `append(...infrav1alpha1.SchemeBuilder.AddToScheme)

2. **Update controller registration**:
   - Run `make generate`
   - Edit `internal/controller/snowflake.go` to register the new controller
   - Add import for controller, e.g. `import(..."github.com/crossplane/provider-snowflake/internal/controller/snowflakeaccount")`
   - Add controller setup to setup function, e.g. 
      ```go
      func Setup(mgr ctrl.Manager, o controller.Options) error {
      for _, setup := range []func(ctrl.Manager, controller.Options) error{
         config.Setup,
         snowflakeaccount.Setup,
      } 
      ```
   - Run `go fmt ./...`

3. **Implement controller logic**:
   - Review generated files in `apis/infra/v1alpha1/`
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
- `**/zz_generated.pc.go` - ProviderConfig interfaces
- `**/zz_generated.pcu.go` - ProviderConfigUsage interfaces

## Project Structure

```
├── apis/                    # Kubernetes API types
│   ├── v1alpha1/           # Provider config & usage types
│   ├── infra/v1alpha1/     # Infrastructure resource types
│   └── snowflake.go        # API registration
├── internal/
│   ├── controller/         # Controller implementations
│   ├── features/          # Feature flags
│   └── version/           # Version information
├── cmd/provider/          # Main provider binary
├── package/              # Crossplane package manifests & CRDs
├── examples/             # Example resource manifests
└── hack/helpers/         # Code generation templates
```

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

