FROM registry.access.redhat.com/ubi10/go-toolset:1.26.7-1788411200@sha256:be70aa468168f1ecd46e56d5f362e697243bcf9d3a2d98819597e43471a5d0e4 AS build

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -buildvcs=false -tags containers_image_openpgp -trimpath -ldflags='-s -w' -o /tmp/olm-catalog-datasource ./cmd/olm-catalog-datasource

FROM registry.access.redhat.com/ubi10/ubi-micro:10.2-1787684489@sha256:37fadb004c6bea628fcdd81376c8fb77bd8d9fd432d90503af4d9e76b1ff7191

COPY --from=build /tmp/olm-catalog-datasource /usr/local/bin/olm-catalog-datasource
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/olm-catalog-datasource"]
CMD ["serve", "--config", "/etc/olm-catalog-datasource/config.yaml"]
