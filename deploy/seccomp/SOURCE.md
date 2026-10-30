# Seccomp profile provenance

Based on https://github.com/moby/profiles/blob/6fe7deb1b9fb7c0397a4593480d7d22b9ee8caef/seccomp/default.json

Retains the upstream deny-by-default profile and adds ALLOW rules for clone, unshare, setns, mount, umount2, pivot_root. These enable nested unprivileged namespaces used by Codex bubblewrap. Kernel capability checks still apply; no Docker capabilities or host namespaces are granted.

Upstream licensing: Apache-2.0; see LICENSE.
