# syntax=docker/dockerfile:1

# One Dockerfile builds both binaries. They share a module, a dependency set
# and a build command; the only difference is which main package is compiled,
# so TARGET selects it rather than duplicating this file twice and letting the
# copies drift.

# ---------------------------------------------------------------- build ----
FROM golang:1.25-alpine AS build

WORKDIR /src

# Dependencies are copied and downloaded before the source. Docker caches each
# layer, so as long as go.mod and go.sum are unchanged this layer is reused and
# a source edit does not re-download the module graph.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGET=api
# CGO_ENABLED=0 forces a statically linked binary with no libc dependency,
# which is what makes the scratch base below possible at all.
# -trimpath removes local filesystem paths, so the build is reproducible and
# the image does not leak the directory layout of the machine that built it.
# -s -w strip the symbol table and DWARF debug data, worth a few megabytes.
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath -ldflags="-s -w" \
      -o /out/app ./workload/${TARGET}

# ---------------------------------------------------------------- final ----
# scratch is genuinely empty: no shell, no package manager, no libc. Nothing to
# exploit and nothing to patch, and the image is the binary plus certificates.
# The size is not vanity - claim C1 measures cold-start penalty, and the image
# a node must pull before a pod can start is a direct term in that number.
FROM scratch

# Copied from the build stage because scratch has no CA bundle, and any
# outbound HTTPS call - the model gateway in Module 7 - would fail with an
# unhelpful certificate error without it.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

COPY --from=build /out/app /app

# Numeric because scratch has no /etc/passwd for a name to resolve against.
# 65534 is nobody. Running as root inside a container that needs no privileges
# is the kind of thing a security review flags immediately.
USER 65534:65534

EXPOSE 8000 9100
ENTRYPOINT ["/app"]
