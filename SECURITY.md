# Security Policy

## Supported versions

Security fixes are made on `main` and released in the next patch release of the latest minor
version.

| Version | Supported |
|---|---|
| Latest minor release | Yes |
| Older releases | No |

## Reporting a vulnerability

Please **do not** report security vulnerabilities through public GitHub issues, discussions or
pull requests.

Report them privately through GitHub's
[private vulnerability reporting](https://github.com/omniva-ai/sweeper/security/advisories/new).

Please include as much of the following as you can:

- The affected version(s) and configuration
- Steps to reproduce, or a proof of concept
- The impact, as you understand it

## What to expect

- We aim to acknowledge your report within 3 business days.
- We will keep you informed while we investigate and work on a fix.
- Once a fix is released we will publish a GitHub Security Advisory and credit you, unless you
  prefer to remain anonymous.

## Verifying releases

Release images and manifests are signed with [cosign](https://github.com/sigstore/cosign) keyless
signing. See the [README](README.md#verifying-releases) for verification instructions.
