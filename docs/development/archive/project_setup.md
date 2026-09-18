# Initial project setup (historical record)

This project was bootstrapped from the official Crossplane provider template:
https://github.com/crossplane/provider-template

This document records what was done during the initial setup, so that a contributor who wonders why
this repository diverges from upstream can find the answer. It is history, not instructions — none of
it needs to be repeated. To add a new resource type today, see
[docs/development/development.md](../development.md).

## Summary of bootstrap actions

1. Copied contents of the upstream template repository into this repo, then removed the `build/`
   directory (it is a submodule, not plain files).
2. Added and initialized the `build` submodule:
   ```shell
   git submodule add https://github.com/crossplane/build build
   git submodule update --init
   ```
   Then verified with:
   ```shell
   make submodules
   ```
3. Ran the template's one-shot `provider.prepare` target to rename and stamp project metadata. That
   target and its script (`hack/helpers/prepare.sh`) have since been deleted: they only ever ran once,
   and leaving them in place invited someone to re-run a rename over an already-renamed repository.
4. Changed the API group domain suffix from `.crossplane.io` to `.yukimi.io` across:
   - API type templates under `hack/helpers/apis/`
   - Generated API package files under `apis/`
   - CRD YAMLs under `package/crds/`
   - Example manifests under `example/`

## Domain suffix change rationale

The API groups had to be owned by this project rather than by Crossplane, so CRDs live under
`.yukimi.io` — today `base.snowflake.yukimi.io` and `base.identity.yukimi.io`. All type generation
uses the modified templates in `hack/helpers/apis/`. If upstream template updates are ever pulled in,
re-apply the suffix change before regenerating.
