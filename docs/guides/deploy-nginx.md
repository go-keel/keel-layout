# Deployment: nginx in front of the service, and the client IP

<!-- Shared file: carried verbatim; never localized outside its source repository.
     Checked by scripts/check-cross-repo-sync.sh; see docs/upgrading-protocol.md. -->

Read this before putting nginx in front of the service, connecting Cloudflare or CloudFront, or investigating a wrong
client IP in the logs.

Keel trusts `X-Forwarded-For` only from the connection peer and listed proxies, and by default lists only the local
host; so the proxy must overwrite or append that header, or the value the visitor sent reaches the service unchanged.
Rules and reasons: [Metadata and the client IP](keel:docs/metadata.md). This guide covers deployment only.

## Files

| File | Where it goes on the server | What it does |
|---|---|---|
| `deploy/nginx/conf.d/client-ip.conf` | `/etc/nginx/conf.d/` (loaded at the http level) | defines how the client IP is computed and the `client_ip` log format; changes no site's requests by itself |
| `deploy/nginx/snippets/client-ip-headers.conf` | `/etc/nginx/snippets/`, **never in `conf.d/`** | the enabling snippet: a site that includes it overwrites the forwarding headers and strips the secret header |
| `deploy/nginx/site.conf.example` | write site configs after it | plain site, WebSocket, gRPC, the CloudFront-only entry, the default site |

These are shared files; the repository copies follow the template verbatim. Secrets and ports go only into the copy
on the server, never into the repository.

## How the client IP is decided

The first rule that holds wins:

1. The request carries the right Cloudflare origin secret (header `X-Origin-Secret-CF`) and `CF-Connecting-IP` is a
   single address → that address. Both "Cloudflare only" and "Cloudflare then CloudFront" take this path.
2. The request came in on the CloudFront-only port and `CloudFront-Viewer-Address` ("IP:port") is valid → its address.
3. Otherwise → whoever connected to nginx.

Anything unrecognised falls to rule 3 instead of being rejected: it cannot be forged; at worst the address is a CDN
node's.

On an enabled site (one that includes the snippet) only these headers to the service change, written once for
`proxy_pass` (`proxy_set_header`) and once for `grpc_pass` (`grpc_set_header`):

| Header | Content |
|---|---|
| `X-Forwarded-For`, `X-Real-IP` | the computed client IP, a single value, overwritten rather than appended: whichever the service reads, and whichever item, it is that address |
| `X-Raw-Forwarded-For`, `X-Raw-Real-IP` | the original values nginx received (including what a CDN wrote), **for logging only**; absent when the request had none, and a visitor's own headers of these names are overwritten |
| `X-Origin-Secret-CF` | the secret, stripped |

Every other header passes through, including `CF-Connecting-IP`, `CloudFront-Viewer-Address`, `True-Client-IP` and
`Forwarded`. The visitor may have written them, so they are for logging only, never for deciding the client IP. The
service reads the client IP only through `metadata.GetRemoteIP`, never from headers.

## Layer one: install and enable (also without a CDN)

nginx needs the realip module: the main file uses its `$realip_remote_addr` to get the connecting party, unaffected
by realip settings of other sites on the same host. Official packages and distributions include it; a source build
needs `--with-http_realip_module`. Check:

```bash
nginx -V 2>&1 | grep -o with-http_realip_module
```

Install:

```bash
install -o root -g root -m 600 deploy/nginx/conf.d/client-ip.conf /etc/nginx/conf.d/
install -d /etc/nginx/snippets
install -o root -g root -m 644 deploy/nginx/snippets/client-ip-headers.conf /etc/nginx/snippets/
nginx -t && nginx -s reload
```

The main file is root-owned with mode 600 because it holds secrets once filled in. With its configuration section
left as placeholders (all commented out), layer two is off and only layer one applies.

**When upgrading the template:** carry the configuration section (between its two marker lines) of the old file on
the server into the new file unchanged, and take everything below the marker from the new file.

Installing the main file only prepares the rules; **each site enables them explicitly**: following
`deploy/nginx/site.conf.example`, write `include /etc/nginx/snippets/client-ip-headers.conf;` in every server block
that forwards to the service. Sites without the include are unaffected, so other sites on the same nginx keep working
as before. The main file's definitions are only valid at the http level, hence `conf.d/` (common packages' `nginx.conf`
loads `conf.d/*.conf` inside `http { }`; if yours does not, include the main file inside `http { }` yourself).

For logs, write `access_log <path> client_ip;` in the server: one JSON object per line with the route (`direct` /
`cf` / `aws` / `cf+aws`), the label of the matched secret `cf_key`, the computed address, the connecting party, the
original address headers and `CF-Ray`; never the secret itself.

## Where to include the snippet

1. **Every server block that forwards to the service** gets `include /etc/nginx/snippets/client-ip-headers.conf;`;
   its locations that set no forwarding headers of their own inherit it.
