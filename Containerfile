FROM golang:1.26-bookworm AS build

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/olm-catalog-datasource ./cmd/olm-catalog-datasource

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/olm-catalog-datasource /usr/local/bin/olm-catalog-datasource
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/olm-catalog-datasource"]
CMD ["serve", "--config", "/etc/olm-catalog-datasource/config.yaml"]
