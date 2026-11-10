# Security policy

## Reporting a vulnerability

Please report vulnerabilities through GitHub private vulnerability reporting
(the "Report a vulnerability" button on the repository's Security tab). Do not
open a public issue for security problems.

## Supported versions

Only the latest minor release receives security fixes.

## Scope

Yagura is read-only by design: its HTTP client can only issue `GET` requests,
and the exporter never changes anything at a provider. Reports about token
handling are in scope, for example a secret leaking into logs, metrics, error
messages or the landing page. Secrets are read from environment variables or
`*_FILE` paths and must never appear in output.
