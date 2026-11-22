---
status: accepted
---

# Deploy to Kubernetes with Gateway API and CloudNativePG, ship one signed OCI image

The service is deployed to Kubernetes from Kustomize manifests (`deploy/k8s/`): the entry point is a Gateway API `Gateway` and `HTTPRoute`s, and PostgreSQL is a CloudNativePG `Cluster`. The only release artifact is one OCI image in GHCR with a SLSA provenance attestation, rolled out by digest after `gh attestation verify`. Dependencies are updated by a self-hosted Renovate run in GitHub Actions.

## Considered Options

- **Ingress instead of Gateway API**: simpler today, but Gateway API is the successor and the manifests would have to be rewritten later. Rejected.
- **A managed or hand-run PostgreSQL**: another system to operate outside the cluster; CloudNativePG gives replicas and failover from one resource. Rejected.
- **Helm instead of Kustomize**: templating is not needed for one service with one production overlay. Rejected.
- **Dependabot**: cannot update the tool versions pinned in workflow variables or the `sqlc` command in the README; Renovate's regex managers can. Rejected.
- **Binary releases next to the image**: nothing consumes them. Rejected.

## Consequences

- The cluster needs Kubernetes 1.30 or newer, the Gateway API CRDs with a controller, the CloudNativePG operator and a CNI that enforces NetworkPolicy.
- Backups of the database are not configured; they need an object store.
- The manifests are validated with `kubeconform` but have never been run in a real cluster, and the workflows have never run on GitHub.
- Running Renovate from Actions needs a `RENOVATE_TOKEN` secret.
