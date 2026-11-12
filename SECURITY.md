# Security policy

snxwboard runs on your machine and sees what your coding agents do, so we take reports seriously.

## Report a vulnerability

Use GitHub's private reporting: **Security → Report a vulnerability** on this repository
(<https://github.com/snxwfairies/snxwboard/security/advisories/new>). Do not open a public issue.

Include the version (`snxw version`), your OS, what an attacker can do, and steps to reproduce. We aim to
acknowledge a report within 3 working days and to ship a fix for a confirmed issue within 30 days. We will credit
you in the advisory unless you ask us not to.

## Supported versions

Only the latest release and `main` get security fixes until v1.0.

## In scope

- Any way for another local user, a web page in your browser, or a remote host to read events, the token, or
  change hook settings.
- Secrets that reach the database without being redacted.
- A way to make an agent hang or fail through snxwboard.

The daemon binds `127.0.0.1` and checks a per-install bearer token on every request. Running it on another
interface is not supported.
