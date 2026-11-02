---
title: How a change goes live
summary: The path from a push on GitHub to a published site, and how the builder handles several pushes at once.
weight: 1
lastmod: 2026-11-02
---

Nobody copies files to the server by hand. A change to a site is a commit on
that site's main branch, and the builder publishes it within about a minute.

## The path

1. **A push.** Someone pushes to the site's repository on GitHub. GitHub sends
   a short notice, called a webhook, to `hooks.rch-anaesthesia.org`, signed
   with a secret that only GitHub and the builder know.
2. **The checks at the door.** The builder confirms the notice is genuine,
   that it concerns a push and not some other event, that it has not been seen
   before, and that the push was to the branch the site is built from. A
   notice that fails any of these is turned away or ignored, as set out in
   [What it refuses](what-it-refuses.md).
3. **The queue.** The builder places the site in its queue and answers GitHub
   at once. It builds one site at a time, because the server has only two
   gigabytes of memory and two builds together have exhausted it before.
4. **The fetch.** The builder fetches only the latest commit of the branch,
   together with any submodules the site uses for its theme or content, and
   records which commit it is building.
5. **The build.** The builder runs Hugo on the checkout with a fixed set of
   options. A site cannot supply its own build command. A build that runs
   longer than ten minutes is stopped, and Hugo is given a soft memory
   ceiling so that it slows down rather than exhausting the server.
6. **The check.** The builder refuses an empty result, so that a broken build
   can never replace a live site with nothing.
7. **The swap.** The builder moves the new version into place under the
   site's live folder in one rename and then removes the old version. Caddy
   never serves a mixture of old and new pages. The builder keeps the last five
   builds of each site in its working area and deletes older ones.
8. **The record.** The builder writes down the outcome: the commit, how long
   the build took, whether it succeeded and, if not, why. The status page at
   `hooks.rch-anaesthesia.org/status` shows these records to signed-in staff.

## Several pushes at once

Each site has a queue that holds at most one waiting build. If a push arrives
while that site is already building, the builder notes that one more build is
needed, and any further pushes in the meantime join that same waiting build.
When the current build finishes, the builder runs once more from the latest
commit. A burst of ten pushes therefore produces two builds, and the second
always reflects the newest work.

## At start-up

Whenever the builder starts, for example after the server restarts or a new
version of the builder is deployed, it builds every site once from scratch.
The builder reports itself ready only when that first round is complete. This
is also why the builder needs no backup: a fresh server repopulates every site
on its first start.
