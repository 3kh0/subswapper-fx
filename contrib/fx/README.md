# fx subscription balancing

This adapter lets fx use Subswapper's Codex proxy. Keep two separate ChatGPT
subscription logins in two `CODEX_HOME` directories. Subswapper reads their
Codex usage and routes fx's next model request to the account with more
remaining headroom. When their highest used quota windows are within two
percentage points, it alternates requests. On a quota rejection, the proxy
retries the request through the other account.

fx continues to use its own refreshable Codex login. Set
`SUBSWAPPER_FX_AUTH_FILE` to fx's `~/.fx/chatgpt-auth.json`; the proxy accepts
the current access token from that file and replaces it with the selected
Codex account token upstream. The file is read afresh for each request, so fx
can refresh its login normally. Keep the proxy bound to loopback.

## Set up on macOS

1. Build this fork: `go build -o subswapper-fx ./cmd/subswapper`.
2. Sign in to each account separately with `CODEX_HOME=<directory> codex login`.
   Confirm they are distinct accounts and each has file-backed `auth.json`.
3. Copy `config.example.json` to a private config path. For existing Codex
   homes, create `accounts/codex/primary` and `accounts/codex/second` as
   symlinks to those homes under the configured `backup_root`. Register each
   with `subswapper-fx home create -config <config> -service codex -account
   <name>`. This keeps token refresh in the original Codex home rather than
   making a second copy of the refresh token.
4. Start the service with `SUBSWAPPER_FX_AUTH_FILE=<fx-auth-path>` and
   `SUBSWAPPER_CODEX_BALANCE=1` in its environment:

   ```sh
   subswapper-fx monitor -config <config> -interval 1m -no-auto -no-warmup -proxy
   ```

   Run it from a macOS LaunchAgent with `RunAtLoad` and `KeepAlive` for normal
   daily use.
5. Preserve the installed fx binary as `fx-direct`. Install `fx-wrapper` as
   `fx` beside it. The wrapper pins the currently installed fx binary and
   points its Codex responses request at the local proxy. Other fx providers
   continue to use their own endpoints.

`subswapper-fx status -config <config>` shows each account's usage and the
selected route. `fx status` still reports fx's own login; it does not report
which account served the previous proxied request.

## Compatibility

The fx wrapper uses `FX_E2E_OPENAI_CODEX_RESPONSES_URL`, an internal endpoint
override present in fx 0.0.11. It is not a documented configuration contract.
Keep `fx-direct` pinned until a newer fx binary is checked with this adapter.
The proxy switches between HTTP requests; it cannot move a response that is
already streaming. Model availability may differ between subscriptions.

The proxy never needs a second copy of either Codex refresh token when its
account homes point to the existing `CODEX_HOME` directories. Do not commit
`auth.json`, `chatgpt-auth.json`, Subswapper state, or local logs.
