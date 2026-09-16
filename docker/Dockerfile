# syntax=docker/dockerfile:1
# Generic build for either binary this module produces — set CMD_PATH to pick which one.
# docker-compose passes the actual subcommand (e.g. "server") via `command:`.
ARG CMD_PATH=./cmd/workflows

FROM golang:1.26.8-alpine3.24@sha256:ce864e7223ac17b1775e6fd0b4c0db580c2eb50e7953a427916379e4b92a1628 AS build
ARG CMD_PATH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/app "${CMD_PATH}"

FROM alpine:3.22.5@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce
RUN apk add --no-cache ca-certificates
COPY --from=build /out/app /usr/local/bin/app
EXPOSE 8080 9105 9109
ENTRYPOINT ["/usr/local/bin/app"]
