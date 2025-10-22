# syntax=docker/dockerfile@sha256:b6afd42430b15f2d2a4c5a02b919e98a525b785b1aaff16747d2f623364e39b6
FROM --platform=amd64 stagex/pallet-go@sha256:5477bcf690aa52d1afdd2ed4ed0e6cc661cabf028a93ac639106b2ad06d7fa9a AS pallet-go

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
