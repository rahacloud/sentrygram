# syntax=docker/dockerfile:1

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /sentrygram ./cmd/sentrygram

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /sentrygram /usr/local/bin/sentrygram
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/sentrygram"]
