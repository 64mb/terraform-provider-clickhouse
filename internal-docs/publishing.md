# Provider checks and publication

The release repository is `64mb/terraform-provider-clickhouse`; the provider source is `64mb/clickhouse` (`registry.opentofu.org/64mb/clickhouse` in OpenTofu).

## Local verification

Use Go 1.25 or later. `task test` runs unit tests without connecting to ClickHouse. Integration and acceptance tests are separate because legacy acceptance tests use `resource.UnitTest` and do not consistently honor `TF_ACC` or `-short`.

Start a disposable local server:

```sh
docker run --rm --name clickhouse-provider-tests \
  -p 127.0.0.1:19000:9000 \
  -e CLICKHOUSE_SKIP_USER_SETUP=1 \
  -e CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT=1 \
  -d clickhouse/clickhouse-server:25.3

docker exec clickhouse-provider-tests clickhouse-client --query 'SELECT 1'
task test-table-integration
task test-table-lifecycle
docker stop clickhouse-provider-tests
```

The lifecycle task requires Terraform on PATH. To use OpenTofu instead, set `TF_ACC_TERRAFORM_PATH` to the absolute path to `tofu`, `TF_ACC_PROVIDER_HOST=registry.opentofu.org` and `TF_ACC_PROVIDER_NAMESPACE=hashicorp` as documented in [OpenTofu provider acceptance testing](https://opentofu.org/docs/language/providers/#running-acceptance-tests-with-opentofu-cli). The test harness namespace is independent of the published provider namespace.

CI builds, vets and race-tests unit tests, checks the GoReleaser configuration, and runs table roundtrip, ALTER, maintenance rebuild, bootstrap and full lifecycle tests against ClickHouse 25.3, 25.8, 26.3 and 26.8 with both Terraform 1.6.6 and OpenTofu 1.13.1. Each of the eight combinations uses disposable local services, including a separate ClickHouse instance with embedded Keeper for Replicated and Distributed tests. Both instances use the same version from the matrix. Major/minor image tags track the latest patch within each selected release series.

## Signed GitHub releases

After merging a reviewed change into `main`, create and push a new semantic version tag, such as `v0.1.0`. This publishes a public release: choose the version intentionally and do not move an existing tag.

The release workflow reruns the same checks, requires the tag commit to be on `main`, imports the existing signing key, and uses GoReleaser to publish ZIP packages, the provider manifest, SHA256 checksums and their detached GPG signature. Publishing is restricted to the canonical repository, so a tag in a fork cannot accidentally publish the canonical provider.

Repository Actions secrets:

- `OPENTOFU_GPG_PRIVATE_KEY`: armored private signing key
- `OPENTOFU_GPG_PASSPHRASE`: passphrase for that key
- `GITHUB_TOKEN`: supplied automatically by GitHub with release-job contents write permission

The dedicated OpenTofu signing secrets were installed on 2026-10-16. The previous `GPG_PRIVATE_KEY` and `PASSPHRASE` secrets remain untouched. The workflow checks that the imported private key matches the committed public key before publishing. No additional registry API token is required. The release workflow does not edit repository settings or register a new provider/key.

Run `task release-check` with GoReleaser 2.18.3 to validate the configuration without publishing. Each platform has one ZIP archive; the checksum file and GPG signature follow the [provider registry protocol](https://opentofu.org/docs/internals/provider-registry-protocol/).

## Initial OpenTofu registry registration

The public registry returned HTTP 404 for `64mb/clickhouse` on 2026-10-16, so automatic release publication alone does not make this source available to `tofu init` yet.

The [OpenTofu registry contribution rules](https://github.com/opentofu/registry#adding-providers-modules-or-gpg-keys-to-the-opentofu-registry) require the GitHub issue form UI for initial registration. Do not submit these requests with `gh`, the GitHub API, or a registry-data pull request: the registry processing pipeline requires structured form data.

1. Open the [provider submission form](https://github.com/opentofu/registry/issues/new?template=provider.yml), enter `64mb/terraform-provider-clickhouse`, and submit it
2. Open the [signing-key submission form](https://github.com/opentofu/registry/issues/new?template=provider_key.yml), use namespace `64mb` and provider name `clickhouse`, and supply the armored **public** GPG key corresponding to the release signing key
3. Wait for OpenTofu to approve/process both submissions
4. Verify that the release is indexed with `https://registry.opentofu.org/v1/providers/64mb/clickhouse/versions`, then test `tofu init` using `source = "64mb/clickhouse"` and the released version

The [signing-key form](https://github.com/opentofu/registry/blob/main/.github/ISSUE_TEMPLATE/provider_key.yml) requires public organization membership. The authenticated `goshander` account has public membership in `64mb` and repository admin/push access as of 2026-10-16. Registration still needs the public key and the form submission; repository permissions do not substitute for registry approval.

After initial registration, the registry discovers provider releases from GitHub; continue publishing correctly signed releases from this repository. See the [registry inclusion policy](https://github.com/opentofu/registry/blob/main/POLICY.md) for the immutable-version policy.

## Release signing key

The public key for new releases is [keys/clickhouse-provider-signing.asc](../keys/clickhouse-provider-signing.asc). Its fingerprint is `2CB93B583D65802D1643F5CB86156326707BDF89`, with UID `64mb ClickHouse Provider Releases` and a signing-only RSA 4096-bit primary key. It has no expiration date; rotation or revocation is an explicit maintainer operation.

Use the entire armored public-key file in the OpenTofu signing-key form. Private keys and passphrases belong only in Actions secrets and a protected local backup, never in this repository. Keep the private-key backup, passphrase and revocation certificate together in a protected backup location. The registry form is prepared separately and is not submitted by this change.

The old `v0.0.0` release was signed with the previous key. Registering this new key does not validate older releases automatically. Publish a new immutable version after merging the release workflow changes; do not replace old release assets or move old tags.
