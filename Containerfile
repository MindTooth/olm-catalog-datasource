FROM registry.access.redhat.com/ubi10/go-toolset:1.26.5-1787695382@sha256:66093c6f0bd7e0f444f6dfc5d3864c0f8b626b7c0a28a4c2f53b3b67b7f68ccc AS build

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -buildvcs=false -tags containers_image_openpgp -trimpath -ldflags='-s -w' -o /tmp/olm-catalog-datasource ./cmd/olm-catalog-datasource

FROM registry.access.redhat.com/ubi10/ubi-micro:10.2-1786324819@sha256:cabedb588644e9da2c95ebb173a67b78d58aaedcb0eaa42a86f880bcef8a0b2f

COPY --from=build /tmp/olm-catalog-datasource /usr/local/bin/olm-catalog-datasource
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/olm-catalog-datasource"]
CMD ["serve", "--config", "/etc/olm-catalog-datasource/config.yaml"]
