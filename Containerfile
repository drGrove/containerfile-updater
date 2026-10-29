# syntax=docker/dockerfile@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e
FROM --platform=amd64 stagex/pallet-go@sha256:d93bfdee5b75f634e4c14a0a80593d47e4fb22ab8fd8ddce6bf8b3cff49ba602 AS pallet-go

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
