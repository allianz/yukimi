# Deploying Yukimi

> **Not usable yet.** `install.yaml` references `ghcr.io/allianz/yukimi:v0.1.0`,
> which is not published. Until it is, applying these manifests gets you a pod in
> `ImagePullBackOff`. Everything else here is ready.

| Path | What it is |
| --- | --- |
| `install.yaml` | Generated and committed. CRDs, RBAC and the Deployment in one file. Regenerate with `make manifests`. |
| `config.example.yaml` | Template for the `yukimi-config` ConfigMap. Copy to `config.yaml` and fill in. |
| `base/` | The kustomize source `install.yaml` is rendered from. Read this rather than `install.yaml` — kustomize strips the comments from the render. |
| `overlays/local/` | kind/minikube: locally built image, `--debug`, AWS credentials from a file instead of IRSA. Needs the `aws-credentials` Secret — `NAMESPACE=yukimi-system hack/dev/sync-aws-credentials.sh`. |
| `overlays/site.example/` | Copy-and-fill template for a real cluster: image registry, IRSA role ARN, egress proxy, network zone. |

Tenant resource examples live in [`../example/`](../example/).

## Install

```bash
cp deploy/config.example.yaml deploy/config.yaml
$EDITOR deploy/config.yaml                # replace the five REPLACE_ME values
kubectl apply -f deploy/install.yaml -f deploy/config.yaml
```

`install.yaml` first — it creates the `yukimi-system` namespace the ConfigMap goes
into.

The ConfigMap is deliberately not part of `install.yaml`: `install.yaml` is
re-applied to upgrade, and if it carried the ConfigMap every upgrade would
overwrite your Snowflake organization with a placeholder. Apply `install.yaml`
alone and the pod waits in `ContainerCreating` until the ConfigMap appears, then
starts by itself.

`config.example.yaml` documents every field inline. Four things there cause silent
misbehaviour rather than an error, so they are worth knowing before you start:

- Unknown keys are ignored — decoding is non-strict, so a typo gets you the
  default with no warning.
- Both config files are read once, at startup. After editing:
  `kubectl -n yukimi-system rollout restart deployment/yukimi-controller`.
- `usePrivateLink` and `deletion.protection` both default to `true`. Set both
  `false` for local testing.
- `aws.region` overrides the `AWS_REGION` environment variable, not the reverse.

## Prerequisites, or the pod crash-loops

Startup is fail-fast. All of these must be true before the pod first starts, and
each one shows up as `CrashLoopBackOff` with the cause on the last log line:

1. The org-admin secret already exists in AWS Secrets Manager at
   `snowflake/org/<org>/<orgAdminAccount>/org-admin-credentials`, as JSON with
   `username`, `public_key` and `private_key`. The controller never creates it —
   `hack/dev/setup_snowflake_credentials.sh` does.
2. That Snowflake user holds `GLOBALORGADMIN`.
3. The pod's AWS identity can read that secret, and use the KMS key if
   `aws.kmsKeyId` is set. On EKS that is the `eks.amazonaws.com/role-arn`
   annotation in `overlays/site/`; locally it is the credentials file mounted by
   `overlays/local/`.
4. Snowflake is reachable within 30 seconds — the startup connect has a hard
   timeout. A wrong `usePrivateLink` for where the pod runs, or `NO_PROXY` on the
   wrong side of the Snowflake hostname, both look like a 30-second pause followed
   by a restart.

The IAM role needs `secretsmanager:GetSecretValue`, `CreateSecret`,
`PutSecretValue` and `DeleteSecret` on `snowflake/org/<org>/*` and
`snowflake/tenant/<org>/*` — those four calls are all the controller makes — and
its trust policy must name the ServiceAccount exactly:
`system:serviceaccount:yukimi-system:yukimi-controller`.

## Regenerating `install.yaml`

```bash
make manifests            # render deploy/base -> deploy/install.yaml
make manifests.check      # fail if the committed copy is stale
make manifests.overlays   # prove both overlays still build
```

`make manifests` also runs as part of `make generate`, so CI fails if
`install.yaml` does not match `base/`. Commit the render with any change to
`base/`.

**A bare `kustomize build deploy/base` fails** with `security; file
'.../package/crds/...' is not in or below '.../deploy/base'`. That is expected:
`base/` references the CRDs in `package/crds/` by relative path so there is one
copy of them in the repo rather than two that drift, and the make target passes
`--load-restrictor=LoadRestrictionsNone`. The CRDs are listed file by file because
kustomize does not expand globs, and `make manifests` cross-checks that list
against `package/crds/` so a new CRD cannot go missing silently.

## Deliberately absent

- **No metrics `Service`.** Metrics are served unauthenticated on `:8080` with no
  authz filter. Nothing scrapes them, and `port-forward` needs no Service.
- **No health endpoint.** `main.go` never sets `HealthProbeBindAddress`, so
  `/healthz` does not exist. The probes are `tcpSocket: 8080`, which is meaningful
  because that listener binds last — after config parsing, the AWS backend and the
  Snowflake connect.
- **No RBAC on `secrets` or `configmaps`.** Credentials live in AWS Secrets
  Manager and config arrives as a mounted volume, so a compromised controller
  cannot enumerate cluster secrets.
- **No CPU limit.** A CPU limit throttles, and this controller mostly waits on
  network calls.
- **One replica, `strategy: Recreate`, leader election on.** `CREATE ACCOUNT` is
  slow, billable and not idempotent, so two controllers must never run at once —
  including during a rollout.
