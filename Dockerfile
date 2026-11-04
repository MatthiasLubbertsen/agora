# syntax=docker/dockerfile:1

# 1. Build the website.
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
# vite writes to ../internal/webui/dist
RUN mkdir -p ../internal/webui/dist && npm run build

# 2. Build the Go binary with the website embedded.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/webui/dist ./internal/webui/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gateway .

# 3. Small runtime image.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/gateway /gateway
ENV ADDR=:8080 DATA_DIR=/data
VOLUME /data
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/gateway"]
