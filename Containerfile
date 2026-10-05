FROM registry.access.redhat.com/ubi10/go-toolset:1.26.7-1791216793@sha256:8106afc02bac6f3d78384a7380460aa51bf0118f8179453609c80ca1858c5454 AS build

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -buildvcs=false -tags containers_image_openpgp -trimpath -ldflags='-s -w' -o /tmp/ratatoskr ./cmd/ratatoskr

FROM registry.access.redhat.com/ubi10/ubi-micro:10.2-1787684489@sha256:37fadb004c6bea628fcdd81376c8fb77bd8d9fd432d90503af4d9e76b1ff7191

COPY --from=build /tmp/ratatoskr /usr/local/bin/ratatoskr
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/ratatoskr"]
CMD ["serve", "--config", "/etc/ratatoskr/config.yaml"]
