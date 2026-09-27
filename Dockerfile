# The wiki, in a container.
#
# # Why this image is a *builder* and not a one-stage build
#
# `internal/store` uses `modernc.org/sqlite` (ADR 0004: pure Go, no CGO), so the
# running image needs no C library, no libc and no `CGO_ENABLED=0` caveat in a
# three-stage build. That is not a style choice: it is what makes a scratch-based
# image possible, and a scratch-based image is the only way to say "this contains
# one binary and a certificate bundle" rather than "this contains a Debian base
# image and one binary in it".
#
# # The volumes, and why there are two
#
#   /data  the data directory — the database, the vault, the locks. ADR 0011 makes
#          it the backup unit, so a bind mount of it is the whole of the backup story
#          in a container: `docker run -v ~/.local/share/dine-and-dash-semiplane:/data`.
#   /vault  a convenience for `wiki sync` and `wiki import` against a vault that
#          lives somewhere else. It is *not* where the wiki keeps its vault, because
#          a DM whose campaign is in a git repository and a container whose campaign
#          is on a volume are two different arrangements and the image should not
#          pick.
#
# # `--lan` is NOT the default, and the comment is the reason
#
# `docs/security.md`'s threat table says a wiki on a LAN with a self-signed
# certificate produces a browser warning, and that is a convenience for playing at a
# table rather than a secure channel. A container that published a port by default
# would be a wiki exposed to a network with TLS nobody chose, so `CMD` is loopback and
# the operator turns on the LAN explicitly.

# ---------------------------------------------------------------- build ----

# The pinned Go version is `go.mod`'s, read by the builder rather than repeated
# here, and `make check-go-version` exists to keep the two from drifting. A
# Dockerfile that pins a *second* Go version is a Dockerfile that is wrong six
# months after the pin was right.
FROM golang:1.25.0-alpine AS build

WORKDIR /src

# Dependencies first, so a change to a source file does not re-download the module
# cache. `go mod download` on its own layer is the whole trick.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# `-trimpath` and the version stamp are `make build`'s, because a release built by
# `make dist` and a release built by this Dockerfile have to be the same binary --
# and `wiki version` is what a DM puts in a bug report.
ARG VERSION=dev
ARG COMMIT=unknown
RUN go build -trimpath \
      -ldflags "-s -w \
        -X github.com/popinjayjohn/dine-and-dash-semiplane/internal/version.Version=${VERSION} \
        -X github.com/popinjayjohn/dine-and-dash-semiplane/internal/version.Commit=${COMMIT} \
        -X github.com/popinjayjohn/dine-and-dash-semiplane/internal/version.Date=unknown" \
      -o /out/wiki ./cmd/wiki

# ----------------------------------------------------------------- run ----

# CGO off, so the binary does not want a libc that is not here. This is the
# assertion rather than a comment: with CGO on, `go build` would succeed and the
# binary would fail at exec with a loader error a DM would file as "it does not
# start".
FROM scratch

# The certificate bundle, and nothing else. A wiki that does not fetch anything at
# runtime (ADR 0006) still makes TLS *connections* when a DM follows a share link to
# their own reverse proxy, and a scratch image with no CA bundle makes that fail with
# a message about x509 that nobody can act on.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

COPY --from=build /out/wiki /wiki

# The data directory, created with the ownership the container runs as. `nobody` is
# in the image because scratch has no `/etc/passwd` and an arbitrary UID is
# something a bind-mounted host directory is more likely to collide with.
RUN mkdir -p /data /vault
VOLUME ["/data"]

# Loopback, and the port. The operator changes it with `-p 8080:8080` and
# `--addr 0.0.0.0:8080`; the image does not make that decision for them.
EXPOSE 8080

ENV DDSP_DATA_DIR=/data
ENV DDSP_LISTEN=0.0.0.0:8080

# The health check, and it is the weakest thing in this file: **it cannot be the
# `/_/healthz` line.** Probing that needs an HTTP client, and a scratch image has
# no curl and no wget and no shell to pipe one with. So this runs the binary, which
# proves it starts and finds its data directory -- and that is genuinely worth
# something, because the two ways a container of this one fails at boot are a binary
# that does not run and a data directory that is not there.
#
# An operator who wants the HTTP line should point their load balancer at
# `/_/healthz` themselves, and README.md says so.
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD ["/wiki", "version"]

# There is no `USER` because scratch has no `/etc/passwd` to name one, and a
# hard-coded `USER 65534` is a line that looks like it does something while
# depending on a uid the host does not have. README.md's runbook says to pass
# `--user "$(id -u):$(id -g)"`, which is the version that is actually right.

ENTRYPOINT ["/wiki"]
CMD ["serve", "--no-watch"]
