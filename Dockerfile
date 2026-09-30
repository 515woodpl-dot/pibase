FROM golang:1.23-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -buildvcs=false -p 1 -trimpath -ldflags="-s -w" -o /out/pibase ./cmd/pibase
RUN mkdir -p /out/data/files

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/pibase /pibase
COPY --from=build --chown=nonroot:nonroot /out/data /data
ENTRYPOINT ["/pibase"]
CMD ["serve"]
