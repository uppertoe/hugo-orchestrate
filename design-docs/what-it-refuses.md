---
title: What it refuses
summary: The checks the builder applies to incoming notices, to its own settings, and to each build.
weight: 2
lastmod: 2026-11-02
---

The builder is designed to refuse anything doubtful rather than guess. A
refusal at the door costs nothing, a refusal of its settings stops it starting,
and a refused build leaves the previous version of the site online.

## Notices from GitHub

| Check | What the builder does when the check fails |
|---|---|
| The site named in the address exists and accepts notices | Answers "not found" and does nothing |
| The notice is no larger than 256 kilobytes | Refuses it unread |
| The signature matches the site's secret | Refuses it and logs the attempt |
| The notice concerns a push | Acknowledges it and does nothing; GitHub's test notice is answered as a success |
| The notice has not been delivered in the last ten minutes | Ignores it and logs the attempt, so a captured notice cannot be replayed to force builds |
| The push was to the site's own branch | Acknowledges it and does nothing, so work on other branches never reaches the live site |

A genuine notice can only ask the builder to rebuild a site from its own
repository. The notice carries no content the builder uses beyond the branch
name, so even a forged one that somehow passed the signature check could not
change what is published.

## Its own settings

The list of sites is a file in the server's repository. The builder reads it
when it starts and refuses to start at all if the file is wrong in any of
these ways:

- A setting it does not recognise, so that a misspelt option can never be
  silently ignored.
- Two sites with the same name, or two sites publishing to the same folder.
- A site that names a secret or an access token which turns out to be empty
  on the server. The builder never falls back to trying without it.
- A site that asks for a version of Hugo the builder does not carry.

Because a refused list stops every site from updating, a change to the list
that needs a newer builder must reach the server together with that builder.
The design-docs site was added this way.

A site with no webhook settings is allowed but logged as a warning at every
start, because it will only ever be built when the builder starts.

## Each build

The builder stops a fetch that takes more than two minutes and a build that
takes more than ten, and it refuses a build that produced no files. A failed
fetch or publish is tried twice more, after one and then two seconds, because
those failures are usually passing network or disk problems. A failed Hugo
build is not retried, because it will fail the same way until someone changes
the site.

## What the builder does not do

Some things are left out on purpose, because each would weaken the confinement
described in [Secrets and access](secrets-and-access.md):

- Sites cannot run their own build commands; only Hugo, with fixed options.
- Hugo Modules are not supported, because they need network access and a
  writable cache during the build. Themes must be vendored or included as Git
  submodules.
- Repositories are fetched over HTTPS with a token, never over SSH.
- Only GitHub can trigger a build. There is no polling and no other provider.
