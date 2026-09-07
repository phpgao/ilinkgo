# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ilinkgo .

FROM alpine:3.24
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /out/ilinkgo /usr/local/bin/ilinkgo

# State (credentials, context tokens) lives in a volume so it survives restarts.
ENV ILINKGO_DATA_DIR=/data
VOLUME /data
EXPOSE 9100

ENTRYPOINT ["/usr/local/bin/ilinkgo"]
# 0.0.0.0 而非默认的 127.0.0.1，否则容器内端口对外不可达
CMD ["serve", "--listen", "0.0.0.0:9100"]
