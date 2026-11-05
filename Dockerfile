# syntax=docker/dockerfile:1

ARG GO_VERSION=1.27

# ---- build ----
# Runs on the build platform and cross-compiles for the target platform (buildx multi-arch).
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION} AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src

# Dependencies first, so this layer is cached until go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/gocsvify .

# Licenses of all linked third-party modules for the target platform.
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH \
    bash scripts/collect-licenses.sh /out/THIRD_PARTY_NOTICES.txt

# Data directory owned by the unprivileged user of the final image.
RUN mkdir /data

# ---- runtime ----
# distroless: no shell and no package manager; includes CA certificates and tzdata.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/gocsvify /app/gocsvify
COPY --from=build /out/THIRD_PARTY_NOTICES.txt /licenses/THIRD_PARTY_NOTICES.txt
COPY LICENSE /licenses/LICENSE
COPY docker/settings.json /etc/gocsvify/settings.json
COPY --from=build --chown=nonroot:nonroot /data /data

VOLUME ["/data"]
EXPOSE 8080
USER nonroot:nonroot

ENTRYPOINT ["/app/gocsvify"]
CMD ["-settings_path=/etc/gocsvify/settings.json"]
