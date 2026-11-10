default:
    @just --list

build:
    go build -o sentrygram ./cmd/sentrygram

test:
    go test -race ./... -covermode=atomic -coverprofile=coverage.out

lint:
    golangci-lint run -c .golangci.yml

tidy:
    go mod tidy

image:
    docker build -t ghcr.io/rahacloud/sentrygram:dev .
