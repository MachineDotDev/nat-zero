# CI/CD Workflows

Internal reference for GitHub Actions workflows, repo rulesets, and the release process. This file is not published to the docs site.

## Workflows Overview

| Workflow | File | Triggers | Required Check |
|----------|------|----------|----------------|
| Pre-commit | `precommit.yml` | All PRs | `precommit` |
| Go Tests | `go-tests.yml` | PRs touching `cmd/lambda/**`; push to `main` | `go-test` |
| Integration Tests | `integration-tests.yml` | PR labeled `integration-test`; manual dispatch; reusable workflow | `integration-test` |
| NAT Images | `nat-images.yml` | Manual dispatch; PR labeled `nat-images` | No (promotion workflow) |
| Docs | `docs.yml` | Push to `main` (filtered paths) | No (post-merge deploy) |
| Release | `release-please.yml` | Push to `main`; manual dispatch | No (post-merge) |

## Pre-commit (`precommit.yml`)

Runs the repo's `.pre-commit-config.yaml` hooks: terraform fmt/validate, tflint, terraform-docs, Go staticcheck, actionlint, shellcheck, and Packer fmt/validate.

- **PR trigger**: All pull requests, all paths (no path filter).
- **Job name**: `precommit` (required status check for merge).

## Go Tests (`go-tests.yml`)

Runs `go test -v -race ./...` in `cmd/lambda/` (Lambda unit tests).

- **PR trigger**: Only when `cmd/lambda/**` changes.
- **Push trigger**: Only on `main`, same path filter.
- **Job name**: `go-test` (required status check for merge).
- **Note**: Path-filtered. If a PR doesn't touch Go code, this check won't run and won't block merge (see ruleset notes below).

## Integration Tests (`integration-tests.yml`)

Full end-to-end test: deploys real AWS infrastructure via Terratest, exercises the Lambda lifecycle (create NAT, scale-down, restart, cleanup), then destroys everything.

- **PR trigger**: `labeled` type only. Runs when the `integration-test` label is added.
- **Manual trigger**: `workflow_dispatch`.
- **Reusable trigger**: `workflow_call`.
- **Condition**: `github.event.label.name == 'integration-test'` (or manual dispatch).
- **Concurrency**: Group `nat-zero-integration`, `cancel-in-progress: false`. Only one integration test runs at a time; new ones queue.
- **Environment**: `integration` (holds the `INTEGRATION_ROLE_ARN` secret for OIDC).
- **Timeout**: 15 minutes.
- **Job name**: `integration-test` (required status check for merge).
- **Optional inputs**:
  - `nat_ami_id` to force the fixture onto a specific NAT AMI. If omitted, the workflow uses the shared private test AMI from the GitHub Actions variable `NAT_ZERO_TEST_AMI_ID`.
  - `updated_nat_ami_id` to exercise the AMI replacement path after a second `terraform apply`.

### Steps

1. Checkout, setup Go, setup Terraform (wrapper disabled).
2. Assume AWS role via OIDC (`aws-actions/configure-aws-credentials`).
3. Build the Lambda binary from source (`cmd/lambda/` -> `.build/lambda.zip`).
4. Run `go test -v -timeout 10m -count=1` in `tests/integration/`.

## NAT Images (`nat-images.yml`)

Manual promotion workflow for the default public nat-zero AMI.

1. Build the AMI with Packer in the chosen source region.
2. Let Packer privately copy it to the regions listed in `ami/nat-zero-private-all-regions.pkrvars.hcl`.
3. Run one us-east-1 integration gate on a single stack:
   - deploy from the shared private test NAT AMI in `NAT_ZERO_TEST_AMI_ID`
   - exercise the normal NAT lifecycle
   - reapply the module with the new AMI
   - verify the old NAT is replaced and the new NAT works
4. After the integration gates pass, run a small publish script that opens launch permissions for the copied AMIs.
5. Open a PR that updates the Terraform defaults (`ami_owner_account`, `ami_name_pattern`) so merge + release-please can publish the new module version.

