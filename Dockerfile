# syntax=docker/dockerfile:1

# 1. Build the dashboard.
FROM node:24-alpine AS dashboard
WORKDIR /src
COPY web/dashboard/package.json web/dashboard/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/dashboard/ ./
RUN npm run build

# 2. Build the server, with the dashboard embedded.
FROM golang:1.27-alpine AS server
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=dashboard /src/dist ./web/dashboard/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/server ./cmd/server

# 3. Run it as a non-root user in a minimal image.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=server /out/server /server
EXPOSE 8080
ENTRYPOINT ["/server"]
