FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /kyo-router ./cmd/kyo-router

FROM gcr.io/distroless/static:nonroot
COPY --from=build /kyo-router /kyo-router
COPY --from=build /src/config.toml /etc/kyo-router/config.toml
USER nonroot
ENTRYPOINT ["/kyo-router"]
CMD ["-config", "/etc/kyo-router/config.toml"]
