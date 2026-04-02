FROM haskell:9.2.8 AS eflint-builder

# Ensure GHC is available as ghc-9.2 (required by the eflint build)
RUN ln -s /opt/ghc/9.2.8/bin/ghc /opt/ghc/9.2.8/bin/ghc-9.2

# Install git and certs for cloning (buster repos are archived)
RUN sed -i 's|deb.debian.org/debian|archive.debian.org/debian|g' /etc/apt/sources.list \
    && sed -i 's|security.debian.org/debian-security|archive.debian.org/debian-security|g' /etc/apt/sources.list \
    && echo 'Acquire::Check-Valid-Until "false";' > /etc/apt/apt.conf.d/99no-check-valid-until \
    && apt-get update && apt-get install -y --no-install-recommends \
    git \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

ARG EFLINT_REPO=https://gitlab.com/eflint/haskell-implementation.git
ARG EFLINT_TAG=v4.1.0.1

RUN git clone --depth 1 --branch "${EFLINT_TAG}" "${EFLINT_REPO}" /tmp/eflint-project
WORKDIR /tmp/eflint-project
RUN cabal update
RUN cabal install
RUN cp /root/.cabal/bin/eflint-server /usr/bin/eflint-server

FROM golang:1.22.1-bullseye AS go-builder
ARG NAME
ENV NAME=$NAME

WORKDIR /app/cmd/$NAME
RUN mkdir /app/pkg
RUN mkdir /app/etcd

# Copy all files from the service directory (including subdirectories like repository/, service/)
COPY . ./

# Remove go.mod/go.sum from service dir (they get copied here by Makefile but must only exist at /app)
RUN rm -f go.mod go.sum
# Remove pkg from service dir (it also gets copied by Makefile but must only exist at /app)
RUN rm -rf pkg

# Copy go.mod and go.sum files into the container
COPY go.mod /app
COPY go.sum /app

# Copy the /pkg directory
COPY pkg /app/pkg

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o /go/bin/$NAME .

FROM debian:bookworm-slim
ARG NAME
ENV NAME=$NAME

RUN apt-get update && apt-get install -y --no-install-recommends \
    libgmp10 \
    libc6 \
    libffi8 \
    libnuma1 \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app/

# Copy eflint-server from the eflint build stage
COPY --from=eflint-builder /usr/bin/eflint-server /usr/bin/eflint-server

# Copy the pre-built service binary
COPY --from=go-builder /go/bin/$NAME .

# Start the application
CMD ["/bin/sh", "-c", "/app/$NAME || ls -al /app"]
