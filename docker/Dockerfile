# syntax=docker/dockerfile:1
# Generic build for either binary this module produces — set CMD_PATH to pick which one.
# docker-compose passes the actual subcommand (e.g. "server") via `command:`.
ARG CMD_PATH=./cmd/workflows

FROM golang:1.25-alpine AS build
ARG CMD_PATH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/app "${CMD_PATH}"

FROM alpine:3.22
RUN apk add --no-cache ca-certificates
COPY --from=build /out/app /usr/local/bin/app
EXPOSE 8080 9105 9109
ENTRYPOINT ["/usr/local/bin/app"]
