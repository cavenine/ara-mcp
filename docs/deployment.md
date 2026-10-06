# Persistent HTTP deployment

This example runs ara-mcp as a non-root systemd service with its archive in a
dedicated state directory. The MCP listener, diagnostics listener, and Ara API
default to loopback; expose remote access only through TLS, using a trusted reverse
proxy or ara-mcp's configured TLS. The proxy must preserve Streamable HTTP streaming
and forward the expected Host/Origin headers.

## Install

Build a Linux/ARM64 executable on a build host, then copy it to the Ara computer:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags='-s -w' \
  -o ara-mcp ./cmd/ara-mcp
sudo install -o root -g root -m 0755 ara-mcp /usr/local/bin/ara-mcp
sudo install -d -o root -g root -m 0750 /etc/ara-mcp
```

Create `/etc/ara-mcp/ara-mcp.env`, owned by root and mode `0600`:

```sh
ARA_MCP_TRANSPORT=http
ARA_MCP_ARA_URL=http://127.0.0.1:5555
ARA_MCP_HTTP_LISTEN=127.0.0.1:8080
ARA_MCP_HTTP_BEARER_TOKEN=replace-with-at-least-32-random-characters
ARA_MCP_DIAGNOSTICS_LISTEN=127.0.0.1:9090
ARA_MCP_RESOURCE_ARCHIVE_DIR=/var/lib/ara-mcp/resources
```

Generate a unique bearer token; don't reuse a sample value or put credentials in
URLs. Diagnostics are a separate read-only listener and do not share MCP access.
Keep both listeners on loopback when a local reverse proxy provides remote TLS.
Without a proxy, configure ara-mcp's HTTP TLS certificate/key and bind MCP to a
non-loopback address; remote diagnostics likewise require separate Basic-auth
credentials and TLS. Never expose either listener as plaintext on a network.

For direct TLS on the diagnostics listener, keep certificate files root-owned and
load them through systemd credentials so the dynamic service user can read them
without making the private key world-readable. Set the diagnostics address and
separate Basic-auth credentials in the environment file, install the certificate
and key as `/etc/ara-mcp/diagnostics.crt` and `/etc/ara-mcp/diagnostics.key` with
mode `0600`, then add this systemd drop-in:

```ini
# /etc/systemd/system/ara-mcp.service.d/diagnostics-tls.conf
[Service]
LoadCredential=diagnostics.crt:/etc/ara-mcp/diagnostics.crt
LoadCredential=diagnostics.key:/etc/ara-mcp/diagnostics.key
ExecStart=
ExecStart=/usr/local/bin/ara-mcp --diagnostics-tls-cert=%d/diagnostics.crt --diagnostics-tls-key=%d/diagnostics.key serve
```

Reload and restart after installing the drop-in. `%d` expands to the unit's
credential directory; systemd supplies the files read-only to the service. Use a
certificate whose SAN matches the browser hostname/IP and a trusted chain in
production.

Install and enable the unit:

```sh
sudo install -o root -g root -m 0644 docs/ara-mcp.service \
  /etc/systemd/system/ara-mcp.service
sudo systemctl daemon-reload
sudo systemctl enable --now ara-mcp.service
sudo systemctl status ara-mcp.service
```

The unit uses `DynamicUser=yes`; systemd creates the writable
`/var/lib/ara-mcp` state directory for the optional bounded archive. The binary and
environment file remain root-owned and read-only to the service. Logs go to
journald:

```sh
sudo journalctl -u ara-mcp.service
sudo journalctl -u ara-mcp.service --since '1 hour ago'
```

Native Go profiles are available only when `ARA_MCP_DIAGNOSTICS_PPROF=true` is set.
Enabling profiling requires the diagnostics Basic-auth username/password even when
the listener is loopback-only. Profiles are served under `/debug/pprof/` on the
diagnostics listener, never on `/mcp`; disable the setting when capture is finished.

Configure the MCP client for `https://<proxy-host>/mcp` and send
`Authorization: Bearer <token>` as an HTTP header. The proxy must not buffer the
MCP response or impose a short response timeout. Diagnostics can stay on loopback
and be accessed locally at `http://127.0.0.1:9090/`.

## Update and rollback

Before maintenance, use `end_control` and inspect Ara's current run state. Ending
control or restarting ara-mcp does not stop an accepted Ara run. After restart, the
previous local control ID and receipts are invalid; inspect Ara's authoritative state
and explicitly begin control again. Uncorrelated work remains unknown, not completed.

Keep the previous executable and environment file until the replacement has started
and its diagnostics are healthy. For example:

```sh
sudo cp -a /usr/local/bin/ara-mcp /usr/local/bin/ara-mcp.previous
sudo cp -a /etc/ara-mcp/ara-mcp.env /etc/ara-mcp/ara-mcp.env.previous
# Install the replacement binary/configuration, then:
sudo systemctl restart ara-mcp.service
sudo systemctl --no-pager --full status ara-mcp.service
sudo journalctl -u ara-mcp.service --since '5 minutes ago'
```

If startup or validation fails, restore the previous binary and configuration and
restart. Confirm current Ara state after either upgrade or rollback; the adapter
does not resume or replay operations automatically.

## Validation

The default test suite is hardware-free. For the pinned Ara/OmniSim sequence lifecycle
recipe and required setup, see [development.md](development.md#t04-ara-control-session-check-opt-in)
and [T10's RPi4 live validation](development.md#t10-rpi4-live-validation-opt-in).
HTTP transport protocol/session coverage is exercised by
`go test -count=1 ./internal/httpmcp`; process startup, archive flushing, and graceful
shutdown are covered by `go test -count=1 ./internal/app`. The T10 opt-in integration
flow runs through both the SDK Streamable HTTP client and a local stdio process against
the pinned simulator. Record daemon build, adapter commit, transport, system image,
and simulator versus physical equipment when running live checks.
