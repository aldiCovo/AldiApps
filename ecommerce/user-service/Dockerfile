# Compiles natively and cross-compiles to the target, so an arm64 Mac needn't emulate amd64.
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine AS build
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY migrations ./migrations
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /user-service .

FROM gcr.io/distroless/static-debian13:nonroot

LABEL org.opencontainers.image.source="https://github.com/aldiCovo/AldiApps"

COPY --from=build /user-service /user-service
EXPOSE 8080
ENTRYPOINT ["/user-service"]
