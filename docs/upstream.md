# Upstream attribution

ESF is an independent fork of [Machinist](https://github.com/owainlewis/machinist),
originally authored by Owain Lewis under the MIT License. The original copyright
and license terms are retained in [LICENSE](../LICENSE).

Factory development began from upstream commit `7b08de0` on `main`. It adds
Temporal orchestration, CubeSandbox execution, agent harnesses, verification and
durable evidence. Initial ESF publication is a sanitized source snapshot,
excluding local credentials, deployment discovery records and execution reports.

The fork's Go module is `github.com/mitkox/esf`. The inherited command remains
`machinist` for compatibility; the factory command is `factory`. ESF is maintained
at [mitkox/esf](https://github.com/mitkox/esf), independently of upstream releases,
website and security reporting.

The execution provider uses the [Tencent CubeSandbox SDK](https://github.com/tencentcloud/CubeSandbox).
ESF consumes an operator-managed deployment and does not install or alter Cube
components or templates. Dependencies are pinned in [go.mod](../go.mod).

Attribution for adapted Temporal configuration is in its
[README](../deployments/dev/temporal/README.md). Third-party Go and npm components
retain their respective licenses; the root MIT license covers this repository's
code and its inherited MIT-licensed Machinist code.
