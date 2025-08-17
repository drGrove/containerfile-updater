FROM stagex/pallet-go@sha256:1f738709d5153f37a19193b1ee85ccbf3b52a956bd4abd0aedf9be34caab6dd4 AS pallet-go
FROM stagex/core-filesystem@sha256:2aaaea601e1725a8292c4c28e723db5761d892b869556f9b05c0983ba11fe30e AS filesystem

FROM scratch AS build
COPY --from=pallet-go . /
ENV GOPATH=/cache/go
ENV GOCACHE=/cache/
ENV GOWORK=off
ENV GOPROXY=https://proxy.golang.org,direct
ENV GOSUMDB=sum.golang.org
ENV CGO_ENABLED=0
ENV GOHOSTOS=linux
ENV GOHOSTARCH=amd64
ADD . /containerfile-updater
WORKDIR /containerfile-updater
RUN go mod download
RUN --network=none <<-EOF
  set -eu
  go build -trimpath -v .
  install -Dm0755 -t /rootfs/usr/bin/ containerfile-updater
  install -Dm0644 -t /rootfs/usr/share/licenses/containerfile-updater/ LICENSE
  install -Dm0644 -t /rootfs/usr/share/licenses/containerfile-updater/ COPYRIGHT
EOF

FROM filesystem AS package
COPY --from=build /rootfs/ /
