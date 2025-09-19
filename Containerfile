# syntax=docker/dockerfile@sha256:dabfc0969b935b2080555ace70ee69a5261af8a8f1b4df97b9e7fbcf6722eddf
FROM --platform=amd64 stagex/pallet-go@sha256:3b645dde2856b2e2845ea4eb34eb351666512c15f0acec13360b299c6dce7bff AS pallet-go

FROM pallet-go AS build
ARG TARGETOS
ARG TARGETARCH

# ENV GOPATH=/cache/go
# ENV GOCACHE=/cache/
# ENV GOWORK=off
# ENV GOPROXY=https://proxy.golang.org,direct
# ENV GOSUMDB=sum.golang.org
# ENV CGO_ENABLED=0
ENV GOOS=${TARGETOS}
ENV GOARCH=${TARGETARCH}
ADD . /containerfile-updater
WORKDIR /containerfile-updater
RUN go mod download
RUN --network=none <<-EOF
  set -eu
  go build \
    -trimpath \
    -v \
    -mod=readonly \
    .
  install -Dm0755 -t /rootfs-${TARGETOS}-${TARGETARCH}/usr/bin/ containerfile-updater
  install -Dm0644 -t /rootfs-${TARGETOS}-${TARGETARCH}/usr/share/licenses/containerfile-updater/ LICENSE
  install -Dm0644 -t /rootfs-${TARGETOS}-${TARGETARCH}/usr/share/licenses/containerfile-updater/ COPYRIGHT
  install -Dm0644 -t /rootfs-${TARGETOS}-${TARGETARCH}/etc/ssl/certs/ /etc/ssl/certs/ca-certificates.crt
EOF

FROM scratch AS package
ARG TARGETOS
ARG TARGETARCH
COPY --from=build /rootfs-${TARGETOS}-${TARGETARCH}/ /
