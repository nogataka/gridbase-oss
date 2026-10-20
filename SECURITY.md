# Security Policy

## Reporting a vulnerability

Please **do not open a public issue** for a security problem.

Use GitHub's private vulnerability reporting: open the repository's **Security** tab and choose **Report a vulnerability**. Please include what you found, how to reproduce it, and the version or commit you tested.

You can expect an acknowledgement within a few days. This is a small, volunteer-maintained project, so fix times are best-effort; we will tell you what we decided and credit you in the release notes if you wish.

## Supported versions

Only the latest commit on the default branch is supported. There are no long-term support branches.

## Scope and known issues

- In scope: the tunnel server and client (`tunnel/`), the admin console (`web/`), the Directus bootstrap (`directus/`), and the compose files in the repository root.
- The admin console uses Next.js 14, which has unresolved upstream advisories (the upstream fix is Next.js 16). Which of them apply to this setup, and how to reduce the exposure, is documented in [web/README.md](./web/README.md#依存関係メモ) and in the "Security notes" of the [README](./README.md#security-notes). Reports that only restate those advisories are known.
- Running the stack exposed to the Internet with the example secrets is not supported: `directus-init` refuses to start in that case. See the README, "Production".
