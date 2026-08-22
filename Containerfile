FROM golang:1.27-bookworm@sha256:484ef6066fa69acb059fdfeda7ba2b8f7391f2ef6abc6f9b8411e669ebd56466 AS build

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -tags containers_image_openpgp -trimpath -ldflags='-s -w' -o /out/olm-catalog-datasource ./cmd/olm-catalog-datasource

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

COPY --from=build /out/olm-catalog-datasource /usr/local/bin/olm-catalog-datasource
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/olm-catalog-datasource"]
CMD ["serve", "--config", "/etc/olm-catalog-datasource/config.yaml"]
