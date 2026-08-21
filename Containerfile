FROM golang:1.27-bookworm@sha256:484ef6066fa69acb059fdfeda7ba2b8f7391f2ef6abc6f9b8411e669ebd56466 AS build

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -tags containers_image_openpgp -trimpath -ldflags='-s -w' -o /out/olm-catalog-datasource ./cmd/olm-catalog-datasource

FROM gcr.io/distroless/static-debian12:nonroot@sha256:1b7b9f0f0e0a1d2155f531db587cc48ec26aaf97ab64364225f5bf18a054e66a

COPY --from=build /out/olm-catalog-datasource /usr/local/bin/olm-catalog-datasource
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/olm-catalog-datasource"]
CMD ["serve", "--config", "/etc/olm-catalog-datasource/config.yaml"]
