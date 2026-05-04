# Initial project setup (historical record)

This project was bootstrapped from the official Crossplane provider template:
https://github.com/crossplane/provider-template

This document records what was done during the initial setup so future contributors understand the divergence from upstream. See also:

- `docs/development/ADDING_A_NEW_TYPE.md` – how to scaffold and implement new managed resource types.

## Summary of bootstrap actions

1. Copied contents of the upstream template repository into this repo, then removed the `build/` directory (it is a submodule, not plain files).
2. Added and initialized the `build` submodule:
   ```shell
   git submodule add https://github.com/crossplane/build build
   git submodule update --init
   ```
   Then verified with:
   ```shell
   make submodules
   ```
3. Ran provider preparation to rename and stamp project metadata:
   ```shell
   make provider.prepare provider=Snowflake
   ```
4. Changed API group domain suffix from `.crossplane.io` to `.allianz.io` across:
   - API type templates under `hack/helpers/apis/`
   - Generated API package files under `apis/`
   - CRD YAMLs under `package/crds/`
   - Example manifests under `examples/`


## Domain suffix change rationale

Organizational ownership required CRDs to live under `.allianz.io`. All future type generation uses the modified templates. If upstream template updates are pulled in, re-apply the suffix change before regenerating.


