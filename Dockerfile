# Stage 1: Frontend build
FROM node:22-alpine AS frontend
WORKDIR /app/web
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

# Stage 2: Go build
FROM golang:1.25-alpine AS backend
WORKDIR /app
ENV GOPROXY=https://goproxy.cn,https://proxy.golang.org,direct
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /app/web/dist ./cmd/server/webdist
RUN CGO_ENABLED=0 go build -o /server ./cmd/server

# Stage 3: Runtime
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
COPY --from=backend /server /server
COPY --from=backend /app/migrations /app/migrations
ENV MIGRATIONS_DIR=/app/migrations
EXPOSE 8080 2525
ENTRYPOINT ["/server"]