For pre-merge validation on a branch, add the `nat-images` label to the PR. That trigger uses the GitHub Actions variable `NAT_ZERO_AMI_BUILD_SUBNET_ID`, runs the build and integration gates on the PR branch, and intentionally skips the public-sharing and promotion-PR jobs.

## Docs (`docs.yml`)

Deploys MkDocs Material to GitHub Pages.

- **Trigger**: Push to `main` only, when `docs/**`, `mkdocs.yml`, `README.md`, or `*.tf` change.
- **Not a merge gate** -- only runs post-merge.
- Runs `mkdocs gh-deploy --force`.

## Release Please (`release-please.yml`)

Two-job workflow that automates versioning, changelogs, and Lambda binary distribution.

### Job 1: `release-please`

Runs `googleapis/release-please-action@v4` with:

- **Config**: `release-please-config.json` -- `terraform-module` release type at repo root.
- **Manifest**: `.release-please-manifest.json` -- tracks current version (starts at `0.0.0`).

#### How release-please works step by step

1. Every push to `main` triggers this job.
2. Release-please scans commits since the last release for Conventional Commits (`feat:`, `fix:`, etc.).
3. If releasable commits exist (`feat` or `fix`), it **creates or updates a release PR** (e.g., `chore(main): release 0.1.0`) containing:
   - Updated `CHANGELOG.md` with grouped entries per the configured sections (Features, Bug Fixes, Performance, Documentation, Miscellaneous).
   - Version bump in `.release-please-manifest.json`.
   - For `terraform-module` type: version strings in Terraform files if present.
4. The release PR sits open until merged.
5. When the release PR is merged, release-please runs again on that push. It detects its own merged PR and:
   - Creates a **GitHub Release** with a version tag (e.g., `v0.1.0`).
   - Sets output `release_created=true` and `tag_name=v0.1.0`.

### Job 2: `build-lambda`

Only runs when `release_created == 'true'` (i.e., the push that merges a release PR).

1. Cross-compiles the Go Lambda for `linux/arm64`.
2. Zips as `lambda.zip`.
3. Writes `lambda.zip.base64sha256`, containing the base64-encoded SHA256 for the zip.
4. **Uploads the zip and checksum to the versioned release** (e.g., `v0.1.0`).
5. **Creates/updates a rolling `nat-zero-lambda-latest` release** with the same zip and checksum for convenience, but the module default now pins to the versioned release asset that matches the tagged module version.

### Changelog sections

| Commit prefix | Changelog section | Triggers release? |
|---------------|-------------------|-------------------|
| `feat:` | Features | Yes (minor bump) |
| `fix:` | Bug Fixes | Yes (patch bump) |
| `perf:` | Performance | No |
| `docs:` | Documentation | No |
| `chore:` | Miscellaneous | No |
| `feat!:` / `BREAKING CHANGE:` | Features | Yes (major bump) |

## Repo Rulesets

### `main` branch ruleset

- **PRs required** with:
  - 0 required approvals
  - Stale reviews dismissed on push
  - All review threads must be resolved
- **Required status checks**: `precommit`, `go-test`
  - strict mode enabled, so required checks must be up to date with `main`
- **Linear history required**
- **No force push**
- **No branch deletion**
- **Bypass**: Admin role can bypass because `enforce_admins` is disabled.

### `tags` ruleset

- Protects `refs/tags/v*` -- no deletion or update of version tags.
- Ensures release-please's tags are immutable.
- Same admin bypass.

## PR Lifecycle Summary

```
Open PR
  -> precommit runs (always)
  -> go-test runs (if cmd/lambda/** changed)
  -> Add "integration-test" label -> integration tests run against real AWS
  -> threads resolved
  -> Squash merge to main

Post-merge to main:
  -> release-please creates/updates a release PR (if feat/fix commits exist)
  -> docs deploy (if docs changed)

Merge release PR:
  -> release-please creates GitHub Release + tag
  -> build-lambda uploads lambda.zip + lambda.zip.base64sha256 to release + rolling latest
```
