.PHONY: build test vet run tidy

build:
	go build ./...

test:
	go test ./...

vet:
	go vet ./...

tidy:
	go mod tidy

# Run the API server locally. Requires postgres + LENS_TRACK_DATABASE_URL.
run:
	go run ./cmd/track
