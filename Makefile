# Talyvor Track — top-level developer commands.
#
# `make up` brings the full stack online; `make dev` runs the backend
# and frontend directly on the host for fast iteration.

.PHONY: up down dev test vet build tidy run frontend-dev frontend-build logs clean

up:
	docker compose up -d

down:
	docker compose down

# Run the API + frontend dev server side-by-side. Backend log goes to
# stdout; the trailing `&` keeps the shell free for npm. ^C kills npm
# but leaves the Go process — `pkill -f 'go run ./cmd/track'` cleans up.
dev:
	go run ./cmd/track & cd frontend && npm run dev

test:
	go test -race -count=1 ./...

vet:
	go vet ./...

tidy:
	go mod tidy

build:
	go build -ldflags="-w -s" -o bin/track ./cmd/track

run:
	go run ./cmd/track

frontend-dev:
	cd frontend && npm run dev

frontend-build:
	cd frontend && npm run build

logs:
	docker compose logs -f track

clean:
	rm -rf bin/ frontend/dist/
