# Security Policy

## What k8s-controller-kit does on a controller's behalf

The kit is a library: it runs inside a controller's process, with the
controller's Kubernetes RBAC and whatever credentials its engine holds for
the target system. It adds none of its own. What it does with them:

- **`secret.Read` and `secret.Get` read only from the referencing
  resource's namespace.** The caller passes that namespace; a Secret
  reference in a spec names a Secret, never a namespace, so a resource
  cannot read another tenant's credentials through the kit.
- **The reconciler deletes only through the consumer's engine**, and only
  in two cases: the revoke plan on deletion (when the consumer's
  `RevokeOnDelete` says so) and pruning (below). It never deletes
  Kubernetes objects other than removing its own finalizer.
- **Pruning trusts the inventory in the resource's status.** `Status.Owned`
  is the list of keys the resource is recorded as owning, and a key in it
  that the spec no longer wants is handed to the engine to delete. Whoever
  can write a resource's **status subresource** can therefore add keys and
  have the controller delete what they name, with the controller's
  credentials. Grant `update`/`patch` on `<resources>/status` to the
  controller alone. Engines should parse keys strictly and skip what they
  do not recognize; the kit hands them over unchanged.
- **The finalizer is patched with an optimistic lock** on a fresh read, so
  a concurrent writer's finalizers are never dropped or restored.
- **Plan text is shown to users** (`status.pending`, `status.warnings`,
  dry-run output). The kit prints what `Step.Describe` returns and never
  adds values of its own; keeping secrets out of it is the engine's job.
  Values passed between steps with `SetOutput`/`Ref` are held in memory for
  one `Apply` and are never written to status or logs.
- **Metrics carry only `{namespace, name}` labels**, never spec values, and
  a resource's series are removed when it is deleted.
- The kit implements and calls no cryptography. Its only network traffic is
  controller-runtime's: the Kubernetes API client, and in `manager.Main`
  the metrics (`:8080`) and health-probe (`:8081`) listeners, both
  configurable by flag. Engines open their own connections.

## Supported versions

k8s-controller-kit is pre-stable (`v0.0.x`). Only the latest tag receives
fixes.

## Verifying a release

A release is a signed git tag; there are no binaries. `go get` checks every
module version against the [Go checksum database](https://sum.golang.org),
so the code you fetch is the code that was tagged. To check the tag itself:

```sh
git fetch --tags https://github.com/blairham/k8s-controller-kit
git verify-tag v0.0.1
```

Each tag also has a GitHub release built by `.github/workflows/release.yml`:
the tagged source as `k8s-controller-kit-X.Y.Z.tar.gz`, a `checksums.txt`
signed with [cosign](https://github.com/sigstore/cosign) keyless signing --
tied to the workflow that built it, not to a key someone could leak -- and
SLSA build provenance for the archive. Verify the signature, then the
archive against it, then the provenance:

```sh
VERSION=v0.0.1
cosign verify-blob \
  --certificate-identity "https://github.com/blairham/k8s-controller-kit/.github/workflows/release.yml@refs/tags/$VERSION" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --bundle checksums.txt.sigstore.json checksums.txt
sha256sum --check --ignore-missing checksums.txt
gh attestation verify "k8s-controller-kit-${VERSION#v}.tar.gz" --repo blairham/k8s-controller-kit
```

The provenance bundle is also attached to the release as
`k8s-controller-kit-$VERSION.intoto.jsonl`, for checking offline with
`gh attestation verify --bundle`.

## Reporting a vulnerability

**Do not open a public issue.** Report it privately through GitHub:
[Security → Report a vulnerability](https://github.com/blairham/k8s-controller-kit/security/advisories/new).

Please include the affected version or commit, what an attacker can do, and
the steps to reproduce. You should receive a response within a week.

In scope, among others:

- the reconciler deleting, pruning or revoking anything that is not in the
  resource's revoke plan or its recorded inventory
- a Secret read from any namespace other than the one the caller passed
- a finalizer update that drops or restores another controller's finalizer
- input -- a plan's graph, its ids and dependencies, an inventory -- that
  makes the kit panic or hang, since that stops a controller for every
  resource it manages
- a value passed between steps reaching status, events, logs or metrics

Out of scope: secrets an engine puts in its own step text; deletions an
engine performs for keys the kit handed it correctly; and anything that
requires already writing the resource's status subresource or controlling
the controller's process or configuration (see the inventory note above).
