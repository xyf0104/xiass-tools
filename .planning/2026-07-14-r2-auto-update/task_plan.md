# Cloudflare R2 Automatic Update Plan

Goal: add a signed, cross-platform CodeStudio Lite automatic update system backed by Cloudflare R2 while preserving the branded Burn installer for Windows interactive installs.

Out of scope: incremental, binary-delta, and file-level patch updates. The first implementation always installs a complete signed platform update package.

## Phase 1: Current-State and Contract Design
Status: complete

- [x] Inventory the current update UI, Tauri configuration, installers, and macOS workflow.
- [x] Confirm the Tauri v2 updater artifact and manifest contract from official documentation.
- [x] Define the R2 object layout, public endpoint, caching rules, release channels, and rollback model.
- [x] Decide how Windows Burn installs transition to signed MSI updater packages.

## Phase 2: Client Updater Integration
Status: complete

- [x] Add the Tauri updater and process plugins with least-privilege capabilities.
- [x] Replace GitHub-only release detection with signed updater checks.
- [x] Add download progress, install confirmation, restart behavior, and localized error states.
- [x] Keep browser-development mocks deterministic and testable.

## Phase 3: Signed Artifact Generation
Status: complete

- [x] Enable Tauri updater artifacts for Windows and macOS.
- [x] Preserve the branded Burn setup executable as the manual Windows installer.
- [x] Generate and validate signatures without committing private signing material.
- [x] Define required CI secrets and local release prerequisites.

## Phase 4: R2 Publication Pipeline
Status: complete

- [x] Add a release manifest generator with platform mappings and signatures.
- [x] Add an idempotent R2 upload script using immutable versioned object keys and SHA-256 metadata.
- [x] Publish `latest.json` only after all artifacts and signatures are present.
- [x] Add dry-run, validation, and rollback commands.

## Phase 5: CI and End-to-End Verification
Status: pending

- [ ] Build Windows and macOS update artifacts in CI.
- [ ] Validate signatures and manifest URLs before publication.
- [ ] Test update discovery from an older signed build without silently applying it.
- [ ] Test an authorized real update on Windows and macOS before production rollout.
- [ ] Confirm full-package retries and rollback behavior without introducing patch-chain state.

## Phase 6: R2-Only Update Source
Status: complete

- [x] Remove GitHub Releases API fallback and related constants.
- [x] Keep unconfigured development builds network-quiet with an explicit UI state.
- [x] Verify configured builds still use only the signed Tauri/R2 endpoint.

## Phase 7: CI-Provider Independence
Status: complete

- [x] Remove updater-specific secrets and artifact assumptions from the GitHub macOS workflow.
- [x] Add explicit Windows and macOS updater build commands usable locally or in any CI provider.
- [x] Rewrite R2 setup guidance around a generic secure release environment.
- [x] Verify the provider-independent release contract and full project checks.

## Phase 8: Enforced R2-Connected Release Builds
Status: complete

- [x] Reproduce the Windows updater command silently falling back to an updater-disabled build.
- [x] Require updater configuration for the explicit Windows updater build command.
- [x] Verify missing configuration fails and configured builds still generate the R2 endpoint.

## Phase 9: Production R2 Domain
Status: complete

- [x] Verify `download.codestudio.build` resolves through Cloudflare over HTTPS.
- [x] Store the production R2 base URL in checked-in public updater configuration.
- [x] Make updater configuration and platform builds consume the checked-in URL with environment overrides.
- [x] Verify the production endpoint is generated and record the remaining signing/manifest requirements.

## Phase 10: Durable Signing Key Storage
Status: complete

- [x] Add random production signing-key generation with DPAPI-protected local password storage.
- [x] Add passphrase-encrypted portable export and Windows import workflows.
- [x] Make Windows updater builds load the protected local signing store automatically.
- [x] Generate the production key, embed its public key, and verify signed updater output.

## Phase 11: Release Filename Normalization
Status: complete

- [x] Enforce kebab-case artifact names in manifest generation and R2 publication.
- [x] Normalize Windows MSI, signatures, and Burn installer names after packaging.
- [x] Normalize macOS updater archive, signature, and DMG names after packaging.
- [x] Rebuild current Windows artifacts and regenerate the desktop manifest with canonical names.

## Phase 12: Platform-Qualified Artifact Names
Status: complete

- [x] Require `CodeStudio-Lite-<version>-<OS>-<arch>` ordering for every release artifact.
- [x] Normalize Windows, macOS, and Linux package outputs with `Windows`, `macOS`, and `Linux` tokens.
- [x] Reject updater artifacts whose filename does not match the platform token selected by the manifest or publisher command.
- [x] Update release documentation, rebuild Windows distribution artifacts, and regenerate the desktop manifest.
- [x] Run focused and full verification without changing Tauri platform identifiers such as `windows-x86_64`.

## Open Inputs

- Cloudflare account ID, bucket name, and scoped R2 credentials for CI.
- Tauri updater signing public key and CI access to the corresponding private key/password.

## Errors Encountered

| Error | Attempt | Resolution |
|---|---|---|
| Broad source search scanned binary resources and target intermediates | Initial updater inventory | Switched to `git ls-files` and targeted source/config reads. |
| Combined updater patch could not match the existing store interface order | Initial Phase 2 edit | Split plugin/config edits from the update-store rewrite and reapplied against the exact file. |
| Tauri signer ignored `TAURI_SIGNING_PRIVATE_KEY_PATH` despite listing it in CLI help | Test updater build | Use the private-key contents in `TAURI_SIGNING_PRIVATE_KEY` for the release build and CI. |
| Manifest test passed a `file:` URL directly to the Windows Node child process | Manifest generator regression test | Convert the script URL with `fileURLToPath` before spawning Node. |
| Browser plugin setup failed with `Cannot redefine property: process` after a clean session reset | Settings visual QA | Kept the verified dev server running, recorded visual QA as a remaining manual check, and did not bypass the mandated browser surface. |
| R2-only combined patch used stale documentation text as context | Phase 6 edit | Apply product-code removal first, then update documentation against its current wording. |
| Signing setup called an ACL helper before PowerShell had executed its function definition | Production key generation | Move helper functions above each script entrypoint, confirm no key files were created, then rerun without rotating existing material. |
| A random signing password beginning with `-` was parsed as a Tauri CLI option | Production key generation retry | Pass password and output path through `--name=value` arguments so arbitrary generated secret bytes cannot cross the CLI option boundary. |
| The signing key was generated but the Security module failed to autoload before DPAPI password persistence | Production key generation retry | Treat the pair as unusable, explicitly import the system Security module before ACL changes, delete only the unrecoverable generated pair, then regenerate and verify an export/import round trip. |
| Native DPAPI types were unavailable in a clean Windows PowerShell child process | Native DPAPI retry | Explicitly load the .NET `System.Security` assembly, without loading the conflicting PowerShell Security module, before calling `ProtectedData`. |
| PowerShell 5 wrote a UTF-8 BOM into `updater.config.json`, causing Node `JSON.parse` to fail | Production signed build | Strip an optional BOM when reading public config and write future config updates with an explicit BOM-less UTF-8 encoder. |
| The Linux normalization harness ran under WSL without a Linux `node` executable | Phase 12 cross-shell verification | Keep Node as the primary JSON reader and fall back to Python 3's standard JSON parser; rerun the same AppImage/DEB/RPM harness successfully. |
