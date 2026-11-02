---
title: When something goes wrong
summary: What readers see when a build fails, and how the builder recovers from failures and crashes.
weight: 4
lastmod: 2026-11-02
---

## A build fails

If a site fails to build, readers notice nothing: the previous version stays
online until a later build succeeds. The builder records the failure, with the
tail of Hugo's own message, in the site's status record and in its log. The
author can read the reason on the status page at
`hooks.rch-anaesthesia.org/status` and push a fix, which the builder picks up
in the usual way.

A failure in one site does not hold up the others. Each site is built and
published separately.

## The server or the builder restarts

When the builder is asked to stop, it accepts no new notices and gives any
build already running thirty seconds to finish before ending it. A notice that
arrives while the builder is stopping is refused, and GitHub records the
failed delivery.

If the builder stops mid-way through a swap, for example because the server
lost power, the next start clears away any half-staged or superseded copies
it finds before it builds anything. At worst, a site is briefly missing until
the start-up round of builds publishes it again.

Pushes made while the builder is down are not lost in practice, because the
start-up round builds every site from the latest commit on its branch.

## A push does not appear

If a change does not appear on the site within a minute or two, the cause is
usually one of these, in rough order of likelihood:

- The push was to a branch other than the one the site is built from.
- The build failed, and the status page gives the reason.
- The repository's webhook is missing or has the wrong secret. GitHub's
  webhook settings show each delivery and the builder's answer to it.
- The browser is showing a cached copy. Pages are served with an instruction
  to check for a newer version, so a reload normally fixes this.

## Limits of the swap

The swap moves the old version aside and the new version into its place in
two renames on the same disk. Readers never see a half-copied site, but in the
instant between the two renames the site's folder is absent, and a request
that lands in that instant receives the site's "page not found" response. The
gap lasts a fraction of a millisecond and has not been seen in practice.
