FROM node:24-alpine AS frontend
WORKDIR /web
COPY server/web/package*.json ./
RUN npm ci
COPY server/web/ ./
RUN npm run build

FROM golang:1.27-alpine AS backend
RUN apk add --no-cache build-base
WORKDIR /src
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/*.go ./
COPY --from=frontend /public ./public
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /jk-monitor .

FROM alpine:3
RUN apk add --no-cache ca-certificates && mkdir /data && chown 10001:10001 /data
COPY --from=backend /jk-monitor /usr/local/bin/jk-monitor
USER 10001:10001
ENV LISTEN_ADDR=0.0.0.0:8080 DATA_DIR=/data
EXPOSE 8080
VOLUME /data
ENTRYPOINT ["/usr/local/bin/jk-monitor"]
