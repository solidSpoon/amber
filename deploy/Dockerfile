# 多阶段构建：前端产物 → Go 编译 → 精简运行时。
# 注意 rigd 需要访问 docker socket 才能起被测容器，因此运行时要挂 /var/run/docker.sock。
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/rigd ./cmd/rigd

FROM alpine:3.22
RUN apk add --no-cache docker-cli ca-certificates tzdata && adduser -D -u 1000 rigd
COPY --from=build /out/rigd /usr/local/bin/rigd
USER rigd
WORKDIR /home/rigd
EXPOSE 8088
ENTRYPOINT ["/usr/local/bin/rigd"]