2. **A location that writes its own `proxy_set_header` or `grpc_set_header` includes it again**, followed by its own
   headers. nginx stops inheriting the whole set of such directives from the outer level as soon as a level writes any
   of them; a WebSocket location adding `Upgrade` is such a case.

Any missed place lets the visitor's own `X-Forwarded-For` reach the service, which trusts local connections by default,
so the client IP can be forged. That is why the acceptance checks with forged headers run against every site and every
location with its own forwarding headers.

## Layer two (optional): which provider needs what

The configuration section of the main file has two independent blocks, the Cloudflare secret and the CloudFront-only
port. Configure the one for the provider you use; an empty one is off.

| Deployment | Cloudflare secret | CloudFront-only port |
|---|---|---|
| no CDN | empty | empty |
| Cloudflare only | set | empty |
| CloudFront only | empty | set |
| Cloudflare → CloudFront | set | recommended: requests that reach CloudFront without Cloudflare otherwise get a CloudFront node's address (not forgeable); with it they get the real one |

CloudFront needs no secret: only VPC origins are supported, and the security group guarantees that only this account's
CloudFront reaches the port, so there is no secret to leak. Cloudflare's origin address ranges are shared by all its
customers, so only a secret tells our sites apart from anyone else's.

## Layer two (optional): Cloudflare

1. "SSL/TLS" encryption mode: **Full (strict)**. "Full" does not verify the certificate, so the origin hop can be
   impersonated and the secret leaks with it.
2. Generate a secret:

   ```bash
   openssl rand -hex 16
   ```

   Add it to the secret table in the configuration section, one line `<secret> "<site>:<label>";`. When all sites
   share one secret, leave the site empty: `<secret> ":<label>";`. The label names this secret; it is logged as
   `cf_key` and tells old and new apart during rotation, so choose a short name that does not contain the secret (the
   month it was enabled, `2026-11`, or `k1`, `k2`). The secret is lowercase hex, so nginx's case-insensitive match
   costs no strength; do not make it longer either, since overlong keys make `nginx -t` complain about
   `map_hash_bucket_size` on some machines.

   **A different secret per site** (several Cloudflare sites on one nginx, and one site's secret must not work on
   another): in the site table of the configuration section write `<domain> <site>;` (`api.a.com` for one domain,
   `.a.com` for a.com and all subdomains), and in the secret table `<secret> "<site>:<label>";`. **A domain listed in
   the site table accepts only its own site's secret, not the global one**; unlisted domains accept only the global
   secret. So add the site table entry and the secret table entry together: the site entry alone sends that domain's
   whole Cloudflare route to `direct`.
3. "Rules → Transform Rules → Modify Request Header": **set** the static header `X-Origin-Secret-CF` to the secret.
   Match **only the host names that reach our nginx or CloudFront** (such as `http.host in {"api.example.com"}`), not
   the whole zone: a subdomain on a third-party service (hosted website, status page) would otherwise receive the
   secret with every request. Include host names that go through CloudFront. Add any new host name that reaches us to
   this rule later. Every host name listed here needs its nginx server to include the snippet, because a site without
   it does not strip the secret header and passes the secret on to its backend.
4. Keep the managed transform "Remove visitor IP headers" **off**; with it on `CF-Connecting-IP` never reaches nginx
   and every address becomes a Cloudflare node's.
5. Block requests from other zones' Workers with a security rule: `cf.worker.upstream_zone ne ""` (exclude your own
   zone if you use Workers). Another zone's Worker request through our site carries the secret, and Cloudflare writes
   the same placeholder address into `CF-Connecting-IP` for all such requests, so they share one client IP and trip
   each other's per-address rate limits.
