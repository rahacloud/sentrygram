# syntax=docker/dockerfile:1

# The build stage runs on the builder's own platform and cross-compiles, so a
# multi-arch image needs no emulation.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /sentrygram ./cmd/sentrygram

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /sentrygram /usr/local/bin/sentrygram
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/sentrygram"]
