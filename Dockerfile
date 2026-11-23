# Build a static binary, then ship it on an empty base image.
# Base images are pinned by digest; Dependabot proposes updates.
FROM golang:1.27@sha256:205c7636d74e0ffbe3d007162df7d74f859f5a8c8322dfb9f907977686c126cf AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go tool templ generate \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/secret-santa ./cmd/secret-santa \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/secret-santa /secret-santa
COPY --from=build --chown=nonroot:nonroot /out/data /data
ENV SECRET_SANTA_ADDR=:8080 \
    SECRET_SANTA_DB=/data/secret-santa.db
VOLUME /data
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/secret-santa"]
