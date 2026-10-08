FROM registry.access.redhat.com/ubi10/go-toolset:1.26.7-1791362847@sha256:f6b33401d7dc17d32bed91be97ba14c642646031ef2e89630b035a822d1755bf AS build

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -buildvcs=false -tags containers_image_openpgp -trimpath -ldflags='-s -w' -o /tmp/ratatoskr ./cmd/ratatoskr

FROM registry.access.redhat.com/ubi10/ubi-micro:10.2-1791441953@sha256:5b13e670e107509be71c066180032017ff5dddff2b6cd60d16311a5cea309953

COPY --from=build /tmp/ratatoskr /usr/local/bin/ratatoskr
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/ratatoskr"]
CMD ["serve", "--config", "/etc/ratatoskr/config.yaml"]
