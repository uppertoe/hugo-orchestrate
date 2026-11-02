---
title: Secrets and access
summary: What the builder holds, who can reach it, and how it is confined on the server.
weight: 3
lastmod: 2026-11-02
---

## What the builder holds

The builder holds two kinds of secret, both kept in its settings file on the
server and never in Git.

- **An access token for reading the repositories.** The department's site
  repositories are private, so the builder reads them with one GitHub token
  that can read every one of them. The builder hands the token to Git through
  environment settings that never appear in a command line, a web address or
  a file on disk, and it blanks the token out of any error message before
  logging it. The token is offered only to the host the repository lives on,
  so it is never sent to another site.
- **One webhook secret for each site.** GitHub signs each notice with the
  site's secret, and the builder checks the signature before it reads
  anything else. A secret opens nothing on its own: the most it lets someone
  do is ask for a rebuild of a site that is already published.

The builder holds no personal information. The sites it builds contain
department content only, and its records of each build name a commit, a time
and an outcome.

## Who can reach it

Caddy is the only way in from the internet, and it passes only a few
addresses on `hooks.rch-anaesthesia.org` to the builder:

| Address | Who can reach it | What it gives |
|---|---|---|
| `/webhook/<site>` | Anyone, but only a correctly signed notice has any effect | A rebuild of that site |
| `/healthz` and `/readyz` | Anyone | Whether the builder is running, and whether its first round of builds is done |
| `/status` | Staff signed in through the gateway with an `rch.org.au` address | The outcome of each site's last build, including any error message |

Caddy removes the gateway's identity headers and session cookie from every
request it passes to the builder, so the builder never holds anything that
would open another tool.

The published sites themselves are served by Caddy, not by the builder, and
each site's login rule is set in Caddy's configuration in the
[server's repository](../server/the-login-wall.md). The builder decides what
the pages contain, never who may read them.

## How it is confined

The builder runs in the same hardened container as every other tool on the
server. It runs as an unprivileged user, has every Linux capability removed,
cannot gain new privileges, and has a read-only root filesystem. It writes to
two volumes only: a working area for checkouts and builds, and the folder of
published sites. Caddy mounts only the published folder, and read-only, so
the web server can never see a checkout, a cache or a half-finished build.

The builder's image is rebuilt every week so that security fixes in its base
system reach it even when its code has not changed. Its tests must pass, and
the image must start and answer, before a new version is published. The
server's repository pins the image by its digest, so the server runs exactly
the version that was reviewed. The versions of Hugo inside the image are
downloaded when the image is built and checked against their published
checksums.

Only one copy of the builder may run against the published folder at a time.
A second copy finds the first one's lock and refuses to start, because two
builders publishing to the same folder could interleave their swaps.
