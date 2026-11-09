# Build a static binary, then ship it on an empty base image.
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go tool templ generate \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/secret-santa ./cmd/secret-santa \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/secret-santa /secret-santa
COPY --from=build --chown=nonroot:nonroot /out/data /data
ENV SECRET_SANTA_ADDR=:8080 \
    SECRET_SANTA_DB=/data/secret-santa.db
VOLUME /data
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/secret-santa"]
