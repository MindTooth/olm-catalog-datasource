FROM registry.access.redhat.com/ubi10/go-toolset:1.26.5 AS build

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -buildvcs=false -tags containers_image_openpgp -trimpath -ldflags='-s -w' -o /tmp/olm-catalog-datasource ./cmd/olm-catalog-datasource

FROM registry.access.redhat.com/ubi10/ubi-micro:10.2

COPY --from=build /tmp/olm-catalog-datasource /usr/local/bin/olm-catalog-datasource
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/olm-catalog-datasource"]
CMD ["serve", "--config", "/etc/olm-catalog-datasource/config.yaml"]
