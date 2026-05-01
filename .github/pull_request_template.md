## What does this PR do?

<!-- Brief description of the change and why it's needed. -->

## PR title

<!-- Your PR title must follow Conventional Commits. It becomes the squash-merge commit message and is parsed by release-please.

Valid prefixes: feat, fix, docs, ci, build, chore, perf, refactor, revert, style, test
Examples:
  feat: add multi-AZ failover support
  fix: prevent duplicate EIP attachment on concurrent events
  docs: update architecture diagrams
  feat!: rename vpc_id to vpc_identifier (BREAKING CHANGE)

Release impact:
  feat: → minor version bump
  fix:  → patch version bump
  feat! / BREAKING CHANGE: → major version bump
  Everything else → no release
-->

## Checklist

- [ ] PR title follows [Conventional Commits](https://www.conventionalcommits.org/) format
- [ ] Changes are tested (unit tests, manual verification, or integration tests as appropriate)
- [ ] Documentation updated if user-facing behavior changed

## Integration tests

<!-- If your change touches Terraform modules, Lambda code, or NAT instance behavior, add the `integration-test` label to this PR to run the full AWS integration suite. This requires environment approval from a code owner.

For AMI changes, add the `nat-images` label instead. -->

- [ ] Not needed — docs, CI, or non-infra change
- [ ] Needed — `integration-test` label added
- [ ] AMI change — `nat-images` label added
