FROM node:24-alpine AS frontend
WORKDIR /build/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.27-alpine AS backend
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
COPY internal/ ./internal/
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/anchor .

FROM alpine:3.22
RUN addgroup -S -g 10001 anchor && adduser -S -D -H -u 10001 -G anchor anchor && mkdir -p /app/web /data && chown anchor:anchor /data
COPY --from=backend /out/anchor /app/anchor
COPY --from=frontend /build/web/dist/ /app/web/
USER anchor
ENV ANCHOR_LISTEN=:8080 ANCHOR_DATA_DIR=/data ANCHOR_WEB_DIR=/app/web
EXPOSE 8080
CMD ["/app/anchor"]
