# Admin UI static assets

These files are placeholders so `go build` works without Docker. The **real**
assets are downloaded from upstream at Docker build time — see the `curl`
step in the repo-root `Dockerfile` (`ARG DS_VERSION`, `ARG HTMX_VERSION`).

To refresh the design system or htmx in built images, bump `DS_VERSION` or
`HTMX_VERSION` in `Dockerfile`. Do not use branch names such as `@main` for
production assets; CDN edge caches can serve stale branch-tip files.

Do **not** commit real builds of `dzarlax.css`, `dzarlax.js`, or
`htmx.min.js` — they bloat diffs and go stale.

`marked.min.js` is intentionally committed as a small local runtime asset so
the Chat tab does not depend on a browser-time CDN request.
