FROM golang:1.23-bookworm AS toolchain

FROM toolchain AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -buildvcs=false -p 1 -trimpath -ldflags="-s -w" -o /out/pibase ./cmd/pibase
RUN mkdir -p /out/data/files /out/data/backups

# Gate image: Go 1.23 plus PostgreSQL 16 client tools for the branching
# integration tests. It is never shipped or used by the production stack.
FROM postgres:16-bookworm AS test
COPY --from=toolchain /usr/local/go /usr/local/go
ENV PATH="/usr/local/go/bin:${PATH}" GOPATH=/go

FROM postgres:16-bookworm AS ops
RUN groupadd --gid 65532 pibase && useradd --system --uid 65532 --gid 65532 --home /nonexistent --shell /usr/sbin/nologin pibase
COPY --from=build --chown=pibase:pibase /out/pibase /pibase
COPY --from=build --chown=pibase:pibase /out/data /data
USER pibase
ENTRYPOINT ["/pibase"]
CMD ["serve"]

FROM gcr.io/distroless/static-debian12:nonroot AS gateway
COPY --from=build /out/pibase /pibase
COPY --from=build --chown=nonroot:nonroot /out/data /data
ENTRYPOINT ["/pibase"]
CMD ["serve"]
