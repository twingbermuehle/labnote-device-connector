# Container image of the connector. Pure-Go build: no cgo, no runtime deps.
FROM golang:1.23-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/labnote-connector ./cmd/connector

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/labnote-connector /usr/local/bin/labnote-connector

# There is no OS credential store in a container, so the ingest API key is
# supplied as LABNOTE_INGEST_API_KEY. Everything else is configured through the
# setup interface.
ENV LABNOTE_CONNECTOR_DATA_DIR=/data \
    LABNOTE_CONNECTOR_UI_ADDR=0.0.0.0:8420

VOLUME ["/data"]
EXPOSE 8420
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/labnote-connector"]
