.PHONY: web build test dev run docker

web:
	cd web && npm ci && npm run build

build: web
	CGO_ENABLED=0 go build -trimpath -o bin/gateway .

test:
	go vet ./...
	go test ./...
	cd web && npm run typecheck

# Run the Go server (needs env, see .env.example) and the Vite dev server together.
dev:
	cd web && npm run dev

run: build
	./bin/gateway

docker:
	docker build -t mcp-gateway .
