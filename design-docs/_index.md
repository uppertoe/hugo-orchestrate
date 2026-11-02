---
title: The site builder
summary: Rebuilds the department's static websites from their Git repositories whenever someone pushes a change, and swaps each new version in whole.
status: live
contact: Eamonn Upperton
decided-by: Eamonn Upperton, for the department's data lab
weight: 3
lastmod: 2026-11-02
---

The department's homepage, the handbooks, the regional anaesthesia site and
this design-docs site are all static websites written in Markdown and built
with Hugo. The site builder is the small program on the
[department server](../server/_index.md) that turns each site's repository into
the pages people read. When someone pushes a change to a site, GitHub tells the
builder, the builder fetches the new version, builds it, checks the result and
puts it live in a single step. Caddy, the server's web server, then serves the
pages behind whatever login rule that site carries.

The builder is written in Go and lives in the `hugo-orchestrate` repository.
It runs as one container on the server, with no database and nothing that
needs backing up: every site it publishes can be rebuilt from its repository
at any time, and the builder does exactly that each time it starts.

These pages are written for someone who is comfortable with terms such as
Git, container and webhook but has not read the code.

## The pages

[How a change goes live](how-a-change-goes-live.md) follows a push from GitHub
to the published site and names each step on the way.
[What it refuses](what-it-refuses.md) lists the checks the builder applies to
requests, to its own settings and to each build.
[Secrets and access](secrets-and-access.md) explains what the builder holds,
who can reach it, and how it is confined on the server.
[When something goes wrong](when-something-goes-wrong.md) describes what
readers see when a build fails, and how the builder recovers.
[Sites built from several repositories](sites-from-several-repositories.md)
explains how a site such as this one gathers pages from other projects.
