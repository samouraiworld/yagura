# Security policy

## Reporting a vulnerability

Please report vulnerabilities through GitHub private vulnerability reporting
(the "Report a vulnerability" button on the repository's Security tab). Do not
open a public issue for security problems.

## Supported versions

Only the latest minor release receives security fixes.

## Scope

Yagura is read-only toward providers by design: only `GET` requests are sent
to provider APIs, and the exporter never changes anything at a provider. When
an HTTPS proxy is configured, the transport opens a `CONNECT` tunnel to that
proxy. Reports about token handling are in scope, for example a secret leaking
into logs, metrics, error messages or the landing page. Secrets are read from
environment variables or `*_FILE` paths and must never appear in output.

The optional budget write API (off by default) is the only write surface: it
changes yagura's own local budget file and nothing else. It listens apart from
`/metrics`, needs a bearer token compared in constant time, and is meant for
loopback or an internal network only. Reports about it are in scope, for
example an unauthenticated change, a token comparison that leaks timing, a
write outside the state directory, or a crafted request that corrupts the
budget file.