6. An entry that Cloudflare reaches nginx on directly can use a Cloudflare origin certificate ("SSL/TLS → Origin
   Server"): only Cloudflare connects there and it accepts that certificate. That entry's security group admits only
   Cloudflare's origin ranges.

**Rotating the secret:**

1. Add a line with the new secret to the secret table, same site as the old line, new label;
   `nginx -t && nginx -s reload`. Both secrets are accepted now.
2. Change the Cloudflare transform rule to the new secret.
3. Watch the access log: once every `cf` / `cf+aws` request shows the new label in `cf_key` and the old one has not
   appeared for a while (at least one business peak), Cloudflare is fully switched. An old label still showing means
   the rule is incomplete (a missed host name, or another rule writing the header).
4. Delete the old line, `nginx -t && nginx -s reload` again.
5. Verify the old secret is revoked: connect to nginx directly once with the old secret and `CF-Connecting-IP:
   1.2.3.4`; the log should show `route=direct`, an empty `cf_key`, and a `client_ip` other than `1.2.3.4`.

## Layer two (optional): CloudFront

Only **VPC origins** (private connection) are supported, not CloudFront reaching the origin over the internet.

1. Open a separate nginx port for CloudFront (8088, say; see the commented-out block in
   `deploy/nginx/site.conf.example`) and add `"8088" cloudfront;` to block 3 of the configuration section. **Never
   use 80 or 443**: every request on this port is treated as coming from CloudFront and its client address is
   believed, while 80 and 443 usually also serve Cloudflare or direct visitors.
2. Security group: this port admits **only** `CloudFront-VPCOrigins-Service-SG`, that is only this account's CloudFront
   distributions. Admit nothing else (Cloudflare, direct visitors, load balancers, health checks), or they can forge
   the client IP with their own `CloudFront-Viewer-Address`. Protocol HTTP.
3. Point CloudFront at this port of this machine through a VPC origin. Behaviour: cache policy `CachingDisabled`;
   origin request policy the managed `AllViewerAndCloudFrontHeaders-2022-06` (`Host`, `CF-Connecting-IP`,
   `X-Origin-Secret-CF` and `CloudFront-Viewer-Address` must all reach the origin).
4. Do not share the VPC origin with other accounts through AWS RAM; shared, "only our distributions get in" no longer
   holds.

## Service-side config

- nginx on the same host as the service: nothing to configure; Keel trusts only the local host by default, and an
  enabled site overwrites `X-Forwarded-For`.
- nginx on another machine (a separate container on the same host included): add its address to
  `client_ip.trusted_proxies` of the server concerned (example in `configs/config.yaml`), and `127.0.0.1` too if
  forwarding also arrives locally
  ([How the client IP is computed](keel:docs/metadata.md#how-the-client-ip-is-computed)).
- nginx forwarding gRPC with `grpc_pass` runs on the same host as the service: the gRPC server trusts only the local
  host.
- A local tool that forwards connections unchanged (socat, frp tcp, an ssh reverse tunnel) connects from the local
  host without writing forwarding headers: set `client_ip.ignore_forwarded_for: true` on the server it reaches.
- Never expose the service port to the internet: on a direct connection the visitor writes the forwarding headers.
  With nginx on the same host, set `addr` to `127.0.0.1:<port>`.
- **Your own load balancer in front of nginx:** do not use this nginx config, since it would overwrite the chain with
  what nginx sees (the load balancer). Make nginx append instead,
  `proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;`, and list both the load balancer and nginx in
  `trusted_proxies`.
- **A CDN connecting to the service directly** (without your nginx) counts as exposing the service to the internet and
  is not supported. Never list a CDN's public ranges as trusted: the whole CDN shares them, other customers' edge
  functions included.

## Acceptance checks

Run each one and read `route` and `client_ip` in the nginx access log (format `client_ip`); replace `example.com`
with the real domain.

| Action | Expected |
|---|---|
| connect to nginx directly with `X-Forwarded-For: 1.2.3.4`, `X-Real-IP: 1.2.3.4`, `CF-Connecting-IP: 1.2.3.4`, `CloudFront-Viewer-Address: 1.2.3.4:1` | `route=direct`, `client_ip` is your egress address |
| connect to nginx directly with a wrong `X-Origin-Secret-CF` and `CF-Connecting-IP: 1.2.3.4` | `route=direct`, address as above |
| go through Cloudflare with the headers above | `route=cf` (`cf+aws` through CloudFront), `client_ip` is your egress address |
| go through CloudFront with the headers above | `route=aws`, `client_ip` is your egress address. **This verifies that CloudFront overwrites a visitor's own `CloudFront-Viewer-Address`**, which AWS does not document, so it must be tested; if `1.2.3.4` appears, disable the CloudFront route |
| repeat each route over IPv6 | `client_ip` is an IPv6 address; a CDN node's address means a header format differs from what is expected, check the raw values in the log |
| for **every site forwarding to the service** and **every location in it with its own forwarding headers** (WebSocket, say), send `X-Forwarded-For: 1.2.3.4` | the client IP in the service log is not `1.2.3.4`; if it is, that server or location misses the include |
| for a site forwarding to the gRPC port with `grpc_pass`, call an endpoint with `X-Forwarded-For: 1.2.3.4` (`grpcurl -H 'X-Forwarded-For: 1.2.3.4' ...`) | the client IP in the service log is not `1.2.3.4`. To see the headers reaching the backend, temporarily point `grpc_pass` at an nginx server with `http2 on` and log `$http_x_forwarded_for` and friends with `log_format` |
| connect to the service port from outside | refused |

## Limits

- **The provider is the root of trust:** through a CDN, the address is what Cloudflare or CloudFront wrote, and it is
  believed.
- **The real address is whatever connected to the provider's edge or to nginx:** behind a VPN, a proxy or a carrier's
  shared egress, it is that egress.
- **A leaked secret can only be rotated:** with it, anyone can connect from their own Worker and fill in
  `CF-Connecting-IP`. For more, add Cloudflare's per-zone Authenticated Origin Pulls (mutual TLS) on the direct entry.
  The CloudFront route does not depend on the secret.

## Related

- [Metadata and the client IP](keel:docs/metadata.md): Keel's rules and config
- `docs/guides/config.md`: config file structure
