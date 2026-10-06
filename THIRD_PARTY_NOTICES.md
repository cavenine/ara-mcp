# Third-party notices

ara-mcp is licensed under [AGPL-3.0-or-later](LICENSE). Runtime Go modules are
compiled into the executable. Their exact direct and transitive versions are pinned
in [`go.mod`](go.mod) and [`go.sum`](go.sum); source module archives retain their
upstream license and copyright files.

Direct dependencies and their upstream source/notice locations:

| Module | Version | Upstream notices |
| --- | --- | --- |
| `github.com/coder/websocket` | `v1.8.12` | [repository](https://github.com/coder/websocket) |
| `github.com/go-chi/chi/v5` | `v5.3.2` | [repository](https://github.com/go-chi/chi) |
| `github.com/go-chi/render` | `v1.0.3` | [repository](https://github.com/go-chi/render) |
| `github.com/go-resty/resty/v2` | `v2.17.2` | [repository](https://github.com/go-resty/resty) |
| `github.com/google/jsonschema-go` | `v0.4.3` | [repository](https://github.com/google/jsonschema-go) |
| `github.com/modelcontextprotocol/go-sdk` | `v1.8.0` | [repository](https://github.com/modelcontextprotocol/go-sdk) |
| `github.com/prometheus/client_golang` | `v1.24.1` | [repository](https://github.com/prometheus/client_golang) |
| `github.com/spf13/cobra` | `v1.10.2` | [repository](https://github.com/spf13/cobra) |
| `github.com/spf13/pflag` | `v1.0.10` | [repository](https://github.com/spf13/pflag) |
| `github.com/spf13/viper` | `v1.21.0` | [repository](https://github.com/spf13/viper) |
| `github.com/starfederation/datastar-go` | `v1.2.2` | [repository](https://github.com/starfederation/datastar-go) |
| `go.opentelemetry.io/otel` and its modules | `v1.47.0` | [repository](https://github.com/open-telemetry/opentelemetry-go) |
| `go.opentelemetry.io/otel/exporters/prometheus` | `v0.69.0` | [repository](https://github.com/open-telemetry/opentelemetry-go-contrib) |

The repositories above contain the applicable license texts and notices. Transitive
modules are listed in `go.mod`; their exact downloaded content is checksummed in
`go.sum`, and their license texts are available from the corresponding module source.
Release archives include this index and the project AGPL license. Consult each
upstream license and notice before redistributing a modified build.

The bundled `.agents/skills/` material is development guidance, not part of the
ara-mcp executable or runtime artifacts; its separate attribution is recorded in
[`.agents/README.md`](.agents/README.md).
