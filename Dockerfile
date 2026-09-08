# Stage 1: Frontend build
FROM node:22-alpine AS frontend
WORKDIR /app/web
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

# Stage 2: Go build
FROM golang:1.25.12-alpine AS backend
WORKDIR /app
ENV GOPROXY=https://goproxy.cn,https://proxy.golang.org,direct
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /app/web/dist ./cmd/server/webdist
RUN CGO_ENABLED=0 go build -o /server ./cmd/server
RUN CGO_ENABLED=0 go build -o /msrecovery ./cmd/msrecovery
RUN CGO_ENABLED=0 go build -o /apple ./cmd/apple
RUN CGO_ENABLED=0 go build -o /aliasworker ./cmd/aliasworker
RUN CGO_ENABLED=0 go build -o /proto ./cmd/proto

# Proto alone needs Python for the native PKL protocol; keep pip out of runtime.
FROM alpine:3.21 AS proto-python
RUN apk add --no-cache python3 py3-pip libstdc++ libffi
COPY internal/proto/infra/proton/requirements-pkl.txt /tmp/requirements-pkl.txt
RUN python3 -m venv /opt/proto-venv \
    && /opt/proto-venv/bin/python -m pip install --no-cache-dir --only-binary=:all: --no-deps -r /tmp/requirements-pkl.txt
COPY internal/proto/infra/proton/pkl_bridge.py /tmp/pkl_bridge.py
RUN /opt/proto-venv/bin/python -I -c "import runpy; bridge = runpy.run_path('/tmp/pkl_bridge.py'); client, _ = bridge['build_client']('fetch', ''); client.session.close()"

# Stage 3: Runtime
FROM alpine:3.21
LABEL io.remail.points-unit="1"
RUN apk add --no-cache ca-certificates tzdata chromium python3 libstdc++ libffi
ENV TZ=Asia/Shanghai
ENV PROTO_PYTHON_BIN=/opt/proto-venv/bin/python
COPY --from=proto-python /opt/proto-venv /opt/proto-venv
COPY --from=backend /server /server
COPY --from=backend /msrecovery /usr/local/bin/msrecovery
COPY --from=backend /apple /usr/local/bin/apple
COPY --from=backend /aliasworker /usr/local/bin/aliasworker
COPY --from=backend /proto /usr/local/bin/proto
COPY --from=backend /app/migrations /app/migrations
ENV MIGRATIONS_DIR=/app/migrations
EXPOSE 8080 2525 2587
ENTRYPOINT ["/server"]
