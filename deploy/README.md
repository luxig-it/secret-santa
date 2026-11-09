# Deploying

The app runs as one container on a server with Plesk. Plesk's nginx
terminates TLS (Let's Encrypt) and proxies to the container, whose port is
bound to 127.0.0.1 only.

Releases are built by GitHub Actions and published as images. Deploying a
release is a manual step on the server: set the version and pull. GitHub has
no access to the server at all.

```
git tag v1.2.0  ──►  Actions: test, build, sign  ──►  ghcr.io/luxig-it/secret-santa:1.2.0
                                                             │
server: SECRET_SANTA_VERSION=1.2.0, docker compose pull && up -d  ◄──┘
```

## Once: GitHub

The repository is public, so anybody can open pull requests. These
settings make sure only you can produce a release.

1. **Tag ruleset** – *Settings → Rules → Rulesets → New tag ruleset*:
   target `refs/tags/v*`, enable *Restrict creations*, *Restrict updates* and
   *Restrict deletions*, and add yourself (Repository admin) as the only
   bypass. Then nobody else can push a release tag.
2. **Actions** – *Settings → Actions → General*: set *Workflow permissions* to
   read-only, and require approval for workflows from fork pull requests
   (*all outside collaborators*). The CI workflow has no secrets anyway.
3. **Code security** – enable *Secret scanning* and *Push protection*, and
   *Dependabot alerts*. `.github/dependabot.yml` already proposes updates
   for actions, Go modules and the pinned images.
4. **Your account** – two-factor authentication, ideally with a passkey or
   hardware key. Whoever controls your account can tag a release.
5. **Package visibility** – after the first release, open the package
   *secret-santa* under your profile's *Packages* and make it public, so the
   server can pull without a token.

## Once: server

The server needs Docker with the compose plugin (the Plesk Docker extension
installs it, or follow https://docs.docker.com/engine/install/).

```sh
sudo mkdir -p /opt/secret-santa && cd /opt/secret-santa
V=v1.2.0   # the release you deploy
for f in compose.yaml env.example backup.sh; do
  sudo curl -fsSLO "https://raw.githubusercontent.com/luxig-it/secret-santa/$V/deploy/$f"
done
sudo mv env.example .env && sudo chmod 600 .env && sudo chmod +x backup.sh
sudo nano .env      # version, public URL, port, organizer keys, optional SMTP
sudo docker compose up -d
curl -s http://127.0.0.1:8080/healthz    # "ok" (or your SECRET_SANTA_HOST_PORT)
```

### In Plesk

1. Create the (sub)domain, e.g. `wichteln.luxig-it.de`, and secure it with
   *SSL/TLS Certificates → Let's Encrypt*. In *SSL It!* enable *Redirect
   from HTTP to HTTPS* and HSTS.
2. Proxy the domain to the container. Either:
   - **Docker Proxy Rules** (with the Docker extension): *Websites & Domains
     → the domain → Docker Proxy Rules → Add rule*, URL `/`, container
     `secret-santa-app-1`, port `8080`.
   - or **nginx directives**: *Apache & nginx Settings*, switch *Proxy
     mode* off, and add under *Additional nginx directives*:

     ```nginx
     location ~ ^/ {
         proxy_pass http://127.0.0.1:8080;   # your SECRET_SANTA_HOST_PORT
         proxy_set_header Host $host;
         proxy_set_header X-Real-IP $remote_addr;
         proxy_set_header X-Forwarded-Proto $scheme;
         client_max_body_size 64k;
     }
     ```

     (`location ~ ^/` instead of `location /`, which Plesk already
     defines.)
3. `SECRET_SANTA_BASE_URL` in `.env` must be exactly that `https://` address.
4. Check that the app sees real visitor addresses: log in once with a wrong
   key, then `sudo docker compose logs app | tail -1` must show your own IP
   as `remote`, not `172.x.x.x`. If it shows 172.x, nginx does not send
   `X-Real-IP` (add the header line above) or `SECRET_SANTA_TRUSTED_PROXY`
   is missing. Without it, everybody shares one login and mail limit, and
   ten wrong logins from anyone lock out every organizer for 15 minutes.

The port is bound to 127.0.0.1, so it is not reachable from outside even
though Docker bypasses the Plesk firewall for published ports.

## Every release

```sh
# On your machine, from an up-to-date main:
git tag v1.2.0 && git push origin v1.2.0
```

Wait for the *Release* workflow to finish. Optionally verify that the image
was built by that workflow from this repository:

```sh
gh attestation verify oci://ghcr.io/luxig-it/secret-santa:1.2.0 --repo luxig-it/secret-santa
```

Then on the server:

```sh
cd /opt/secret-santa
sudo sed -i 's/^SECRET_SANTA_VERSION=.*/SECRET_SANTA_VERSION=1.2.0/' .env
sudo docker compose pull && sudo docker compose up -d
sudo docker image prune -f
```

The database lives in the `data` volume and survives updates; new columns
are added automatically on start. To roll back, set the previous version
and run the same two commands – as long as the newer release added no
column the older one trips over, which so far none does.

If a release changes `compose.yaml`, fetch it again for
the new tag as above. The release notes say so when it matters.

## Backups

The whole state is one SQLite file. `backup.sh` takes a consistent copy
while the app keeps running and keeps 30 days of them in `backups/`:

```sh
sudo /opt/secret-santa/backup.sh
# nightly, via `sudo crontab -e`:
15 3 * * *  /opt/secret-santa/backup.sh
```

Copy `backups/` off the server too. To restore, stop the app, copy a
backup over `secret-santa.db` in the `secret-santa_data` volume (and
delete any `-wal`/`-shm` files next to it), and start the app again.

## Hardening already in place

- The app image is distroless, runs as a non-root user, and its base
  images are pinned by digest.
- The container runs with a read-only root filesystem, no Linux
  capabilities, `no-new-privileges` and memory and process limits; its
  port is bound to 127.0.0.1.
- The app sends a strict CSP, `no-referrer` and `no-store`; HSTS comes from
  Plesk.
- The app ignores `X-Real-IP` except from `SECRET_SANTA_TRUSTED_PROXY`, so
  clients cannot fake their address to dodge the login and mail limits.
- It refuses to start on `https://` with an organizer key from the
  examples (`REPLACE-…`, `change-me`, `dev-organizer-token`, …).
