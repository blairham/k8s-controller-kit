<!--
Explain the why. The diff already says what.
k8s-controller-kit is pre-stable, but every exported change lands in a
controller: say what a consumer has to change, if anything. A change to a
condition reason, a message shape, a metric name or the plan hash is a
breaking change for the consumers' alerts and statuses -- call it out.
-->

## What this changes



## Why



## How it was verified

<!--
A new exported function needs a test; a fix needs a test that fails
without it. Reconciler behavior is tested against controller-runtime's
fake client; say which behavior your test pins.
-->



---

- [ ] I have signed the CLA (see CLA.md)
- [ ] CHANGELOG.md has an entry under [Unreleased]

Closes #
