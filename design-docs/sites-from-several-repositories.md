---
title: Sites built from several repositories
summary: How a site gathers pages from other repositories, and why a push to any of them rebuilds the site.
weight: 5
lastmod: 2026-11-02
---

Most sites live in a single repository. This design-docs site is different:
it holds no pages of its own, and each project keeps its pages in a
`design-docs` folder in the project's own repository. The site repository
includes each project as a Git submodule, and Hugo reads only the
`design-docs` folder from each one.

## Following the latest version

Ordinarily a submodule is fixed at the commit the site repository records, so
a project's change would reach the site only after someone moved that
pointer. For a site like this one, that step would be easy to forget and
would add nothing.

A site can therefore be marked so that every build takes each submodule at the
latest commit on its own branch, whatever the site repository records. The
design-docs site is marked in this way. A project's change still has to be
merged to that project's main branch before it appears, so the project's own
review applies as before.

## Who triggers the build

Each contributing project's repository sends its webhook to the design-docs
site's address on the builder, signed with the site's secret. A push to a
project's main branch then rebuilds the whole design-docs site from the
latest version of every project. Pushes to other branches are ignored, as for
any site.

## Limits

The builder's single token reaches private repositories on GitHub, so a
private submodule on GitHub works. A private submodule hosted anywhere else
would need its own credentials, which the builder does not provide.

The design-docs site checks each project's pages against its own rules as it
builds, and a page that breaks them fails the build. As with any failed build,
the last good version of the whole site stays online, and the status page names
the project and file at fault.
