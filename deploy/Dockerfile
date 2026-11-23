FROM golang:1.27.3-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /gateway ./cmd/gateway

FROM alpine:3.22
COPY --from=build /gateway /usr/local/bin/gateway
USER nobody
EXPOSE 8080
ENTRYPOINT ["gateway"]
