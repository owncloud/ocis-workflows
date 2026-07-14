# syntax=docker/dockerfile:1
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/workflows ./cmd/workflows

FROM alpine:3.22
RUN apk add --no-cache ca-certificates
COPY --from=build /out/workflows /usr/local/bin/workflows
EXPOSE 9105 9109
ENTRYPOINT ["workflows"]
CMD ["server"]
