FROM golang:1.18-alpine AS builder

WORKDIR /app

COPY go.mod ./
COPY go.sum ./
RUN --mount=type=cache,target=/cache/gomod go mod download

COPY *.go .
RUN --mount=type=cache,target=/cache/gomod \
  --mount=type=cache,target=/cache/gobuild,sharing=locked \
  go build -o /nsg-updater

FROM alpine
COPY --from=builder /nsg-updater /nsg-updater
CMD [ "/nsg-updater" ]
