# Compiles natively and cross-compiles to the target, so an arm64 Mac needn't emulate amd64.
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine AS build
ARG TARGETARCH
WORKDIR /src
COPY go.mod ./
COPY *.go ./
COPY static ./static
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /web .

FROM gcr.io/distroless/static-debian13:nonroot

LABEL org.opencontainers.image.source="https://github.com/aldiCovo/AldiApps"

COPY --from=build /web /web
EXPOSE 8080
ENTRYPOINT ["/web"]
